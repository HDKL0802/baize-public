package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"baize/internal/llm"
)

// 本文件是「记忆术」的检索与巩固核心（OpenHuman memory 子系统的 Go 重写）。
//
// 与普通 RAG 的三点区别，都实现在这里：
//  1. **混合打分**：不只看关键词，也不只看向量，而是
//     graph（联想）+ vector（语义）+ keyword（字面）+ freshness（新鲜度）四项加权，
//     并给四档权重档位（balanced / semantic / lexical / graph_first）；
//  2. **主动召回（auto recall）**：不是"等模型来查"，而是派活前自动按最低相似度
//     + 相对分数下限 + token 预算把相关记忆塞进上下文；
//  3. **分区与键**：namespace 隔离多用户/多智能体，doc_key 让同一条记忆的更新是
//     「覆盖」而不是越堆越多。

const (
	// DefaultNamespace 默认记忆分区
	DefaultNamespace = "default"

	// CategoryCore 核心记忆（默认）
	CategoryCore = "core"
	// CategoryDaily 日常流水
	CategoryDaily = "daily"
	// CategoryConversation 会话记忆
	CategoryConversation = "conversation"
	// CategoryCustom 自定义
	CategoryCustom = "custom"
)

// Categories 合法的记忆分类
var Categories = []string{CategoryCore, CategoryDaily, CategoryConversation, CategoryCustom}

// NamespaceAllowed 分区名是否合法（小写字母/数字/下划线/连字符/点，字母数字开头）
func NamespaceAllowed(ns string) bool {
	if ns == "" || len([]rune(ns)) > 64 {
		return false
	}
	for i, r := range ns {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '_' || r == '-' || r == '.') && i > 0:
		default:
			return false
		}
	}
	return true
}

// NormalizeNamespace 收敛分区名；非法值一律落到 default（宁可写进默认分区，也不丢内容）
func NormalizeNamespace(ns string) string {
	ns = strings.ToLower(strings.TrimSpace(ns))
	if NamespaceAllowed(ns) {
		return ns
	}
	return DefaultNamespace
}

// NormalizeCategory 收敛分类；未知值落到 core
func NormalizeCategory(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	for _, k := range Categories {
		if c == k {
			return c
		}
	}
	return CategoryCore
}

// MakeKey 生成记忆的文档键：可读词干 + 内容 sha256 前 12 位。
// 一样的内容得到一样的键，所以「同一个键再写」= 覆盖更新，不会堆出一堆近似重复的记忆。
func MakeKey(content string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return keySlug(content) + "-" + hex.EncodeToString(sum[:])[:12]
}

// keySlug 取内容首行的前几个词做可读词干（给人看的部分）
func keySlug(content string) string {
	line := firstLine(content)
	fields := strings.Fields(line)
	if len(fields) > 4 {
		fields = fields[:4]
	}
	var b strings.Builder
	for _, r := range strings.Join(fields, "-") {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		case r == '-' || r == '_':
			b.WriteRune('-')
		case r == ' ' || r == '.' || r == '/' || r == ':' || r == ',' || r == '，' || r == '。':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "mem"
	}
	if runes := []rune(out); len(runes) > 40 {
		out = string(runes[:40])
	}
	return out
}

// DeviceTaskKey 跨端任务回执的文档键：同一台设备的同一个动作**共用一把键**，
// 于是新回执是「覆盖」旧的，而不是每跑一次就多堆一条。
//
// 为什么要这样：todo.list 这类机械回执内容大同小异，按任务 id 各存一条的话，
// 跑十次就在记忆里留下十条几乎一样的记录，把语义检索的结果整个稀释掉
// （实测 11 条记忆里有 6 条是同一个动作的回执）。
// 逐次的任务历史在「运行记录 / 任务与审批」里都有，记忆这边只需要"最后一次的样子"。
func DeviceTaskKey(deviceID, action string) string {
	return "device-task-" + slugPart(deviceID) + "-" + slugPart(action)
}

// RunGoalKey 一次运行的记忆键：同一个目标 + 同一天共用一把键（后写覆盖先写）。
// 反复问同一件事只会把结论刷新，不会堆出一串近似重复的条目；过程历史在运行记录里。
func RunGoalKey(goal string, at int64) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(goal)))
	day := time.UnixMilli(at).Format("2006-01-02")
	return "run-goal-" + day + "-" + hex.EncodeToString(sum[:])[:12]
}

// slugPart 把设备 id / 动作名这类标识符收敛成能进文档键的样子
func slugPart(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "x"
	}
	if runes := []rune(out); len(runes) > 60 {
		out = string(runes[:60])
	}
	return out
}

