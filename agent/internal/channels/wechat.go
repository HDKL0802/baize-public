package channels

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"baize/internal/config"
)

// 个人微信频道（微信官方 iLink Bot API）。
//
// 为什么是这套协议：QwenPaw 的微信频道（src/qwenpaw/app/channels/wechat/）走的不是
// WebSocket，也不是第三方逆向网关，而是微信官方的 iLink Bot HTTP/JSON 接口
// （基址 https://ilinkai.weixin.qq.com，微信 AI「ClawBot」开放能力）。它只有三种交互：
//
//	登录：GET  /ilink/bot/get_bot_qrcode?bot_type=3              → {qrcode, qrcode_img_content}
//	     GET  /ilink/bot/get_qrcode_status?qrcode=<qrcode>       → {status, bot_token, baseurl}
//	收信：POST /ilink/bot/getupdates  {get_updates_buf, base_info} 长轮询（服务端 hold 约 35s）
//	     → {ret, msgs:[{from_user_id, context_token, message_type, item_list:[{type:1,text_item:{text}}]}],
//	        get_updates_buf}
//	发信：POST /ilink/bot/sendmessage {msg:{to_user_id, context_token, message_type:2, message_state:2,
//	     item_list:[{type:1,text_item:{text}}]}, base_info}
//
// 关键约束（照 1:1 实现，未做任何杜撰）：
//   - 每个请求都要带 AuthorizationType: ilink_bot_token、X-WECHAT-UIN（base64(随机 uint32)），
//     登录后用 Authorization: Bearer <bot_token>。
//   - 回复必须原样带上入站消息里的 context_token，否则被拒（ret=-2 表示该 token 已失效/被消费）。
//   - getupdates 的 ret=-1 是「长轮询超时、本次没消息」，属正常；ret=-14 表示会话过期，要重新扫码。
//   - message_type=1 才是「用户发给机器人」，=2 是机器人自己发的（要跳过，避免回环）。
//
// 这套接口是纯 HTTP，所以把 Domain 指向本地假服务即可在**没有真实微信账号**时完整验证；
// 真正上线仍需用微信扫码拿 bot_token。登录凭证按约定落到
// os.UserConfigDir()/baize/channels/<id>/wechat_login.json，下次启动自动复用。
const (
	weChatDefaultBaseURL = "https://ilinkai.weixin.qq.com"
	weChatChannelVersion = "2.0.1"

	// getupdates 是长轮询，服务端会 hold 约 35s，客户端要给足超时
	weChatGetUpdatesTimeout = 45 * time.Second
	// 普通请求（发信 / 取码）超时
	weChatRequestTimeout = 15 * time.Second
	// 二维码状态查询是长轮询，服务端会 hold 约 30~60s
	weChatQrcodeTimeout = 60 * time.Second
	// 整体保护网：比上面任何单次超时都大
	weChatClientMaxTimeout = 90 * time.Second

	weChatRetryMin = 3 * time.Second
	weChatRetryMax = 60 * time.Second

	// 扫码登录：轮询间隔与最长等待
	weChatQRPollInterval = 1500 * time.Millisecond
	weChatLoginMaxWait   = 5 * time.Minute
	// 非 http 开头的 qrcode_img_content 时，用它拼一个可扫码的登录页链接
	weChatQRScanURL = "https://liteapp.weixin.qq.com/q/7GiQu1"

	// 去重的表大小上限（超出整体清空，别让内存无界增长）
	weChatSeenMax = 2000

	// item_list[].type：1 = 文本
	weChatItemText = 1
	// msg.message_type：1 = 用户发给机器人，2 = 机器人（BOT）
	weChatMsgTypeUser = 1
	weChatMsgTypeBot  = 2
	// msg.message_state：2 = FINISH
	weChatMsgStateFinish = 2
)

// errWeChatSessionExpired 会话过期（ret=-14），需要重新扫码登录
var errWeChatSessionExpired = errors.New("微信 iLink 会话已过期")

