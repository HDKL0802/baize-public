package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"baize/internal/channels"
	"baize/internal/config"
)

// 频道（QwenPaw 频道机制的 Go 版）：你和白泽在"哪里"对话的接入点。
//
//	GET  /api/agent/channels         频道清单（含可选类型 / 出站格式）
//	POST /api/agent/channels         管理：action = save | toggle | remove | test
//	POST /api/channels/{id}/inbound  入站回调（用频道自己的令牌，不走 Baize 令牌闸门）
//
// 入站刻意不走 s.api：外部平台（飞书 / 钉钉 / Slack…）不可能带 Baize 令牌，它们带的是
// 这个频道自己的 token；出站管理接口仍走 s.api（只有持 Baize 令牌的本机才能改频道）。
func (s *Server) registerChannels(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	mux.HandleFunc("GET /api/agent/channels", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.channelsState())
	}))
	mux.HandleFunc("POST /api/agent/channels", s.api(s.handleChannelAdmin))
	mux.HandleFunc("POST /api/channels/{id}/inbound", s.handleChannelInbound)
	mux.HandleFunc("GET /api/channels/{id}/ws", s.handleChannelWS)
}

// channelsState 频道清单 + 可选类型 / 格式（GET 与管理接口回同一个形状）
func (s *Server) channelsState() map[string]any {
	list := []channels.Info{}
	if mgr := s.agent.Channels(); mgr != nil {
		list = mgr.List()
	}
	return map[string]any{
		"channels": list,
		"kinds":    config.ChannelKinds(),
		"formats":  config.ChannelFormats(),
	}
}

// handleChannelAdmin 增删改查 + 测试发送（走 s.api，只有本机能用）
func (s *Server) handleChannelAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action      string  `json:"action"`
		ID          string  `json:"id"`
		Kind        string  `json:"kind"`
		Enabled     *bool   `json:"enabled"`
		Token       *string `json:"token"`
		OutboundURL *string `json:"outboundUrl"`
		Format      *string `json:"format"`
		BotPrefix   *string `json:"botPrefix"`
		Text        string  `json:"text"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "save"
	}
	id := strings.TrimSpace(req.ID)

	cfg := s.agent.Config()
	idx := -1
	for i := range cfg.Channels {
		if cfg.Channels[i].ID == id {
			idx = i
			break
		}
	}

	switch action {
	case "save", "add", "update":
		if err := validateChannelID(id); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		cur := config.ChannelConfig{Enabled: true}
		if idx >= 0 {
			cur = cfg.Channels[idx]
		}
		if k := strings.TrimSpace(req.Kind); k != "" {
			if !config.ValidChannelKind(k) {
				writeErr(w, http.StatusBadRequest, "频道类型不合法："+k+"（可用 "+strings.Join(config.ChannelKinds(), " | ")+"）")
				return
			}
			cur.Kind = strings.ToLower(k)
		}
		if cur.Kind == "" {
			cur.Kind = "webhook"
		}
		if req.Enabled != nil {
			cur.Enabled = *req.Enabled
		}
		if req.Token != nil {
			cur.Token = strings.TrimSpace(*req.Token)
		}
		if req.OutboundURL != nil {
			cur.OutboundURL = strings.TrimSpace(*req.OutboundURL)
		}
		if req.Format != nil {
			f := strings.TrimSpace(*req.Format)
			if f != "" && !config.ValidChannelFormat(f) {
				writeErr(w, http.StatusBadRequest, "出站格式不合法："+f+"（可用 "+strings.Join(config.ChannelFormats(), " | ")+"）")
				return
			}
			cur.Format = strings.ToLower(f)
		}
		if req.BotPrefix != nil {
			cur.BotPrefix = *req.BotPrefix
		}
		cur.ID = id
		// 按类型做各自的必要校验：宁可在门口拒绝，也别存一个收不到 / 发不出的频道
		switch cur.Kind {
		case "webhook":
			// 出站地址必须有，否则回复没处发
			if cur.OutboundURL == "" {
				writeErr(w, http.StatusBadRequest, "webhook 频道必须给出站地址（outboundUrl）")
				return
			}
			if u, err := url.Parse(cur.OutboundURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				writeErr(w, http.StatusBadRequest, "出站地址要是一个 http/https URL")
				return
			}
		case "onebot":
			// 反向 WS 要鉴权：没令牌谁都能连进来派活
			if strings.TrimSpace(cur.Token) == "" {
				writeErr(w, http.StatusBadRequest, "onebot 频道必须设入站令牌（实现端连接时要带上它）")
				return
			}
		}
		if idx >= 0 {
			cfg.Channels[idx] = cur
		} else {
			cfg.Channels = append(cfg.Channels, cur)
		}

	case "toggle":
		if idx < 0 {
			writeErr(w, http.StatusNotFound, "没有这个频道："+id)
			return
		}
		cfg.Channels[idx].Enabled = !cfg.Channels[idx].Enabled
		if req.Enabled != nil {
			cfg.Channels[idx].Enabled = *req.Enabled
		}

	case "remove":
		if idx < 0 {
			writeErr(w, http.StatusNotFound, "没有这个频道："+id)
			return
		}
		cfg.Channels = append(cfg.Channels[:idx], cfg.Channels[idx+1:]...)

	case "test":
		mgr := s.agent.Channels()
		if mgr == nil {
			writeErr(w, http.StatusServiceUnavailable, "频道未装配")
			return
		}
		text := strings.TrimSpace(req.Text)
		if text == "" {
			text = "白泽频道连通性测试。"
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		if err := mgr.Send(ctx, id, channels.Message{Channel: id, Session: "test"}, text); err != nil {
			writeErr(w, http.StatusBadGateway, "测试发送失败："+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sent": id, "text": text})
		return

	default:
		writeErr(w, http.StatusBadRequest, "不支持的 action："+action+"（可用 save | toggle | remove | test）")
		return
	}

	if err := s.agent.SaveConfig(cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "保存失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.channelsState())
}

// handleChannelInbound 入站回调：跑一次 Agent 并把结论回发到出站地址。
// 用频道自己的令牌校验（外部平台带不了 Baize 令牌）。
func (s *Server) handleChannelInbound(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil {
		writeErr(w, http.StatusServiceUnavailable, "Agent 未就绪")
		return
	}
	mgr := s.agent.Channels()
	if mgr == nil {
		writeErr(w, http.StatusServiceUnavailable, "频道未装配")
		return
	}
	id := r.PathValue("id")
	var req struct {
		Session string `json:"session"`
		Sender  string `json:"sender"`
		Text    string `json:"text"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	token := r.Header.Get("X-Baize-Channel-Token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	msg := channels.Message{Channel: id, Session: req.Session, Sender: req.Sender, Text: req.Text}
	if err := mgr.Inbound(msg, token); err != nil {
		var ie *channels.InboundError
		if errors.As(err, &ie) {
			writeErr(w, ie.Code, ie.Msg)
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "channel": id})
}