/* ---------- 打分 ---------- */

// Weights 四项权重
type Weights struct {
	Graph     float64 `json:"graph"`
	Vector    float64 `json:"vector"`
	Keyword   float64 `json:"keyword"`
	Freshness float64 `json:"freshness"`
}

// Profiles 四档权重档位。
// 说明：公式（四项加权和）与档位命名对齐 OpenHuman；具体数值是白泽自己的标定，
// 不是从上游抄的常数，调参直接改这里。
var Profiles = map[string]Weights{
	"balanced":    {Graph: 0.25, Vector: 0.35, Keyword: 0.30, Freshness: 0.10},
	"semantic":    {Graph: 0.10, Vector: 0.60, Keyword: 0.20, Freshness: 0.10},
	"lexical":     {Graph: 0.10, Vector: 0.15, Keyword: 0.65, Freshness: 0.10},
	"graph_first": {Graph: 0.45, Vector: 0.20, Keyword: 0.25, Freshness: 0.10},
}

// ProfileNames 档位名列表（给界面/工具列选用）
func ProfileNames() []string {
	return []string{"balanced", "semantic", "lexical", "graph_first"}
}

func weightsFor(profile string) Weights {
	if w, ok := Profiles[strings.ToLower(strings.TrimSpace(profile))]; ok {
		return w
	}
	return Profiles["balanced"]
}

// dropVector 没配向量通道时，把 vector 的权重按比例分给其它三项，
// 这样分数仍然落在 0..1，不同档位之间可比。
func dropVector(w Weights) Weights {
	rest := w.Graph + w.Keyword + w.Freshness
	if rest <= 0 {
		return Weights{Keyword: 1}
	}
	scale := (w.Vector + rest) / rest
	return Weights{Graph: w.Graph * scale, Keyword: w.Keyword * scale, Freshness: w.Freshness * scale}
}

/* ---------- 关键词项的 IDF 加权 ---------- */

// idfWeights 给查询里每个词算一个 0..1 的权重：在候选集里越稀有越接近 1。
//
// 为什么需要：中文长文档里"怎么 / 什么 / 一个"这类词到处都是，不做加权的话，
// 一条只碰巧含这些词的长记录会把真正含「密码本」的结果压下去。
// 权重最后归一到 0..1，只改词与词之间的相对权重，不改分数的量纲 ——
// 这样档位权重和 recallMinScore 都不用跟着重标。
//
// 库里压根没出现过的词不进 idf（见 matchScore 的分母），
// 免得"换个说法的同义词"把整批结果的关键词分一起拉低。
func idfWeights(chunks []Chunk, terms []string) map[string]float64 {
	idf := make(map[string]float64, len(terms))
	if len(terms) == 0 || len(chunks) == 0 {
		return idf
	}
	n := float64(len(chunks))
	df := make(map[string]int, len(terms))
	for _, c := range chunks {
		title := strings.ToLower(c.Title)
		body := strings.ToLower(c.Content)
		seen := make(map[string]bool, len(terms))
		for _, t := range terms {
			if seen[t] {
				continue
			}
			if strings.Contains(title, t) || strings.Contains(body, t) {
				df[t]++
				seen[t] = true
			}
		}
	}
	maxIDF := 1.0
	for _, t := range terms {
		if df[t] == 0 {
			continue // 库里压根没这个词：不进 map，也就不会进 matchScore 的分母
		}
		v := math.Log(1 + n/float64(1+df[t]))
		idf[t] = v
		if v > maxIDF {
			maxIDF = v
		}
	}
	for t, v := range idf {
		idf[t] = v / maxIDF
	}
	return idf
}

/* ---------- 混合检索 ---------- */

// HybridQuery 一次混合检索的入参
type HybridQuery struct {
	Text           string  // 查询文本；空 = 只要最近的
	Namespace      string  // 空 = 跨分区
	Category       string  // 空 = 不限分类
	Source         string  // 数据源过滤（对应 OpenHuman 的 source_kind）
	Profile        string  // 权重档位
	Limit          int     // 返回条数
	MinScore       float64 // 混合分下限（0 = 不限）
	MinVector      float64 // 语义相似度下限（0 = 不限；只在真用上向量时生效）
	TimeWindowDays int     // 只看最近 N 天（0 = 不限）
	Diverse        bool    // MMR 多样性选择（避免返回一堆几乎一样的记忆）
	VectorOnly     bool    // 纯语义检索：忽略关键词项
}

