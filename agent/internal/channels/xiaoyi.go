package channels

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"baize/internal/config"
)

// 小艺频道（华为小艺开放平台，A2A / Agent-to-Agent 协议，走 WebSocket 长连接）。
//
// 协议照 QwenPaw（agentscope-ai/QwenPaw）的 src/qwenpaw/app/channels/xiaoyi/
// （auth.py / channel.py / constants.py）：
//
//   - 长连接：白泽作为 WebSocket **客户端**主动连出去（NAS 没公网入口也能用），
//     同时连「主域名 + 备用 IP」两条链路（服务端可能从任一条推消息下来）。
//   - 鉴权：AK/SK 签名，握手时带四个请求头（见 auth.py:generate_auth_headers）：
//     x-access-key = AK
//     x-ts         = 毫秒时间戳
//     x-sign       = Base64(HMAC-SHA256(SK, x-ts))
//     x-agent-id   = Agent ID
//   - 握手后先发一条 clawd_bot_init（带 agentId + hostname），之后每 30s 发一条 heartbeat。
//   - 入站：服务端推 {"method":"message/stream","params":{...}}，正文在
//     params.message.parts[].text（parts 里 kind 为 "text"）。
//   - 出站：回 {"msgType":"agent_response","agentId":...,"sessionId":...,"taskId":...,
//     "msgDetail":"<jsonrpc>"}；msgDetail 里是 A2A 的 artifact-update（parts 为 text）。
//     整段回复发完再补 status-update(state=completed) + artifact-update(final=true) 关闭流，
//     否则小艺界面会一直停在「运行中」。
//
// 只做「文本」：parts 里 kind="file"（文件/图片）需要下载到本地媒体目录，这里先不处理，
// 只记日志、不假装收到。依赖只用标准库 + github.com/gorilla/websocket。
//
// 映射到白泽的接口：Send 一次调用 = QwenPaw 的 send()（分片发文本）+ send_final_message()
// （关闭流）两段的合并——白泽的 Send 就是「把这条回复彻底发完」的唯一出口。
type XiaoYi struct {
	id      string
	prefix  string
	ak      string // AppID（Access Key）
	sk      string // AppSecret（Secret Key）
	agentID string // 小艺开放平台的 Agent ID

	primaryURL string // 主链路 WebSocket 地址
	backupURL  string // 备用链路（仅未自定义 domain 时才连）

	lg *slog.Logger

	mu            sync.Mutex
	conns         map[string]*xyConn // serverName -> 连接（"primary" / "backup"）
	sessionServer map[string]string  // sessionId -> 该会话来自哪条链路
	sessionTask   map[string]string  // sessionId -> 最近一次任务 taskId
}

// xyConn 一条小艺 WebSocket 连接 + 写锁（gorilla 不允许并发写）
type xyConn struct {
	name string
	c    *websocket.Conn
	mu   sync.Mutex
}

func (wc *xyConn) writeJSON(v any) error {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	_ = wc.c.SetWriteDeadline(time.Now().Add(xiaoYiWriteTimeout))
	return wc.c.WriteJSON(v)
}

const (
	// 小艺开放平台默认地址（constants.py：DEFAULT_WS_URL / DEFAULT_WS_URL_BACKUP）
	xiaoYiDefaultWSURL       = "wss://hag.cloud.huawei.com/openclaw/v1/ws/link"
	xiaoYiDefaultWSURLBackup = "wss://116.63.174.231/openclaw/v1/ws/link"

	// constants.py：HEARTBEAT_INTERVAL / CONNECTION_TIMEOUT / TEXT_CHUNK_LIMIT
	xiaoYiHeartbeatInterval = 30 * time.Second
	xiaoYiConnTimeout       = 30 * time.Second
	xiaoYiTextChunkLimit    = 4000

	// 写超时（协议未定，取与其它频道一致的合理值）
	xiaoYiWriteTimeout = 10 * time.Second

	// 会话路由映射上限（对应 channel.py 的 _SESSION_MAP_MAX），超过就整体清空，避免无限增长
	xiaoYiSessionMapMax = 4096
)

