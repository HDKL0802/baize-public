package memory

// 本文件是「记忆星图」的成图部分：把笔记之间的关系算成一张图。
//
// 两类边：
//  1. 显式边：正文里写了 `[[目标]]`（notes.go 解析），有向；
//  2. 自动边：两篇笔记的向量语义相似度够高就自动连上（AutoLink），无向（双向各记一条）。
//
// 「自动」是白泽对 Obsidian 的补充：Obsidian 的双链得人手动写，白泽替用户
// 把「其实在讲同一件事」的笔记自己连起来——这就是用户要的「自己进行双向链接」。

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"baize/internal/llm"
)

// noteVec 一篇笔记的向量（可能没有）
type noteVec struct {
	key   string
	vec   []float32
	at    int64
	title string
}

// AutoLinkOptions 自动成边参数
type AutoLinkOptions struct {
	Namespace string
	TopK      int     // 每篇笔记最多连几个「最像的」
	MinSim    float64 // 相似度下限（低于它的不算相关）
}

// AutoLinkResult 自动成边结果
type AutoLinkResult struct {
	Namespace  string  `json:"namespace"`
	Notes      int     `json:"notes"`      // 参与计算的笔记数
	Edges      int     `json:"edges"`      // 落库的自动边条数（含双向）
	Pairs      int     `json:"pairs"`      // 去重后的「对」数
	VectorUsed bool    `json:"vectorUsed"` // 本次是否真用上了向量
	MinSim     float64 `json:"minSim"`
	TopK       int     `json:"topK"`
	Note       string  `json:"note,omitempty"`
}

// AutoLink 用向量语义相似度自动建立笔记之间的双向链接。
//
// 会把该分区里**旧的自动边整体替换掉**（显式边不碰），所以可以反复跑。
// 没有向量通道时不做任何事，并明确说明原因——绝不假装连过了。
func (s *Store) AutoLink(ctx context.Context, opt AutoLinkOptions) (AutoLinkResult, error) {
	ns := NormalizeNamespace(opt.Namespace)
	if opt.TopK <= 0 {
		opt.TopK = 5
	}
	if opt.MinSim <= 0 {
		opt.MinSim = 0.55
	}
	out := AutoLinkResult{Namespace: ns, TopK: opt.TopK, MinSim: opt.MinSim}

	notes, err := s.noteVectors(ns)
	if err != nil {
		return out, err
	}
	out.Notes = len(notes)
	if len(notes) == 0 {
		out.Note = "这个分区里还没有笔记"
		return out, nil
	}
	// 有向量的笔记才参与（没向量的没法算相似度）
	var withVec []noteVec
	for _, n := range notes {
		if len(n.vec) > 0 {
			withVec = append(withVec, n)
		}
	}
	if len(withVec) == 0 {
		out.Note = "笔记都还没有向量：先在「模型通道」配好 embedding 通道，" +
			"再对笔记执行一次「补向量」，然后才能自动连边"
		return out, nil
	}
	out.VectorUsed = true

	// 每篇取 TopK 个邻居（相似度 ≥ 下限）
	type pair struct {
		a, b string
		sim  float64
	}
	best := map[string][]pair{} // key → 它的邻居（已按 sim 倒序截断到 TopK）
	for i := range withVec {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var cands []pair
		for j := range withVec {
			if i == j {
				continue
			}
			sim := cosine01(withVec[i].vec, withVec[j].vec)
			if sim >= opt.MinSim {
				cands = append(cands, pair{a: withVec[i].key, b: withVec[j].key, sim: sim})
			}
		}
		sort.SliceStable(cands, func(x, y int) bool { return cands[x].sim > cands[y].sim })
		if len(cands) > opt.TopK {
			cands = cands[:opt.TopK]
		}
		best[withVec[i].key] = cands
	}

	// 合并成「无序对」：任意一方选了对方就成了（这样两边都能看见反向链接）
	type undirected struct {
		a, b string
		sim  float64
	}
	seen := map[string]bool{}
	var pairs []undirected
	for _, cands := range best {
		for _, p := range cands {
			k := pairKey(p.a, p.b)
			if seen[k] {
				continue
			}
			seen[k] = true
			pairs = append(pairs, undirected{a: p.a, b: p.b, sim: p.sim})
		}
	}
	out.Pairs = len(pairs)

	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM note_links WHERE namespace=? AND kind=?`, ns, LinkAuto); err != nil {
		return out, err
	}
	for _, p := range pairs {
		// 双向各记一条：无向关系，两篇笔记的「反向链接」里都能看到对方
		for _, d := range [][2]string{{p.a, p.b}, {p.b, p.a}} {
			if _, err := s.db.Exec(`INSERT OR REPLACE INTO note_links(namespace,from_key,target,to_key,kind,weight,created_at)
				VALUES(?,?,?,?,?,?,?)`, ns, d[0], d[1], d[1], LinkAuto, p.sim, now); err != nil {
				return out, err
			}
			out.Edges++
		}
	}
	if out.Pairs == 0 {
		out.Note = "没有相似度达到阈值的笔记对；可以调低相似度下限再试"
	}
	return out, nil
}

// noteVectors 取分区内所有笔记的向量（BLOB 单独查，避免拖慢列表查询）
func (s *Store) noteVectors(ns string) ([]noteVec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT doc_key,title,created_at,embedding FROM chunks
		WHERE namespace=? AND source=? AND kind=? ORDER BY created_at DESC LIMIT 5000`,
		ns, SourceNote, KindNote)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []noteVec{}
	for rows.Next() {
		var n noteVec
		var blob []byte
		if err := rows.Scan(&n.key, &n.title, &n.at, &blob); err != nil {
			return nil, err
		}
		n.vec = decodeVec(blob)
		out = append(out, n)
	}
	return out, rows.Err()
}

