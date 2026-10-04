package memory

import (
	"context"
	"database/sql"
	"hash/fnv"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

/* ---------- 一个确定性的假向量通道 ---------- */

// fakeEmbedder 词袋哈希向量：同样的词越多向量越像，够用来验证"语义那条路真的通了"
type fakeEmbedder struct {
	dim  int
	name string
}

func (f *fakeEmbedder) Name() string {
	if f.name != "" {
		return f.name
	}
	return "fake-embed"
}
func (f *fakeEmbedder) Dim() int { return f.dim }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		out = append(out, bagVector(t, f.dim))
	}
	return out, nil
}

func bagVector(s string, dim int) []float32 {
	v := make([]float32, dim)
	for _, tok := range Tokenize(s) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		v[int(h.Sum32())%dim] += 1
	}
	return v
}

func openStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开记忆库失败：%v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

/* ---------- 结构演进 ---------- */

// 老库（只有基础列）打开后必须能自动补列并把老数据读出来，不能报错、也不能丢数据
func TestMigrationFromOldSchema(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "memory.db")) + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("建老库失败：%v", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatalf("建老表失败：%v", err)
	}
	if _, err := db.Exec(`INSERT INTO chunks(source,source_ref,kind,title,content,tokens,importance,recency,
		richness,score,hits,node_id,created_at,updated_at,meta)
		VALUES('agent','run:old','note','老记忆','这是升级前写进去的内容',10,0.5,1,0.3,0.4,0,0,1000,1000,'{}')`); err != nil {
		db.Close()
		t.Fatalf("写老数据失败：%v", err)
	}
	db.Close()

	st, err := Open(dir)
	if err != nil {
		t.Fatalf("打开老库失败：%v", err)
	}
	defer st.Close()
	hits, err := st.Search("升级前", 5)
	if err != nil {
		t.Fatalf("老库检索失败：%v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("老数据应该还能查到：%+v", hits)
	}
	if hits[0].Chunk.Namespace != DefaultNamespace || hits[0].Chunk.Category != CategoryCore {
		t.Fatalf("老数据的默认分区/分类不对：%+v", hits[0].Chunk)
	}
	if hits[0].Chunk.HasVector {
		t.Fatal("老数据不该被当成有向量")
	}
	// 老数据要补上文档键，否则"再记一遍同一件事"会堆成两条
	if hits[0].Chunk.DocKey == "" {
		t.Fatal("迁移时应给老数据补上文档键")
	}
	// 再写一遍老内容：应当覆盖那条老数据，而不是新增
	res, err := st.Ingest("这是升级前写进去的内容", IngestOptions{})
	if err != nil {
		t.Fatalf("再写失败：%v", err)
	}
	if res.Replaced != 1 || res.Chunks != 1 {
		t.Fatalf("老数据应被同键覆盖：%+v", res)
	}
	var total int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("覆盖后库里应只剩 1 条，实际 %d", total)
	}
}

/* ---------- 分区 / 分类 / 文档键 ---------- */

func TestNamespaceCategoryAndKeyOverwrite(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	if _, err := st.Ingest("部署方式：KNative", IngestOptions{
		Namespace: "team-a", Category: CategoryCore, DocKey: "deploy", Title: "部署",
	}); err != nil {
		t.Fatalf("写失败：%v", err)
	}
	// 同一把键再写 = 覆盖，不是追加
	res, err := st.Ingest("部署方式：systemd + NAS", IngestOptions{
		Namespace: "team-a", Category: CategoryCore, DocKey: "deploy", Title: "部署",
	})
	if err != nil {
		t.Fatalf("覆盖写失败：%v", err)
	}
	if res.Replaced != 1 || res.Chunks != 1 {
		t.Fatalf("同键应覆盖旧条：%+v", res)
	}
	var total int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM chunks WHERE namespace='team-a' AND doc_key='deploy'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("同键只应留一条，实际 %d", total)
	}

	// 分区隔离：team-b 里看不到 team-a 的东西
	if _, err := st.Ingest("部署方式：老古董方案", IngestOptions{
		Namespace: "team-b", DocKey: "deploy", Title: "部署",
	}); err != nil {
		t.Fatalf("写 team-b 失败：%v", err)
	}
	a, err := st.HybridSearch(ctx, HybridQuery{Text: "部署方式", Namespace: "team-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Hits) != 1 || a.Hits[0].Chunk.Namespace != "team-a" {
		t.Fatalf("分区隔离失效：%+v", a.Hits)
	}
	if a.Hits[0].Chunk.Content != "部署方式：systemd + NAS" {
		t.Fatalf("team-a 里应是覆盖后的新内容：%+v", a.Hits[0].Chunk)
	}

	// 非法分区名收敛到 default，非法分类收敛到 core（而不是报错丢内容）
	r2, err := st.Ingest("随便记一条", IngestOptions{Namespace: "有空格 不行", Category: "乱写的分类"})
	if err != nil {
		t.Fatalf("非法分区不该让写入失败：%v", err)
	}
	if r2.Chunks != 1 {
		t.Fatalf("应照常写入：%+v", r2)
	}
	var ns, cat string
	if err := st.db.QueryRow(`SELECT namespace,category FROM chunks WHERE id=(SELECT MAX(id) FROM chunks)`).Scan(&ns, &cat); err != nil {
		t.Fatal(err)
	}
	if ns != DefaultNamespace || cat != CategoryCore {
		t.Fatalf("非法值应收敛：ns=%q cat=%q", ns, cat)
	}
}