// 重连节奏（constants.py：RECONNECT_DELAYS）
var xiaoYiReconnectDelays = []time.Duration{
	1 * time.Second, 2 * time.Second, 5 * time.Second,
	10 * time.Second, 30 * time.Second, 60 * time.Second,
}

// NewXiaoYi 造一个小艺频道。凭据取 ChannelConfig：
//
//	AppID     = AK（Access Key）
//	AppSecret = SK（Secret Key）
//	AgentID   = 小艺开放平台的 Agent ID
//	Domain    = 可选，覆盖主链路 WebSocket 地址（自建/测试可改）；留空用官方默认地址
//
// 其余字段：ID = 频道 id，BotPrefix = 回复前缀。
func NewXiaoYi(cfg config.ChannelConfig, lg *slog.Logger) *XiaoYi {
	if lg == nil {
		lg = slog.Default()
	}
	primary := strings.TrimSpace(cfg.Domain)
	backup := ""
	if primary == "" {
		primary = xiaoYiDefaultWSURL
		// 与 channel.py 一致：只有没自定义 ws_url 时才连备用 IP 链路
		backup = xiaoYiDefaultWSURLBackup
	}
	return &XiaoYi{
		id:            strings.TrimSpace(cfg.ID),
		prefix:        cfg.BotPrefix,
		ak:            strings.TrimSpace(cfg.AppID),
		sk:            strings.TrimSpace(cfg.AppSecret),
		agentID:       strings.TrimSpace(cfg.AgentID),
		primaryURL:    primary,
		backupURL:     backup,
		lg:            lg,
		conns:         map[string]*xyConn{},
		sessionServer: map[string]string{},
		sessionTask:   map[string]string{},
	}
}

// Poll 建立并维持小艺的 WebSocket 长连接（主 + 备），断线按固定节奏重连，ctx 取消即停。
// 未配 AK / SK / AgentID 时明确报错（Error 日志 + 直接返回），不伪装成已连接。
func (x *XiaoYi) Poll(ctx context.Context, onEvent func(Message)) {
	if x.ak == "" || x.sk == "" || x.agentID == "" {
		x.lg.Error("小艺频道没配 AK / SK / AgentID，连不上（appId=AK、appSecret=SK、agentId）", "channel", x.id)
		return
	}
	delayIdx := 0
	for {
		if ctx.Err() != nil {
			return
		}
		connected := x.runConnections(ctx, onEvent)
		if ctx.Err() != nil {
			return
		}
		if connected {
			delayIdx = 0 // 连上过就重置退避（对应 channel.py 里 _reconnect_attempts = 0）
		}
		delay := xiaoYiReconnectDelays[min(delayIdx, len(xiaoYiReconnectDelays)-1)]
		x.lg.Warn("小艺连接全部断开，稍后重连", "channel", x.id, "afterSec", int(delay.Seconds()))
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if !connected && delayIdx < len(xiaoYiReconnectDelays)-1 {
			delayIdx++
		}
	}
}

// runConnections 同时连主链路与备用链路，阻塞到两条都结束（ctx 取消或对端断开）。
// 返回是否有过任意一条成功连上。
func (x *XiaoYi) runConnections(ctx context.Context, onEvent func(Message)) bool {
	x.mu.Lock()
	x.conns = map[string]*xyConn{}
	x.mu.Unlock()

	type target struct{ name, wsURL string }
	targets := []target{{"primary", x.primaryURL}}
	if x.backupURL != "" && x.backupURL != x.primaryURL {
		targets = append(targets, target{"backup", x.backupURL})
	}

	var anyConn atomic.Bool
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t target) {
			defer wg.Done()
			if err := x.runConn(ctx, t.name, t.wsURL, onEvent, &anyConn); err != nil && ctx.Err() == nil {
				x.lg.Warn("小艺连接断开", "channel", x.id, "server", t.name, "err", err)
			}
		}(t)
	}
	wg.Wait()
	return anyConn.Load()
}

