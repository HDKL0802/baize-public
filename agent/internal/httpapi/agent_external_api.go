package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"baize/internal/config"
)

// registerExternalAgents 注册「外部 Agent」委托接口。
//
// 外部 Agent = 白泽可以把一段独立的活委托过去执行的"别的 Agent"（另一个白泽实例，或任意 HTTP 端点）。
// 它是**不可信执行体**：只传任务文本、不外泄本机文件/记忆/密钥，回来的内容当外部输入。故派活走人工审批。
func (s *Server) registerExternalAgents(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	a := s.agent

	// 配置 + 最近委托记录（记录是进程内的，重启即清零 —— 界面上会标清）
	mux.HandleFunc("GET /api/agent/external-agents", s.api(func(w http.ResponseWriter, r *http.Request) {
		cfg := a.Config()
		writeJSON(w, http.StatusOK, map[string]any{
			"agents":      a.ExternalAgents(),
			"types":       config.ExternalAgentTypes(),
			"allowRemote": cfg.AllowRemote,
			"recent":      a.Delegations(),
		})
	}))

	// action=upsert|remove|test|call
	mux.HandleFunc("POST /api/agent/external-agents", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action      string                `json:"action"`
			ID          string                `json:"id"`
			Goal        string                `json:"goal"`
			Config      *config.ExternalAgent `json:"config"`
			AllowRemote bool                  `json:"allowRemote"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		if action == "" {
			action = "upsert"
		}
		switch action {
		case "upsert":
			if req.Config == nil {
				writeErr(w, http.StatusBadRequest, "upsert 需要 config")
				return
			}
			list, err := a.ExternalAgentSave(*req.Config, req.AllowRemote)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"agents": list, "saved": req.Config.ID})
		case "remove":
			list, err := a.ExternalAgentRemove(req.ID)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"agents": list, "removed": req.ID})
		case "test":
			if strings.TrimSpace(req.ID) == "" {
				writeErr(w, http.StatusBadRequest, "test 需要 id")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
			defer cancel()
			res, err := a.ExternalAgentTest(ctx, req.ID)
			out := map[string]any{"result": res}
			if err != nil {
				out["error"] = err.Error()
			}
			writeJSON(w, http.StatusOK, out)
		case "call":
			if strings.TrimSpace(req.ID) == "" || strings.TrimSpace(req.Goal) == "" {
				writeErr(w, http.StatusBadRequest, "call 需要 id 与 goal")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
			defer cancel()
			res, err := a.CallExternalAgent(ctx, req.ID, req.Goal)
			out := map[string]any{"result": res}
			if err != nil {
				out["error"] = err.Error()
			}
			writeJSON(w, http.StatusOK, out)
		default:
			writeErr(w, http.StatusBadRequest, "不支持的 action："+action+"（可用 upsert | remove | test | call）")
		}
	}))
}
