package httpapi

import (
	"net/http"
	"strings"

	"baize/internal/agentsvc"
	"baize/internal/config"
	"baize/internal/cron"
)

// registerHeartbeat 注册「心跳」管理接口（QwenPaw heartbeat 机制的 Go 版）。
//
// 心跳 = 定时用工作区的 HEARTBEAT.md 当查询词跑一次 Agent。
// 排期复用定时任务那套 cron 解析与 20 秒 tick，配置改动靠调度器热加载生效，
// 这里只做三件事：改配置、改 HEARTBEAT.md、手动触发一次。
//
//	GET  /api/agent/heartbeat       当前状态（配置 + 排期 + 文件落地情况 + 可选 target 清单）
//	GET  /api/agent/heartbeat/file  读 HEARTBEAT.md 原文（不存在时 exists=false，不算错）
//	POST /api/agent/heartbeat       action=config|save|run
func (s *Server) registerHeartbeat(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	a := s.agent

	mux.HandleFunc("GET /api/agent/heartbeat", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.heartbeatState())
	}))

	mux.HandleFunc("GET /api/agent/heartbeat/file", s.api(func(w http.ResponseWriter, r *http.Request) {
		content, exists, err := a.HeartbeatRead()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"name": "HEARTBEAT.md", "path": a.HeartbeatPath(),
			"content": content, "exists": exists,
		})
	}))

	// action=config|save|run
	// config 的每个字段都先校验再落盘：坏表达式/坏 target/坏时段如果直接写进配置，
	// normalize 会悄悄"纠正"成默认值，用户以为设置生效了其实没有——宁可在门口就拒绝。
	mux.HandleFunc("POST /api/agent/heartbeat", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action      string  `json:"action"`
			Enabled     *bool   `json:"enabled"`
			Every       string  `json:"every"`
			Target      string  `json:"target"`
			TimeoutSec  *int    `json:"timeoutSec"`
			ActiveHours *string `json:"activeHours"`
			Content     string  `json:"content"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		runID := ""

		switch action {
		case "config":
			cfg := a.Config()
			if req.Enabled != nil {
				cfg.Heartbeat.Enabled = *req.Enabled
			}
			if e := strings.TrimSpace(req.Every); e != "" {
				if _, err := cron.Parse(e); err != nil {
					writeErr(w, http.StatusBadRequest, "间隔表达式不合法："+err.Error())
					return
				}
				cfg.Heartbeat.Every = e
			}
			if t := strings.TrimSpace(req.Target); t != "" {
				if !config.ValidHeartbeatTarget(t) {
					writeErr(w, http.StatusBadRequest,
						"分发目标不合法："+t+"（可用 "+strings.Join(config.HeartbeatTargets(), " | ")+"）")
					return
				}
				cfg.Heartbeat.Target = strings.ToLower(t)
			}
			if req.TimeoutSec != nil {
				if *req.TimeoutSec < 0 {
					writeErr(w, http.StatusBadRequest, "超时秒数不能为负（0 或不传 = 用默认 10 分钟）")
					return
				}
				cfg.Heartbeat.TimeoutSec = *req.TimeoutSec
			}
			if req.ActiveHours != nil {
				ah := strings.TrimSpace(*req.ActiveHours)
				if err := agentsvc.ParseActiveHours(ah); err != nil {
					writeErr(w, http.StatusBadRequest, err.Error())
					return
				}
				cfg.Heartbeat.ActiveHours = ah
			}
			if err := a.SaveConfig(cfg); err != nil {
				writeErr(w, http.StatusBadRequest, "保存失败："+err.Error())
				return
			}

		case "save": // 写 HEARTBEAT.md（空内容由 HeartbeatWrite 拒绝：不想让心跳干活请关开关，而不是清空文件）
			if err := a.HeartbeatWrite(req.Content); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}

		case "run": // 立即手动跑一次（与调度触发走同一条代码路径）
			id, err := a.StartHeartbeat()
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			runID = id

		default:
			writeErr(w, http.StatusBadRequest, "不支持的 action："+action+"（可用 config | save | run）")
			return
		}
		// 动作完成后回最新状态（配置改动/手动触发都会体现在里面）
		out := s.heartbeatState()
		if runID != "" {
			out["runId"] = runID
		}
		writeJSON(w, http.StatusOK, out)
	}))
}

// heartbeatState 心跳当前状态（GET 与 POST 回同一个形状，前端只写一处解析）
func (s *Server) heartbeatState() map[string]any {
	return map[string]any{
		"state":   s.agent.Heartbeat(),
		"targets": config.HeartbeatTargets(),
	}
}
