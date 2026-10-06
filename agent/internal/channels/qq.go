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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"baize/internal/config"
)

// 官方 QQ 机器人频道（QQ 开放平台 Bot，即 bot.q.qq.com 那套）。
//
// 为什么选 WebSocket 网关：白泽跑在 NAS 上，多半没有公网入口；网关由白泽**主动连出去**，
// 事件从这条长连接推下来，回复走 OpenAPI（HTTP）。协议照 QwenPaw 源码
// （src/qwenpaw/app/channels/qq/channel.py）与官方文档 bot.q.qq.com：
//
//	POST {tokenURL}  {"appId","clientSecret"}            → {access_token, expires_in}
//	GET  {apiBase}/gateway  Authorization: QQBot {token} → {"url": "wss://..."}
//	WS   dial {url}；服务端先推 OpCode 10 Hello{heartbeat_interval}
//	    客户端发 OpCode 2 Identify{token:"QQBot {token}", intents, shard:[0,1]}
//	    之后按周期发 OpCode 1 Heartbeat{d: 最新 seq}，服务端回 OpCode 11 Heartbeat ACK
//	    OpCode 0 Dispatch{t, d} 携带事件；OpCode 7 Reconnect / OpCode 9 InvalidSession 表示需重连
//
// 出站（官方 OpenAPI，统一域名 {apiBase}，鉴权头 Authorization: QQBot {token}）：
//
//	c2c  ：POST /v2/users/{user_openid}/messages   （需 msg_type=0 + msg_seq）
//	group：POST /v2/groups/{group_openid}/messages （需 msg_type=0 + msg_seq）
//	guild：POST /channels/{channel_id}/messages
//	dm   ：POST /dms/{guild_id}/messages
const (
	// QQ 官方 OpenAPI 正式环境地址（沙箱可把 Domain 覆盖为 https://sandbox.api.sgroup.qq.com）
	qqDefaultAPIBase = "https://api.sgroup.qq.com"
	// 取 access_token 的官方地址（与 OpenAPI 域名不同，故单独一条常量）
	qqDefaultTokenURL = "https://api.bot.qq.com/app/getAppAccessToken"
	qqTokenPath       = "/app/getAppAccessToken"

	// WebSocket OpCode（官方文档 / QwenPaw 一致）
	qqOpDispatch       = 0
	qqOpHeartbeat      = 1
	qqOpIdentify       = 2
	qqOpResume         = 6
	qqOpReconnect      = 7
	qqOpInvalidSession = 9
	qqOpHello          = 10
	qqOpHeartbeatACK   = 11

	// 事件订阅 intents（只订阅收消息与互动需要的位）
	qqIntentGuildMembers    = 1 << 1  // 频道成员（拿成员昵称）
	qqIntentDirectMessage   = 1 << 12 // 频道私信
	qqIntentGroupAndC2C     = 1 << 25 // 群聊 + 单聊（QQ 群 / 好友）
	qqIntentInteraction     = 1 << 26 // 互动（按钮回调）
	qqIntentPublicGuildMsgs = 1 << 30 // 频道公域消息（@ 机器人）

	// Hello 里给的默认心跳周期（官方示例 45000ms）
	qqHeartbeatDefaultMS = 45000

	// 去重窗口：网关在会话恢复后会重投未确认事件，按 msg_id 去重避免重复跑 Agent
	qqSeenLimit = 256

	qqRetryMin = 1 * time.Second
	qqRetryMax = 60 * time.Second
)

// QQ 官方 QQ 机器人频道：实现 Channel（出站回复）与 PollChannel（长连接收事件）。
type QQ struct {
	id       string
	prefix   string
	appID    string
	secret   string
	apiBase  string // OpenAPI 根地址（域名，默认正式；Domain 可覆盖为沙箱）
	tokenURL string // 取 access_token 的地址

	lg     *slog.Logger
	client *http.Client

	mu     sync.Mutex // 保护令牌缓存与 msg_seq 计数
	tok    string
	tokExp time.Time
	msgSeq int

	wmu  sync.Mutex // WebSocket 不允许并发写（读循环发 Identify、心跳协程发 Heartbeat）
	conn *websocket.Conn

	seen qqSeen
}