// whereSQL 把过滤条件拼成 SQL（allChunks / vectorsFor 共用一份，避免两处漂移）
func (q HybridQuery) whereSQL() (string, []any) {
	where := []string{"1=1"}
	args := []any{}
	if ns := strings.TrimSpace(q.Namespace); ns != "" {
		where = append(where, "namespace=?")
		args = append(args, ns)
	}
	if cat := strings.TrimSpace(q.Category); cat != "" {
		where = append(where, "category=?")
		args = append(args, cat)
	}
	if src := strings.TrimSpace(q.Source); src != "" {
		where = append(where, "source=?")
		args = append(args, src)
	}
	if q.TimeWindowDays > 0 {
		cut := time.Now().AddDate(0, 0, -q.TimeWindowDays).UnixMilli()
		where = append(where, "created_at>=?")
		args = append(args, cut)
	}
	return strings.Join(where, " AND "), args
}

// SearchResult 一次检索的结果
type SearchResult struct {
	Query      string  `json:"query"`
	Profile    string  `json:"profile"`
	Weights    Weights `json:"weights"`
	VectorUsed bool    `json:"vectorUsed"` // 本次是否真的用上了语义检索
	VectorNote string  `json:"vectorNote,omitempty"`
	Candidates int     `json:"candidates"`
	Filtered   int     `json:"filtered"` // 因为没够上语义相似度下限而被挡掉的条数
	Hits       []Hit   `json:"hits"`
}

// HybridSearch 混合检索：四项加权打分 → 过滤 → （可选）MMR 去重 → 截断
func (s *Store) HybridSearch(ctx context.Context, q HybridQuery) (SearchResult, error) {
	if q.Limit <= 0 {
		q.Limit = 8
	}
	profile := strings.ToLower(strings.TrimSpace(q.Profile))
	if _, ok := Profiles[profile]; !ok {
		profile = "balanced"
	}
	res := SearchResult{Query: q.Text, Profile: profile, Weights: weightsFor(profile)}

	// 没有查询词：按"最近入库"给结果，别硬套打分
	if strings.TrimSpace(q.Text) == "" {
		recent, err := s.Recent(q.Limit)
		if err != nil {
			return res, err
		}
		res.Hits = recent
		return res, nil
	}

	chunks, err := s.allChunks(q)
	if err != nil {
		return res, err
	}
	res.Candidates = len(chunks)
	if len(chunks) == 0 {
		return res, nil
	}

	terms := Tokenize(q.Text)
	if q.VectorOnly {
		terms = nil
	}
	idf := idfWeights(chunks, terms)

	// 语义项：有通道就试；失败只降级，不把整个检索搞挂
	weights := res.Weights
	var qvec []float32
	if e := s.embedderRef(); e != nil {
		if vecs, err := e.Embed(ctx, []string{q.Text}); err == nil && len(vecs) == 1 && len(vecs[0]) > 0 {
			qvec = vecs[0]
			res.VectorUsed = true
		} else if err != nil {
			res.VectorNote = "这次没用上语义检索（向量化失败：" + err.Error() + "），已按关键词 + 新鲜度打分"
		}
	} else {
		res.VectorNote = "未配置 embedding 通道，本次按关键词 + 新鲜度打分"
	}
	if !res.VectorUsed {
		weights = dropVector(weights)
	}
	res.Weights = weights

	cands := make([]cand, 0, len(chunks))
	needVec := qvec != nil
	var vecs map[int64][]float32
	if needVec {
		vecs, err = s.vectorsFor(q)
		if err != nil {
			return res, err
		}
	}
	now := time.Now().UnixMilli()
	cutByVector := 0
	for _, c := range chunks {
		var p Parts
		why := ""
		if len(terms) > 0 {
			if kw, w := matchScore(c, terms, idf); kw > 0 {
				p.Keyword = kw
				why = w
			}
		}
		if needVec {
			if v, ok := vecs[c.ID]; ok {
				if sim := cosine01(qvec, v); sim > 0 {
					p.Vector = sim
				}
			}
		}
		p.Freshness = RecencyFactor(now, c.CreatedAt)
		if p.Keyword <= 0 && p.Vector <= 0 {
			continue // 字面和语义都没声，靠新鲜度不该冒头
		}
		// 语义下限：向量模型对任何两段中文都会给出 0.3~0.45 的"底噪相似度"，
		// 不卡一道，无关的问题也能把 limit 塞满（看着像是"搜到了"）。
		// 这里跟派活时的自动召回用同一个口径，所以同一个阈值在两处行为一致。
		if needVec && q.MinVector > 0 && p.Vector < q.MinVector {
			cutByVector++
			continue
		}
		cands = append(cands, cand{chunk: c, vec: vecs[c.ID], parts: p, why: why})
	}
	res.Filtered = cutByVector
	if len(cands) == 0 {
		if cutByVector > 0 {
			// 别静默返回空：说清楚是"有结果但不够相关"，人才知道去调阈值
			res.VectorNote = fmt.Sprintf("有 %d 条记忆的语义相似度低于 %.2f，都没够上阈值（可在「记忆术」里调低）",
				cutByVector, q.MinVector)
		}
		return res, nil
	}

	fillGraph(cands)

	hits := make([]Hit, 0, len(cands))
	for _, cd := range cands {
		final := weights.Graph*cd.parts.Graph + weights.Vector*cd.parts.Vector +
			weights.Keyword*cd.parts.Keyword + weights.Freshness*cd.parts.Freshness
		if q.MinScore > 0 && final < q.MinScore {
			continue
		}
		if cd.why == "" {
			cd.why = describeWhy(cd.parts, res.VectorUsed)
		}
		c := cd.chunk
		c.vec = cd.vec
		hits = append(hits, Hit{Chunk: c, Score: round4(final), Why: cd.why, Parts: roundParts(cd.parts)})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		// 同分时重要度高的在前（白泽自有口径：重要度不当权重项，只当兜底）
		return hits[i].Chunk.Importance > hits[j].Chunk.Importance
	})
	if q.Diverse {
		hits = mmrSelect(hits, 0.7, q.Limit)
	} else if len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}

	res.Hits = hits
	ids := make([]int64, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.Chunk.ID)
	}
	s.bumpHits(ids)
	return res, nil
}