// WeChat 个人微信频道：实现 Channel（发信）与 PollChannel（长轮询收信 + 扫码登录）。
type WeChat struct {
	id      string
	prefix  string
	baseURL string // iLink 基址（默认官方；测试可指向本地假服务）
	token   string // bot_token（Bearer）；空 = 未登录
	credDir string // 凭证目录：os.UserConfigDir()/baize/channels/<id>/

	qrPoll time.Duration // 二维码轮询间隔（默认 weChatQRPollInterval，测试可调小）

	lg     *slog.Logger
	client *http.Client

	mu sync.Mutex
}

// 编译期确认实现了两个接口
var (
	_ Channel     = (*WeChat)(nil)
	_ PollChannel = (*WeChat)(nil)
)

// NewWeChat 造一个个人微信频道（iLink Bot）。
// 配置字段：Domain = iLink 基址（空则用官方默认）；Token = bot_token（空则尝试读本地凭证，
// 没有就等 Poll / Login 时扫码）；BotPrefix = 回复前缀；ID = 频道 id。
func NewWeChat(cfg config.ChannelConfig, lg *slog.Logger) *WeChat {
	if lg == nil {
		lg = slog.Default()
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.Domain), "/")
	if base == "" {
		base = weChatDefaultBaseURL
	}
	w := &WeChat{
		id:      strings.TrimSpace(cfg.ID),
		prefix:  cfg.BotPrefix,
		baseURL: base,
		token:   strings.TrimSpace(cfg.Token),
		qrPoll:  weChatQRPollInterval,
		lg:      lg,
		client:  &http.Client{Timeout: weChatClientMaxTimeout},
	}
	w.credDir = w.credentialDir()
	if w.token == "" {
		if cr, err := w.loadCredential(); err == nil && cr.BotToken != "" {
			w.token = cr.BotToken
			if cr.BaseURL != "" {
				w.baseURL = strings.TrimRight(cr.BaseURL, "/")
			}
			w.lg.Info("微信频道已从本地恢复登录凭证", "channel", w.id)
		}
	}
	return w
}

// Poll 长轮询收信：先把登录状态备好（没登录就扫码登录），再不断 getupdates 拉新消息；
// 断线按指数退避重连，ctx 取消即停。ret=-14（会话过期）会自动清凭证并重新扫码。
func (w *WeChat) Poll(ctx context.Context, onEvent func(Message)) {
	if w.currentToken() == "" {
		if err := w.Login(ctx); err != nil {
			w.lg.Warn("微信频道未登录，收不了消息（请先扫码登录）", "channel", w.id, "err", err)
			return
		}
	}
	cursor := ""
	seen := map[string]struct{}{}
	backoff := weChatRetryMin
	for {
		if ctx.Err() != nil {
			return
		}
		msgs, newCursor, err := w.getUpdates(ctx, cursor)
		if err == nil {
			cursor = newCursor
			backoff = weChatRetryMin
			for _, m := range msgs {
				if msg, ok := w.parseInbound(m, seen); ok {
					onEvent(msg)
				}
			}
			continue // 立刻继续长轮询
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errWeChatSessionExpired) {
			w.lg.Warn("微信登录已失效，重新扫码登录", "channel", w.id)
			w.clearLogin()
			if lerr := w.Login(ctx); lerr != nil {
				w.lg.Warn("微信重新登录失败", "channel", w.id, "err", lerr)
			}
			continue
		}
		w.lg.Warn("微信长轮询断开，稍后重连", "channel", w.id, "err", err)
		if !weChatSleep(ctx, backoff) {
			return
		}
		if backoff < weChatRetryMax {
			backoff *= 2
		}
	}
}

