package notebook

// 星图层：把笔记之间的关系算成一张可直接渲染的图。
//
// 与旧实现（memory/graph.go）的差别：
//   - 节点不再只有笔记：未解析的悬空链接、标签都是独立节点（和 Obsidian 一致）
//   - 边的类型明确：link / embed / tag / missing / related
//   - 「自动连边」不再依赖 LLM 向量，改用可解释的共引（co-citation）：
//     两篇笔记共享的出链/入链/标签越多就越相关
//     —— 不需要 embedding 通道也能用，结果可解释、可复现
//   - 图在 SQLite 里增量维护（note_related 表），请求时不再全量重算

import (
	"math"
	"sort"
	"strings"
	"time"
)

// 节点类型
const (
	NodeNote       = "note"
	NodeTag        = "tag"
	NodeUnresolved = "unresolved"
)

// 边类型
const (
	EdgeLink    = "link"
	EdgeEmbed   = "embed"
	EdgeTag     = "tag"
	EdgeMissing = "missing"
	EdgeRelated = "related"
)

// GraphNode 图上一个点
type GraphNode struct {
	ID             string   `json:"id"`
	Key            string   `json:"key"`
	Kind           string   `json:"kind"` // note / tag / unresolved
	Title          string   `json:"title"`
	Path           string   `json:"path"`
	Tags           []string `json:"tags"`
	Degree         int      `json:"degree"`
	ExplicitDegree int      `json:"explicitDegree"` // link + embed
	AutoDegree     int      `json:"autoDegree"`     // related
	Size           float64  `json:"size"`
	Tokens         int      `json:"tokens"`
	At             int64    `json:"at"`
}

// GraphEdge 图上一条边
type GraphEdge struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Kind   string  `json:"kind"`
	Weight float64 `json:"weight"`
}

// GraphOptions 取图参数
type GraphOptions struct {
	Limit             int     // 最多几个点（0 不限），超了按连接数留
	IncludeTags       bool    // 把标签也画成节点
	IncludeUnresolved bool    // 把悬空链接画成虚线节点
	IncludeRelated    bool    // 带上「相关」边
	MinWeight         float64 // 相关边的最小权重
	Root              string  // 以某篇笔记为中心取局部图
	Hops              int     // 局部图跳数（默认 1）
	Tag               string  // 只看某个标签下的笔记
}

// Graph 星图数据
type Graph struct {
	Nodes         []GraphNode `json:"nodes"`
	Edges         []GraphEdge `json:"edges"`
	NoteCount     int         `json:"noteCount"`
	Shown         int         `json:"shown"`
	ExplicitEdges int         `json:"explicitEdges"`
	AutoEdges     int         `json:"autoEdges"`
	TagEdges      int         `json:"tagEdges"`
	MissingEdges  int         `json:"missingEdges"`
	Orphans       int         `json:"orphans"`
	Note          string      `json:"note,omitempty"`
}

type rawLink struct {
	from, to, target, kind, anchor string
	embed                          bool
}