// cand 打分过程中的一条候选
type cand struct {
	chunk Chunk
	vec   []float32
	parts Parts
	why   string
}

// fillGraph 联想项：与候选集合里其它条目「同一个来源引用」的程度，归一到 0..1。
//
// 白泽没有知识图谱，所以这里不做多跳。**只用 source_ref 做关联，不用当天节点**：
// 当天节点是按天分桶的，同一天写入的所有记忆都会共享它，用它算关联会让每条候选
// 都拿到满分（联想分变成常数），把排序整个压平——无关查询也能蹭到高分。
// source_ref 是"同一次任务/同一份文档"的真实身份，只有它才配当联想的枢纽。
func fillGraph(cands []cand) {
	const hubCap = 5.0 // 一簇最多认 5 个"同类"，避免一个大簇把联想分全吃掉
	for i := range cands {
		ref := cands[i].chunk.SourceRef
		if ref == "" {
			continue // 没有来源引用就没有联想可言，老老实实 0
		}
		same := 0.0
		for j := range cands {
			if i == j {
				continue
			}
			if cands[j].chunk.SourceRef == ref {
				same++
			}
		}
		if same > hubCap {
			same = hubCap
		}
		cands[i].parts.Graph = same / hubCap
	}
}

// mmrSelect 最大边际相关：在"分高"和"别重复"之间取平衡（lambda 越大越看重分数）
func mmrSelect(hits []Hit, lambda float64, limit int) []Hit {
	if len(hits) <= limit {
		return hits
	}
	picked := make([]Hit, 0, limit)
	used := make([]bool, len(hits))
	for len(picked) < limit {
		best, bestScore := -1, -1e9
		for i := range hits {
			if used[i] {
				continue
			}
			penalty := 0.0
			for _, p := range picked {
				if sim := chunkSimilarity(hits[i], p); sim > penalty {
					penalty = sim
				}
			}
			score := lambda*hits[i].Score - (1-lambda)*penalty
			if score > bestScore {
				best, bestScore = i, score
			}
		}
		if best < 0 {
			break
		}
		used[best] = true
		picked = append(picked, hits[best])
	}
	return picked
}

// chunkSimilarity 两条命中的"像不像"：有向量用余弦，没向量退化为文本重合度
func chunkSimilarity(a, b Hit) float64 {
	if len(a.Chunk.vec) > 0 && len(b.Chunk.vec) > 0 {
		return cosine01(a.Chunk.vec, b.Chunk.vec)
	}
	if a.Chunk.DocKey != "" && a.Chunk.DocKey == b.Chunk.DocKey {
		return 1
	}
	return 0.15 * float64(len(sharedTerms(a.Chunk.Content, b.Chunk.Content)))
}

func sharedTerms(x, y string) []string {
	set := map[string]bool{}
	for _, t := range Tokenize(x) {
		set[t] = true
	}
	out := []string{}
	for _, t := range Tokenize(y) {
		if set[t] {
			out = append(out, t)
		}
	}
	return out
}

// cosine01 余弦相似度归一到 0..1（负相关按 0 处理：我们不关心"越不像越相关"）
func cosine01(a, b []float32) float64 {
	return clamp01(llm.CosineSimilarity(a, b))
}