// runConn 连一条链路上：握手 → 发 init → 起心跳 → 读到断开为止。
func (x *XiaoYi) runConn(ctx context.Context, name, wsURL string, onEvent func(Message), anyConn *atomic.Bool) error {
	dialer := websocket.Dialer{HandshakeTimeout: xiaoYiConnTimeout}
	// 备用链路是 IP 直连，证书 CN 对不上 IP：跳过校验（对应 channel.py: _get_ssl_for_url）
	if xiaoYiIsIPURL(wsURL) {
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	conn, _, err := dialer.DialContext(ctx, wsURL, x.authHeaders())
	if err != nil {
		return fmt.Errorf("连小艺 %s 失败：%w", name, err)
	}
	wc := &xyConn{name: name, c: conn}
	x.setConn(name, wc)
	defer x.clearConn(name, wc)
	anyConn.Store(true)
	x.lg.Info("小艺频道已连上", "channel", x.id, "server", name)

	// 握手：先发初始化消息
	if err := wc.writeJSON(x.initMessage()); err != nil {
		return fmt.Errorf("发小艺初始化消息失败：%w", err)
	}

	stop := make(chan struct{})
	defer close(stop)
	go x.heartbeatLoop(ctx, wc, stop)
	// ctx 取消时主动关连接，让 ReadMessage 立刻返回
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
		x.handleFrame(wc, data, onEvent)
	}
}

// heartbeatLoop 每 30s 发一条 heartbeat（对应 channel.py: _heartbeat_loop）
func (x *XiaoYi) heartbeatLoop(ctx context.Context, wc *xyConn, stop <-chan struct{}) {
	t := time.NewTicker(xiaoYiHeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-t.C:
			msg := map[string]any{
				"msgType":   "heartbeat",
				"agentId":   x.agentID,
				"msgDetail": xiaoYiJSON(map[string]any{"timestamp": time.Now().UnixMilli()}),
			}
			if err := wc.writeJSON(msg); err != nil {
				x.lg.Warn("小艺心跳发送失败", "channel", x.id, "server", wc.name, "err", err)
				return
			}
		}
	}
}

// initMessage 握手后的初始化帧（对应 channel.py: _send_init_message）
func (x *XiaoYi) initMessage() map[string]any {
	host, _ := os.Hostname()
	return map[string]any{
		"msgType":   "clawd_bot_init",
		"agentId":   x.agentID,
		"msgDetail": xiaoYiJSON(map[string]any{"agentId": x.agentID, "hostname": host}),
	}
}

// handleFrame 处理一条服务端帧：分流 clearContext / tasks/cancel / message/stream。
func (x *XiaoYi) handleFrame(wc *xyConn, raw []byte, onEvent func(Message)) {
	var frame xiaoYiFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return
	}
	// agentId 校验（对应 channel.py: 不匹配就忽略）
	if frame.AgentID != "" && frame.AgentID != x.agentID {
		x.lg.Warn("小艺消息的 agentId 不匹配，忽略", "channel", x.id, "got", frame.AgentID)
		return
	}
	sessionID := firstNonEmpty(frame.Params.SessionID, frame.SessionID)
	if sessionID != "" {
		x.setSession(wc.name, sessionID, "")
	}

	// 清除上下文
	if frame.Method == "clearContext" || frame.Action == "clear" {
		x.sendClearContextResponse(wc, sessionID, frame.ID)
		x.forgetTask(sessionID)
		return
	}
	// 取消任务
	if frame.Method == "tasks/cancel" || frame.Action == "tasks/cancel" {
		x.sendTasksCancelResponse(wc, sessionID, frame.ID)
		return
	}
	// 只认 A2A 的消息流
	if frame.Method != "message/stream" {
		return
	}
	if sessionID == "" {
		x.lg.Warn("小艺消息没有 sessionId，忽略", "channel", x.id)
		return
	}
	taskID := firstNonEmpty(frame.Params.ID, frame.ID)
	x.setSession(wc.name, sessionID, taskID)

	var parts []string
	for _, p := range frame.Params.Message.Parts {
		switch p.Kind {
		case "text":
			if p.Text != "" {
				parts = append(parts, p.Text)
			}
		case "file":
			// 文件 / 图片分片需要下载到媒体目录，这里暂不处理，只如实记日志
			x.lg.Warn("小艺消息里的文件分片暂不支持，已跳过", "channel", x.id, "session", sessionID)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, " "))
	if text == "" {
		return
	}
	onEvent(Message{
		Channel: x.id,
		Session: "xiaoyi:" + sessionID,
		Sender:  sessionID,
		Text:    text,
		Meta: map[string]any{
			"session_id": sessionID,
			"task_id":    taskID,
			"message_id": frame.ID,
			"server":     wc.name,
		},
	})
}

