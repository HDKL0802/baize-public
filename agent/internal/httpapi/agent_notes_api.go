package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"baize/internal/memory"
)

// registerNotes 注册「记忆星图」（笔记 + 双向链接 + 图谱）接口。
//
// 笔记正本存在记忆库（memory.db）里：source='note' 的分块，关系存 note_links / note_tags。
// 这样笔记天然继承记忆的语义检索与向量通道，「自动连边」直接用同一份向量。
//
// **多用户**：记忆库按登录身份路由（登录 = 该用户的分区，未登录 = 默认主体），
// 所以每个人的笔记、星图、标签云天然隔离开，互不可见。
func (s *Server) registerNotes(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	a := s.agent

	// 默认分区：跟记忆用同一个（配置里的 memory.namespace）
	nsOf := func(q string) string {
		if strings.TrimSpace(q) != "" {
			return memory.NormalizeNamespace(q)
		}
		return memory.NormalizeNamespace(a.Config().Memory.Namespace)
	}

	// 笔记列表（可按标签 / 关键词过滤）+ 标签云 + 概览
	mux.HandleFunc("GET /api/agent/notes", s.api(func(w http.ResponseWriter, r *http.Request) {
		mem, err := s.memOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		urlq := r.URL.Query()
		ns := nsOf(urlq.Get("namespace"))
		notes, err := mem.ListNotes(ns, urlq.Get("tag"), urlq.Get("q"),
			atoiDefault(urlq.Get("limit"), 50), atoiDefault(urlq.Get("offset"), 0))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		tags, err := mem.Tags(ns)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		stats, err := mem.NoteStats(ns)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"namespace": ns, "notes": notes, "tags": tags, "stats": stats,
		})
	}))

	// 星图数据（点 + 边）。参数：
	//   namespace 分区；limit 最多几个点；auto=0 关掉自动边；minWeight 自动边相似度下限
	//   root 以某篇笔记为中心看局部图；hops 跳数（默认 1）
	mux.HandleFunc("GET /api/agent/notes/graph", s.api(func(w http.ResponseWriter, r *http.Request) {
		mem, err := s.memOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		urlq := r.URL.Query()
		ns := nsOf(urlq.Get("namespace"))
		auto := urlq.Get("auto") != "0"
		minW := 0.0
		if raw := strings.TrimSpace(urlq.Get("minWeight")); raw != "" {
			if f, err := strconv.ParseFloat(raw, 64); err == nil {
				minW = f
			}
		}
		g, err := mem.Graph(memory.GraphOptions{
			Namespace:   ns,
			Limit:       atoiDefault(urlq.Get("limit"), 300),
			IncludeAuto: auto,
			MinWeight:   minW,
			RootKey:     urlq.Get("root"),
			Hops:        atoiDefault(urlq.Get("hops"), 1),
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, g)
	}))

	// 单篇笔记：正文 + 出链 + 反向链接（界面右侧两栏就靠它）
	mux.HandleFunc("GET /api/agent/notes/{key}", s.api(func(w http.ResponseWriter, r *http.Request) {
		mem, err := s.memOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		ns := nsOf(r.URL.Query().Get("namespace"))
		key := strings.TrimSpace(r.PathValue("key"))
		note, ok, err := mem.GetNote(ns, key)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "没有这篇笔记："+key)
			return
		}
		out, err := mem.Outlinks(ns, key)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		bl, err := mem.Backlinks(ns, key)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"namespace": ns, "note": note, "outlinks": out, "backlinks": bl,
		})
	}))

	// 新建 / 保存一篇笔记（按路径覆盖）
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
		mem, err := s.memOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		note, err := mem.WriteNote(ctx, memory.WriteNoteOptions{
			Namespace: nsOf(req.Namespace), Path: req.Path, Title: req.Title,
			Content: req.Content, Tags: req.Tags,
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": note})
	}))

	// 批量导入（Obsidian 库就是一堆 .md）。files:[{path,title,content}]
	mux.HandleFunc("POST /api/agent/notes/import", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Namespace string            `json:"namespace"`
			Files     []memory.NoteFile `json:"files"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(req.Files) == 0 {
			writeErr(w, http.StatusBadRequest, "files 不能为空")
			return
		}
		mem, err := s.memOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		ns := nsOf(req.Namespace)
		res, err := mem.ImportNotes(ctx, ns, req.Files)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// 导入完统一解析一次链接（跨文件的 [[双链]] 这时才都能接上）
		if n, err := mem.ResolveLinks(ns); err == nil {
			res.Links = n
		}
		writeJSON(w, http.StatusOK, res)
	}))

	// 自动连边：按向量语义相似度把相关笔记连起来（这就是「自动双向链接」）
	mux.HandleFunc("POST /api/agent/notes/autolink", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Namespace string  `json:"namespace"`
			TopK      int     `json:"topK"`
			MinSim    float64 `json:"minSim"`
		}
		_ = decodeBody(r, &req)
		mem, err := s.memOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		res, err := mem.AutoLink(ctx, memory.AutoLinkOptions{
			Namespace: nsOf(req.Namespace), TopK: req.TopK, MinSim: req.MinSim,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
	}))

	// 重新解析悬空链接（导入顺序错乱、或手工补过笔记之后用一次）
	mux.HandleFunc("POST /api/agent/notes/resolve", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Namespace string `json:"namespace"`
		}
		_ = decodeBody(r, &req)
		mem, err := s.memOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		ns := nsOf(req.Namespace)
		n, err := mem.ResolveLinks(ns)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resolved": n, "namespace": ns})
	}))

	// 删除一篇笔记（连同它的链接）
	mux.HandleFunc("POST /api/agent/notes/{key}/forget", s.api(func(w http.ResponseWriter, r *http.Request) {
		mem, err := s.memOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		ns := nsOf(r.URL.Query().Get("namespace"))
		n, err := mem.ForgetNote(ns, strings.TrimSpace(r.PathValue("key")))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if n == 0 {
			writeErr(w, http.StatusNotFound, "没有这篇笔记，什么都没删")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "forgot": n})
	}))
}
