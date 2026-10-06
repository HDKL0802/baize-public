package channels

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"baize/internal/config"
)

// 腾讯元宝（Yuanbao）频道。
//
// 协议照 QwenPaw 的 yuanbao 频道（src/qwenpaw/app/channels/yuanbao/，含 auth.py / codec.py /
// constants.py / proto/{conn,biz}.json）。元宝机器人不是「事件回调 + JSON」，而是
// **protobuf 二进制帧 over WebSocket**：
//
//  1. 先用 app_id / app_secret 调 sign-token 换 token（HMAC-SHA256 签名）；
//  2. WS 连 DEFAULT_WS_URL，发一条 AuthBind 帧（模块 conn_access，cmd=auth-bind）；
//  3. 心跳 Ping（模块 conn_access，cmd=ping），服务端 PingRsp 可回 heartInterval 覆盖间隔；
//  4. 收发消息走业务模块 yuanbao_openclaw_proxy：
//     - 入站：服务端推 cmdType=PUSH 的帧，其 data 字段是 **UTF-8 JSON 字符串**
//     （channel.py:_handle_push 直接 json.loads(data)，不是 protobuf 编码的 InboundMessagePush）；
//     - 出站：builder 编 SendC2CMessageReq / SendGroupMessageReq 再塞进 ConnMsg.data。
//
// conn.json / biz.json 是 protobufjs 的 JSON descriptor（不是 .proto）。这里按源码
// **手写最小编解码**，只覆盖收发消息真正用到的那几个字段（无 protoc 可用）。
const (
	// constants.py：DEFAULT_WS_URL / DEFAULT_API_DOMAIN / SIGN_TOKEN_PATH
	yuanbaoDefaultWSURL     = "wss://bot-wss.yuanbao.tencent.com/wss/connection"
	yuanbaoDefaultAPIDomain = "bot.yuanbao.tencent.com"
	yuanbaoSignTokenPath    = "/api/v5/robotLogic/sign-token"

	// codec.py：CMD_TYPE_* （帧头 cmdType）
	yuanbaoCmdTypeRequest  = 0
	yuanbaoCmdTypeResponse = 1
	yuanbaoCmdTypePush     = 2
	yuanbaoCmdTypePushAck  = 3

	// codec.py：内置命令与模块
	yuanbaoCmdAuthBind      = "auth-bind"
	yuanbaoCmdPing          = "ping"
	yuanbaoCmdKickout       = "kickout"
	yuanbaoModuleConnAccess = "conn_access"

	// codec.py：业务命令与模块
	yuanbaoModuleBiz    = "yuanbao_openclaw_proxy"
	yuanbaoCmdSendC2C   = "send_c2c_message"
	yuanbaoCmdSendGroup = "send_group_message"

	// codec.py：AuthBind 固定值（biz_id / deviceInfo.instanceId）
	yuanbaoBizID            = "ybBot"
	yuanbaoDeviceInstanceID = "16"

	// constants.py：鉴权 / 重试常量
	yuanbaoRetryableSignCode  = 10099
	yuanbaoSignMaxRetries     = 3
	yuanbaoAuthAlreadyCode    = 41101 // RetCode.ALREADY_AUTH：已鉴权也算成功
	yuanbaoDefaultHeartbeat   = 5 * time.Second
	yuanbaoConnTimeout        = 15 * time.Second
	yuanbaoSendTimeout        = 30 * time.Second
	yuanbaoTEXTChunkLimit     = 3000 // channel.py 里 send 调 split_text(text)，默认 max_len=3000
	yuanbaoSessionSuffixLen   = 8    // c2c 会话键取账号后 8 位（SESSION_ID_SUFFIX_LEN）
	yuanbaoSenderSuffixLen    = 4    // 发送者展示名取账号后 4 位
	yuanbaoSessionMapMaxLimit = 4096

	yuanbaoSignRetryDelay = time.Second
)

// 重连节奏（constants.py：RECONNECT_DELAYS）
var yuanbaoReconnectDelays = []time.Duration{
	1 * time.Second, 2 * time.Second, 5 * time.Second,
	10 * time.Second, 30 * time.Second, 60 * time.Second,
}

// Yuanbao 腾讯元宝频道：实现 Channel（出站回复）与 PollChannel（长连接收消息）。
type Yuanbao struct {
	id     string
	prefix string

	appID     string // ChannelConfig.AppID（= app_id）
	secret    string // ChannelConfig.AppSecret（= app_secret）
	apiDomain string // ChannelConfig.Domain（= api_domain，留空用默认）
	wsURL     string // ChannelConfig.OutboundURL（= ws_url，留空用默认）

	lg     *slog.Logger
	client *http.Client

	// 写连接与出站写串行化（gorilla/websocket 不允许并发写）
	mu   sync.Mutex
	conn *websocket.Conn

	// sign-token 缓存
	tokMu sync.Mutex
	tok   yuanbaoToken

	// 机器人身份（鉴权后才有）
	botMu sync.Mutex
	botID string

	// 心跳间隔（纳秒；PingRsp 可覆盖）
	hbInterval atomic.Int64

	// 序列号
	seq atomic.Int64

	// 会话路由（session -> 原始账号 / 群号）
	sessMu   sync.Mutex
	sessions map[string]yuanbaoTarget
}