func describeWhy(p Parts, vectorUsed bool) string {
	parts := []string{}
	if p.Keyword > 0 {
		parts = append(parts, fmt.Sprintf("关键词 %.2f", p.Keyword))
	}
	if vectorUsed && p.Vector > 0 {
		parts = append(parts, fmt.Sprintf("语义 %.2f", p.Vector))
	}
	if p.Graph > 0 {
		parts = append(parts, fmt.Sprintf("联想 %.2f", p.Graph))
	}
	if len(parts) == 0 {
		return "新鲜度"
	}
	return strings.Join(parts, " + ")
}

func round4(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}

func roundParts(p Parts) Parts {
	return Parts{
		Graph:     round4(p.Graph),
		Vector:    round4(p.Vector),
		Keyword:   round4(p.Keyword),
		Freshness: round4(p.Freshness),
	}
}

/* ---------- 给存量记忆补向量 ---------- */

// ReindexResult 补向量的结果
type ReindexResult struct {
	Done      int `json:"done"`      // 本次补上了多少条
	Remaining int `json:"remaining"` // 还剩多少条没向量
	Failed    int `json:"failed"`    // 向量化失败（内容照留在库里）
}

// ReindexEmbeddings 给"还没有向量"的记忆补上向量。
//
// 典型场景：先跑了一阵关键词检索，之后才配好 embedding 通道——存量记忆必须补一遍，
// 否则语义检索对它们**永远无效**（只能靠关键词命中），看着像"记忆没生效"。
func (s *Store) ReindexEmbeddings(ctx context.Context, batch int) (ReindexResult, error) {
	res := ReindexResult{}
	e := s.embedderRef()
	if e == nil {
		rem, err := s.countMissingVectors()
		res.Remaining = rem
		if err != nil {
			return res, err
		}
		return res, errors.New("还没配置向量化通道，先配上再来补")
	}
	if batch <= 0 {
		batch = 200
	}
	for {
		todo, err := s.chunksMissingVectors(batch)
		if err != nil {
			return res, err
		}
		if len(todo) == 0 {
			break
		}
		texts := make([]string, len(todo))
		for i, c := range todo {
			// 与写入时保持一致：只向量化正文（写入时也是拿分块正文去算的）
			texts[i] = c.Content
		}
		vecs, err := e.Embed(ctx, texts)
		if err != nil {
			return res, fmt.Errorf("向量化失败（已补 %d 条）：%w", res.Done, err)
		}
		for i, c := range todo {
			if i >= len(vecs) || len(vecs[i]) == 0 {
				res.Failed++
				continue
			}
			if err := s.setVector(c.ID, vecs[i], e.Name()); err != nil {
				return res, err
			}
			res.Done++
		}
		if len(todo) < batch {
			break
		}
	}
	rem, err := s.countMissingVectors()
	if err != nil {
		return res, err
	}
	res.Remaining = rem
	return res, nil
}

/* ---------- 整理已经堆在库里的重复记忆 ---------- */

// CompactResult 整理结果
type CompactResult struct {
	Groups int `json:"groups"` // 发现几组重复
	Merged int `json:"merged"` // 合并掉几条
	Kept   int `json:"kept"`   // 留下几条（并把键改成稳定的那把）
}

// CompactDuplicates 把已经堆在库里的重复记忆合并掉，每组只留最新的一条。
//
// 处理两类（都靠"标题就是这一组东西的身份"来判断）：
//  1. 跨端任务回执（source=device，标题「跨端任务：<动作> @<设备>」）——
//     todo.list 跑六次就是六条几乎一样的；
//  2. 同一目标的运行结论（source=agent、kind=task，标题「任务：<目标>」）——
//     同一天反复问同一件事会留下好几条近似记录。
//     标题被 brief 截断过（结尾是「…」）的一律跳过：那种标题不唯一，合并会误伤。
//
// 留下那条的文档键会被换成稳定键，于是以后写进来的是**覆盖**它，不会再堆。
// 逐次历史在「运行记录 / 任务与审批」里一直都有。
//
// 这是删除操作，所以只由人手动触发（界面上的「整理重复记忆」），绝不自动跑。
func (s *Store) CompactDuplicates() (CompactResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := CompactResult{}
	a, err := s.compactGroup(`source='device' AND title LIKE '跨端任务：%'`,
		func(title string, _ int64) string { return deviceKeyFromTitle(title) })
	if err != nil {
		return res, err
	}
	b, err := s.compactGroup(`source='agent' AND kind='task' AND title LIKE '任务：%' AND title NOT LIKE '%…'`,
		func(title string, at int64) string { return runGoalKeyFromTitle(title, at) })
	if err != nil {
		return res, err
	}
	res.Groups, res.Merged, res.Kept = a.Groups+b.Groups, a.Merged+b.Merged, a.Kept+b.Kept
	return res, nil
}