// handleChannelWS "反向 WebSocket"频道的接入点（如 OneBot）：实现端主动连进来，
// 事件从这条连接进（跑 Agent），回复也从这条连接出。令牌在升级之前就校验，错就干净地回状态码。
func (s *Server) handleChannelWS(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil {
		writeErr(w, http.StatusServiceUnavailable, "Agent 未就绪")
		return
	}
	mgr := s.agent.Channels()
	if mgr == nil {
		writeErr(w, http.StatusServiceUnavailable, "频道未装配")
		return
	}
	id := r.PathValue("id")
	ch, ok := mgr.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个频道："+id)
		return
	}
	wc, ok := ch.(channels.WSChannel)
	if !ok {
		writeErr(w, http.StatusBadRequest, "频道 "+id+" 不支持反向 WebSocket 接入（这个类型不走 WS）")
		return
	}
	// 令牌：OneBot 惯例用 access_token，也认 token / Authorization: Bearer
	token := r.URL.Query().Get("access_token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			token = strings.TrimSpace(strings.TrimPrefix(a, "Bearer "))
		}
	}
	if err := mgr.CheckInbound(id, token); err != nil {
		var ie *channels.InboundError
		if errors.As(err, &ie) {
			writeErr(w, ie.Code, ie.Msg)
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.lg.Warn("频道 WebSocket 升级失败", "channel", id, "err", err)
		return
	}
	defer conn.Close()
	s.lg.Info("频道实现端已接入", "channel", id, "addr", r.RemoteAddr)
	wc.Serve(conn, func(m channels.Message) {
		if err := mgr.Inbound(m, token); err != nil {
			s.lg.Warn("频道消息被拒", "channel", id, "err", err)
		}
	})
}

// validateChannelID 频道 id 会出现在 URL 路径里，限制成安全的字符集
func validateChannelID(id string) error {
	if id == "" {
		return errors.New("频道 id 不能为空")
	}
	if len(id) > 64 {
		return errors.New("频道 id 太长（最多 64 字符）")
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return errors.New("频道 id 只能用字母 / 数字 / 连字符 / 下划线：" + id)
	}
	return nil
}