// Login 走二维码扫码登录：取码 → 轮询状态 → 拿到 bot_token 并持久化。
// 二维码链接会写进日志（headless 场景照日志里的链接用微信扫即可）。ctx 取消即中止。
func (w *WeChat) Login(ctx context.Context) error {
	var qr struct {
		Qrcode           string `json:"qrcode"`
		QrcodeImgContent string `json:"qrcode_img_content"`
	}
	getCtx, cancelGet := context.WithTimeout(ctx, weChatRequestTimeout)
	err := w.doJSON(getCtx, http.MethodGet, "ilink/bot/get_bot_qrcode",
		url.Values{"bot_type": {"3"}}, nil, &qr)
	cancelGet()
	if err != nil {
		return fmt.Errorf("取微信登录二维码失败：%w", err)
	}
	if strings.TrimSpace(qr.Qrcode) == "" {
		return errors.New("微信没给出二维码 token（qrcode 为空）")
	}
	scan := strings.TrimSpace(qr.QrcodeImgContent)
	if !strings.HasPrefix(scan, "http") {
		scan = fmt.Sprintf("%s?qrcode=%s&bot_type=3", weChatQRScanURL, url.QueryEscape(qr.Qrcode))
	}
	w.lg.Info("微信频道等待扫码登录：请用微信扫描二维码", "channel", w.id, "scan_url", scan)

	deadline := time.Now().Add(weChatLoginMaxWait)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errors.New("微信扫码登录超时（请重试）")
		}
		var st struct {
			Status   string `json:"status"`
			BotToken string `json:"bot_token"`
			BaseURL  string `json:"baseurl"`
			BotID    string `json:"ilink_bot_id"`
			UserID   string `json:"ilink_user_id"`
		}
		pollCtx, cancelPoll := context.WithTimeout(ctx, weChatQrcodeTimeout)
		err := w.doJSON(pollCtx, http.MethodGet, "ilink/bot/get_qrcode_status",
			url.Values{"qrcode": {qr.Qrcode}}, nil, &st)
		cancelPoll()
		if err != nil {
			// 状态查询本身是长轮询，超时属正常，继续轮询
			w.lg.Debug("微信扫码状态查询失败，重试", "channel", w.id, "err", err)
			if !weChatSleep(ctx, w.qrPoll) {
				return ctx.Err()
			}
			continue
		}
		switch strings.ToLower(strings.TrimSpace(st.Status)) {
		case "confirmed":
			if strings.TrimSpace(st.BotToken) == "" {
				return errors.New("微信登录已确认但没给 bot_token")
			}
			w.setLogin(st.BotToken, st.BaseURL)
			cr := weChatCredential{
				BotToken: st.BotToken, BaseURL: st.BaseURL,
				BotID: st.BotID, UserID: st.UserID,
			}
			if err := w.saveCredential(cr); err != nil {
				w.lg.Warn("微信登录凭证保存失败（下次可能要重新扫码）", "channel", w.id, "err", err)
			}
			w.lg.Info("微信频道登录成功", "channel", w.id)
			return nil
		case "expired":
			return errors.New("微信登录二维码已过期，请重试")
		default: // waiting / scanned / wait / scaned …
			if !weChatSleep(ctx, w.qrPoll) {
				return ctx.Err()
			}
		}
	}
}

// Send 把回复发回消息来源：iLink 要求带上入站消息的 context_token，发给入站消息的 from_user_id。
func (w *WeChat) Send(ctx context.Context, msg Message, reply string) error {
	token := w.currentToken()
	if token == "" {
		return errors.New("微信频道未登录（缺 bot_token），先扫码登录")
	}
	to := strings.TrimSpace(messageStr(msg, "from_user_id"))
	if to == "" {
		to = strings.TrimSpace(messageStr(msg, "user_id"))
	}
	if to == "" {
		return errors.New("这条微信消息没有可回发的对象（缺 from_user_id）")
	}
	ctxToken := strings.TrimSpace(messageStr(msg, "context_token"))
	if ctxToken == "" {
		return errors.New("这条微信消息没有 context_token，iLink 要求回复必须原样带上它")
	}
	body := map[string]any{
		"msg": map[string]any{
			"from_user_id":  "",
			"to_user_id":    to,
			"client_id":     weChatNewUUID(),
			"message_type":  weChatMsgTypeBot,
			"message_state": weChatMsgStateFinish,
			"context_token": ctxToken,
			"item_list": []map[string]any{
				{"type": weChatItemText, "text_item": map[string]string{"text": w.prefix + reply}},
			},
		},
		"base_info": map[string]string{"channel_version": weChatChannelVersion},
	}
	var out struct {
		Ret     int    `json:"ret"`
		Errcode int    `json:"errcode"`
		Errmsg  string `json:"errmsg"`
	}
	sendCtx, cancel := context.WithTimeout(ctx, weChatRequestTimeout)
	defer cancel()
	if err := w.doJSON(sendCtx, http.MethodPost, "ilink/bot/sendmessage", nil, body, &out); err != nil {
		return fmt.Errorf("调微信 iLink 发消息失败：%w", err)
	}
	if out.Ret == -2 || out.Errcode == -2 {
		return errors.New("微信 iLink 说 context_token 已失效（ret=-2）")
	}
	if out.Ret != 0 || out.Errcode != 0 {
		return fmt.Errorf("微信 iLink 拒绝了发送（ret=%d，errcode=%d）：%s",
			out.Ret, out.Errcode, strings.TrimSpace(out.Errmsg))
	}
	return nil
}