// NewQQ 造一个官方 QQ 机器人频道。AppID / AppSecret 用 QQ 开放平台的机器人凭证。
func NewQQ(cfg config.ChannelConfig, lg *slog.Logger) *QQ {
	if lg == nil {
		lg = slog.Default()
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.Domain), "/")
	tokenURL := qqDefaultTokenURL
	if base == "" {
		base = qqDefaultAPIBase
	} else {
		// 自定义 Domain（沙箱 / 自建代理 / 测试）时，令牌端点一并跟随，便于整体替换。
		// 生产留空则令牌走官方 api.bot.qq.com。
		tokenURL = base + qqTokenPath
	}
	return &QQ{
		id:       strings.TrimSpace(cfg.ID),
		prefix:   cfg.BotPrefix,
		appID:    strings.TrimSpace(cfg.AppID),
		secret:   strings.TrimSpace(cfg.AppSecret),
		apiBase:  base,
		tokenURL: tokenURL,
		lg:       lg,
		client:   &http.Client{Timeout: 20 * time.Second},
	}
}

// Poll 建立并维持一条 WebSocket 网关长连接：断线自动重连（指数退避），ctx 取消即停。
func (q *QQ) Poll(ctx context.Context, onEvent func(Message)) {
	if q.appID == "" || q.secret == "" {
		q.lg.Warn("QQ 频道没配 appId / appSecret，连不上", "channel", q.id)
		return
	}
	backoff := qqRetryMin
	for {
		if ctx.Err() != nil {
			return
		}
		err := q.runOnce(ctx, onEvent)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			q.lg.Warn("QQ 长连接断开，稍后重连", "channel", q.id, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < qqRetryMax {
			backoff *= 2
		}
	}
}

// runOnce 连一次：取令牌 → 换网关地址 → 连 WS → 收事件直到断开。
func (q *QQ) runOnce(ctx context.Context, onEvent func(Message)) error {
	tok, err := q.accessToken(ctx)
	if err != nil {
		return err
	}
	gw, err := q.gatewayURL(ctx, tok)
	if err != nil {
		return err
	}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, gw, nil)
	if err != nil {
		return fmt.Errorf("连 QQ 网关失败：%w", err)
	}
	q.setConn(conn)
	defer q.clearConn(conn)
	q.lg.Info("QQ 频道已连上网关", "channel", q.id)

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

	// 心跳协程：拿到 Hello 的周期后按周期发 OpCode 1
	var hbOnce sync.Once
	hbInterval := make(chan time.Duration, 1)
	hbStop := make(chan struct{})
	defer close(hbStop)
	var seq qqSeq
	go q.heartbeatLoop(ctx, conn, hbStop, hbInterval, &seq)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var p qqPayload
		if err := json.Unmarshal(data, &p); err != nil {
			continue // 不是 JSON：跳过
		}
		if p.S != nil {
			seq.set(*p.S)
		}
		switch p.Op {
		case qqOpHello:
			var hi struct {
				HeartbeatInterval int `json:"heartbeat_interval"`
			}
			_ = json.Unmarshal(p.D, &hi)
			hbOnce.Do(func() { hbInterval <- time.Duration(hi.HeartbeatInterval) * time.Millisecond })
			// 登录鉴权（每次连接都重新 Identify；不做跨连接的 Resume）
			if err := q.writeJSON(conn, map[string]any{
				"op": qqOpIdentify,
				"d": map[string]any{
					"token":   "QQBot " + tok,
					"intents": qqIntents(),
					"shard":   []int{0, 1},
				},
			}); err != nil {
				return err
			}
		case qqOpDispatch:
			q.handleDispatch(p, onEvent)
		case qqOpReconnect:
			return errors.New("QQ 网关要求重连")
		case qqOpInvalidSession:
			return errors.New("QQ 会话失效，重连")
		}
	}
}