/* ---------- 星图 ---------- */

// GraphNode 星图上的一个点（一篇笔记）
type GraphNode struct {
	ID       int64    `json:"id"`
	Key      string   `json:"key"`
	Title    string   `json:"title"`
	Path     string   `json:"path"`
	Tags     []string `json:"tags"`
	Degree   int      `json:"degree"` // 连接数（显式 + 自动）
	Explicit int      `json:"explicitDegree"`
	Auto     int      `json:"autoDegree"`
	Size     float64  `json:"size"` // 界面用来画点的大小
	Tokens   int      `json:"tokens"`
	At       int64    `json:"at"`
}

// GraphEdge 星图上的一条边
type GraphEdge struct {
	From   string  `json:"from"` // from_key
	To     string  `json:"to"`   // to_key
	Kind   string  `json:"kind"` // explicit / auto
	Weight float64 `json:"weight"`
}

// GraphOptions 取图参数
type GraphOptions struct {
	Namespace   string
	Limit       int     // 最多几个点（0 = 不限）；超了按连接数从多到少留
	IncludeAuto bool    // 是否带上自动边
	MinWeight   float64 // 自动边的最小权重（相似度），默认 0
	RootKey     string  // 以某篇笔记为中心取局部图（留空 = 全图）
	Hops        int     // 局部图的跳数，默认 1
}

// Graph 星图数据
type Graph struct {
	Namespace     string      `json:"namespace"`
	Nodes         []GraphNode `json:"nodes"`
	Edges         []GraphEdge `json:"edges"`
	Notes         int         `json:"noteCount"`
	Shown         int         `json:"shown"`
	ExplicitEdges int         `json:"explicitEdges"`
	AutoEdges     int         `json:"autoEdges"`
	Orphans       int         `json:"orphans"` // 一个连接都没有的笔记数
	Note          string      `json:"note,omitempty"`
}