// 自动生成的键：一样的内容 → 一样的键（避免同一条记忆越堆越多）
func TestAutoKeyIsStableAndOverwrites(t *testing.T) {
	if MakeKey("同一段内容") != MakeKey("同一段内容") {
		t.Fatal("同样的内容必须得到同样的键")
	}
	if MakeKey("A") == MakeKey("B") {
		t.Fatal("不同内容不该撞键")
	}
	st := openStore(t)
	if _, err := st.Ingest("项目代号白泽", IngestOptions{}); err != nil {
		t.Fatal(err)
	}
	res, err := st.Ingest("项目代号白泽", IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Replaced != 1 {
		t.Fatalf("同内容再写应按自动键覆盖：%+v", res)
	}
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n)
	if n != 1 {
		t.Fatalf("库里应只剩 1 条，实际 %d", n)
	}
}

/* ---------- 混合打分与档位 ---------- */

func TestHybridProfilesAndParts(t *testing.T) {
	st := openStore(t)
	st.SetEmbedder(&fakeEmbedder{dim: 64})
	ctx := context.Background()

	seed := []struct{ text, ns, ref string }{
		// 前两条来自同一份文档（同 sourceRef）→ 它们之间应当有联想分
		{"部署方式：白泽后端跑在 NAS 上用 systemd 管理", "ops", "doc:deploy"},
		{"部署方式：手机端用 Go 编译成静态二进制", "ops", "doc:deploy"},
		{"今天天气很好适合出门散步", "life", ""},
	}
	for _, s := range seed {
		if _, err := st.Ingest(s.text, IngestOptions{Namespace: s.ns, Title: firstLine(s.text), SourceRef: s.ref}); err != nil {
			t.Fatal(err)
		}
	}

	res, err := st.HybridSearch(ctx, HybridQuery{Text: "部署方式怎么做的", Limit: 5})
	if err != nil {
		t.Fatalf("混合检索失败：%v", err)
	}
	if !res.VectorUsed {
		t.Fatalf("配了向量通道就该用上：%+v", res)
	}
	if len(res.Hits) == 0 {
		t.Fatal("应该检索到部署相关的记忆")
	}
	top := res.Hits[0]
	if top.Parts.Keyword <= 0 && top.Parts.Vector <= 0 {
		t.Fatalf("命中的分项分应至少有一项 > 0：%+v", top.Parts)
	}
	if top.Score <= 0 || top.Score > 1 {
		t.Fatalf("分数应落在 0..1：%v", top.Score)
	}
	if top.Parts.Graph <= 0 {
		t.Fatalf("同一份文档（同 sourceRef）的两条之间应有联想分：%+v", top.Parts)
	}
	// 没有 sourceRef 的记忆不该凭空获得联想分（否则"同一天"会让所有人满分，把排序压平）
	weather, err := st.HybridSearch(ctx, HybridQuery{Text: "出门散步天气", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(weather.Hits) == 0 {
		t.Fatal("应该能按关键词搜到天气那条")
	}
	if weather.Hits[0].Chunk.Title != "今天天气很好适合出门散步" {
		t.Fatalf("天气查询应先命中天气那条：%+v", weather.Hits[0].Chunk.Title)
	}
	if weather.Hits[0].Parts.Graph != 0 {
		t.Fatalf("没有 sourceRef 就不该有联想分：%+v", weather.Hits[0].Parts)
	}

	// 四种档位都要能跑，且权重归一（向量开着的四档相加应为 1）
	for _, p := range ProfileNames() {
		r, err := st.HybridSearch(ctx, HybridQuery{Text: "部署", Profile: p, Limit: 3})
		if err != nil {
			t.Fatalf("档位 %s 检索失败：%v", p, err)
		}
		w := r.Weights
		if sum := w.Graph + w.Vector + w.Keyword + w.Freshness; sum < 0.99 || sum > 1.01 {
			t.Fatalf("档位 %s 权重没归一：%+v（和 %v）", p, w, sum)
		}
		if r.Profile != p {
			t.Fatalf("档位回显不对：%v", r.Profile)
		}
	}
	// 非法档位回落 balanced（而不是报错或乱算）
	r, err := st.HybridSearch(ctx, HybridQuery{Text: "部署", Profile: "不存在的档位"})
	if err != nil || r.Profile != "balanced" {
		t.Fatalf("非法档位应回落 balanced：%v %v", r.Profile, err)
	}
}

// 没配向量通道时：必须明确说明，并把向量权重让出去（分数仍是 0..1）
func TestHybridWithoutEmbedderDegradesHonestly(t *testing.T) {
	st := openStore(t)
	if _, err := st.Ingest("部署方式：白泽后端跑在 NAS 上", IngestOptions{Title: "部署"}); err != nil {
		t.Fatal(err)
	}
	res, err := st.HybridSearch(context.Background(), HybridQuery{Text: "部署", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.VectorUsed {
		t.Fatal("没配通道就不该声称用上了语义检索")
	}
	if res.VectorNote == "" {
		t.Fatal("必须明确说明这次没用上语义检索")
	}
	w := res.Weights
	if w.Vector != 0 {
		t.Fatalf("没向量时向量权重应为 0：%+v", w)
	}
	if sum := w.Graph + w.Keyword + w.Freshness; sum < 0.99 || sum > 1.01 {
		t.Fatalf("让出的权重应分给其它三项：%+v", w)
	}
	if len(res.Hits) == 0 || res.Hits[0].Parts.Vector != 0 {
		t.Fatalf("没向量时语义分应为 0：%+v", res.Hits)
	}
}

// 纯语义检索：关键词完全不重合也要能按语义找到
func TestVectorOnlySearch(t *testing.T) {
	st := openStore(t)
	st.SetEmbedder(&fakeEmbedder{dim: 128})
	ctx := context.Background()
	if _, err := st.Ingest("白泽后端的部署方式是 systemd 守护进程", IngestOptions{Title: "部署"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ingest("今天天气很好适合出门散步", IngestOptions{Title: "天气"}); err != nil {
		t.Fatal(err)
	}
	res, err := st.HybridSearch(ctx, HybridQuery{Text: "部署方式", VectorOnly: true, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !res.VectorUsed {
		t.Fatal("纯语义检索必须真的用上向量")
	}
	if len(res.Hits) == 0 {
		t.Fatal("应该按语义找到部署那条")
	}
	if res.Hits[0].Chunk.Title != "部署" {
		t.Fatalf("语义上最像的应是部署：%+v", res.Hits[0].Chunk.Title)
	}
	if res.Hits[0].Parts.Keyword != 0 {
		t.Fatalf("纯语义检索不该混入关键词分：%+v", res.Hits[0].Parts)
	}
	// 无关的那条即便有微弱的哈希碰撞分，也必须排在后面
	for _, h := range res.Hits[1:] {
		if h.Score >= res.Hits[0].Score {
			t.Fatalf("不相关的记忆不该排到相关的前面：%+v", res.Hits)
		}
	}
	// 设了分数下限就能把它滤掉（证明 minScore 真的在起作用）
	cut, err := st.HybridSearch(ctx, HybridQuery{
		Text: "部署方式", VectorOnly: true, Limit: 5, MinScore: res.Hits[0].Score,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cut.Hits) != 1 {
		t.Fatalf("设了分数下限后应只剩最有名那条：%+v", cut.Hits)
	}
}

/* ---------- 语义下限（不相关就别硬凑） ---------- */

// 向量模型对任何两段中文都会给出 0.3~0.45 的"底噪相似度"（百炼实测），
// 不卡一道的话，无关的问题也能把 limit 塞满，看着像是"搜到了"。
func TestMinVectorGatesWeakMatches(t *testing.T) {
	st := openStore(t)
	st.SetEmbedder(&fakeEmbedder{dim: 128})
	ctx := context.Background()
	for _, s := range []string{
		"白泽后端部署在 NAS 上，用 systemd 守护，端口 8787",
		"今天天气很好，适合出门散步",
	} {
		if _, err := st.Ingest(s, IngestOptions{Title: firstLine(s)}); err != nil {
			t.Fatal(err)
		}
	}
	const q = "部署在哪儿"

	loose, err := st.HybridSearch(ctx, HybridQuery{Text: q, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(loose.Hits) == 0 {
		t.Fatal("不卡阈值时应该能捞到东西")
	}
	if loose.Filtered != 0 {
		t.Fatalf("没设阈值就不该有被挡掉的：%+v", loose)
	}
	best := 0.0
	for _, h := range loose.Hits {
		if h.Parts.Vector > best {
			best = h.Parts.Vector
		}
	}
	if best <= 0 {
		t.Fatal("配了向量通道就该给出语义分")
	}

	// 阈值放到比最高相似度还高 → 全挡掉，并且要如实报告挡了几条、为什么
	strict, err := st.HybridSearch(ctx, HybridQuery{Text: q, Limit: 10, MinVector: best + 0.01})
	if err != nil {
		t.Fatal(err)
	}
	if len(strict.Hits) != 0 {
		t.Fatalf("阈值高于最高相似度时应全部挡掉：%+v", strict.Hits)
	}
	if strict.Filtered == 0 {
		t.Fatal("被阈值挡掉时要如实报条数")
	}
	if strict.VectorNote == "" {
		t.Fatal("全被挡掉时必须说明原因，不能静默返回空")
	}

	// 阈值放回最高相似度以下 → 最相关的那条能过，且放行的每一条都得够上阈值
	back, err := st.HybridSearch(ctx, HybridQuery{Text: q, Limit: 10, MinVector: best - 0.01})
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Hits) == 0 {
		t.Fatal("阈值低于最高相似度时，最相关的那条不该被挡")
	}
	for _, h := range back.Hits {
		if h.Parts.Vector < best-0.01 {
			t.Fatalf("放行的都必须够上阈值：%+v", h)
		}
	}
}

// 没配向量通道时，语义下限不生效（否则关键词检索会被一个用不上的阈值整死）
func TestMinVectorIgnoredWithoutEmbedder(t *testing.T) {
	st := openStore(t)
	if _, err := st.Ingest("白泽后端部署在 NAS 上", IngestOptions{Title: "部署"}); err != nil {
		t.Fatal(err)
	}
	res, err := st.HybridSearch(context.Background(), HybridQuery{Text: "部署", Limit: 5, MinVector: 0.99})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("没有向量通道时语义下限不该生效：%+v", res)
	}
}

/* ---------- 关键词的 IDF 加权 ---------- */

func TestIDFWeightsRareOverCommon(t *testing.T) {
	chunks := []Chunk{
		{Title: "任务", Content: "任务 任务 任务 把待办念出来"},
		{Title: "任务", Content: "任务 改一下密码本的加密方式"},
	}
	idf := idfWeights(chunks, []string{"任务", "密码"})
	if idf["密码"] <= idf["任务"] {
		t.Fatalf("稀有词权重应高于常见词：密码=%v 任务=%v", idf["密码"], idf["任务"])
	}
	for w := range idf {
		if idf[w] <= 0 || idf[w] > 1 {
			t.Fatalf("权重应归一到 0..1：%v=%v", w, idf[w])
		}
	}
	// 库里压根没有的词不进 idf（matchScore 靠这个把它排除在分母外）
	if _, ok := idfWeights(chunks, []string{"火星"})["火星"]; ok {
		t.Fatal("库里没出现过的词不该进 idf")
	}
}

// 查询里带一个库里不存在的词，不该把其它结果的关键词分一起拉低
func TestKeywordIgnoresTermsAbsentFromStore(t *testing.T) {
	st := openStore(t)
	if _, err := st.Ingest("白泽后端部署在 NAS 上，端口 8787", IngestOptions{Title: "部署方式"}); err != nil {
		t.Fatal(err)
	}
	hit, err := st.HybridSearch(context.Background(), HybridQuery{Text: "部署方式", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	withNoise, err := st.HybridSearch(context.Background(), HybridQuery{Text: "部署方式 火星探测器", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hit.Hits) != 1 || len(withNoise.Hits) != 1 {
		t.Fatalf("两条路径都应只命中那一条：%+v / %+v", hit.Hits, withNoise.Hits)
	}
	if hit.Hits[0].Parts.Keyword != withNoise.Hits[0].Parts.Keyword {
		t.Fatalf("库里不存在的词不该影响关键词分：%v vs %v",
			hit.Hits[0].Parts.Keyword, withNoise.Hits[0].Parts.Keyword)
	}
}

/* ---------- 同类记忆不堆积（文档键治理） ---------- */

// 跨端任务回执：同一设备同一动作只留最新一条。
// 治理前实测 11 条记忆里有 6 条是同一个 todo.list 的回执，检索结果被稀释得厉害。
func TestDeviceTaskKeyCollapsesRepeatedReceipts(t *testing.T) {
	st := openStore(t)
	opt := func(times int) IngestOptions {
		return IngestOptions{
			Source: "device", SourceRef: "task:tm" + strconv.Itoa(times), Kind: "task",
			Title: "跨端任务：todo.list @phone-abc123", Category: CategoryDaily,
			DocKey: DeviceTaskKey("phone-abc123", "todo.list"),
		}
	}
	for i := 1; i <= 6; i++ {
		if _, err := st.Ingest("设备 phone-abc123；动作 todo.list；任务 tm"+strconv.Itoa(i)+"；状态 done",
			opt(i)); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("同一设备同一动作重复落盘后应只剩 1 条，实际 %d", n)
	}
	last, err := st.Search("todo.list 回执", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 1 || !strings.Contains(last[0].Chunk.Content, "tm6") {
		t.Fatalf("留下的应是最后一次回执：%+v", last)
	}
	// 换一个动作 / 换一台设备 = 另一把键，不能互相覆盖
	if DeviceTaskKey("phone-abc123", "todo.list") == DeviceTaskKey("phone-abc123", "device.info") {
		t.Fatal("不同动作应是不同的键")
	}
	if DeviceTaskKey("phone-a", "todo.list") == DeviceTaskKey("phone-b", "todo.list") {
		t.Fatal("不同设备应是不同的键")
	}
}

// 同一天重复跑同一个目标 → 只留最新结论；换一天则各留一份
func TestRunGoalKeyDedupesSameGoalSameDay(t *testing.T) {
	day1 := time.Date(2026, 3, 4, 10, 0, 0, 0, time.Local).UnixMilli()
	day2 := time.Date(2026, 3, 5, 10, 0, 0, 0, time.Local).UnixMilli()
	const goal = "看看我知识库里现在有几条待办"
	if RunGoalKey(goal, day1) != RunGoalKey(goal, day1) {
		t.Fatal("同一天同一个目标必须得到同一把键")
	}
	if RunGoalKey(goal, day1) == RunGoalKey(goal, day2) {
		t.Fatal("跨天不该共用一把键")
	}
	if RunGoalKey(goal, day1) == RunGoalKey("换一个目标", day1) {
		t.Fatal("不同目标不该共用一把键")
	}

	st := openStore(t)
	for i := 0; i < 2; i++ {
		if _, err := st.Ingest("任务："+goal+"\n结论：第 "+strconv.Itoa(i+1)+" 次跑的结论",
			IngestOptions{Source: "agent", SourceRef: "run:r" + strconv.Itoa(i), Kind: "task",
				Title: "任务：" + goal, DocKey: RunGoalKey(goal, day1)}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("同一天同目标重复跑应只剩 1 条，实际 %d", n)
	}
}

// 整理历史遗留的重复记忆：每组只留最新一条，并把键换成稳定键（以后不再堆）
func TestCompactDuplicates(t *testing.T) {
	st := openStore(t)
	// 6 条 todo.list 回执（历史遗留：键是按内容算的，所以各存一条）
	for i := 1; i <= 6; i++ {
		if _, err := st.Ingest("设备 phone-abc123；动作 todo.list；任务 tm"+strconv.Itoa(i)+"；状态 done",
			IngestOptions{Source: "device", SourceRef: "task:tm" + strconv.Itoa(i),
				Kind: "task", Title: "跨端任务：todo.list @phone-abc123", Category: CategoryDaily}); err != nil {
			t.Fatal(err)
		}
	}
	// 另一台设备的、以及一条不该被碰的 agent 记忆
	if _, err := st.Ingest("设备 pc-1；动作 todo.list；状态 done",
		IngestOptions{Source: "device", SourceRef: "task:pc", Kind: "task",
			Title: "跨端任务：todo.list @pc-1", Category: CategoryDaily}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ingest("结论：密码本用 AES-GCM 加密",
		IngestOptions{Source: "agent", Title: "密码本方案", Kind: "decision"}); err != nil {
		t.Fatal(err)
	}

	res, err := st.CompactDuplicates()
	if err != nil {
		t.Fatal(err)
	}
	if res.Groups != 1 || res.Merged != 5 || res.Kept != 2 {
		t.Fatalf("整理结果不对：%+v", res)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("整理后应剩 3 条（每台设备一条 + agent 那条），实际 %d", n)
	}
	// 留下的那条要带稳定键，而且是最新一次的回执
	var content, key string
	if err := st.db.QueryRow(`SELECT content, doc_key FROM chunks WHERE source='device' AND title LIKE '%@phone-abc123'`).
		Scan(&content, &key); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "tm6") {
		t.Fatalf("留下的应是最新一次回执：%s", content)
	}
	if key != DeviceTaskKey("phone-abc123", "todo.list") {
		t.Fatalf("留下的那条要换成稳定键，实际 %q", key)
	}
	// 整理完再跑一次：已经没什么可合的了
	again, err := st.CompactDuplicates()
	if err != nil {
		t.Fatal(err)
	}
	if again.Merged != 0 {
		t.Fatalf("再整理不该再删东西：%+v", again)
	}
}

// 同一目标的运行结论也要合并；但标题被 brief 截断过（结尾「…」）的不合并——那种标题不唯一
func TestCompactMergesSameGoalRunsButSkipsTruncated(t *testing.T) {
	st := openStore(t)
	const goal = "看看我知识库里现在有几条待办"
	for i := 0; i < 2; i++ {
		if _, err := st.Ingest("任务："+goal+"\n结论：第 "+strconv.Itoa(i+1)+" 次跑的结论",
			IngestOptions{Source: "agent", Kind: "task", Title: "任务：" + goal}); err != nil {
			t.Fatal(err)
		}
	}
	// 两条被截断的标题（不同的长目标 brief 之后可能长一样）——不该被合并
	truncated := "任务：很长很长的目标" + strings.Repeat("啊", 10) + "…"
	for i := 0; i < 2; i++ {
		if _, err := st.Ingest("任务：很长很长的目标\n结论：第 "+strconv.Itoa(i+1)+" 次",
			IngestOptions{Source: "agent", Kind: "task", Title: truncated}); err != nil {
			t.Fatal(err)
		}
	}

	res, err := st.CompactDuplicates()
	if err != nil {
		t.Fatal(err)
	}
	if res.Groups != 1 || res.Merged != 1 {
		t.Fatalf("只该合并同目标那一组：%+v", res)
	}
	var truncatedLeft int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM chunks WHERE title=?`, truncated).Scan(&truncatedLeft); err != nil {
		t.Fatal(err)
	}
	if truncatedLeft != 2 {
		t.Fatalf("被截断的标题不能合并（怕误伤不同目标），实际剩 %d 条", truncatedLeft)
	}
	// 留下的那条要换成按目标算的稳定键
	var key string
	if err := st.db.QueryRow(`SELECT doc_key FROM chunks WHERE title=?`, "任务："+goal).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "run-goal-") {
		t.Fatalf("应换成按目标算的稳定键，实际 %q", key)
	}
}

/* ---------- 记忆树摘要的按需重建 ---------- */

func TestRebuildTreeIfStale(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if _, err := st.Ingest("白泽后端部署在 NAS 上用 systemd 守护", IngestOptions{}); err != nil {
		t.Fatal(err)
	}
	stale, err := st.StaleDays(8)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 {
		t.Fatalf("刚写完记忆，当天摘要应为空 → 算过期：%+v", stale)
	}

	sum := &fakeSummarizer{}
	n, err := st.RebuildTreeIfStale(ctx, sum, 8)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || sum.calls == 0 {
		t.Fatalf("应重建 1 天并真的调了摘要器：days=%d calls=%d", n, sum.calls)
	}
	// 重建完就不该再过期（否则后台每小时都会把同一份内容摘要一遍）
	again, err := st.StaleDays(8)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("重建后不该再过期：%+v", again)
	}
	calls := sum.calls
	if n2, err := st.RebuildTreeIfStale(ctx, sum, 8); err != nil || n2 != 0 || sum.calls != calls {
		t.Fatalf("没过期时一个模型调用都不该发：days=%d calls=%d err=%v", n2, sum.calls, err)
	}

	// 又写进新记忆 → 重新算过期
	if _, err := st.Ingest("又记了一件事：明天下午三点开会", IngestOptions{}); err != nil {
		t.Fatal(err)
	}
	back, err := st.StaleDays(8)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 {
		t.Fatalf("有新记忆写入后应重新算过期：%+v", back)
	}
}

/* ---------- MMR 多样性 ---------- */

func TestDiverseReducesDuplicates(t *testing.T) {
	st := openStore(t)
	st.SetEmbedder(&fakeEmbedder{dim: 64})
	ctx := context.Background()
	// 三条几乎一样的 + 一条不同角度的
	for _, s := range []string{
		"部署方式：用 systemd 管理白泽后端进程",
		"部署方式：用 systemd 管理白泽后端服务",
		"部署方式：systemd 托管白泽后端",
		"回滚方式：保留上一版二进制并改软链",
	} {
		if _, err := st.Ingest(s, IngestOptions{Title: "部署"}); err != nil {
			t.Fatal(err)
		}
	}
	plain, err := st.HybridSearch(ctx, HybridQuery{Text: "部署方式", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	div, err := st.HybridSearch(ctx, HybridQuery{Text: "部署方式", Limit: 2, Diverse: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(div.Hits) != 2 {
		t.Fatalf("多样性检索应返回 2 条：%+v", div.Hits)
	}
	if div.Hits[0].Chunk.DocKey == div.Hits[1].Chunk.DocKey {
		t.Fatal("多样性检索不该返回同一把键的两条")
	}
	if len(plain.Hits) > 0 && len(div.Hits) > 0 && plain.Hits[0].Chunk.DocKey == div.Hits[0].Chunk.DocKey && div.Hits[1].Score > plain.Hits[1].Score {
		t.Fatal("MMR 只是在牺牲一点分数换多样性，不该反而更高")
	}
}

/* ---------- 遗忘 ---------- */

func TestForget(t *testing.T) {
	st := openStore(t)
	if _, err := st.Ingest("要忘掉的内容", IngestOptions{DocKey: "tmp-key", Title: "临时"}); err != nil {
		t.Fatal(err)
	}
	n, err := st.Forget(DefaultNamespace, "tmp-key", 0)
	if err != nil || n != 1 {
		t.Fatalf("按文档键删除失败：n=%d err=%v", n, err)
	}
	if n, err := st.Forget(DefaultNamespace, "tmp-key", 0); err != nil || n != 0 {
		t.Fatalf("再删一次应删到 0 条：n=%d err=%v", n, err)
	}
	if _, err := st.Forget("", "", 0); err == nil {
		t.Fatal("既不给 id 也不给 docKey 必须报错")
	}

	res, err := st.Ingest("另一条要按 id 删的", IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := st.db.QueryRow(`SELECT id FROM chunks ORDER BY id DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if n, err := st.Forget("", "", id); err != nil || n != 1 {
		t.Fatalf("按 id 删除失败：n=%d err=%v", n, err)
	}
	_ = res
}

/* ---------- 主动召回的三道闸门 ---------- */

func TestAutoRecallGates(t *testing.T) {
	st := openStore(t)
	st.SetEmbedder(&fakeEmbedder{dim: 128})
	ctx := context.Background()
	if _, err := st.Ingest("白泽后端部署在 NAS 上，用 systemd 守护", IngestOptions{Title: "部署"}); err != nil {
		t.Fatal(err)
	}

	// 阈值以下要能召回（相似度的绝对值跟具体向量模型有关，测试里不假设它的量纲）
	res, err := st.AutoRecall(ctx, RecallOptions{Text: "部署在哪", BudgetTokens: 200, MinVectorSim: 0.1})
	if err != nil {
		t.Fatalf("召回失败：%v", err)
	}
	if res.Context == "" || len(res.Citations) == 0 {
		t.Fatalf("应召回部署那条：%+v", res)
	}
	if res.Citations[0].Title != "部署" || res.Citations[0].Score <= 0 {
		t.Fatalf("溯源信息不对：%+v", res.Citations[0])
	}

	// 相关记忆被阈值挡掉时必须**说清楚**，不能静默不注入
	blocked, err := st.AutoRecall(ctx, RecallOptions{Text: "部署在哪", MinVectorSim: 0.9999})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked.Citations) != 0 {
		t.Fatalf("阈值没到就不该注入：%+v", blocked)
	}
	if blocked.Note == "" {
		t.Fatal("被阈值挡掉时要说明白，否则用户只会觉得「记忆没生效」")
	}

	// 闸门一：相似度门槛拉到 1.0 以上 → 一条都不要（宁可不注入）
	strict, err := st.AutoRecall(ctx, RecallOptions{Text: "部署在哪", MinVectorSim: 0.999})
	if err != nil {
		t.Fatal(err)
	}
	if len(strict.Citations) != 0 || strict.Context != "" {
		t.Fatalf("相似度不达标就该空手而归：%+v", strict)
	}

	// 闸门三：预算只有 1 token → 塞不进任何一条
	tiny, err := st.AutoRecall(ctx, RecallOptions{Text: "部署在哪", BudgetTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if tiny.Context != "" || tiny.Tokens != 0 {
		t.Fatalf("预算不够就不该硬塞：%+v", tiny)
	}

	// 闸门二：相对分数下限拉到 1.0 以上，只留最高分那条
	only, err := st.AutoRecall(ctx, RecallOptions{Text: "部署在哪", RelativeFloor: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if len(only.Citations) > 1 {
		t.Fatalf("相对下限应只留最高分：%+v", only.Citations)
	}
}

/* ---------- 存量记忆补向量 ---------- */

// 先按关键词跑一阵、之后才配好向量通道：存量记忆必须能补上向量，否则语义检索永远搜不到它们
func TestReindexBackfillsOldMemories(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	// 没有向量通道时写入 → 这些条没有向量
	if _, err := st.Ingest("白泽后端部署在 NAS 上，用 systemd 守护", IngestOptions{Title: "部署"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ingest("密码本用 PBKDF2 加 AES-GCM 加密", IngestOptions{Title: "加密"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.Stats(); s.Missing != 2 || s.Embedded != 0 {
		t.Fatalf("此时应一条向量都没有：%+v", s)
	}
	// 没配通道就补：要明确报错，并且如实报出还差多少
	if _, err := st.ReindexEmbeddings(ctx, 0); err == nil {
		t.Fatal("没配通道时补向量应报错")
	} else {
		t.Logf("没配通道的报错（符合预期）：%v", err)
	}

	st.SetEmbedder(&fakeEmbedder{dim: 64})
	res, err := st.ReindexEmbeddings(ctx, 0)
	if err != nil {
		t.Fatalf("补向量失败：%v", err)
	}
	if res.Done != 2 || res.Remaining != 0 {
		t.Fatalf("应补上 2 条且无剩余：%+v", res)
	}
	if s, _ := st.Stats(); s.Embedded != 2 || s.Missing != 0 {
		t.Fatalf("统计应更新：%+v", s)
	}
	// 补完之后纯语义检索才搜得到（之前连关键词都只能碰运气）
	hit, err := st.HybridSearch(ctx, HybridQuery{Text: "部署方式", VectorOnly: true, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hit.Hits) == 0 || !hit.Hits[0].Chunk.HasVector {
		t.Fatalf("补完向量后应能按语义搜到：%+v", hit.Hits)
	}
	// 再补一次是幂等的（没有待补的就不该重复花钱）
	again, err := st.ReindexEmbeddings(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if again.Done != 0 {
		t.Fatalf("没有待补的就不该再调向量接口：%+v", again)
	}
}

// 换了向量模型之后：旧向量维度对不上、余弦恒为 0，必须算成"缺向量"并整库重算，
// 否则界面显示一切正常、语义检索却一条都搜不到（最隐蔽的静默失效）
func TestReindexRebuildsAfterModelChange(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	st.SetEmbedder(&fakeEmbedder{dim: 64, name: "model-a"})
	if _, err := st.Ingest("白泽后端部署在 NAS 上，用 systemd 守护", IngestOptions{Title: "部署"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ingest("密码本用 PBKDF2 加 AES-GCM 加密", IngestOptions{Title: "加密"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.Stats(); s.Missing != 0 || s.Embedded != 2 {
		t.Fatalf("换模型前应当都算有向量：%+v", s)
	}

	// 换模型（维度也不同）
	st.SetEmbedder(&fakeEmbedder{dim: 512, name: "model-b"})
	if s, _ := st.Stats(); s.Missing != 2 || s.Embedded != 0 {
		t.Fatalf("换模型后旧向量应算成缺：%+v", s)
	}
	rep, err := st.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if rep.StaleModel != 2 {
		t.Fatalf("自检应报出 2 条向量来自别的模型：%+v", rep)
	}
	if rep.Note == "" {
		t.Fatal("这种情况下必须给出提示，不能一声不吭")
	}

	res, err := st.ReindexEmbeddings(ctx, 0)
	if err != nil {
		t.Fatalf("重算失败：%v", err)
	}
	if res.Done != 2 || res.Remaining != 0 {
		t.Fatalf("应把 2 条全部重算：%+v", res)
	}
	if s, _ := st.Stats(); s.Embedded != 2 || s.Missing != 0 {
		t.Fatalf("重算后统计应恢复：%+v", s)
	}
	// 重算后才真的能按语义搜到（维度一致了）
	hit, err := st.HybridSearch(ctx, HybridQuery{Text: "部署方式", VectorOnly: true, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hit.Hits) == 0 {
		t.Fatal("换模型重算后应能按语义搜到")
	}
}

/* ---------- 概览与自检 ---------- */

func TestKindsAndDoctor(t *testing.T) {
	st := openStore(t)
	if _, err := st.Ingest("记一条", IngestOptions{Namespace: "ops", Category: CategoryDaily, Kind: "event"}); err != nil {
		t.Fatal(err)
	}
	kinds, err := st.Kinds()
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 1 || kinds[0].Namespace != "ops" || kinds[0].Kind != "event" || kinds[0].Count != 1 {
		t.Fatalf("类型统计不对：%+v", kinds)
	}

	rep, err := st.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if rep.EmbedderOn {
		t.Fatal("没配通道时不该说通道是开的")
	}
	if rep.Note == "" {
		t.Fatal("没配通道必须明确说明")
	}
	if rep.Chunks != 1 || rep.Embedded != 0 || rep.MissingEmbedding != 1 {
		t.Fatalf("自检数字不对：%+v", rep)
	}

	st.SetEmbedder(&fakeEmbedder{dim: 32})
	if _, err := st.Ingest("再记一条", IngestOptions{Namespace: "ops"}); err != nil {
		t.Fatal(err)
	}
	rep2, err := st.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if !rep2.EmbedderOn || rep2.Embedded != 1 || rep2.MissingEmbedding != 1 {
		t.Fatalf("接入向量后自检数字应更新：%+v", rep2)
	}
	if len(rep2.Namespaces) != 1 || rep2.Namespaces[0] != "ops" {
		t.Fatalf("分区列表不对：%+v", rep2.Namespaces)
	}
}
