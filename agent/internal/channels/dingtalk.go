package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"baize/internal/config"
)

// 钉钉频道（Stream 模式长连接）。
//
// 为什么选 Stream 而非事件回调：白泽跑在 NAS 上，多半没有公网入口；Stream 由白泽
// **主动连出去**，消息从这条长连接推下来，回复走消息里带的 `sessionWebhook`（临时地址）。
// 协议照钉钉官方 Stream 规范：
//
//	POST {host}/v1.0/gateway/connections/open  → {endpoint, ticket}
//	WS  dial {endpoint}?ticket={ticket}
//	服务端推 DataFrame{type:"CALLBACK", headers:{topic:"/v1.0/im/bot/messages/get"}, data:"<json>"}
//	客户端回 ACK DataFrameResponse{code:200, headers:{messageId, contentType:"application/json"}}
//
// 系统帧（type:"SYSTEM"）里 topic=ping 要回一个 pong，topic=disconnect 表示服务端要求断开（重连）。
const (
	dingTalkGatewayURL = "https://api.dingtalk.com/v1.0/gateway/connections/open"
	dingTalkBotTopic   = "/v1.0/im/bot/messages/get"

	dingTalkRetryMin = 3 * time.Second
	dingTalkRetryMax = 60 * time.Second

	// 钉钉对话类型（conversationType）：1=单聊，2=群聊
	dingTalkSingleChat = "1"
	dingTalkGroupChat  = "2"
)

// DingTalk 钉钉频道：实现 Channel（出站回复 + 主动推送）与 PollChannel（长连接收消息）。
type DingTalk struct {
	id       string
	prefix   string
	clientID string
	secret   string
	gateway  string // 网关地址（默认官方；测试可覆盖）

	lg     *slog.Logger
	client *http.Client

	mu   sync.Mutex
	conn *websocket.Conn
}

// NewDingTalk 造一个钉钉频道。clientID / clientSecret 用钉钉应用的 AppKey / AppSecret。
func NewDingTalk(cfg config.ChannelConfig, lg *slog.Logger) *DingTalk {
	if lg == nil {
		lg = slog.Default()
	}
	gw := strings.TrimSpace(cfg.Domain)
	if gw == "" {
		gw = dingTalkGatewayURL
	}
	return &DingTalk{
		id:       strings.TrimSpace(cfg.ID),
		prefix:   cfg.BotPrefix,
		clientID: strings.TrimSpace(cfg.AppID),
		secret:   strings.TrimSpace(cfg.AppSecret),
		gateway:  gw,
		lg:       lg,
		client:   &http.Client{Timeout: 15 * time.Second},
	}
}

// Poll 建立并维持一条 Stream 长连接：断线自动重连（指数退避），ctx 取消即停。
func (d *DingTalk) Poll(ctx context.Context, onEvent func(Message)) {
	if d.clientID == "" || d.secret == "" {
		d.lg.Warn("钉钉频道没配 appId / appSecret，连不上", "channel", d.id)
		return
	}
	backoff := dingTalkRetryMin
	for {
		if ctx.Err() != nil {
			return
		}
		err := d.runOnce(ctx, onEvent)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			d.lg.Warn("钉钉 Stream 断开，稍后重连", "channel", d.id, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < dingTalkRetryMax {
			backoff *= 2
		}
	}
}

// runOnce 连一次（换接入点 → 连 WS → 读到断开为止）。成功读一段就重置退避由外层负责。
func (d *DingTalk) runOnce(ctx context.Context, onEvent func(Message)) error {
	endpoint, ticket, err := d.openConnection(ctx)
	if err != nil {
		return err
	}
	wsURL := endpoint
	if strings.Contains(wsURL, "?") {
		wsURL += "&ticket=" + ticket
	} else {
		wsURL += "?ticket=" + ticket
	}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("连钉钉 Stream 失败：%w", err)
	}
	d.setConn(conn)
	defer d.clearConn(conn)
	d.lg.Info("钉钉频道已连上 Stream", "channel", d.id)

	// ctx 取消时主动关闭连接，让 ReadMessage 立刻返回
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		d.handleFrame(conn, data, onEvent)
	}
}