// getUpdates 调一次长轮询。ret=-1（本次无消息）不算错误；ret=-14 返回 errWeChatSessionExpired。
func (w *WeChat) getUpdates(ctx context.Context, cursor string) ([]weChatMessage, string, error) {
	body := map[string]any{
		"get_updates_buf": cursor,
		"base_info":       map[string]string{"channel_version": weChatChannelVersion},
	}
	var out struct {
		Ret                  int             `json:"ret"`
		Errcode              int             `json:"errcode"`
		Errmsg               string          `json:"errmsg"`
		Msgs                 []weChatMessage `json:"msgs"`
		Buf                  string          `json:"get_updates_buf"`
		LongPollingTimeoutMs int             `json:"longpolling_timeout_ms"`
	}
	reqCtx, cancel := context.WithTimeout(ctx, weChatGetUpdatesTimeout)
	defer cancel()
	if err := w.doJSON(reqCtx, http.MethodPost, "ilink/bot/getupdates", nil, body, &out); err != nil {
		return nil, cursor, err
	}
	if out.Ret == -14 || out.Errcode == -14 {
		return nil, cursor, errWeChatSessionExpired
	}
	if out.Ret == -1 {
		return nil, cursor, nil // 长轮询超时，正常
	}
	if out.Ret != 0 {
		return nil, cursor, fmt.Errorf("微信 getupdates 返回 ret=%d（%s）", out.Ret, strings.TrimSpace(out.Errmsg))
	}
	if out.Buf != "" {
		cursor = out.Buf
	}
	return out.Msgs, cursor, nil
}

// parseInbound 把一条 iLink 消息解成统一的入站 Message。
// 只处理 message_type==1（用户发给机器人）；同一 context_token 只产出一次。
func (w *WeChat) parseInbound(m weChatMessage, seen map[string]struct{}) (Message, bool) {
	if m.MessageType != weChatMsgTypeUser {
		return Message{}, false
	}
	text := m.text()
	if text == "" {
		return Message{}, false // 先把文本做扎实；图片/语音/文件后续再补
	}
	from := strings.TrimSpace(m.FromUserID)
	key := strings.TrimSpace(m.ContextToken)
	if key == "" {
		key = from + "_" + strings.TrimSpace(m.MsgID)
	}
	if key != "" {
		if len(seen) > weChatSeenMax {
			for k := range seen {
				delete(seen, k)
			}
		}
		if _, dup := seen[key]; dup {
			return Message{}, false
		}
		seen[key] = struct{}{}
	}
	session := "wechat:" + from
	if strings.TrimSpace(m.GroupID) != "" {
		session = "wechat:group:" + strings.TrimSpace(m.GroupID)
	}
	return Message{
		Channel: w.id,
		Session: session,
		Sender:  from,
		Text:    text,
		Meta: map[string]any{
			"context_token": m.ContextToken,
			"from_user_id":  m.FromUserID,
			"to_user_id":    m.ToUserID,
			"group_id":      m.GroupID,
			"message_id":    m.MsgID,
		},
	}, true
}

// doJSON 发一个带 iLink 头的请求并把 JSON 响应解进 out（out 为 nil 时只校验状态码）。
func (w *WeChat) doJSON(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := strings.TrimRight(w.currentBaseURL(), "/") + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("X-WECHAT-UIN", weChatUIN())
	if tok := w.currentToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("iLink 返回 http %d：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("解析 iLink 响应失败：%w", err)
	}
	return nil
}

/* ---------- 登录状态 / 凭证持久化 ---------- */