// Send 把回复发到会话来源的那条链路：按 TEXT_CHUNK_LIMIT 分片发 artifact-update，
// 发完再补 status-update(completed) + artifact-update(final) 关闭流。
func (x *XiaoYi) Send(ctx context.Context, msg Message, reply string) error {
	if x.ak == "" || x.sk == "" || x.agentID == "" {
		return errors.New("小艺频道没配 AK / SK / AgentID，发不出去")
	}
	sessionID := xiaoYiNativeSessionID(xiaoYiMetaString(msg.Meta, "session_id"))
	if sessionID == "" {
		sessionID = xiaoYiNativeSessionID(msg.Session)
	}
	if sessionID == "" {
		return errors.New("这条小艺消息没有会话 id（session_id），回不了")
	}
	taskID := xiaoYiMetaString(msg.Meta, "task_id")
	if taskID == "" {
		taskID = x.taskFor(sessionID)
	}
	if taskID == "" {
		return fmt.Errorf("小艺会话 %s 没有 task_id，回不了（主动推送要先告诉会话的 task）", sessionID)
	}
	// 与小艺协议一致：不发空文本（channel.py: send()）
	if strings.TrimSpace(reply) == "" {
		return nil
	}
	target := x.connFor(sessionID)
	if target == nil {
		target = x.anyConn()
	}
	if target == nil {
		return errors.New("小艺频道当前没有可用连接（服务端未连上）")
	}
	messageID := xiaoYiMetaString(msg.Meta, "message_id")
	if messageID == "" {
		messageID = xiaoYiNewID()
	}
	// 发送前尊重调用方的取消语义
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, chunk := range xiaoYiChunkText(x.prefix + reply) {
		frame := x.buildArtifactMsg(sessionID, taskID, messageID,
			[]map[string]any{{"kind": "text", "text": chunk}}, false)
		if err := target.writeJSON(frame); err != nil {
			return fmt.Errorf("发小艺消息失败：%w", err)
		}
	}
	return x.sendFinalMessage(target, sessionID, taskID, messageID)
}

// sendFinalMessage 关闭回复流：先 status-update(state=completed)，再 artifact-update(final)。
// 少了 status-update 小艺界面会一直停在「运行中」（channel.py: send_final_message 的注释）。
func (x *XiaoYi) sendFinalMessage(target *xyConn, sessionID, taskID, messageID string) error {
	status := map[string]any{
		"msgType":   "agent_response",
		"agentId":   x.agentID,
		"sessionId": sessionID,
		"taskId":    taskID,
		"msgDetail": xiaoYiJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      messageID,
			"result": map[string]any{
				"taskId": taskID,
				"kind":   "status-update",
				"final":  false,
				"status": map[string]any{
					"message": map[string]any{
						"role":  "agent",
						"parts": []map[string]any{{"kind": "text", "text": ""}},
					},
					"state": "completed",
				},
			},
		}),
	}
	if err := target.writeJSON(status); err != nil {
		return fmt.Errorf("发小艺完成状态失败：%w", err)
	}
	final := x.buildArtifactMsg(sessionID, taskID, messageID,
		[]map[string]any{{"kind": "text", "text": ""}}, true)
	if err := target.writeJSON(final); err != nil {
		return fmt.Errorf("发小艺结束帧失败：%w", err)
	}
	return nil
}

