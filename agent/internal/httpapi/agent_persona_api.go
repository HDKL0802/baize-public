package httpapi

import (
	"net/http"
	"strings"

	"baize/internal/persona"
)

// registerPersona 注册「人设」管理接口（QwenPaw 人设机制的 Go 版）。
//
// 人设是拼进系统提示的一组 Markdown 文件，改完立即生效（每次运行现读，不缓存）。
// 管理动作与技能那块同口径：都是"改文件 + 改配置里的一行清单"，不另起一套运行时。
//
//	GET  /api/agent/persona            当前状态（总开关 / 目录 / 文件清单 / 拼装预览）
//	GET  /api/agent/persona/file?name= 读单个文件原文
//	POST /api/agent/persona            action=save|enable|disable|order|archive|reset|master
func (s *Server) registerPersona(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	a := s.agent

	mux.HandleFunc("GET /api/agent/persona", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.personaState())
	}))

	mux.HandleFunc("GET /api/agent/persona/file", s.api(func(w http.ResponseWriter, r *http.Request) {
		lib := a.Persona()
		if lib == nil {
			writeErr(w, http.StatusServiceUnavailable, "人设库没启用")
			return
		}
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		content, err := lib.Read(name)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "content": content})
	}))

	mux.HandleFunc("POST /api/agent/persona", s.api(func(w http.ResponseWriter, r *http.Request) {
		lib := a.Persona()
		if lib == nil {
			writeErr(w, http.StatusServiceUnavailable, "人设库没启用")
			return
		}
		var req struct {
			Action  string   `json:"action"`
			Name    string   `json:"name"`
			Content string   `json:"content"`
			Enabled *bool    `json:"enabled"`
			Files   []string `json:"files"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		name := strings.TrimSpace(req.Name)

		switch action {
		case "save": // 有则改写、无则新建（界面上就一个「保存」按钮）
			if !persona.Valid(name) {
				writeErr(w, http.StatusBadRequest, "文件名不合法：只允许目录内的 .md 文件名（不含路径分隔符、不以点开头）")
				return
			}
			if strings.TrimSpace(req.Content) == "" {
				writeErr(w, http.StatusBadRequest, "内容不能为空（要停用某个文件，请关掉它的开关，而不是清空内容）")
				return
			}
			if err := lib.Write(name, req.Content); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}

		case "enable", "disable":
			if !persona.Valid(name) {
				writeErr(w, http.StatusBadRequest, "文件名不合法："+name)
				return
			}
			cfg := a.Config()
			on := action == "enable"
			if req.Enabled != nil {
				on = *req.Enabled
			}
			cfg.Persona.Files = setPersonaFile(cfg.Persona.Files, name, on)
			if err := a.SaveConfig(cfg); err != nil {
				writeErr(w, http.StatusBadRequest, "保存失败："+err.Error())
				return
			}

		case "order": // 整份替换加载清单（顺序 = 拼进系统提示的顺序）
			for _, f := range req.Files {
				if !persona.Valid(f) {
					writeErr(w, http.StatusBadRequest, "加载清单里有非法文件名："+f)
					return
				}
			}
			cfg := a.Config()
			cfg.Persona.Files = dedupePersonaFiles(req.Files)
			if err := a.SaveConfig(cfg); err != nil {
				writeErr(w, http.StatusBadRequest, "保存失败："+err.Error())
				return
			}

		case "archive": // 删除 = 归档（与技能删除同口径，不硬删）
			if !persona.Valid(name) {
				writeErr(w, http.StatusBadRequest, "文件名不合法："+name)
				return
			}
			if err := lib.Archive(name); err != nil {
				writeErr(w, http.StatusNotFound, err.Error())
				return
			}
			cfg := a.Config()
			cfg.Persona.Files = setPersonaFile(cfg.Persona.Files, name, false)
			if err := a.SaveConfig(cfg); err != nil {
				writeErr(w, http.StatusBadRequest, "保存失败："+err.Error())
				return
			}

		case "reset": // 恢复成内置模板（只对有模板的三个文件有效）
			if err := lib.Reset(name); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}

		case "master": // 人设总开关
			if req.Enabled == nil {
				writeErr(w, http.StatusBadRequest, "master 需要 enabled")
				return
			}
			cfg := a.Config()
			cfg.Persona.Enabled = *req.Enabled
			if err := a.SaveConfig(cfg); err != nil {
				writeErr(w, http.StatusBadRequest, "保存失败："+err.Error())
				return
			}

		default:
			writeErr(w, http.StatusBadRequest, "不支持的 action："+action+"（可用 save | enable | disable | order | archive | reset | master）")
			return
		}
		writeJSON(w, http.StatusOK, s.personaState())
	}))
}

// personaState 人设当前状态（GET 与 POST 回同一个形状，前端只写一处解析）
func (s *Server) personaState() map[string]any {
	lib := s.agent.Persona()
	cfg := s.agent.Config()
	out := map[string]any{
		"enabled": cfg.Persona.Enabled,
		"files":   []persona.File{},
		"builtin": persona.BuiltinNames(),
		"dir":     "",
	}
	if lib == nil {
		return out
	}
	out["dir"] = lib.Dir()
	out["files"] = lib.List(cfg.Persona.Files)
	// 拼装预览：让人直观看到"白泽实际读到的开头长什么样"（截断，不让界面扛整篇）
	if cfg.Persona.Enabled {
		built := lib.Build(cfg.Persona.Files, false)
		out["tokens"] = estimatePromptTokens(built)
		out["promptPreview"] = truncateRunes(built, 1200)
	}
	return out
}

// setPersonaFile 在加载清单里加上/移除一个文件（保持其余顺序不动）
func setPersonaFile(files []string, name string, on bool) []string {
	out := make([]string, 0, len(files)+1)
	for _, f := range files {
		if f == name {
			continue
		}
		out = append(out, f)
	}
	if on {
		out = append(out, name)
	}
	return out
}

func dedupePersonaFiles(files []string) []string {
	out := make([]string, 0, len(files))
	seen := map[string]bool{}
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

func estimatePromptTokens(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if r > 0x2E80 {
			cjk++
		} else {
			other++
		}
	}
	return cjk + other/4
}

func truncateRunes(s string, limit int) string {
	rs := []rune(s)
	if len(rs) <= limit {
		return s
	}
	return string(rs[:limit]) + "\n…（已截断，完整内容在文件里）"
}