// heartbeatLoop 按 Hello 给的周期发心跳，d 为最近收到的 seq（没有则 null）。
func (q *QQ) heartbeatLoop(ctx context.Context, conn *websocket.Conn, stop <-chan struct{}, interval <-chan time.Duration, seq *qqSeq) {
	var ticker *time.Ticker
	select {
	case d := <-interval:
		if d <= 0 {
			d = qqHeartbeatDefaultMS * time.Millisecond
		}
		ticker = time.NewTicker(d)
	case <-stop:
		return
	case <-ctx.Done():
		return
	}
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			var d any
			if v, ok := seq.get(); ok {
				d = v
			}
			if err := q.writeJSON(conn, map[string]any{"op": qqOpHeartbeat, "d": d}); err != nil {
				return
			}
		case <-stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

// handleDispatch 处理一条 Dispatch：READY/RESUMED 只记录会话，消息事件解成 Message 回调。
func (q *QQ) handleDispatch(p qqPayload, onEvent func(Message)) {
	switch p.T {
	case "READY", "RESUMED":
		return
	}
	if msg, ok := q.parseEvent(p.T, p.D); ok {
		onEvent(msg)
	}
}

// parseEvent 把一条 Dispatch 事件（eventType + d）解成统一的 Message。
// 只认四类消息事件（频道 @ / 频道私信 / 群 @ / 单聊），其余事件忽略。
func (q *QQ) parseEvent(eventType string, d json.RawMessage) (Message, bool) {
	spec, ok := qqMsgSpecs[eventType]
	if !ok {
		return Message{}, false
	}
	var ev struct {
		ID      string `json:"id"`
		Content string `json:"content"`
		Author  struct {
			UserOpenID   string `json:"user_openid"`
			MemberOpenID string `json:"member_openid"`
			ID           string `json:"id"`
			Username     string `json:"username"`
		} `json:"author"`
		GroupOpenID string `json:"group_openid"`
		ChannelID   string `json:"channel_id"`
		GuildID     string `json:"guild_id"`
	}
	if err := json.Unmarshal(d, &ev); err != nil {
		return Message{}, false
	}
	text := strings.TrimSpace(ev.Content)
	if text == "" {
		return Message{}, false // 先把文本做扎实；图片/文件/语音后续再补
	}
	// 机器人自己回的内容带着前缀，回环丢弃（与 QwenPaw 同口径）
	if q.prefix != "" && strings.HasPrefix(text, q.prefix) {
		return Message{}, false
	}
	sender := ""
	for _, key := range spec.senderKeys {
		if s := strings.TrimSpace(qqAuthor(ev.Author, key)); s != "" {
			sender = s
			break
		}
	}
	if sender == "" {
		return Message{}, false
	}
	if !q.markSeen(ev.ID) {
		return Message{}, false // 网关重投的重复事件，丢弃
	}

	extra := map[string]string{
		"channel_id":   strings.TrimSpace(ev.ChannelID),
		"guild_id":     strings.TrimSpace(ev.GuildID),
		"group_openid": strings.TrimSpace(ev.GroupOpenID),
	}
	meta := map[string]any{
		"message_type": spec.messageType,
		"message_id":   ev.ID,
		"sender_id":    sender,
		"user_name":    strings.TrimSpace(ev.Author.Username),
		"is_group":     spec.messageType == "group" || spec.messageType == "guild",
	}
	for _, key := range spec.extraMetaKeys {
		meta[key] = extra[key]
	}
	return Message{
		Channel: q.id,
		Session: qqSession(spec.messageType, sender, extra["group_openid"], extra["channel_id"], extra["guild_id"]),
		Sender:  firstNonEmpty(ev.Author.Username, sender),
		Text:    text,
		Meta:    meta,
	}, true
}

// Send 把回复发到消息来源（按 message_type 选对应的 OpenAPI 端点）。
func (q *QQ) Send(ctx context.Context, msg Message, reply string) error {
	if q.appID == "" || q.secret == "" {
		return errors.New("QQ 频道没配 appId / appSecret，发不出去")
	}
	mt, _ := msg.Meta["message_type"].(string)
	senderID, _ := msg.Meta["sender_id"].(string)
	groupOpenID, _ := msg.Meta["group_openid"].(string)
	channelID, _ := msg.Meta["channel_id"].(string)
	guildID, _ := msg.Meta["guild_id"].(string)
	path, useSeq := qqSendPath(mt, senderID, groupOpenID, channelID, guildID)
	if path == "" {
		return errors.New("这条 QQ 消息没有可回发的地点（缺 message_type / 目标 id）")
	}
	tok, err := q.accessToken(ctx)
	if err != nil {
		return err
	}
	// QQ 明文消息不允许含链接，官方口径是剥掉（QwenPaw 同做法）
	body := map[string]any{"content": qqStripURLs(q.prefix + reply)}
	if useSeq {
		body["msg_type"] = 0 // 0 = 纯文本
		body["msg_seq"] = q.nextMsgSeq()
	}
	if id, _ := msg.Meta["message_id"].(string); strings.TrimSpace(id) != "" {
		body["msg_id"] = strings.TrimSpace(id)
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.apiBase+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "QQBot "+tok)
	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("调 QQ 发消息失败：%w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var out struct {
		ErrCode int    `json:"err_code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode >= 300 || out.ErrCode != 0 {
		return fmt.Errorf("QQ 回发失败（http %d，err_code %d）：%s", resp.StatusCode, out.ErrCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// qqSendPath 按对话类型给出 OpenAPI 路径，以及是否要带 msg_type + msg_seq
// （c2c / 群聊要带，频道 / 频道私信不带）。
func qqSendPath(messageType, senderID, groupOpenID, channelID, guildID string) (string, bool) {
	switch messageType {
	case "c2c":
		if strings.TrimSpace(senderID) == "" {
			return "", false
		}
		return "/v2/users/" + senderID + "/messages", true
	case "group":
		if strings.TrimSpace(groupOpenID) == "" {
			return "", false
		}
		return "/v2/groups/" + groupOpenID + "/messages", true
	case "guild":
		if strings.TrimSpace(channelID) == "" {
			return "", false
		}
		return "/channels/" + channelID + "/messages", false
	case "dm":
		if strings.TrimSpace(guildID) == "" {
			return "", false
		}
		return "/dms/" + guildID + "/messages", false
	}
	return "", false
}

// accessToken 取（并缓存）app access_token。官方 expires_in 可能是字符串也可能是数字。
func (q *QQ) accessToken(ctx context.Context) (string, error) {
	if q.appID == "" || q.secret == "" {
		return "", errors.New("QQ 频道没配 appId / appSecret，取不到 access_token")
	}
	q.mu.Lock()
	if q.tok != "" && time.Now().Before(q.tokExp) {
		t := q.tok
		q.mu.Unlock()
		return t, nil
	}
	q.mu.Unlock()

	payload, _ := json.Marshal(map[string]string{"appId": q.appID, "clientSecret": q.secret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.tokenURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := q.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("取 QQ access_token 失败：%w", err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string    `json:"access_token"`
		ExpiresIn   qqFlexInt `json:"expires_in"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析 QQ 令牌响应失败：%w", err)
	}
	if resp.StatusCode >= 300 || out.AccessToken == "" {
		return "", fmt.Errorf("QQ 没给出令牌（http %d）：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 7200 * time.Second
	}
	q.mu.Lock()
	q.tok = out.AccessToken
	q.tokExp = time.Now().Add(ttl - 5*time.Minute) // 留 5 分钟余量
	q.mu.Unlock()
	return out.AccessToken, nil
}

// gatewayURL 换一个 WebSocket 网关地址
func (q *QQ) gatewayURL(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q.apiBase+"/gateway", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := q.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("取 QQ 网关地址失败：%w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("QQ 网关返回 http %d：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析 QQ 网关响应失败：%w", err)
	}
	if strings.TrimSpace(out.URL) == "" {
		return "", errors.New("QQ 没给出网关地址（url 为空）")
	}
	return out.URL, nil
}

// nextMsgSeq 递增并返回 msg_seq（c2c / 群聊被动回复要求同一条消息的 msg_seq 递增）。
func (q *QQ) nextMsgSeq() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.msgSeq++
	return q.msgSeq
}

// markSeen 记下已处理的事件 id；重复返回 false（网关恢复会话后会重投未确认事件）。
func (q *QQ) markSeen(id string) bool {
	return q.seen.add(id)
}

func (q *QQ) writeJSON(conn *websocket.Conn, v any) error {
	q.wmu.Lock()
	defer q.wmu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteJSON(v)
}

func (q *QQ) setConn(c *websocket.Conn) {
	q.mu.Lock()
	q.conn = c
	q.mu.Unlock()
}

func (q *QQ) clearConn(c *websocket.Conn) {
	q.mu.Lock()
	if q.conn == c {
		q.conn = nil
	}
	q.mu.Unlock()
	_ = c.Close()
}

// qqIntents 本次连接订阅的事件位（收消息 + 互动 + 频道成员昵称）
func qqIntents() int {
	return qqIntentPublicGuildMsgs | qqIntentGuildMembers | qqIntentInteraction |
		qqIntentDirectMessage | qqIntentGroupAndC2C
}

// qqSession 会话键：群按群、频道按子频道、频道私信按频道、单聊按人
// （与 QwenPaw resolve_session_id 同口径）
func qqSession(messageType, senderID, groupOpenID, channelID, guildID string) string {
	switch messageType {
	case "group":
		return "qq:group:" + firstNonEmpty(groupOpenID, "unknown")
	case "guild":
		return "qq:guild:" + firstNonEmpty(channelID, "unknown")
	case "dm":
		return "qq:dm:" + firstNonEmpty(guildID, "unknown")
	default:
		return "qq:c2c:" + firstNonEmpty(senderID, "unknown")
	}
}

// qqAuthor 从 author 里按 key 取值（不同事件类型的发送者字段名不一样）
func qqAuthor(a struct {
	UserOpenID   string `json:"user_openid"`
	MemberOpenID string `json:"member_openid"`
	ID           string `json:"id"`
	Username     string `json:"username"`
}, key string) string {
	switch key {
	case "user_openid":
		return a.UserOpenID
	case "member_openid":
		return a.MemberOpenID
	case "id":
		return a.ID
	case "username":
		return a.Username
	}
	return ""
}

// qqURLRe QQ 明文消息里不允许出现链接（官方会拒），发之前剥掉
var qqURLRe = regexp.MustCompile(`(?i)https?://[^\s]+|www\.[^\s]+`)

func qqStripURLs(text string) string {
	return qqURLRe.ReplaceAllString(text, "[链接已省略]")
}

// qqPayload 一条 WebSocket 帧
type qqPayload struct {
	Op int             `json:"op"`
	S  *int            `json:"s"`
	T  string          `json:"t"`
	D  json.RawMessage `json:"d"`
}

// qqSeq 最近收到的 seq（供心跳回填 d），无则 ok=false
type qqSeq struct {
	mu sync.Mutex
	v  int
	ok bool
}

func (s *qqSeq) set(v int) {
	s.mu.Lock()
	s.v = v
	s.ok = true
	s.mu.Unlock()
}

func (s *qqSeq) get() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.v, s.ok
}