// Graph 生成星图
func (v *Vault) Graph(opt GraphOptions) (Graph, error) {
	if opt.Hops <= 0 {
		opt.Hops = 1
	}
	out := Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}}

	v.mu.Lock()
	defer v.mu.Unlock()

	// 1) 笔记节点
	byKey := map[string]*GraphNode{}
	{
		q := `SELECT n.key,n.path,n.title,n.tokens,n.updated_at FROM notes n`
		args := []any{}
		if t := normalizeTag(opt.Tag); t != "" {
			q = `SELECT n.key,n.path,n.title,n.tokens,n.updated_at FROM notes n
				WHERE EXISTS(SELECT 1 FROM note_tags x WHERE x.note_key=n.key AND lower(x.tag)=lower(?))`
			args = append(args, t)
		}
		rows, err := v.db.Query(q, args...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var n GraphNode
			if err := rows.Scan(&n.Key, &n.Path, &n.Title, &n.Tokens, &n.At); err != nil {
				rows.Close()
				return out, err
			}
			n.ID = "n:" + n.Key
			n.Kind = NodeNote
			n.Tags = []string{}
			byKey[n.Key] = &n
		}
		rows.Close()
	}
	out.NoteCount = len(byKey)
	if out.NoteCount == 0 {
		out.Note = "还没有笔记；新建一篇，或把已有的 .md 目录扫描进来"
		return out, nil
	}

	// 标签
	{
		rows, err := v.db.Query(`SELECT note_key,tag FROM note_tags ORDER BY tag`)
		if err == nil {
			for rows.Next() {
				var k, t string
				if err := rows.Scan(&k, &t); err == nil {
					if n, ok := byKey[k]; ok {
						n.Tags = append(n.Tags, t)
					}
				}
			}
			rows.Close()
		}
	}

	// 2) 边
	var links []rawLink
	{
		rows, err := v.db.Query(`SELECT from_key,target,to_key,kind,anchor,embed FROM note_links`)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var l rawLink
			var embed int
			if err := rows.Scan(&l.from, &l.target, &l.to, &l.kind, &l.anchor, &embed); err != nil {
				rows.Close()
				return out, err
			}
			l.embed = embed > 0 || l.kind == LinkKindEmbed
			links = append(links, l)
		}
		rows.Close()
	}

	edges := []GraphEdge{}
	nodeOf := func(key string) *GraphNode { return byKey[key] }

	// 悬空节点
	if opt.IncludeUnresolved {
		for _, l := range links {
			if l.to != "" {
				continue
			}
			if _, ok := byKey[l.from]; !ok {
				continue
			}
			uk := "u:" + linksTargetKey(l.target)
			n, ok := byKey[uk]
			if !ok {
				n = &GraphNode{ID: uk, Key: uk, Kind: NodeUnresolved, Title: l.target, Tags: []string{}}
				byKey[uk] = n
			}
		}
	}

	// 笔记 ↔ 笔记 / 悬空
	for _, l := range links {
		from := nodeOf(l.from)
		if from == nil {
			continue
		}
		var to *GraphNode
		kind := EdgeLink
		if l.to != "" {
			to = nodeOf(l.to)
			if to == nil {
				continue
			}
			if l.embed {
				kind = EdgeEmbed
			}
		} else {
			if !opt.IncludeUnresolved {
				continue
			}
			to = nodeOf("u:" + linksTargetKey(l.target))
			if to == nil {
				continue
			}
			kind = EdgeMissing
		}
		if from.Key == to.Key {
			continue
		}
		edges = append(edges, GraphEdge{From: from.Key, To: to.Key, Kind: kind, Weight: 1})
	}

	// 笔记 ↔ 标签
	if opt.IncludeTags {
		rows, err := v.db.Query(`SELECT note_key,tag FROM note_tags`)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var k, t string
			if err := rows.Scan(&k, &t); err != nil {
				rows.Close()
				return out, err
			}
			if _, ok := byKey[k]; !ok {
				continue
			}
			tk := "t:" + strings.ToLower(t)
			if _, ok := byKey[tk]; !ok {
				byKey[tk] = &GraphNode{ID: tk, Key: tk, Kind: NodeTag, Title: t, Tags: []string{}}
			}
			edges = append(edges, GraphEdge{From: k, To: tk, Kind: EdgeTag, Weight: 1})
		}
		rows.Close()
	}

	// 相关边
	if opt.IncludeRelated {
		rows, err := v.db.Query(`SELECT a,b,weight FROM note_related WHERE weight>=?`, opt.MinWeight)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var a, b string
			var w float64
			if err := rows.Scan(&a, &b, &w); err != nil {
				rows.Close()
				return out, err
			}
			if _, ok := byKey[a]; !ok {
				continue
			}
			if _, ok := byKey[b]; !ok {
				continue
			}
			edges = append(edges, GraphEdge{From: a, To: b, Kind: EdgeRelated, Weight: w})
		}
		rows.Close()
	}

	// 3) 去重（无向同一对同类只留一条，权重取大）
	edges = dedupeEdges(edges)

	// 4) 度数
	deg := map[string]int{}
	degEx := map[string]int{}
	degAuto := map[string]int{}
	degTag := map[string]int{}
	for _, e := range edges {
		deg[e.From]++
		deg[e.To]++
		switch e.Kind {
		case EdgeRelated:
			degAuto[e.From]++
			degAuto[e.To]++
		case EdgeTag:
			degTag[e.From]++
		default:
			degEx[e.From]++
			degEx[e.To]++
		}
	}
	for k, n := range byKey {
		n.Degree = deg[k]
		n.ExplicitDegree = degEx[k]
		n.AutoDegree = degAuto[k]
		n.Size = 1 + math.Log2(1+float64(n.Degree))
	}

	// 5) 局部图
	keep := map[string]bool{}
	if root := linkTargetKey(opt.Root); root != "" {
		rk := ""
		if _, ok := byKey[root]; ok {
			rk = root
		} else if k := resolveKeyFromNotesLocked(v, root); k != "" {
			rk = k
		}
		if rk == "" {
			out.Note = "指定的中心笔记不存在"
			return out, nil
		}
		adj := map[string][]string{}
		for _, e := range edges {
			adj[e.From] = append(adj[e.From], e.To)
			adj[e.To] = append(adj[e.To], e.From)
		}
		keep[rk] = true
		frontier := []string{rk}
		for hop := 0; hop < opt.Hops; hop++ {
			var next []string
			for _, cur := range frontier {
				for _, nb := range adj[cur] {
					if !keep[nb] {
						keep[nb] = true
						next = append(next, nb)
					}
				}
			}
			frontier = next
		}
	} else {
		for k := range byKey {
			keep[k] = true
		}
	}

	// 6) 截断：按连接数留前 N
	nodes := make([]GraphNode, 0, len(keep))
	for k := range keep {
		nodes = append(nodes, *byKey[k])
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Degree != nodes[j].Degree {
			return nodes[i].Degree > nodes[j].Degree
		}
		return nodes[i].At > nodes[j].At
	})
	if opt.Limit > 0 && len(nodes) > opt.Limit {
		nodes = nodes[:opt.Limit]
	}
	sealed := map[string]bool{}
	for _, n := range nodes {
		sealed[n.Key] = true
		if n.Kind == NodeNote && n.Degree == 0 {
			out.Orphans++
		}
	}
	for _, e := range edges {
		if !sealed[e.From] || !sealed[e.To] {
			continue
		}
		out.Edges = append(out.Edges, e)
		switch e.Kind {
		case EdgeRelated:
			out.AutoEdges++
		case EdgeTag:
			out.TagEdges++
		case EdgeMissing:
			out.MissingEdges++
		default:
			out.ExplicitEdges++
		}
	}
	out.Nodes = nodes
	out.Shown = len(nodes)
	if len(out.Edges) == 0 {
		out.Note = "图里还没有连线：写点 [[双向链接]]，或点「自动连边」按共引关系补上"
	}
	return out, nil
}

