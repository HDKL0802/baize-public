package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"baize/internal/kb"
)

// SetKB 挂上知识库（必须在 Handler() 之前调用）
func (s *Server) SetKB(k *kb.Service) { s.kb = k }

// registerKB 注册知识库接口。
//
// 这是「正本」这一侧的接口：手机内核（跨端模式）与桌面控制台都读它。
// 口径刻意与手机内核的 /api/state、/api/op 保持一致，两边的操作名完全通用。
func (s *Server) registerKB(mux *http.ServeMux) {
	if s.kb == nil {
		return
	}
	k := s.kb

	// 快照：直接回 Snapshot 本体（手机内核的 Remote.State 就按这个形状解）
	mux.HandleFunc("GET /api/kb/state", s.api(func(w http.ResponseWriter, r *http.Request) {
		snap, err := k.State()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读知识库失败："+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, snap)
	}))

	// 操作：{"op":"todo.add","args":{...}} → {"ok":true,"data":...} / {"ok":false,"error":"..."}
	mux.HandleFunc("POST /api/kb/op", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Op   string          `json:"op"`
			Args json.RawMessage `json:"args"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		data, err := k.Do(req.Op, req.Args)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": data})
	}))

	// 整库导入（手机端首次迁移用；重复调用安全）
	mux.HandleFunc("POST /api/kb/import", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Todos []map[string]any `json:"todos"`
			Vault []map[string]any `json:"vault"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		raw, err := json.Marshal(map[string]any{"todos": req.Todos, "vault": req.Vault})
		if err != nil {
			writeErr(w, http.StatusBadRequest, "组装导入请求失败："+err.Error())
			return
		}
		data, err := k.Do("kb.import", raw)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": data})
	}))

	// 控制台概览
	mux.HandleFunc("GET /api/kb/stats", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, k.Stats())
	}))

	/* ---- 附件：手机上不留文件，附件统一存这儿 ---- */

	mux.HandleFunc("GET /api/kb/files", s.api(func(w http.ResponseWriter, r *http.Request) {
		list, err := k.Files().List(atoiDefault(r.URL.Query().Get("limit"), 200))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读附件列表失败："+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"files": list, "dir": k.Files().Dir()})
	}))

	// 上传：JSON + base64（手机壳的桥只能传文本，multipart 过不去）
	mux.HandleFunc("POST /api/kb/files", s.api(func(w http.ResponseWriter, r *http.Request) {
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
			writeErr(w, http.StatusBadRequest, "附件内容不是合法的 base64："+err.Error())
			return
		}
		info, err := k.Files().Put(req.Name, req.Kind, req.Mime, data)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"file": info})
	}))

	// 下载：同样回 base64，手机端拿得到就能落成本地文件再打开
	mux.HandleFunc("GET /api/kb/files/{id}", s.api(func(w http.ResponseWriter, r *http.Request) {
		info, data, err := k.Files().Get(r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"file": info, "dataBase64": base64.StdEncoding.EncodeToString(data),
		})
	}))

	mux.HandleFunc("DELETE /api/kb/files/{id}", s.api(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := k.Files().Remove(id); err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": id})
	}))
}

// decodeBodyLimit 与 decodeBody 同款，只是把请求体上限放宽（附件走 base64 会比较大）
func decodeBodyLimit(r *http.Request, v any, limit int64) error {
	if r.Body == nil {
		return errors.New("请求体为空")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	if err := dec.Decode(v); err != nil {
		return errors.New("请求体解析失败：" + err.Error())
	}
	return nil
}