// openConnection 向钉钉换一个临时接入点（endpoint + ticket）
func (d *DingTalk) openConnection(ctx context.Context) (endpoint, ticket string, err error) {
	body, _ := json.Marshal(map[string]any{
		"clientId":     d.clientID,
		"clientSecret": d.secret,
		"subscriptions": []map[string]string{
			{"type": "CALLBACK", "topic": dingTalkBotTopic},
		},
		"ua": "baize-agent",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.gateway, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("换钉钉接入点失败：%w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("钉钉接入点返回 http %d：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Endpoint string `json:"endpoint"`
		Ticket   string `json:"ticket"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", fmt.Errorf("解析钉钉接入点响应失败：%w", err)
	}
	if out.Endpoint == "" || out.Ticket == "" {
		return "", "", errors.New("钉钉没给出接入点（endpoint / ticket 为空）")
	}
	return out.Endpoint, out.Ticket, nil
}

// handleFrame 处理一条服务端帧：系统帧回 pong / 断开；回调帧解出消息并回调。
func (d *DingTalk) handleFrame(conn *websocket.Conn, raw []byte, onEvent func(Message)) {
	var frame dingTalkFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return
	}
	switch frame.Type {
	case "SYSTEM":
		switch frame.Headers["topic"] {
		case "ping":
			d.writeAck(conn, frame.messageID()) // pong 用同样的 ACK 形状
		case "disconnect":
			_ = conn.Close()
		}
	case "CALLBACK":
		d.writeAck(conn, frame.messageID())
		if frame.Headers["topic"] != dingTalkBotTopic {
			return
		}
		if msg, ok := d.parseInbound(frame.Data); ok {
			onEvent(msg)
		}
	}
}

// parseInbound 把钉钉机器人消息（data JSON）解成一条入站 Message。
func (d *DingTalk) parseInbound(data string) (Message, bool) {
	var m struct {
		ConversationID   string `json:"conversationId"`
		ConversationType string `json:"conversationType"`
		SenderID         string `json:"senderId"`
		SenderStaffID    string `json:"senderStaffId"`
		SenderNick       string `json:"senderNick"`
		MsgID            string `json:"msgId"`
		Msgtype          string `json:"msgtype"`
		RobotCode        string `json:"robotCode"`
		SessionWebhook   string `json:"sessionWebhook"`
		Text             struct {
			Content string `json:"content"`
		} `json:"text"`
	}
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return Message{}, false
	}
	text := strings.TrimSpace(m.Text.Content)
	if text == "" {
		return Message{}, false // 先把文本做扎实；图片/文件/语音后续再补
	}
	group := m.ConversationType == dingTalkGroupChat
	session := "dingtalk:single:" + firstNonEmpty(m.SenderStaffID, m.SenderID)
	if group {
		session = "dingtalk:group:" + m.ConversationID
	}
	sender := strings.TrimSpace(m.SenderNick)
	if sender == "" {
		sender = firstNonEmpty(m.SenderStaffID, m.SenderID)
	}
	return Message{
		Channel: d.id,
		Session: session,
		Sender:  sender,
		Text:    text,
		Meta: map[string]any{
			"session_webhook": strings.TrimSpace(m.SessionWebhook),
			"conversation_id": m.ConversationID,
			"is_group":        group,
			"message_id":      m.MsgID,
		},
	}, true
}

// Send 把回复发到消息来源：优先走消息里带的 sessionWebhook（临时地址，最简单）。
func (d *DingTalk) Send(ctx context.Context, msg Message, reply string) error {
	hook, _ := msg.Meta["session_webhook"].(string)
	hook = strings.TrimSpace(hook)
	if hook == "" {
		return errors.New("这条钉钉消息没有 sessionWebhook，回不了（可能是主动推送；钉钉主动推送要另配机器人 OpenAPI）")
	}
	payload, _ := json.Marshal(map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": d.prefix + reply},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("调钉钉 sessionWebhook 失败：%w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode >= 300 || out.ErrCode != 0 {
		return fmt.Errorf("钉钉回发失败（http %d，errcode %d）：%s", resp.StatusCode, out.ErrCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// writeAck 回一条 ACK（钉钉要求每个回调帧都 ACK，否则会重投）
func (d *DingTalk) writeAck(conn *websocket.Conn, messageID string) {
	if conn == nil {
		return
	}
	ack := map[string]any{
		"code": 200,
		"headers": map[string]string{
			"messageId":   messageID,
			"contentType": "application/json",
		},
		"message": "ok",
		"data":    "",
	}
	b, _ := json.Marshal(ack)
	_ = conn.WriteMessage(websocket.TextMessage, b)
}

// dingTalkFrame 钉钉 Stream 数据帧
type dingTalkFrame struct {
	SpecVersion string            `json:"specVersion"`
	Type        string            `json:"type"`
	Time        int64             `json:"time"`
	Headers     map[string]string `json:"headers"`
	Data        string            `json:"data"`
}

func (f dingTalkFrame) messageID() string {
	if f.Headers == nil {
		return ""
	}
	return f.Headers["messageId"]
}

func (d *DingTalk) setConn(c *websocket.Conn) {
	d.mu.Lock()
	d.conn = c
	d.mu.Unlock()
}

func (d *DingTalk) clearConn(c *websocket.Conn) {
	d.mu.Lock()
	if d.conn == c {
		d.conn = nil
	}
	d.mu.Unlock()
	_ = c.Close()
}

// firstNonEmpty 返回第一个非空串（都为空则空串）
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