// compactGroup 按标题分组，每组只留最新的那条，并把它的键换成 keyFn 给的稳定键
func (s *Store) compactGroup(where string, keyFn func(title string, at int64) string) (CompactResult, error) {
	// 按 updated_at 排（不是 created_at）：updated_at 是严格递增的写入戳，
	// 同一毫秒里连着写六次也能分得清谁最后写；created_at 会打平，顺序随数据库心情。
	rows, err := s.db.Query(`SELECT id, title, updated_at FROM chunks WHERE ` + where +
		` ORDER BY title, updated_at DESC`)
	if err != nil {
		return CompactResult{}, err
	}
	type grp struct {
		ids []int64
		at  int64 // 组里最新那条的时间（列表已按 updated_at 倒序，取第一个就是）
	}
	groups := map[string]*grp{}
	order := []string{}
	for rows.Next() {
		var id, at int64
		var title string
		if err := rows.Scan(&id, &title, &at); err != nil {
			rows.Close()
			return CompactResult{}, err
		}
		g, ok := groups[title]
		if !ok {
			g = &grp{at: at}
			groups[title] = g
			order = append(order, title)
		}
		g.ids = append(g.ids, id)
	}
	rows.Close()

	res := CompactResult{}
	for _, title := range order {
		g := groups[title]
		if g == nil || len(g.ids) == 0 {
			continue
		}
		if len(g.ids) > 1 {
			res.Groups++
		}
		for _, id := range g.ids[1:] {
			if _, err := s.db.Exec(`DELETE FROM chunks WHERE id=?`, id); err != nil {
				return res, err
			}
			res.Merged++
		}
		// 把留下那条的键换成稳定键：以后写进来的是覆盖它，不会再堆
		if key := keyFn(title, g.at); key != "" {
			if _, err := s.db.Exec(`UPDATE chunks SET doc_key=?, updated_at=? WHERE id=?`,
				key, time.Now().UnixMilli(), g.ids[0]); err != nil {
				return res, err
			}
		}
		res.Kept++
	}
	return res, nil
}

// deviceKeyFromTitle 从「跨端任务：<动作> @<设备>」里还原出稳定键；格式不对就返回空
func deviceKeyFromTitle(title string) string {
	body := strings.TrimPrefix(strings.TrimSpace(title), "跨端任务：")
	at := strings.LastIndex(body, " @")
	if at <= 0 {
		return ""
	}
	action := strings.TrimSpace(body[:at])
	device := strings.TrimSpace(body[at+2:])
	if action == "" || device == "" {
		return ""
	}
	return DeviceTaskKey(device, action)
}

// runGoalKeyFromTitle 从「任务：<目标>」里还原出稳定键（标题没被截断过才算数）
func runGoalKeyFromTitle(title string, at int64) string {
	goal := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(title), "任务："))
	if goal == "" || strings.HasSuffix(goal, "…") {
		return ""
	}
	return RunGoalKey(goal, at)
}

// embedModelName 当前向量通道的名字（没配就是空）
func (s *Store) embedModelName() string {
	if e := s.embedderRef(); e != nil {
		return e.Name()
	}
	return ""
}

// vectorStateClause 判断"这条记忆的向量能不能用"。
//
// 两种算不能用：① 压根没有向量；② **向量是别的模型算的**。
// ② 特别隐蔽：换了模型之后维度往往对不上，余弦相似度恒为 0，检索会静默失效——
// 看着"有向量"，其实一条都搜不到。所以换模型后必须整库重算。
func vectorStateClause(model string) (string, []any) {
	if model == "" {
		return "(embedding IS NULL OR length(embedding)=0)", nil
	}
	return "(embedding IS NULL OR length(embedding)=0 OR embedding_model IS NULL OR embedding_model<>?)",
		[]any{model}
}

// countMissingVectors 当前模型下"还没有可用向量"的条数
func (s *Store) countMissingVectors() (int, error) {
	clause, args := vectorStateClause(s.embedModelName())
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM chunks WHERE `+clause, args...).Scan(&n)
	return n, err
}

func (s *Store) chunksMissingVectors(limit int) ([]Chunk, error) {
	clause, args := vectorStateClause(s.embedModelName())
	args = append(args, limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id,title,content FROM chunks WHERE `+clause+` ORDER BY id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chunk{}
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.Title, &c.Content); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) setVector(id int64, vec []float32, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE chunks SET embedding=?,embedding_model=?,updated_at=? WHERE id=?`,
		llm.EncodeVec(vec), model, time.Now().UnixMilli(), id)
	return err
}