// yuanbaoToken sign-token 结果
type yuanbaoToken struct {
	botID     string
	token     string
	source    string
	expiresAt time.Time
}

// yuanbaoTarget 一条会话的回发目标
type yuanbaoTarget struct {
	isGroup   bool
	groupCode string
	senderID  string // 原始账号 id（c2c 用）
}

func (t yuanbaoTarget) targetID() string {
	if t.isGroup {
		return t.groupCode
	}
	return t.senderID
}

// NewYuanbao 造一个腾讯元宝频道。凭据取 ChannelConfig：
//
//	AppID       = app_id
//	AppSecret   = app_secret
//	Domain      = api_domain（sign-token / REST 用；留空用官方默认）
//	OutboundURL = ws_url（WebSocket 地址；留空用官方默认）
//
// 其余：ID = 频道 id，BotPrefix = 回复前缀。
func NewYuanbao(cfg config.ChannelConfig, lg *slog.Logger) *Yuanbao {
	if lg == nil {
		lg = slog.Default()
	}
	ws := strings.TrimSpace(cfg.OutboundURL)
	if ws == "" {
		ws = yuanbaoDefaultWSURL
	}
	y := &Yuanbao{
		id:        strings.TrimSpace(cfg.ID),
		prefix:    cfg.BotPrefix,
		appID:     strings.TrimSpace(cfg.AppID),
		secret:    strings.TrimSpace(cfg.AppSecret),
		apiDomain: strings.TrimSpace(cfg.Domain),
		wsURL:     ws,
		lg:        lg,
		client:    &http.Client{Timeout: 20 * time.Second},
		sessions:  map[string]yuanbaoTarget{},
	}
	y.hbInterval.Store(int64(yuanbaoDefaultHeartbeat))
	return y
}

// Poll 维持一条元宝 WebSocket 长连接：换 token → 连 WS → AuthBind → 心跳 → 收到断开为止。
// 断线按固定节奏重连，ctx 取消即停。未配 appId / appSecret 时明确报错（不伪装成已连接）。
func (y *Yuanbao) Poll(ctx context.Context, onEvent func(Message)) {
	if y.appID == "" || y.secret == "" {
		y.lg.Error("腾讯元宝频道没配 appId / appSecret，连不上", "channel", y.id)
		return
	}
	delayIdx := 0
	authed := false
	for {
		if ctx.Err() != nil {
			return
		}
		connected, err := y.runOnce(ctx, onEvent)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			y.lg.Warn("腾讯元宝连接断开，稍后重连", "channel", y.id, "err", err)
		}
		if connected {
			authed = true
			delayIdx = 0 // 连上过就重置退避
		}
		delay := yuanbaoReconnectDelays[min(delayIdx, len(yuanbaoReconnectDelays)-1)]
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if !authed && delayIdx < len(yuanbaoReconnectDelays)-1 {
			delayIdx++
		}
	}
}

// runOnce 连一次：取 token → 拨 WS → 发 AuthBind → 等应答 → 起心跳 + 读循环，读到断开为止。
// 返回是否成功鉴权过。
func (y *Yuanbao) runOnce(ctx context.Context, onEvent func(Message)) (bool, error) {
	tok, err := y.signToken(ctx)
	if err != nil {
		return false, err
	}

	dialer := websocket.Dialer{HandshakeTimeout: yuanbaoConnTimeout}
	conn, _, err := dialer.DialContext(ctx, y.wsURL, nil)
	if err != nil {
		return false, fmt.Errorf("连腾讯元宝 WebSocket 失败：%w", err)
	}
	y.setConn(conn)
	defer y.clearConn(conn)

	// 发 AuthBind（模块 conn_access，cmd=auth-bind）
	authHead := yuanbaoHead{
		cmdType: yuanbaoCmdTypeRequest,
		cmd:     yuanbaoCmdAuthBind,
		seqNo:   y.nextSeq(),
		msgID:   yuanbaoNewID(),
		module:  yuanbaoModuleConnAccess,
	}
	if err := y.writeFrame(conn, encodeConnMsg(authHead,
		encodeAuthBindReq(yuanbaoBizID, tok.botID, tok.source, tok.token))); err != nil {
		return false, fmt.Errorf("发腾讯元宝 AuthBind 失败：%w", err)
	}
	if err := y.waitAuthRsp(conn); err != nil {
		return false, err
	}
	y.setBotID(tok.botID)
	y.lg.Info("腾讯元宝频道已连上", "channel", y.id, "bot", tok.botID)

	stop := make(chan struct{})
	defer close(stop)
	go y.heartbeatLoop(ctx, conn, stop)
	// ctx 取消时主动关连接，让 ReadMessage 立刻返回
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return true, err
		}
		if mt != websocket.BinaryMessage {
			continue // 元宝只走二进制帧
		}
		y.handleFrame(conn, data, onEvent)
	}
}