// sendClearContextResponse 回 clearContext 的应答（channel.py: _send_clear_context_response）
func (x *XiaoYi) sendClearContextResponse(wc *xyConn, sessionID, requestID string) {
	if sessionID == "" {
		return
	}
	msg := x.agentResponseMsg(sessionID, requestID, map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"result":  map[string]any{"status": map[string]any{"state": "cleared"}},
	})
	if err := x.writeSession(sessionID, wc, msg); err != nil {
		x.lg.Warn("小艺 clearContext 应答发送失败", "channel", x.id, "err", err)
	}
}

// sendTasksCancelResponse 回 tasks/cancel 的应答（channel.py: _send_tasks_cancel_response）
func (x *XiaoYi) sendTasksCancelResponse(wc *xyConn, sessionID, requestID string) {
	if sessionID == "" {
		return
	}
	msg := x.agentResponseMsg(sessionID, requestID, map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"result": map[string]any{
			"id":     requestID,
			"status": map[string]any{"state": "canceled"},
		},
	})
	if err := x.writeSession(sessionID, wc, msg); err != nil {
		x.lg.Warn("小艺 tasks/cancel 应答发送失败", "channel", x.id, "err", err)
	}
}

// buildArtifactMsg 组一条 artifact-update 帧（channel.py: _build_artifact_msg）
func (x *XiaoYi) buildArtifactMsg(sessionID, taskID, messageID string, parts []map[string]any, final bool) map[string]any {
	return x.agentResponseMsg(sessionID, taskID, map[string]any{
		"jsonrpc": "2.0",
		"id":      messageID,
		"result": map[string]any{
			"taskId":    taskID,
			"kind":      "artifact-update",
			"append":    true,
			"lastChunk": true,
			"final":     final,
			"artifact": map[string]any{
				"artifactId": "artifact_" + xiaoYiNewHex(16),
				"parts":      parts,
			},
		},
	})
}

// agentResponseMsg 组装最外层的 agent_response 信封
func (x *XiaoYi) agentResponseMsg(sessionID, taskID string, inner map[string]any) map[string]any {
	return map[string]any{
		"msgType":   "agent_response",
		"agentId":   x.agentID,
		"sessionId": sessionID,
		"taskId":    taskID,
		"msgDetail": xiaoYiJSON(inner),
	}
}

// writeSession 把消息发到会话所属链路；没有就退到当前链路，再退到任意可用链路。
func (x *XiaoYi) writeSession(sessionID string, fallback *xyConn, msg map[string]any) error {
	target := x.connFor(sessionID)
	if target == nil {
		target = fallback
	}
	if target == nil {
		target = x.anyConn()
	}
	if target == nil {
		return errors.New("小艺频道没有可用连接")
	}
	return target.writeJSON(msg)
}

// authHeaders 生成握手鉴权头（auth.py: generate_auth_headers）
func (x *XiaoYi) authHeaders() http.Header {
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	h := http.Header{}
	h.Set("x-access-key", x.ak)
	h.Set("x-sign", xiaoYiSign(x.sk, ts))
	h.Set("x-ts", ts)
	h.Set("x-agent-id", x.agentID)
	return h
}

func (x *XiaoYi) setConn(name string, wc *xyConn) {
	x.mu.Lock()
	x.conns[name] = wc
	x.mu.Unlock()
}

func (x *XiaoYi) clearConn(name string, wc *xyConn) {
	x.mu.Lock()
	if x.conns[name] == wc {
		delete(x.conns, name)
	}
	x.mu.Unlock()
	_ = wc.c.Close()
}

// anyConn 任取一条可用连接（优先主链路）
func (x *XiaoYi) anyConn() *xyConn {
	x.mu.Lock()
	defer x.mu.Unlock()
	if c := x.conns["primary"]; c != nil {
		return c
	}
	if c := x.conns["backup"]; c != nil {
		return c
	}
	return nil
}

// connFor 取会话所属链路的连接（没有则 nil）
func (x *XiaoYi) connFor(sessionID string) *xyConn {
	x.mu.Lock()
	defer x.mu.Unlock()
	name := x.sessionServer[sessionID]
	if name == "" {
		return nil
	}
	return x.conns[name]
}