/* ---------- 遗忘 ---------- */

// Forget 忘掉一条/一组记忆。id > 0 时按 id 删；否则按 (分区, 文档键) 删。
// 删了就是真删（记忆要能被清掉，否则隐私上说不通）；tree 摘要由 RebuildTree 重算。
func (s *Store) Forget(namespace, docKey string, id int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case id > 0:
		r, err := s.db.Exec(`DELETE FROM chunks WHERE id=?`, id)
		if err != nil {
			return 0, err
		}
		n, _ := r.RowsAffected()
		return int(n), nil
	case strings.TrimSpace(docKey) != "":
		r, err := s.db.Exec(`DELETE FROM chunks WHERE namespace=? AND doc_key=?`,
			NormalizeNamespace(namespace), strings.TrimSpace(docKey))
		if err != nil {
			return 0, err
		}
		n, _ := r.RowsAffected()
		return int(n), nil
	}
	return 0, errors.New("要忘掉哪条记忆：请给 id 或 docKey（文档键）")
}

/* ---------- 主动召回（auto recall） ---------- */

// Citation 召回结果的溯源信息（对齐 OpenHuman 的 MemoryCitation）
type Citation struct {
	ID        int64   `json:"id"`
	DocKey    string  `json:"docKey"`
	Namespace string  `json:"namespace"`
	Title     string  `json:"title"`
	Score     float64 `json:"score"`
	At        int64   `json:"at"`
	Snippet   string  `json:"snippet"`
}

// RecallOptions 主动召回参数
type RecallOptions struct {
	Text          string
	Namespace     string
	Profile       string
	BudgetTokens  int     // 注入上下文的总 token 预算
	MinVectorSim  float64 // 最低语义相似度（没配向量时该项不生效）
	RelativeFloor float64 // 相对分数下限：分数低于"最高分 × 该系数"的直接丢
	Limit         int
}

// RecallResult 主动召回结果
type RecallResult struct {
	Query      string     `json:"query"`
	Profile    string     `json:"profile"`
	VectorUsed bool       `json:"vectorUsed"`
	Note       string     `json:"note,omitempty"`
	Tokens     int        `json:"tokens"`
	Context    string     `json:"context"`
	Citations  []Citation `json:"citations"`
}

// AutoRecall 派活前自动想起相关记忆。
//
// 三道闸门（对齐 OpenHuman 的口径）：最低语义相似度、相对分数下限、token 预算。
// 一条都没过就返回空上下文——宁可不注入，也别塞一堆不相关的旧账进提示词。
func (s *Store) AutoRecall(ctx context.Context, opt RecallOptions) (RecallResult, error) {
	if opt.Limit <= 0 {
		opt.Limit = 20
	}
	cut := opt.MinVectorSim
	if cut <= 0 {
		cut = 0.35
	}
	floor := opt.RelativeFloor
	if floor <= 0 {
		floor = 0.6
	}
	out := RecallResult{Query: opt.Text}
	res, err := s.HybridSearch(ctx, HybridQuery{
		Text: opt.Text, Namespace: opt.Namespace, Profile: opt.Profile, Limit: opt.Limit,
	})
	if err != nil {
		return out, err
	}
	out.Profile = res.Profile
	out.VectorUsed = res.VectorUsed
	out.Note = res.VectorNote
	if len(res.Hits) == 0 {
		return out, nil
	}

	// 闸门一：语义相似度下限（只在真的用上向量时才卡）
	kept := make([]Hit, 0, len(res.Hits))
	cutByVector := 0
	for _, h := range res.Hits {
		if res.VectorUsed && h.Parts.Vector < cut {
			cutByVector++
			continue
		}
		kept = append(kept, h)
	}
	if len(kept) == 0 {
		if cutByVector > 0 {
			// 别静默不注入：说清楚"其实有相关记忆，是被阈值挡了"，用户才知道去调阈值
			out.Note = fmt.Sprintf("有 %d 条检索结果，但语义相似度都低于阈值 %.2f，本次没有注入（阈值可在配置 memory.recallMinScore 里调低）",
				cutByVector, cut)
		}
		return out, nil
	}
	// 闸门二：相对分数下限（最高分的一个比例以下的都不要）
	best := kept[0].Score
	filtered := make([]Hit, 0, len(kept))
	for _, h := range kept {
		if h.Score+1e-9 < best*floor {
			continue
		}
		filtered = append(filtered, h)
	}

	// 闸门三：token 预算
	budget := opt.BudgetTokens
	if budget <= 0 {
		budget = 600
	}
	var b strings.Builder
	used := 0
	for _, h := range filtered {
		block := formatMemoryBlock(h)
		t := EstimateTokens(block)
		if used+t > budget {
			break
		}
		b.WriteString(block)
		used += t
		out.Citations = append(out.Citations, Citation{
			ID: h.Chunk.ID, DocKey: h.Chunk.DocKey, Namespace: h.Chunk.Namespace,
			Title: h.Chunk.Title, Score: h.Score, At: h.Chunk.CreatedAt,
			Snippet: truncateRunes(h.Chunk.Content, 80),
		})
	}
	out.Context = strings.TrimSpace(b.String())
	out.Tokens = used
	return out, nil
}

