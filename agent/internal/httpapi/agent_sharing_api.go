package httpapi

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"baize/internal/accounts"
	"baize/internal/kb"
)

// 用户组共享文档：组内任何成员都能上传/看/下载别人的文档。
//
// 这是用户诉求里最具体的一条：「A 上传了文档 2，B 刷新一下就能看到并下载」。
// 上传/下载都走 JSON + base64（与知识库附件同款），手机壳的桥也能直接过。
//
// **分叉**：共享文档是内容寻址的，同名不同内容会各存一份 → 天然就是"两份并存"。
// 列表接口把同名文档归到一起（versions），左边 A 的一版、右边 B 的一版就是这么来的。
func (s *Server) registerSharing(mux *http.ServeMux) {
	if s.agent == nil || s.agent.Accounts() == nil {
		return
	}

	mux.HandleFunc("GET /api/agent/groups/{id}/docs", s.api(func(w http.ResponseWriter, r *http.Request) {
		gid := r.PathValue("id")
		store, err := s.groupDocsStore(r, gid)
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		list, err := store.List(atoiDefault(r.URL.Query().Get("limit"), 500))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读共享文档失败："+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"group": gid, "docs": list, "versions": groupByName(list), "dir": store.Dir(),
		})
	}))

	mux.HandleFunc("POST /api/agent/groups/{id}/docs", s.api(func(w http.ResponseWriter, r *http.Request) {
		gid := r.PathValue("id")
		store, err := s.groupDocsStore(r, gid)
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		var req struct {
			Name       string `json:"name"`
			Kind       string `json:"kind"`
			Mime       string `json:"mime"`
			DataBase64 string `json:"dataBase64"`
		}
		if err := decodeBodyLimit(r, &req, 48<<20); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.DataBase64))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "文档内容不是合法的 base64："+err.Error())
			return
		}
		// 上传者 = 当前登录用户（没登录则留空，但下面已经要求必须登录）
		_, u, _ := s.principalOf(r)
		info, err := store.PutBy(req.Name, req.Kind, req.Mime, u.Name, data)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		list, _ := store.List(0)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "doc": info, "versions": groupByName(list),
		})
	}))

	mux.HandleFunc("GET /api/agent/groups/{id}/docs/{docId}", s.api(func(w http.ResponseWriter, r *http.Request) {
		store, err := s.groupDocsStore(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		info, data, err := store.Get(r.PathValue("docId"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"doc": info, "dataBase64": base64.StdEncoding.EncodeToString(data),
		})
	}))

	mux.HandleFunc("DELETE /api/agent/groups/{id}/docs/{docId}", s.api(func(w http.ResponseWriter, r *http.Request) {
		store, err := s.groupDocsStore(r, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		id := r.PathValue("docId")
		if err := store.Remove(id); err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		list, _ := store.List(0)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "removed": id, "versions": groupByName(list),
		})
	}))
}

// groupDocsStore 解析并鉴权：只有**组内成员**（或管理员）能碰这个组的共享空间。
// 未登录 / 非成员一律 403 —— 共享文档是组内隐私，不因为是"同一个后端"就放开。
func (s *Server) groupDocsStore(r *http.Request, groupID string) (*kb.FileStore, error) {
	reg := s.agent.Accounts()
	if reg == nil {
		return nil, errors.New("多用户未启用")
	}
	g, err := reg.Group(groupID)
	if err != nil {
		return nil, err
	}
	_, u, loggedIn := s.principalOf(r)
	if !loggedIn {
		return nil, errors.New("请先登录再访问用户组的共享文档")
	}
	if !u.Admin && !reg.IsMember(g.ID, u.ID) {
		return nil, errors.New("你不是用户组「" + g.Name + "」的成员，看不到它的共享文档")
	}
	return s.agent.GroupDocs(accounts.GroupPrincipal(g.ID))
}

// groupByName 把文档按文件名归组，同名多份 = 分叉（界面上左右对照就取这里）
func groupByName(list []kb.FileInfo) []map[string]any {
	order := []string{}
	by := map[string][]kb.FileInfo{}
	for _, f := range list {
		if _, ok := by[f.Name]; !ok {
			order = append(order, f.Name)
		}
		by[f.Name] = append(by[f.Name], f)
	}
	out := make([]map[string]any, 0, len(order))
	for _, name := range order {
		items := by[name]
		out = append(out, map[string]any{
			"name":     name,
			"forked":   len(items) > 1,
			"versions": items,
		})
	}
	return out
}