// resolveKeyFromNotesLocked 把 root 参数（可能是标题/文件名）解析成笔记 key
func resolveKeyFromNotesLocked(v *Vault, target string) string {
	rows, err := v.db.Query(`SELECT key,path,title,aliases FROM notes`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var key, p, title, aliasesJSON string
		if err := rows.Scan(&key, &p, &title, &aliasesJSON); err != nil {
			return ""
		}
		switch target {
		case keyOf(p), strings.ToLower(baseStem(p)), strings.ToLower(strings.TrimSpace(title)):
			return key
		}
	}
	return ""
}

func linksTargetKey(t string) string { return linkTargetKey(t) }

func dedupeEdges(in []GraphEdge) []GraphEdge {
	key := func(a, b string) string {
		if a > b {
			a, b = b, a
		}
		return a + "\x00" + b
	}
	// 不同类保留（link 与 related 可以同时存在）；同类取权重大的
	picked := map[string]GraphEdge{}
	for _, e := range in {
		k := key(e.From, e.To) + "\x00" + e.Kind
		if prev, ok := picked[k]; ok && prev.Weight >= e.Weight {
			continue
		}
		picked[k] = e
	}
	out := make([]GraphEdge, 0, len(picked))
	for _, e := range picked {
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

/* ---------- 自动相关边（共引，不依赖 LLM） ---------- */

// RelatedOptions 自动相关参数
type RelatedOptions struct {
	TopK     int
	MinSim   float64
	MaxNotes int // 参与计算的上限（防止大库 O(n²) 卡死）
}

// RelatedResult 结果
type RelatedResult struct {
	Notes  int     `json:"notes"`
	Pairs  int     `json:"pairs"`
	Edges  int     `json:"edges"`
	TopK   int     `json:"topK"`
	MinSim float64 `json:"minSim"`
	Method string  `json:"method"`
	Note   string  `json:"note,omitempty"`
}

// AutoRelated 按「共享出链 / 入链 / 标签」的 Jaccard 相似度补相关边。
//
// 会整体替换旧的 related 边（link/embed 边不碰），可以反复跑。
func (v *Vault) AutoRelated(opt RelatedOptions) (RelatedResult, error) {
	if opt.TopK <= 0 {
		opt.TopK = 5
	}
	if opt.MinSim <= 0 {
		opt.MinSim = 0.25
	}
	if opt.MaxNotes <= 0 {
		opt.MaxNotes = 3000
	}
	out := RelatedResult{TopK: opt.TopK, MinSim: opt.MinSim, Method: "co-citation"}

	v.mu.Lock()
	defer v.mu.Unlock()

	rows, err := v.db.Query(`SELECT key FROM notes ORDER BY updated_at DESC LIMIT ?`, opt.MaxNotes)
	if err != nil {
		return out, err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return out, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	out.Notes = len(keys)
	if len(keys) < 2 {
		out.Note = "至少要有两篇笔记才能自动连边"
		return out, nil
	}

	// 每篇的邻居集合（出链 + 入链，无向）与标签集合
	neigh := map[string]map[string]bool{}
	tags := map[string]map[string]bool{}
	for _, k := range keys {
		neigh[k] = map[string]bool{}
		tags[k] = map[string]bool{}
	}
	lrows, err := v.db.Query(`SELECT from_key,to_key FROM note_links WHERE to_key<>''`)
	if err != nil {
		return out, err
	}
	for lrows.Next() {
		var a, b string
		if err := lrows.Scan(&a, &b); err != nil {
			lrows.Close()
			return out, err
		}
		if _, ok := neigh[a]; ok {
			neigh[a][b] = true
		}
		if _, ok := neigh[b]; ok {
			neigh[b][a] = true
		}
	}
	lrows.Close()
	trows, err := v.db.Query(`SELECT note_key,tag FROM note_tags`)
	if err != nil {
		return out, err
	}
	for trows.Next() {
		var k, t string
		if err := trows.Scan(&k, &t); err != nil {
			trows.Close()
			return out, err
		}
		if _, ok := tags[k]; ok {
			tags[k][strings.ToLower(t)] = true
		}
	}
	trows.Close()

	type pair struct {
		a, b string
		w    float64
	}
	best := map[string][]pair{}
	for i := 0; i < len(keys); i++ {
		a := keys[i]
		for j := i + 1; j < len(keys); j++ {
			b := keys[j]
			w := 0.6*jaccard(neigh[a], neigh[b]) + 0.4*jaccard(tags[a], tags[b])
			if w < opt.MinSim || w == 0 {
				continue
			}
			best[a] = append(best[a], pair{a: a, b: b, w: w})
			best[b] = append(best[b], pair{a: a, b: b, w: w})
		}
	}
	// 每篇只留 TopK 最相关的
	picked := map[string]pair{}
	for _, cands := range best {
		sort.SliceStable(cands, func(i, j int) bool { return cands[i].w > cands[j].w })
		if len(cands) > opt.TopK {
			cands = cands[:opt.TopK]
		}
		for _, c := range cands {
			k := pairKey(c.a, c.b)
			if prev, ok := picked[k]; ok && prev.w >= c.w {
				continue
			}
			picked[k] = c
		}
	}
	out.Pairs = len(picked)

	now := time.Now().UnixMilli()
	if _, err := v.db.Exec(`DELETE FROM note_related`); err != nil {
		return out, err
	}
	for _, p := range picked {
		for _, d := range [][2]string{{p.a, p.b}, {p.b, p.a}} {
			if _, err := v.db.Exec(`INSERT OR REPLACE INTO note_related(a,b,weight,updated_at) VALUES(?,?,?,?)`,
				d[0], d[1], p.w, now); err != nil {
				return out, err
			}
			out.Edges++
		}
	}
	if out.Pairs == 0 {
		out.Note = "没有相关度达到阈值的笔记对；可以调低 minSim 再试"
	}
	return out, nil
}

// jaccard 集合相似度；两边都空时返回 0（没有证据就不连）
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}
