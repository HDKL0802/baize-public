package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"baize/internal/memory"
	"baize/internal/notebook"
)

// registerNotes 注册「记忆星图」（笔记 + 双向链接 + 图谱）接口。
//
// 笔记的正本是磁盘上的 .md 文件（数据目录下的 vault/，Obsidian 兼容结构），
// SQLite（notes.db）只是派生索引，删了可以随时用 POST /notes/scan 重建。
// 这与旧实现最大的差别：用户能直接看到、编辑、带走自己的笔记。
//
// **多用户**：笔记库按登录身份路由（用户 / 用户组各自一份 vault），
// 所以每个人的笔记、星图、标签云天然隔离。
//
// 响应字段保持旧前端契约（docKey / hasVector / explicitDegree / autoDegree /
// 边的 kind=explicit|auto），这样老界面不用改也能用。
func (s *Server) registerNotes(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	a := s.agent

	nsOf := func(q string) string {
		if strings.TrimSpace(q) != "" {
			return memory.NormalizeNamespace(q)
		}
		return memory.NormalizeNamespace(a.Config().Memory.Namespace)
	}

	// 笔记列表 + 标签云 + 概览
	mux.HandleFunc("GET /api/agent/notes", s.api(func(w http.ResponseWriter, r *http.Request) {
		nb, err := s.nbOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		urlq := r.URL.Query()
		ns := nsOf(urlq.Get("namespace"))
		notes, total, err := nb.List(notebook.ListOptions{
			Tag: urlq.Get("tag"), Query: urlq.Get("q"),
			Limit: atoiDefault(urlq.Get("limit"), 50), Offset: atoiDefault(urlq.Get("offset"), 0),
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		tags, err := nb.Tags()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		stats, err := nb.Stats()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		cards := make([]noteCard, 0, len(notes))
		for _, n := range notes {
			cards = append(cards, noteCardOf(n, ns))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"namespace": ns, "notes": cards, "total": total, "tags": tags, "stats": stats,
		})
	}))

	// 全文检索（倒排索引，中文按 bigram 分词）
	mux.HandleFunc("GET /api/agent/notes/search", s.api(func(w http.ResponseWriter, r *http.Request) {
		nb, err := s.nbOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		urlq := r.URL.Query()
		hits, err := nb.Search(urlq.Get("q"), atoiDefault(urlq.Get("limit"), 20))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"query": urlq.Get("q"), "hits": hits})
	}))

	// 星图数据（点 + 边）。参数：
	//   limit 最多几个点；auto=0 关掉相关边；minWeight 相关边权重下限
	//   tags=1 把标签画成节点；missing=0 不画悬空链接；root 以某篇为中心；hops 跳数
	mux.HandleFunc("GET /api/agent/notes/graph", s.api(func(w http.ResponseWriter, r *http.Request) {
		nb, err := s.nbOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		urlq := r.URL.Query()
		minW := 0.0
		if raw := strings.TrimSpace(urlq.Get("minWeight")); raw != "" {
			if f, err := strconv.ParseFloat(raw, 64); err == nil {
				minW = f
			}
		}
		g, err := nb.Graph(notebook.GraphOptions{
			Limit:             atoiDefault(urlq.Get("limit"), 300),
			IncludeTags:       urlq.Get("tags") == "1",
			IncludeUnresolved: urlq.Get("missing") != "0",
			IncludeRelated:    urlq.Get("auto") != "0",
			MinWeight:         minW,
			Root:              urlq.Get("root"),
			Hops:              atoiDefault(urlq.Get("hops"), 1),
			Tag:               urlq.Get("tag"),
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		edges := make([]graphEdge, 0, len(g.Edges))
		for _, e := range g.Edges {
			kind := "explicit"
			if e.Kind == notebook.EdgeRelated {
				kind = "auto"
			}
			edges = append(edges, graphEdge{From: e.From, To: e.To, Kind: kind, Detail: e.Kind, Weight: e.Weight})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"nodes": g.Nodes, "edges": edges, "note": g.Note,
			"noteCount": g.NoteCount, "shown": g.Shown, "orphans": g.Orphans,
			"explicitEdges": g.ExplicitEdges, "autoEdges": g.AutoEdges,
			"tagEdges": g.TagEdges, "missingEdges": g.MissingEdges,
		})
	}))

	// 单篇笔记：正文 + 出链 + 反向链接
	mux.HandleFunc("GET /api/agent/notes/{key...}", s.api(func(w http.ResponseWriter, r *http.Request) {
		nb, err := s.nbOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		ns := nsOf(r.URL.Query().Get("namespace"))
		key := strings.TrimSpace(r.PathValue("key"))
		note, ok, err := nb.Read(key)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "没有这篇笔记："+key)
			return
		}
		outs, err := nb.Outlinks(key)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		bl, err := nb.Backlinks(key)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"namespace": ns, "note": noteDetailOf(note, ns), "outlinks": outs, "backlinks": bl,
		})
	}))

	// 新建 / 保存一篇笔记（按路径覆盖；不传路径就用标题当文件名）
	mux.HandleFunc("POST /api/agent/notes", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Namespace string   `json:"namespace"`
			Path      string   `json:"path"`
			Title     string   `json:"title"`
			Content   string   `json:"content"`
			Tags      []string `json:"tags"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		nb, err := s.nbOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		note, err := nb.Write(notebook.WriteOptions{
			Path: req.Path, Title: req.Title, Content: req.Content, Tags: req.Tags,
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": noteDetailOf(note, nsOf(req.Namespace))})
	}))

	// 批量导入（Obsidian 库就是一堆 .md）。files:[{path,title,content}]
	mux.HandleFunc("POST /api/agent/notes/import", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Namespace string `json:"namespace"`
			Files     []struct {
				Path    string `json:"path"`
				Title   string `json:"title"`
				Content string `json:"content"`
			} `json:"files"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(req.Files) == 0 {
			writeErr(w, http.StatusBadRequest, "files 不能为空")
			return
		}
		nb, err := s.nbOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		files := make([]notebook.ImportFile, 0, len(req.Files))
		for _, f := range req.Files {
			files = append(files, notebook.ImportFile{Path: f.Path, Content: f.Content})
		}
		res, err := nb.Import(files)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"namespace": nsOf(req.Namespace), "imported": res.Notes, "skipped": res.Skipped,
			"tags": res.Tags, "errors": res.Errors,
		})
	}))

	// 自动连边：按「共享出链/入链/标签」的共引关系补相关边（不依赖 embedding 通道）
	mux.HandleFunc("POST /api/agent/notes/autolink", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Namespace string  `json:"namespace"`
			TopK      int     `json:"topK"`
			MinSim    float64 `json:"minSim"`
		}
		_ = decodeBody(r, &req)
		nb, err := s.nbOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		res, err := nb.AutoRelated(notebook.RelatedOptions{TopK: req.TopK, MinSim: req.MinSim})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"namespace": nsOf(req.Namespace), "ok": true, "links": res.Edges, "auto": res.Edges,
			"pairs": res.Pairs, "notes": res.Notes, "method": res.Method, "note": res.Note,
		})
	}))

	// 重新解析悬空链接
	mux.HandleFunc("POST /api/agent/notes/resolve", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Namespace string `json:"namespace"`
		}
		_ = decodeBody(r, &req)
		nb, err := s.nbOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		n, err := nb.ResolveLinks()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resolved": n, "namespace": nsOf(req.Namespace)})
	}))

	// 扫描磁盘增量（在 Obsidian / 编辑器里改过文件之后重建索引）
	mux.HandleFunc("POST /api/agent/notes/scan", s.api(func(w http.ResponseWriter, r *http.Request) {
		nb, err := s.nbOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		res, err := nb.Scan()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scan": res})
	}))

	// 重命名 / 移动（body: {key, path}）
	mux.HandleFunc("POST /api/agent/notes/rename", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Namespace string `json:"namespace"`
			Key       string `json:"key"`
			Path      string `json:"path"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		nb, err := s.nbOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		note, err := nb.Rename(req.Key, req.Path)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": noteDetailOf(note, nsOf(req.Namespace))})
	}))

	// 删除（body: {key}；key 可以是带目录的相对路径）
	mux.HandleFunc("POST /api/agent/notes/forget", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Namespace string `json:"namespace"`
			Key       string `json:"key"`
		}
		_ = decodeBody(r, &req)
		s.forgetNote(w, r, req.Key)
	}))

	// 兼容旧界面：POST /notes/<key>/forget（key 里带目录时走下面的兜底路由）
	// 这条兜底也承接 GET 之外任何「notes/ 之后带斜杠」的 POST，
	// 只有后缀是 /forget 才处理，其余一律 404（具体路由更优先，不会抢）。
	mux.HandleFunc("POST /api/agent/notes/{rest...}", s.api(func(w http.ResponseWriter, r *http.Request) {
		rest := r.PathValue("rest")
		if key, ok := strings.CutSuffix(rest, "/forget"); ok {
			s.forgetNote(w, r, key)
			return
		}
		writeErr(w, http.StatusNotFound, "不认识的笔记接口："+r.URL.Path)
	}))
}

// forgetNote 删除一篇笔记（按 key）
func (s *Server) forgetNote(w http.ResponseWriter, r *http.Request, key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		writeErr(w, http.StatusBadRequest, "缺少 key")
		return
	}
	nb, err := s.nbOf(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok, err := nb.Delete(key)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这篇笔记，什么都没删")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "forgot": 1})
}

// nbOf 取这次请求该用的笔记库（按登录身份路由到用户/用户组分区）
func (s *Server) nbOf(r *http.Request) (*notebook.Vault, error) {
	if s.agent == nil {
		return nil, errors.New("Agent 服务未启动")
	}
	p, _, _ := s.principalOf(r)
	return s.agent.PartitionNotebook(p)
}

/* ---------- 响应结构（兼容旧前端的字段命名） ---------- */

type noteCard struct {
	Key       string   `json:"key"`
	DocKey    string   `json:"docKey"`
	Namespace string   `json:"namespace"`
	Path      string   `json:"path"`
	Title     string   `json:"title"`
	Aliases   []string `json:"aliases"`
	Tags      []string `json:"tags"`
	Excerpt   string   `json:"excerpt"`
	Links     []string `json:"links"`
	Tokens    int      `json:"tokens"`
	Size      int64    `json:"size"`
	CreatedAt int64    `json:"createdAt"`
	UpdatedAt int64    `json:"updatedAt"`
	HasVector bool     `json:"hasVector"`
}

type noteDetail struct {
	noteCard
	Content     string         `json:"content"`
	Frontmatter map[string]any `json:"frontmatter,omitempty"`
}

type graphEdge struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Kind   string  `json:"kind"`   // 兼容旧界面：explicit / auto
	Detail string  `json:"detail"` // 细分类型：link / embed / tag / missing / related
	Weight float64 `json:"weight"`
}

func noteCardOf(n notebook.NoteSummary, ns string) noteCard {
	return noteCard{
		Key: n.Key, DocKey: n.Key, Namespace: ns, Path: n.Path, Title: n.Title,
		Aliases: n.Aliases, Tags: n.Tags, Excerpt: n.Excerpt, Links: []string{},
		Tokens: n.Tokens, Size: n.Size, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
		HasVector: false, // 笔记模块不落向量
	}
}

func noteDetailOf(n notebook.Note, ns string) noteDetail {
	links := make([]string, 0, len(n.Links))
	for _, l := range n.Links {
		links = append(links, l.Target)
	}
	return noteDetail{
		noteCard: noteCard{
			Key: n.Key, DocKey: n.Key, Namespace: ns, Path: n.Path, Title: n.Title,
			Aliases: n.Aliases, Tags: n.Tags, Excerpt: excerptOf(n.Content, 160), Links: links,
			Tokens: n.Tokens, Size: n.Size, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
			HasVector: false,
		},
		Content: n.Content, Frontmatter: n.Frontmatter,
	}
}

func excerptOf(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}