// weChatCredential 本地持久化的登录凭证
type weChatCredential struct {
	BotToken string `json:"bot_token"`
	BaseURL  string `json:"base_url,omitempty"`
	BotID    string `json:"ilink_bot_id,omitempty"`
	UserID   string `json:"ilink_user_id,omitempty"`
	SavedAt  string `json:"saved_at,omitempty"`
}

// credentialDir 凭证目录：os.UserConfigDir()/baize/channels/<id>/
func (w *WeChat) credentialDir() string {
	base, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "baize", "channels", weChatSafeID(w.id))
}

func (w *WeChat) credentialPath() string {
	return filepath.Join(w.credDir, "wechat_login.json")
}

// loadCredential 读本地凭证（不存在返回错误）
func (w *WeChat) loadCredential() (weChatCredential, error) {
	var cr weChatCredential
	raw, err := os.ReadFile(w.credentialPath())
	if err != nil {
		return cr, err
	}
	if err := json.Unmarshal(raw, &cr); err != nil {
		return cr, err
	}
	return cr, nil
}

// saveCredential 写本地凭证（0600，目录 0700）
func (w *WeChat) saveCredential(cr weChatCredential) error {
	if strings.TrimSpace(w.credDir) == "" {
		return errors.New("没有可用的配置目录")
	}
	if err := os.MkdirAll(w.credDir, 0o700); err != nil {
		return err
	}
	cr.SavedAt = time.Now().Format(time.RFC3339)
	b, err := json.MarshalIndent(cr, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(w.credentialPath(), b, 0o600)
}

// clearLogin 清空内存里的登录态并删掉本地凭证（会话过期时用）
func (w *WeChat) clearLogin() {
	w.mu.Lock()
	w.token = ""
	w.mu.Unlock()
	_ = os.Remove(w.credentialPath())
}

func (w *WeChat) setLogin(token, baseURL string) {
	w.mu.Lock()
	w.token = strings.TrimSpace(token)
	if b := strings.TrimRight(strings.TrimSpace(baseURL), "/"); b != "" {
		w.baseURL = b
	}
	w.mu.Unlock()
}

func (w *WeChat) currentToken() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.token
}

func (w *WeChat) currentBaseURL() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.baseURL
}

/* ---------- 消息 / 工具 ---------- */

// weChatMessage iLink 入站消息（只取需要的字段）
type weChatMessage struct {
	FromUserID   string       `json:"from_user_id"`
	ToUserID     string       `json:"to_user_id"`
	ContextToken string       `json:"context_token"`
	GroupID      string       `json:"group_id"`
	MessageType  int          `json:"message_type"`
	MsgID        string       `json:"msg_id"`
	ItemList     []weChatItem `json:"item_list"`
}

// weChatItem item_list 里的一项（文本只用 type==1 的 text_item）
type weChatItem struct {
	Type     int `json:"type"`
	TextItem struct {
		Text string `json:"text"`
	} `json:"text_item"`
}

// text 取出所有文本项拼成的正文
func (m weChatMessage) text() string {
	var b strings.Builder
	for _, it := range m.ItemList {
		if it.Type == weChatItemText {
			b.WriteString(it.TextItem.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// messageStr 从 Meta 里取一个字符串字段
func messageStr(msg Message, key string) string {
	if msg.Meta == nil {
		return ""
	}
	v, _ := msg.Meta[key].(string)
	return v
}

// weChatUIN 每个请求都要带 X-WECHAT-UIN：base64(十进制随机 uint32)
func weChatUIN() string {
	var b [4]byte
	if _, err := crand.Read(b[:]); err != nil {
		return base64.StdEncoding.EncodeToString([]byte("0"))
	}
	n := binary.BigEndian.Uint32(b[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(n), 10)))
}

// weChatNewUUID 生成 v4 UUID（只依赖标准库）
func weChatNewUUID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		u := uint64(time.Now().UnixNano())
		for i := range b {
			b[i] = byte(u >> (uint(i%8) * 8))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// weChatSafeID 把频道 id 收拾成安全的目录名
func weChatSafeID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// weChatSleep 可被 ctx 打断的 sleep，返回 false 表示 ctx 已取消
func weChatSleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