// waitAuthRsp 读一条帧并校验是 AuthBind 应答。
// 与 channel.py 一致：应答 data 可能为空（成功信息在 head.status 里），code=41101（已鉴权）也算成功。
func (y *Yuanbao) waitAuthRsp(conn *websocket.Conn) error {
	_ = conn.SetReadDeadline(time.Now().Add(yuanbaoSendTimeout))
	_, data, err := conn.ReadMessage()
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("等腾讯元宝 AuthBind 应答失败：%w", err)
	}
	cm, ok := decodeConnMsg(data)
	if !ok {
		return errors.New("腾讯元宝 AuthBind 应答解不出来")
	}
	if cm.head.cmd != yuanbaoCmdAuthBind {
		return fmt.Errorf("腾讯元宝 AuthBind 应答的 cmd 不对：%q", cm.head.cmd)
	}
	code := int32(cm.head.status)
	if c, present := decodeAuthRspCode(cm.data); present {
		code = c
	}
	if code == 0 || code == yuanbaoAuthAlreadyCode {
		return nil
	}
	return fmt.Errorf("腾讯元宝 AuthBind 失败（code %d）", code)
}

// handleFrame 处理一条服务端帧：应答（心跳/鉴权）与推送（入站消息 / 踢下线）。
func (y *Yuanbao) handleFrame(conn *websocket.Conn, raw []byte, onEvent func(Message)) {
	cm, ok := decodeConnMsg(raw)
	if !ok {
		y.lg.Warn("腾讯元宝收到解不出的帧", "channel", y.id, "bytes", len(raw))
		return
	}
	switch cm.head.cmdType {
	case yuanbaoCmdTypeResponse:
		switch cm.head.cmd {
		case yuanbaoCmdPing:
			if iv := decodePingHeartInterval(cm.data); iv > 0 {
				y.hbInterval.Store(int64(iv) * int64(time.Second))
			}
		case yuanbaoCmdAuthBind:
			// 鉴权失败会带 AUTH_FAILED_CODES；此处仅记日志，重连由读循环兜底
			if cm.head.status != 0 {
				y.lg.Warn("腾讯元宝鉴权应答异常", "channel", y.id, "status", cm.head.status)
			}
		}
	case yuanbaoCmdTypePush:
		if cm.head.needAck {
			ack := encodeConnMsg(yuanbaoHead{
				cmdType: yuanbaoCmdTypePushAck,
				cmd:     cm.head.cmd,
				seqNo:   y.nextSeq(),
				msgID:   cm.head.msgID,
				module:  cm.head.module,
			}, nil)
			if err := y.writeFrame(conn, ack); err != nil {
				y.lg.Warn("腾讯元宝回 ACK 失败", "channel", y.id, "err", err)
			}
		}
		if cm.head.cmd == yuanbaoCmdKickout {
			y.lg.Warn("腾讯元宝被踢下线", "channel", y.id)
			_ = conn.Close()
			return
		}
		if len(cm.data) == 0 {
			return
		}
		if msg, ok := y.parseInbound(cm.data); ok {
			onEvent(msg)
		}
	}
}

// heartbeatLoop 每 hbInterval 发一条 Ping（模块 conn_access，cmd=ping）。
// PingRsp 里的 heartInterval 会在 handleFrame 里更新间隔。
func (y *Yuanbao) heartbeatLoop(ctx context.Context, conn *websocket.Conn, stop <-chan struct{}) {
	for {
		interval := time.Duration(y.hbInterval.Load())
		if interval <= 0 {
			interval = yuanbaoDefaultHeartbeat
		}
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-time.After(interval):
		}
		ping := encodeConnMsg(yuanbaoHead{
			cmdType: yuanbaoCmdTypeRequest,
			cmd:     yuanbaoCmdPing,
			seqNo:   y.nextSeq(),
			msgID:   yuanbaoNewID(),
			module:  yuanbaoModuleConnAccess,
		}, nil)
		if err := y.writeFrame(conn, ping); err != nil {
			y.lg.Warn("腾讯元宝心跳发送失败", "channel", y.id, "err", err)
			_ = conn.Close() // 断开连接让读循环重连
			return
		}
	}
}