func formatMemoryBlock(h Hit) string {
	day := time.UnixMilli(h.Chunk.CreatedAt).Format("2006-01-02")
	body := truncateRunes(strings.TrimSpace(h.Chunk.Content), 300)
	return fmt.Sprintf("- [%s｜%s｜%.2f] %s：%s\n", day, h.Chunk.Kind, h.Score, h.Chunk.Title, body)
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

/* ---------- 概览与自检 ---------- */

// KindStat 一类记忆的条数
type KindStat struct {
	Namespace string `json:"namespace"`
	Category  string `json:"category"`
	Kind      string `json:"kind"`
	Count     int    `json:"count"`
}

// Kinds 列出记忆库里都有哪些分区/分类/类型（模型据此选对 namespace）
func (s *Store) Kinds() ([]KindStat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT namespace,category,kind,COUNT(*) FROM chunks
		GROUP BY namespace,category,kind ORDER BY namespace,category,kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KindStat{}
	for rows.Next() {
		var k KindStat
		if err := rows.Scan(&k.Namespace, &k.Category, &k.Kind, &k.Count); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DoctorReport 记忆库自检报告
type DoctorReport struct {
	Store            string   `json:"store"`
	Chunks           int      `json:"chunks"`
	Nodes            int      `json:"nodes"`
	Embedded         int      `json:"embedded"`
	MissingEmbedding int      `json:"missingEmbedding"` // 没有向量，或向量来自别的模型
	StaleModel       int      `json:"staleModel"`       // 其中"有向量但模型不对"的条数（换过模型时 >0）
	EmbeddingDim     int      `json:"embeddingDim"`
	EmbedderOn       bool     `json:"embedderOn"`
	EmbedderName     string   `json:"embedderName,omitempty"`
	Profiles         []string `json:"profiles"`
	Namespaces       []string `json:"namespaces"`
	Note             string   `json:"note,omitempty"`
}

// Doctor 自检：向量通道在不在、有多少条向量不可用、有哪些分区
func (s *Store) Doctor() (DoctorReport, error) {
	rep := DoctorReport{Store: s.Path(), Profiles: ProfileNames()}
	model := s.embedModelName()
	clause, args := vectorStateClause(model)
	s.mu.Lock()
	err := s.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN `+clause+` THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN embedding IS NOT NULL AND length(embedding)>0 THEN 1 ELSE 0 END),0)
		FROM chunks`, args...).Scan(&rep.Chunks, &rep.MissingEmbedding, &rep.Embedded)
	if err != nil {
		s.mu.Unlock()
		return rep, err
	}
	rep.StaleModel = rep.Embedded - (rep.Chunks - rep.MissingEmbedding)
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&rep.Nodes); err != nil {
		s.mu.Unlock()
		return rep, err
	}
	rows, err := s.db.Query(`SELECT DISTINCT namespace FROM chunks ORDER BY namespace`)
	if err == nil {
		for rows.Next() {
			var ns string
			if err := rows.Scan(&ns); err == nil {
				rep.Namespaces = append(rep.Namespaces, ns)
			}
		}
		rows.Close()
	}
	s.mu.Unlock()

	if e := s.embedderRef(); e != nil {
		rep.EmbedderOn = true
		rep.EmbedderName = e.Name()
		rep.EmbeddingDim = e.Dim()
		switch {
		case rep.StaleModel > 0:
			rep.Note = fmt.Sprintf("有 %d 条记忆的向量是别的模型算的（当前模型 %s），维度对不上、余弦恒为 0 —— 请点一次「给旧记忆补向量」重算",
				rep.StaleModel, model)
		case rep.MissingEmbedding > 0:
			rep.Note = fmt.Sprintf("有 %d 条记忆还没有向量，语义检索搜不到它们；点一次「给旧记忆补向量」即可", rep.MissingEmbedding)
		}
	} else {
		rep.Note = "未配置 embedding 通道：语义检索与「最低相似度」闸门都不生效，检索按关键词 + 新鲜度打分"
	}
	return rep, nil
}