// setSession 记下会话来自哪条链路 / 对应 taskId（有界，超过上限整体清空）
func (x *XiaoYi) setSession(serverName, sessionID, taskID string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.sessionServer) >= xiaoYiSessionMapMax {
		x.sessionServer = map[string]string{}
	}
	x.sessionServer[sessionID] = serverName
	if taskID != "" {
		if len(x.sessionTask) >= xiaoYiSessionMapMax {
			x.sessionTask = map[string]string{}
		}
		x.sessionTask[sessionID] = taskID
	}
}

func (x *XiaoYi) taskFor(sessionID string) string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.sessionTask[sessionID]
}

func (x *XiaoYi) forgetTask(sessionID string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.sessionTask, sessionID)
}

// xiaoYiFrame 小艺 A2A 帧（只取我们关心的字段）
type xiaoYiFrame struct {
	MsgType   string `json:"msgType"`
	AgentID   string `json:"agentId"`
	SessionID string `json:"sessionId"`
	TaskID    string `json:"taskId"`
	Method    string `json:"method"`
	Action    string `json:"action"`
	ID        string `json:"id"`
	Params    struct {
		SessionID string `json:"sessionId"`
		ID        string `json:"id"`
		Message   struct {
			Parts []struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"message"`
	} `json:"params"`
}

// xiaoYiSign 生成签名：Base64(HMAC-SHA256(SK, timestamp))（auth.py: generate_signature）
func xiaoYiSign(sk, timestamp string) string {
	mac := hmac.New(sha256.New, []byte(sk))
	mac.Write([]byte(timestamp))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// xiaoYiJSON 把对象序列化成 JSON 字符串（msgDetail 是「字符串化的 JSON」）
func xiaoYiJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// xiaoYiIsIPURL 判断地址的主机是否是 IPv4 字面量（对应 channel.py: _is_ip_address）
func xiaoYiIsIPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() != nil && strings.Count(host, ".") == 3
}

// xiaoYiNativeSessionID 去掉 "xiaoyi:" 频道前缀，得到小艺原生 sessionId
// （channel.py: _native_session_id）
func xiaoYiNativeSessionID(sessionID string) string {
	return strings.TrimPrefix(strings.TrimSpace(sessionID), "xiaoyi:")
}

// xiaoYiMetaString 从 Meta 里取字符串（去空白）
func xiaoYiMetaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	s, _ := meta[key].(string)
	return strings.TrimSpace(s)
}

// xiaoYiNewID 生成一个 v4 UUID 字符串（对应 uuid.uuid4()）
func xiaoYiNewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// xiaoYiNewHex 生成 n 个十六进制字符（对应 uuid.uuid4().hex[:n]）
func xiaoYiNewHex(n int) string {
	buf := make([]byte, (n+1)/2)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", n)
	}
	return hex.EncodeToString(buf)[:n]
}

// xiaoYiChunkText 按「字符数」把文本切成不超过 TEXT_CHUNK_LIMIT 的片段，
// 尽量在换行处断开（channel.py: _chunk_text 的逐字符对齐版：Python 的 len 数的是码点，
// 所以这里用 []rune 数，中文才不会被按字节切坏）。
func xiaoYiChunkText(text string) []string {
	if len([]rune(text)) <= xiaoYiTextChunkLimit {
		return []string{text}
	}
	var chunks []string
	cur := ""
	for _, line := range strings.Split(text, "\n") {
		if len([]rune(line)) > xiaoYiTextChunkLimit {
			if cur != "" {
				chunks = append(chunks, strings.TrimRight(cur, "\n"))
				cur = ""
			}
			r := []rune(line)
			for i := 0; i < len(r); i += xiaoYiTextChunkLimit {
				end := i + xiaoYiTextChunkLimit
				if end > len(r) {
					end = len(r)
				}
				chunks = append(chunks, string(r[i:end]))
			}
			continue
		}
		test := line
		if cur != "" {
			test = cur + "\n" + line
		}
		if len([]rune(test)) > xiaoYiTextChunkLimit {
			if cur != "" {
				chunks = append(chunks, cur)
			}
			cur = line
		} else {
			cur = test
		}
	}
	if cur != "" {
		chunks = append(chunks, cur)
	}
	return chunks
}