// qqSeen 有界的已处理事件 id 集合（去重网关重投）
type qqSeen struct {
	mu    sync.Mutex
	order []string
	set   map[string]bool
}

func (s *qqSeen) add(id string) bool {
	if id == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.set == nil {
		s.set = map[string]bool{}
	}
	if s.set[id] {
		return false
	}
	s.set[id] = true
	s.order = append(s.order, id)
	if len(s.order) > qqSeenLimit {
		old := s.order[0]
		s.order = s.order[1:]
		delete(s.set, old)
	}
	return true
}

// qqFlexInt 容忍 expires_in 是数字或字符串（官方返回示例是字符串 "7200"）
type qqFlexInt int

func (n *qqFlexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*n = qqFlexInt(v)
	return nil
}

// qqMsgSpec 一类消息事件的差异：对话类型、发送者字段、额外 meta 字段
type qqMsgSpec struct {
	messageType   string
	senderKeys    []string
	extraMetaKeys []string
}

// qqMsgSpecs 四类消息事件（与 QwenPaw _MESSAGE_EVENT_SPECS 一致）
var qqMsgSpecs = map[string]qqMsgSpec{
	"C2C_MESSAGE_CREATE": {
		messageType: "c2c",
		senderKeys:  []string{"user_openid", "id"},
	},
	"AT_MESSAGE_CREATE": {
		messageType:   "guild",
		senderKeys:    []string{"id", "username"},
		extraMetaKeys: []string{"channel_id", "guild_id"},
	},
	"DIRECT_MESSAGE_CREATE": {
		messageType:   "dm",
		senderKeys:    []string{"id", "username"},
		extraMetaKeys: []string{"channel_id", "guild_id"},
	},
	"GROUP_AT_MESSAGE_CREATE": {
		messageType:   "group",
		senderKeys:    []string{"member_openid", "id"},
		extraMetaKeys: []string{"group_openid"},
	},
}
