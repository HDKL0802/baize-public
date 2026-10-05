package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"baize/internal/config"
)

// Webhook 通用 HTTP 频道：
//   - 出站：把回复 POST 到 outboundUrl。format 决定请求体形状，
//     可直接对接飞书 / 钉钉 / Slack 的群机器人 webhook（这三个都只要一个 URL）。
//   - 入站：由 HTTP 层 POST /api/channels/{id}/inbound 驱动（见 Manager.Inbound）。
type Webhook struct {
	id     string
	url    string
	format string
	prefix string
	client *http.Client
}

// NewWebhook 按配置造一个 webhook 频道
func NewWebhook(cfg config.ChannelConfig) *Webhook {
	return &Webhook{
		id:     strings.TrimSpace(cfg.ID),
		url:    strings.TrimSpace(cfg.OutboundURL),
		format: NormalizeFormat(cfg.Format),
		prefix: cfg.BotPrefix,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// NormalizeFormat 归一化出站体格式（空 / 未知一律 generic）
func NormalizeFormat(f string) string {
	f = strings.ToLower(strings.TrimSpace(f))
	for _, k := range config.ChannelFormats() {
		if f == k {
			return f
		}
	}
	return "generic"
}

// Send 把一条回复发到 outboundUrl
func (w *Webhook) Send(ctx context.Context, msg Message, reply string) error {
	if w.url == "" {
		return errors.New("频道没配 outboundUrl，发不出去")
	}
	body, err := json.Marshal(w.buildBody(msg, w.prefix+reply))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST 出站地址失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// 读一小段响应体，方便定位（限长，避免把日志撑爆）
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("出站地址返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// buildBody 按 format 组装请求体
func (w *Webhook) buildBody(msg Message, text string) any {
	switch w.format {
	case "feishu": // 飞书群自定义机器人
		return map[string]any{"msg_type": "text", "content": map[string]any{"text": text}}
	case "dingtalk": // 钉钉群机器人
		return map[string]any{"msgtype": "text", "text": map[string]any{"content": text}}
	case "slack": // Slack Incoming Webhook
		return map[string]any{"text": text}
	default: // generic：带上下文的原始 JSON，交给下游自己处理
		return map[string]any{
			"channel": msg.Channel, "session": msg.Session, "sender": msg.Sender, "text": text,
		}
	}
}