// Graph 取星图（点 + 边）。默认带上自动边；需要干净的手工图时把 IncludeAuto 关掉。
func (s *Store) Graph(opt GraphOptions) (Graph, error) {
	ns := NormalizeNamespace(opt.Namespace)
	if opt.Hops <= 0 {
		opt.Hops = 1
	}
	out := Graph{Namespace: ns, Nodes: []GraphNode{}, Edges: []GraphEdge{}}

	// 1) 点：分区内全部笔记
	s.mu.Lock()
	rows, err := s.db.Query(`SELECT id,doc_key,title,source_ref,tokens,created_at FROM chunks
		WHERE namespace=? AND source=? AND kind=? ORDER BY created_at DESC LIMIT 5000`,
		ns, SourceNote, KindNote)
	if err != nil {
		s.mu.Unlock()
		return out, err
	}
	byKey := map[string]*GraphNode{}
	for rows.Next() {
		var n GraphNode
		if err := rows.Scan(&n.ID, &n.Key, &n.Title, &n.Path, &n.Tokens, &n.At); err != nil {
			rows.Close()
			s.mu.Unlock()
			return out, err
		}
		n.Tags = []string{}
		cp := n
		byKey[n.Key] = &cp
	}
	rows.Close()

	// 标签
	if len(byKey) > 0 {
		trows, err := s.db.Query(`SELECT note_key,tag FROM note_tags WHERE namespace=? ORDER BY tag`, ns)
		if err == nil {
			for trows.Next() {
				var k, t string
				if err := trows.Scan(&k, &t); err == nil {
					if n, ok := byKey[k]; ok {
						n.Tags = append(n.Tags, t)
					}
				}
			}
			trows.Close()
		}
	}

	// 2) 边
	kinds := []string{LinkExplicit}
	if opt.IncludeAuto {
		kinds = append(kinds, LinkAuto)
	}
	ph := placeholders(len(kinds))
	eargs := []any{ns}
	for _, k := range kinds {
		eargs = append(eargs, k)
	}
	erows, err := s.db.Query(`SELECT from_key,to_key,kind,weight FROM note_links
		WHERE namespace=? AND kind IN (`+ph+`) AND to_key<>''`, eargs...)
	if err != nil {
		s.mu.Unlock()
		return out, err
	}
	type rawEdge struct {
		from, to, kind string
		w              float64
	}
	var raws []rawEdge
	for erows.Next() {
		var e rawEdge
		if err := erows.Scan(&e.from, &e.to, &e.kind, &e.w); err != nil {
			erows.Close()
			s.mu.Unlock()
			return out, err
		}
		raws = append(raws, e)
	}
	erows.Close()
	s.mu.Unlock()

	out.Notes = len(byKey)
	if out.Notes == 0 {
		out.Note = "还没有笔记；先写一条，或从 Obsidian 库导入"
		return out, nil
	}

	// 3) 去重：同一对（无序）最多一条边，显式优先于自动
	edgeKey := func(a, b string) string { return pairKey(a, b) }
	picked := map[string]GraphEdge{}
	for _, e := range raws {
		if _, ok := byKey[e.from]; !ok {
			continue
		}
		if _, ok := byKey[e.to]; !ok {
			continue
		}
		if e.from == e.to {
			continue
		}
		if opt.IncludeAuto && e.kind == LinkAuto && opt.MinWeight > 0 && e.w < opt.MinWeight {
			continue
		}
		k := edgeKey(e.from, e.to)
		if prev, ok := picked[k]; ok {
			// 已经有边了：显式优先；同类则取权重大的
			if prev.Kind == LinkExplicit {
				continue
			}
			if e.kind == LinkAuto && prev.Weight >= e.w {
				continue
			}
			if e.kind == LinkExplicit {
				picked[k] = GraphEdge{From: e.from, To: e.to, Kind: e.kind, Weight: e.w}
			}
			continue
		}
		picked[k] = GraphEdge{From: e.from, To: e.to, Kind: e.kind, Weight: e.w}
	}

	edges := make([]GraphEdge, 0, len(picked))
	for _, e := range picked {
		edges = append(edges, e)
	}

	// 4) 度数（无向计）
	deg := map[string]int{}
	degEx := map[string]int{}
	degAuto := map[string]int{}
	for _, e := range edges {
		deg[e.From]++
		deg[e.To]++
		if e.Kind == LinkExplicit {
			degEx[e.From]++
			degEx[e.To]++
		} else {
			degAuto[e.From]++
			degAuto[e.To]++
		}
	}
	for k, n := range byKey {
		n.Degree = deg[k]
		n.Explicit = degEx[k]
		n.Auto = degAuto[k]
		// 点的大小：连接越多越大，但有上限（免得一个枢纽把图撑爆）
		n.Size = 1 + math.Log2(1+float64(n.Degree))
	}

	// 5) 局部图：从 RootKey 出发 BFS 指定跳数
	keep := map[string]bool{}
	if rk := strings.TrimSpace(opt.RootKey); rk != "" {
		if _, ok := byKey[rk]; !ok {
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

	// 6) 截断：按连接数从多到少留 limit 个
	nodes := make([]GraphNode, 0, len(byKey))
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
	final := map[string]bool{}
	for _, n := range nodes {
		final[n.Key] = true
	}
	for _, n := range nodes {
		if n.Degree == 0 {
			out.Orphans++
		}
	}
	for _, e := range edges {
		if !final[e.From] || !final[e.To] {
			continue
		}
		out.Edges = append(out.Edges, e)
		if e.Kind == LinkExplicit {
			out.ExplicitEdges++
		} else {
			out.AutoEdges++
		}
	}
	out.Nodes = nodes
	out.Shown = len(nodes)
	if out.ExplicitEdges == 0 && out.AutoEdges == 0 {
		out.Note = "图里还没有连线：写点 [[双向链接]]，或点「自动连边」让白泽按语义把相关笔记连起来"
	}
	return out, nil
}

// NoteStats 笔记层概览（控制台卡片用）
type NoteStats struct {
	Namespace     string `json:"namespace"`
	Notes         int    `json:"notes"`
	WithVector    int    `json:"withVector"`
	ExplicitEdges int    `json:"explicitEdges"`
	AutoEdges     int    `json:"autoEdges"`
	Orphans       int    `json:"orphans"`
	Tags          int    `json:"tags"`
	MissingLinks  int    `json:"missingLinks"` // 悬空引用（指向不存在的笔记）
}

// NoteStats 汇总笔记层现状
func (s *Store) NoteStats(ns string) (NoteStats, error) {
	ns = NormalizeNamespace(ns)
	out := NoteStats{Namespace: ns}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN embedding IS NOT NULL AND length(embedding)>0 THEN 1 ELSE 0 END),0)
		FROM chunks WHERE namespace=? AND source=? AND kind=?`,
		ns, SourceNote, KindNote).Scan(&out.Notes, &out.WithVector); err != nil {
		return out, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM note_tags WHERE namespace=?`, ns).Scan(&out.Tags); err != nil {
		return out, err
	}
	if err := s.db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN kind='explicit' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN kind='auto' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN kind='explicit' AND (to_key IS NULL OR to_key='') THEN 1 ELSE 0 END),0)
		FROM note_links WHERE namespace=?`, ns).Scan(&out.ExplicitEdges, &out.AutoEdges, &out.MissingLinks); err != nil {
		return out, err
	}
	// 孤立点：没有任何（去重后的）连接
	rows, err := s.db.Query(`SELECT c.doc_key FROM chunks c
		WHERE c.namespace=? AND c.source=? AND c.kind=?
		AND NOT EXISTS(SELECT 1 FROM note_links l WHERE l.namespace=c.namespace
			AND (l.from_key=c.doc_key OR l.to_key=c.doc_key) AND l.to_key<>'')`,
		ns, SourceNote, KindNote)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return out, err
		}
		out.Orphans++
	}
	rows.Close()
	return out, rows.Err()
}

/* ---------- 小工具 ---------- */

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

// decodeVec 解向量 BLOB
func decodeVec(b []byte) []float32 {
	if len(b) == 0 {
		return nil
	}
	return llm.DecodeVec(b)
}