// parseInbound 把推送帧里的 JSON（channel.py:_handle_push 的 json.loads(data)）解成一条入站 Message。
//
// 关键字段来自 channel.py:_handle_chat_message：
//   - callback_command 以 "Group." 开头 = 群聊，否则 = 单聊（c2c）；
//   - from_account 以 "bot_" 开头 = 机器人消息，跳过（默认 accept_bot_messages=False）；
//   - 文本取 msg_body 里 msg_type=="TIMTextElem" 的 msg_content.text。
func (y *Yuanbao) parseInbound(data []byte) (Message, bool) {
	var in struct {
		CallbackCommand string `json:"callback_command"`
		FromAccount     string `json:"from_account"`
		SenderNickname  string `json:"sender_nickname"`
		GroupCode       string `json:"group_code"`
		MsgID           string `json:"msg_id"`
		MsgKey          string `json:"msg_key"`
		MsgBody         []struct {
			MsgType    string `json:"msg_type"`
			MsgContent struct {
				Text string `json:"text"`
			} `json:"msg_content"`
		} `json:"msg_body"`
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return Message{}, false
	}
	if strings.TrimSpace(in.CallbackCommand) == "" {
		return Message{}, false
	}
	// 跳过机器人自己发的消息，避免回环
	if strings.HasPrefix(in.FromAccount, "bot_") {
		return Message{}, false
	}

	isGroup := strings.HasPrefix(in.CallbackCommand, "Group.")
	groupCode := ""
	if isGroup {
		groupCode = strings.TrimSpace(in.GroupCode)
	}

	botID := y.currentBotID()
	var texts []string
	for _, elem := range in.MsgBody {
		if elem.MsgType != "TIMTextElem" {
			continue // 图片 / 文件 / @提及后续再补
		}
		text := strings.TrimSpace(elem.MsgContent.Text)
		if botID != "" {
			text = strings.TrimSpace(strings.ReplaceAll(text, "@"+botID, ""))
		}
		if text != "" {
			texts = append(texts, text)
		}
	}
	text := strings.Join(texts, "\n")
	if text == "" {
		return Message{}, false
	}

	session := "yuanbao:group:" + groupCode
	if !isGroup {
		session = "yuanbao:c2c:" + yuanbaoShortID(in.FromAccount, yuanbaoSessionSuffixLen)
	}
	y.rememberSession(session, yuanbaoTarget{
		isGroup:   isGroup,
		groupCode: groupCode,
		senderID:  in.FromAccount,
	})

	return Message{
		Channel: y.id,
		Session: session,
		Sender:  yuanbaoSenderDisplay(in.SenderNickname, in.FromAccount),
		Text:    text,
		Meta: map[string]any{
			"is_group":   isGroup,
			"group_code": groupCode,
			"sender_id":  in.FromAccount, // 原始账号 id（c2c 回发目标）
			"message_id": firstNonEmpty(in.MsgID, in.MsgKey),
		},
	}, true
}

// Send 把回复发回消息来源：群聊发 send_group_message，单聊发 send_c2c_message。
// 目标优先取 Message.Meta（is_group / group_code / sender_id），否则查会话路由表。
// 空正文不发（与 channel.py:send 一致）。
func (y *Yuanbao) Send(ctx context.Context, msg Message, reply string) error {
	if y.appID == "" || y.secret == "" {
		return errors.New("腾讯元宝频道没配 appId / appSecret，发不出去")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(reply) == "" {
		return nil
	}
	isGroup, target, err := y.resolveTarget(msg)
	if err != nil {
		return err
	}
	conn := y.currentConn()
	if conn == nil {
		return errors.New("腾讯元宝频道当前没有可用连接（WebSocket 未连上）")
	}
	botID := y.currentBotID()
	for _, chunk := range yuanbaoChunkText(y.prefix + reply) {
		var cmd string
		var data []byte
		if isGroup {
			cmd = yuanbaoCmdSendGroup
			data = encodeSendGroupReq(target, botID, strconv.FormatUint(uint64(yuanbaoRandomUint32()), 10), chunk)
		} else {
			cmd = yuanbaoCmdSendC2C
			data = encodeSendC2CReq(target, botID, yuanbaoRandomUint32(), chunk)
		}
		frame := encodeConnMsg(yuanbaoHead{
			cmdType: yuanbaoCmdTypeRequest,
			cmd:     cmd,
			seqNo:   y.nextSeq(),
			msgID:   yuanbaoNewID(),
			module:  yuanbaoModuleBiz,
		}, data)
		if err := y.writeFrame(conn, frame); err != nil {
			return fmt.Errorf("发腾讯元宝消息失败：%w", err)
		}
	}
	return nil
}

// resolveTarget 解析回发目标：优先 Message.Meta，其次会话路由表。
func (y *Yuanbao) resolveTarget(msg Message) (isGroup bool, target string, err error) {
	if v, ok := msg.Meta["is_group"].(bool); ok {
		isGroup = v
	}
	if isGroup {
		if gc, _ := msg.Meta["group_code"].(string); strings.TrimSpace(gc) != "" {
			return true, gc, nil
		}
	} else if sid, _ := msg.Meta["sender_id"].(string); strings.TrimSpace(sid) != "" {
		return false, sid, nil
	}
	if t, ok := y.sessionTarget(msg.Session); ok && t.targetID() != "" {
		return t.isGroup, t.targetID(), nil
	}
	if sid, _ := msg.Meta["sender_id"].(string); strings.TrimSpace(sid) != "" {
		return isGroup, sid, nil
	}
	if gc, _ := msg.Meta["group_code"].(string); strings.TrimSpace(gc) != "" {
		return true, gc, nil
	}
	return false, "", errors.New("这条腾讯元宝消息没有可回发的会话（缺 target / session）")
}

// signToken 换（并缓存）sign-token。签名算法照 auth.py：
//
//	plain = nonce + timestamp + app_id + app_secret，signature = HMAC-SHA256(app_secret, plain).hex()
//	POST {api_domain}/api/v5/robotLogic/sign-token  body={app_key,nonce,signature,timestamp}
func (y *Yuanbao) signToken(ctx context.Context) (yuanbaoToken, error) {
	y.tokMu.Lock()
	if y.tok.token != "" && time.Now().Before(y.tok.expiresAt) {
		t := y.tok
		y.tokMu.Unlock()
		return t, nil
	}
	y.tokMu.Unlock()

	for attempt := 0; attempt <= yuanbaoSignMaxRetries; attempt++ {
		nonce := yuanbaoNonce()
		timestamp := yuanbaoBeijingTimestamp()
		signature := yuanbaoSignature(nonce, timestamp, y.appID, y.secret)
		body, _ := json.Marshal(map[string]string{
			"app_key":   y.appID,
			"nonce":     nonce,
			"signature": signature,
			"timestamp": timestamp,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, y.signTokenURL(), bytes.NewReader(body))
		if err != nil {
			return yuanbaoToken{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := y.client.Do(req)
		if err != nil {
			if attempt < yuanbaoSignMaxRetries {
				if !yuanbaoSleep(ctx, yuanbaoSignRetryDelay) {
					return yuanbaoToken{}, ctx.Err()
				}
				continue
			}
			return yuanbaoToken{}, fmt.Errorf("取腾讯元宝 sign-token 失败：%w", err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return yuanbaoToken{}, fmt.Errorf("腾讯元宝 sign-token 返回 http %d：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
		}
		var out struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				BotID    string `json:"bot_id"`
				Token    string `json:"token"`
				Source   string `json:"source"`
				Duration int    `json:"duration"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return yuanbaoToken{}, fmt.Errorf("解析腾讯元宝 sign-token 响应失败：%w", err)
		}
		if out.Code == 0 {
			if out.Data.Token == "" {
				return yuanbaoToken{}, errors.New("腾讯元宝 sign-token 没给出 token")
			}
			tok := yuanbaoToken{
				botID:     out.Data.BotID,
				token:     out.Data.Token,
				source:    out.Data.Source,
				expiresAt: time.Now().Add(time.Duration(out.Data.Duration) * time.Second),
			}
			y.tokMu.Lock()
			y.tok = tok
			y.tokMu.Unlock()
			return tok, nil
		}
		if out.Code == yuanbaoRetryableSignCode && attempt < yuanbaoSignMaxRetries {
			if !yuanbaoSleep(ctx, yuanbaoSignRetryDelay) {
				return yuanbaoToken{}, ctx.Err()
			}
			continue
		}
		return yuanbaoToken{}, fmt.Errorf("腾讯元宝 sign-token 出错（code %d）：%s", out.Code, out.Msg)
	}
	return yuanbaoToken{}, errors.New("腾讯元宝 sign-token 重试次数用尽")
}

// signTokenURL 拼 sign-token 地址：api_domain 带 scheme 就沿用（自建/测试可 http），否则按 https 补。
func (y *Yuanbao) signTokenURL() string {
	domain := strings.TrimSpace(y.apiDomain)
	if domain == "" {
		domain = yuanbaoDefaultAPIDomain
	}
	domain = strings.TrimRight(domain, "/")
	if strings.HasPrefix(domain, "http://") || strings.HasPrefix(domain, "https://") {
		return domain + yuanbaoSignTokenPath
	}
	return "https://" + domain + yuanbaoSignTokenPath
}

/* ---------- 连接 / 会话状态 ---------- */

func (y *Yuanbao) writeFrame(conn *websocket.Conn, b []byte) error {
	if conn == nil {
		return errors.New("腾讯元宝连接为空")
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(yuanbaoSendTimeout))
	return conn.WriteMessage(websocket.BinaryMessage, b)
}

func (y *Yuanbao) currentConn() *websocket.Conn {
	y.mu.Lock()
	defer y.mu.Unlock()
	return y.conn
}

func (y *Yuanbao) setConn(c *websocket.Conn) {
	y.mu.Lock()
	y.conn = c
	y.mu.Unlock()
}

func (y *Yuanbao) clearConn(c *websocket.Conn) {
	y.mu.Lock()
	if y.conn == c {
		y.conn = nil
	}
	y.mu.Unlock()
	_ = c.Close()
}

func (y *Yuanbao) setBotID(id string) {
	y.botMu.Lock()
	y.botID = id
	y.botMu.Unlock()
}

func (y *Yuanbao) currentBotID() string {
	y.botMu.Lock()
	defer y.botMu.Unlock()
	return y.botID
}

func (y *Yuanbao) nextSeq() uint32 {
	v := y.seq.Add(1)
	return uint32(v % (1 << 31))
}

func (y *Yuanbao) sessionTarget(session string) (yuanbaoTarget, bool) {
	session = strings.TrimSpace(session)
	if session == "" {
		return yuanbaoTarget{}, false
	}
	y.sessMu.Lock()
	defer y.sessMu.Unlock()
	t, ok := y.sessions[session]
	return t, ok
}

func (y *Yuanbao) rememberSession(session string, t yuanbaoTarget) {
	if strings.TrimSpace(session) == "" {
		return
	}
	y.sessMu.Lock()
	defer y.sessMu.Unlock()
	if len(y.sessions) > yuanbaoSessionMapMaxLimit {
		y.sessions = map[string]yuanbaoTarget{}
	}
	y.sessions[session] = t
}

/* ---------- 小工具（对齐 constants.py / channel.py 的语义） ---------- */

// yuanbaoShortID 取原始账号 id 的后 n 位（channel.py:_short_id）
func yuanbaoShortID(rawID string, n int) string {
	r := []rune(rawID)
	if len(r) <= n {
		return rawID
	}
	return string(r[len(r)-n:])
}

// yuanbaoSenderDisplay 生成展示名：昵称#账号后 4 位（channel.py:_sender_display）
func yuanbaoSenderDisplay(nickname, rawID string) string {
	nick := strings.TrimSpace(nickname)
	if nick == "" {
		nick = "unknown"
	}
	suffix := yuanbaoShortID(rawID, yuanbaoSenderSuffixLen)
	if suffix == "" {
		suffix = "????"
	}
	return nick + "#" + suffix
}

// yuanbaoChunkText 按字符数切分文本，尽量在换行处断开（channel.py 调 split_text 的简化版：
// 仅保留「按行聚合 + 超长行硬切」；QwenPaw 的 markdown 代码围栏 / 表格保持逻辑这里不重现）。
func yuanbaoChunkText(text string) []string {
	if len([]rune(text)) <= yuanbaoTEXTChunkLimit {
		return []string{text}
	}
	var chunks []string
	cur := ""
	for _, line := range strings.Split(text, "\n") {
		if len([]rune(line)) > yuanbaoTEXTChunkLimit {
			if cur != "" {
				chunks = append(chunks, strings.TrimRight(cur, "\n"))
				cur = ""
			}
			r := []rune(line)
			for i := 0; i < len(r); i += yuanbaoTEXTChunkLimit {
				end := i + yuanbaoTEXTChunkLimit
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
		if len([]rune(test)) > yuanbaoTEXTChunkLimit {
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

func yuanbaoNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func yuanbaoNewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func yuanbaoRandomUint32() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint32(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint32(b[:])
}

// yuanbaoBeijingTimestamp 北京时间 ISO 字符串（auth.py:_beijing_timestamp，形如 2026-05-29T15:00:00+08:00）
func yuanbaoBeijingTimestamp() string {
	loc := time.FixedZone("CST", 8*60*60)
	return time.Now().In(loc).Format("2006-01-02T15:04:05-07:00")
}

// yuanbaoSignature HMAC-SHA256 签名（auth.py:_compute_signature）
func yuanbaoSignature(nonce, timestamp, appID, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(nonce + timestamp + appID + secret))
	return hex.EncodeToString(mac.Sum(nil))
}

func yuanbaoSleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

/* ---------- 手写最简 protobuf 编解码 ----------
 *
 * 只覆盖元宝收发消息用到的 message 字段。字段号/类型全部来自 proto/conn.json 与 proto/biz.json，
 * 未确证的字段一律不碰。
 *
 * conn.proto:
 *   Head       { cmdType=1 uint32, cmd=2 string, seqNo=3 uint32, msgId=4 string,
 *                module=5 string, needAck=6 bool, meta=7 repeated, status=10 int32 }
 *   ConnMsg    { head=1 Head, data=2 bytes }
 *   AuthBindReq{ bizId=1 string, authInfo=2 AuthInfo, deviceInfo=3 DeviceInfo, envName=5 string }
 *                AuthInfo   { uid=1, source=2, token=3 }（均 string）
 *                DeviceInfo { instanceId=10 string }
 *   AuthBindRsp{ code=1 int32, message=2 string }
 *   PingReq    {}
 *   PingRsp    { heartInterval=1 uint32, timestamp=2 uint64 }
 *
 * biz.proto（包 trpc.yuanbao.yuanbao_conn.yuanbao_openclaw_proxy）:
 *   MsgBodyElement { msgType=1 string, msgContent=2 MsgContent }
 *   MsgContent     { text=1 string, ... }
 *   SendC2CMessageReq { toAccount=2 string, fromAccount=3 string, msgRandom=4 uint32, msgBody=5 repeated MsgBodyElement, groupCode=6 }
 *   SendGroupMessageReq{ groupCode=2 string, fromAccount=3 string, random=5 string, msgBody=6 repeated MsgBodyElement }
 */

// yuanbaoHead ConnMsg 的 Head（只保留收发消息真正要用的字段）
type yuanbaoHead struct {
	cmdType uint32
	cmd     string
	seqNo   uint32
	msgID   string
	module  string
	needAck bool
	status  int32
}

// yuanbaoConnMsg 解出来的 ConnMsg
type yuanbaoConnMsg struct {
	head yuanbaoHead
	data []byte
}

func pbPutVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

func pbPutTag(dst []byte, field, wire int) []byte {
	return pbPutVarint(dst, uint64(field)<<3|uint64(wire))
}

// pbPutString 写 length-delimited 字符串；proto3 语义：空串不编码。
func pbPutString(dst []byte, field int, s string) []byte {
	if s == "" {
		return dst
	}
	dst = pbPutTag(dst, field, 2)
	dst = pbPutVarint(dst, uint64(len(s)))
	return append(dst, s...)
}

// pbPutBytes 写 length-delimited 字节（子消息：即使为空也保留存在性）。
func pbPutBytes(dst []byte, field int, b []byte) []byte {
	dst = pbPutTag(dst, field, 2)
	dst = pbPutVarint(dst, uint64(len(b)))
	return append(dst, b...)
}

// pbPutUint 写 varint；proto3 语义：0 不编码。
func pbPutUint(dst []byte, field int, v uint64) []byte {
	if v == 0 {
		return dst
	}
	dst = pbPutTag(dst, field, 0)
	return pbPutVarint(dst, v)
}

func pbPutBool(dst []byte, field int, v bool) []byte {
	if !v {
		return dst
	}
	dst = pbPutTag(dst, field, 0)
	return append(dst, 1)
}

type pbReader struct {
	b []byte
	i int
}

func (r *pbReader) more() bool { return r.i < len(r.b) }

func (r *pbReader) varint() (uint64, error) {
	var v uint64
	var shift uint
	for {
		if r.i >= len(r.b) {
			return 0, errors.New("proto: varint 越界")
		}
		c := r.b[r.i]
		r.i++
		v |= uint64(c&0x7f) << shift
		if c < 0x80 {
			return v, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, errors.New("proto: varint 过长")
		}
	}
}

func (r *pbReader) tag() (field, wire int, err error) {
	v, err := r.varint()
	if err != nil {
		return 0, 0, err
	}
	return int(v >> 3), int(v & 7), nil
}

func (r *pbReader) bytes() ([]byte, error) {
	n, err := r.varint()
	if err != nil {
		return nil, err
	}
	if uint64(r.i)+n > uint64(len(r.b)) {
		return nil, errors.New("proto: 长度越界")
	}
	out := r.b[r.i : r.i+int(n)]
	r.i += int(n)
	return out, nil
}

func (r *pbReader) skip(wire int) error {
	switch wire {
	case 0:
		_, err := r.varint()
		return err
	case 1:
		if r.i+8 > len(r.b) {
			return errors.New("proto: 64 位字段越界")
		}
		r.i += 8
		return nil
	case 2:
		_, err := r.bytes()
		return err
	case 5:
		if r.i+4 > len(r.b) {
			return errors.New("proto: 32 位字段越界")
		}
		r.i += 4
		return nil
	default:
		return fmt.Errorf("proto: 不支持的 wire type %d", wire)
	}
}

func encodeHead(h yuanbaoHead) []byte {
	var b []byte
	b = pbPutUint(b, 1, uint64(h.cmdType))
	b = pbPutString(b, 2, h.cmd)
	b = pbPutUint(b, 3, uint64(h.seqNo))
	b = pbPutString(b, 4, h.msgID)
	b = pbPutString(b, 5, h.module)
	b = pbPutBool(b, 6, h.needAck)
	// status(10) 只在应答里出现，出站不带
	return b
}

// encodeConnMsg 编一条 ConnMsg（head=1，data=2）
func encodeConnMsg(h yuanbaoHead, data []byte) []byte {
	out := pbPutBytes(nil, 1, encodeHead(h))
	if len(data) > 0 {
		out = pbPutBytes(out, 2, data)
	}
	return out
}

func decodeHead(b []byte) (yuanbaoHead, bool) {
	r := &pbReader{b: b}
	var h yuanbaoHead
	for r.more() {
		field, wire, err := r.tag()
		if err != nil {
			return h, false
		}
		switch {
		case field == 1 && wire == 0:
			v, err := r.varint()
			if err != nil {
				return h, false
			}
			h.cmdType = uint32(v)
		case field == 2 && wire == 2:
			s, err := r.bytes()
			if err != nil {
				return h, false
			}
			h.cmd = string(s)
		case field == 3 && wire == 0:
			v, err := r.varint()
			if err != nil {
				return h, false
			}
			h.seqNo = uint32(v)
		case field == 4 && wire == 2:
			s, err := r.bytes()
			if err != nil {
				return h, false
			}
			h.msgID = string(s)
		case field == 5 && wire == 2:
			s, err := r.bytes()
			if err != nil {
				return h, false
			}
			h.module = string(s)
		case field == 6 && wire == 0:
			v, err := r.varint()
			if err != nil {
				return h, false
			}
			h.needAck = v != 0
		case field == 10 && wire == 0:
			v, err := r.varint()
			if err != nil {
				return h, false
			}
			h.status = int32(v)
		default:
			if err := r.skip(wire); err != nil {
				return h, false
			}
		}
	}
	return h, true
}

// decodeConnMsg 解一条 ConnMsg
func decodeConnMsg(raw []byte) (yuanbaoConnMsg, bool) {
	r := &pbReader{b: raw}
	var out yuanbaoConnMsg
	for r.more() {
		field, wire, err := r.tag()
		if err != nil {
			return out, false
		}
		switch {
		case field == 1 && wire == 2:
			hb, err := r.bytes()
			if err != nil {
				return out, false
			}
			h, ok := decodeHead(hb)
			if !ok {
				return out, false
			}
			out.head = h
		case field == 2 && wire == 2:
			d, err := r.bytes()
			if err != nil {
				return out, false
			}
			out.data = d
		default:
			if err := r.skip(wire); err != nil {
				return out, false
			}
		}
	}
	return out, true
}

// encodeAuthBindReq 编 AuthBindReq（biz_id / authInfo / deviceInfo.instanceId）
func encodeAuthBindReq(bizID, uid, source, token string) []byte {
	var auth []byte
	auth = pbPutString(auth, 1, uid)
	auth = pbPutString(auth, 2, source)
	auth = pbPutString(auth, 3, token)

	var dev []byte
	dev = pbPutString(dev, 10, yuanbaoDeviceInstanceID)

	var req []byte
	req = pbPutString(req, 1, bizID)
	req = pbPutBytes(req, 2, auth)
	req = pbPutBytes(req, 3, dev)
	return req
}

// decodeAuthRspCode 从 AuthBindRsp 里取 code（field 1）。返回 (code, 是否存在)。
func decodeAuthRspCode(b []byte) (int32, bool) {
	r := &pbReader{b: b}
	for r.more() {
		field, wire, err := r.tag()
		if err != nil {
			return 0, false
		}
		if field == 1 && wire == 0 {
			v, err := r.varint()
			if err != nil {
				return 0, false
			}
			return int32(v), true
		}
		if err := r.skip(wire); err != nil {
			return 0, false
		}
	}
	return 0, false
}

// decodePingHeartInterval 从 PingRsp 里取 heartInterval（field 1）；没有则返回 0。
func decodePingHeartInterval(b []byte) uint32 {
	r := &pbReader{b: b}
	for r.more() {
		field, wire, err := r.tag()
		if err != nil {
			return 0
		}
		if field == 1 && wire == 0 {
			v, err := r.varint()
			if err != nil {
				return 0
			}
			return uint32(v)
		}
		if err := r.skip(wire); err != nil {
			return 0
		}
	}
	return 0
}

// encodeMsgBodyElement 编一条文本 MsgBodyElement（msgType=TIMTextElem，msgContent.text）
func encodeMsgBodyElement(text string) []byte {
	var content []byte
	content = pbPutString(content, 1, text)

	var elem []byte
	elem = pbPutString(elem, 1, "TIMTextElem")
	elem = pbPutBytes(elem, 2, content)
	return elem
}

// encodeSendC2CReq 编 SendC2CMessageReq（toAccount/fromAccount/msgRandom/msgBody）
func encodeSendC2CReq(toAccount, fromAccount string, msgRandom uint32, text string) []byte {
	var b []byte
	b = pbPutString(b, 2, toAccount)
	b = pbPutString(b, 3, fromAccount)
	b = pbPutUint(b, 4, uint64(msgRandom))
	b = pbPutBytes(b, 5, encodeMsgBodyElement(text))
	return b
}

// encodeSendGroupReq 编 SendGroupMessageReq（groupCode/fromAccount/random/msgBody）
func encodeSendGroupReq(groupCode, fromAccount, random string, text string) []byte {
	var b []byte
	b = pbPutString(b, 2, groupCode)
	b = pbPutString(b, 3, fromAccount)
	b = pbPutString(b, 5, random)
	b = pbPutBytes(b, 6, encodeMsgBodyElement(text))
	return b
}
