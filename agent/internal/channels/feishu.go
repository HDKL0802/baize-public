package channels

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"baize/internal/config"
)

// EventChannel 支持"平台事件回调"的频道（如飞书）：平台把事件 POST 给我们，
// 频道自己解析（含鉴权）并产出入站 Message。
type EventChannel interface {
	Channel
	// HandleEvent 解析一次事件回调。
	//   challenge 非空：这是 URL 验证握手，把 challenge 原样回给平台即可；
	//   msgs：本次要跑的入站消息（可能为空，例如非消息事件）；
	//   鉴权失败返回 *InboundError（带 HTTP 语义状态码）。
	HandleEvent(body []byte, headers map[string]string) (challenge string, msgs []Message, err error)
}

// Feishu 飞书频道（事件回调模式）：
//   - 入站：飞书把事件 POST 到 /api/channels/{id}/event（先 url_verification 握手，再 im.message.receive_v1）
//   - 出站：换 tenant_access_token 后调 im/v1/messages 发文本
//
// 选事件回调而非长连接：长连接要引入 lark SDK 且难在本地验证；事件回调是纯 HTTP，
// 可把 domain 指向本地假飞书来完整验证。
type Feishu struct {
	id      string
	domain  string
	appID   string
	secret  string
	token   string // 验证令牌（飞书后台「事件订阅」里那个）
	encKey  string // 可选：事件加密时用来解密
	prefix  string
	chatIDs []string // 轮询监听的会话（配了就启用轮询入站，免公网）
	pollSec int

	lg *slog.Logger

	mu     sync.Mutex
	tok    string
	tokExp time.Time
	client *http.Client
}

// feishuDefaultDomain 飞书开放平台地址（自建/测试可改）
const feishuDefaultDomain = "https://open.feishu.cn"

// NewFeishu 造一个飞书频道
func NewFeishu(cfg config.ChannelConfig, lg *slog.Logger) *Feishu {
	if lg == nil {
		lg = slog.Default()
	}
	domain := strings.TrimRight(strings.TrimSpace(cfg.Domain), "/")
	if domain == "" {
		domain = feishuDefaultDomain
	}
	ids := make([]string, 0, len(cfg.ChatIDs))
	for _, id := range cfg.ChatIDs {
		if s := strings.TrimSpace(id); s != "" {
			ids = append(ids, s)
		}
	}
	return &Feishu{
		id: strings.TrimSpace(cfg.ID), domain: domain,
		appID: strings.TrimSpace(cfg.AppID), secret: strings.TrimSpace(cfg.AppSecret),
		token: strings.TrimSpace(cfg.Token), encKey: strings.TrimSpace(cfg.EncryptKey),
		prefix: cfg.BotPrefix, chatIDs: ids, pollSec: cfg.PollSec,
		lg: lg, client: &http.Client{Timeout: 20 * time.Second},
	}
}

// HandleEvent 解析飞书事件回调
func (f *Feishu) HandleEvent(body []byte, _ map[string]string) (string, []Message, error) {
	raw := body
	// 配了加密：body 形如 {"encrypt":"..."}，先解密
	if f.encKey != "" {
		var enc struct {
			Encrypt string `json:"encrypt"`
		}
		if err := json.Unmarshal(body, &enc); err != nil {
			return "", nil, errors.New("事件不是合法 JSON")
		}
		if enc.Encrypt != "" {
			dec, err := feishuDecrypt(f.encKey, enc.Encrypt)
			if err != nil {
				return "", nil, fmt.Errorf("事件解密失败：%w", err)
			}
			raw = dec
		}
	}
	var env struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Token     string `json:"token"`
		Header    struct {
			EventType string `json:"event_type"`
			Token     string `json:"token"`
		} `json:"header"`
		Event struct {
			Sender struct {
				SenderID struct {
					OpenID string `json:"open_id"`
					UserID string `json:"user_id"`
				} `json:"sender_id"`
			} `json:"sender"`
			Message struct {
				MessageID   string `json:"message_id"`
				ChatID      string `json:"chat_id"`
				ChatType    string `json:"chat_type"`
				MessageType string `json:"message_type"`
				Content     string `json:"content"`
			} `json:"message"`
		} `json:"event"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", nil, errors.New("事件不是合法 JSON")
	}
	// 验证令牌（配置了就必须匹配；两代格式都在 header.token 或顶层 token 里）
	got := strings.TrimSpace(env.Token)
	if got == "" {
		got = strings.TrimSpace(env.Header.Token)
	}
	if f.token != "" && got != f.token {
		return "", nil, &InboundError{Code: 401, Msg: "飞书验证令牌不对"}
	}
	// URL 验证握手
	if env.Type == "url_verification" && env.Challenge != "" {
		return env.Challenge, nil, nil
	}
	// 只处理消息事件
	if env.Header.EventType != "im.message.receive_v1" {
		return "", nil, nil
	}
	if env.Event.Message.MessageType != "text" || env.Event.Message.ChatID == "" {
		return "", nil, nil
	}
	text := feishuText(env.Event.Message.Content)
	if text == "" {
		return "", nil, nil
	}
	sender := env.Event.Sender.SenderID.OpenID
	if sender == "" {
		sender = env.Event.Sender.SenderID.UserID
	}
	return "", []Message{{
		Channel: f.id,
		Session: "feishu:" + env.Event.Message.ChatID,
		Sender:  sender,
		Text:    text,
		Meta:    map[string]any{"receive_id": env.Event.Message.ChatID, "receive_id_type": "chat_id"},
	}}, nil
}

// Send 把回复发到飞书会话
func (f *Feishu) Send(ctx context.Context, msg Message, reply string) error {
	chatID, _ := msg.Meta["receive_id"].(string)
	if chatID == "" {
		return errors.New("这条消息没有可回发的会话（缺 chat_id）")
	}
	tok, err := f.tenantToken(ctx)
	if err != nil {
		return err
	}
	content, _ := json.Marshal(map[string]string{"text": f.prefix + reply})
	payload, _ := json.Marshal(map[string]any{
		"receive_id": chatID, "msg_type": "text", "content": string(content),
	})
	u := f.domain + "/open-apis/im/v1/messages?receive_id_type=chat_id"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("调飞书发消息失败：%w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode >= 300 || out.Code != 0 {
		return fmt.Errorf("飞书返回错误（http %d，code %d）：%s", resp.StatusCode, out.Code, strings.TrimSpace(string(body)))
	}
	return nil
}

// tenantToken 取（并缓存）tenant_access_token
func (f *Feishu) tenantToken(ctx context.Context) (string, error) {
	if f.appID == "" || f.secret == "" {
		return "", errors.New("飞书频道没配 appId / appSecret，发不出去")
	}
	f.mu.Lock()
	if f.tok != "" && time.Now().Before(f.tokExp) {
		t := f.tok
		f.mu.Unlock()
		return t, nil
	}
	f.mu.Unlock()

	payload, _ := json.Marshal(map[string]string{"app_id": f.appID, "app_secret": f.secret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		f.domain+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("取飞书 tenant_access_token 失败：%w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Tok  string `json:"tenant_access_token"`
		Exp  int    `json:"expire"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&out); err != nil {
		return "", fmt.Errorf("解析飞书令牌响应失败：%w", err)
	}
	if out.Code != 0 || out.Tok == "" {
		return "", fmt.Errorf("飞书没给出令牌（code %d：%s）", out.Code, out.Msg)
	}
	ttl := time.Duration(out.Exp) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	f.mu.Lock()
	f.tok = out.Tok
	f.tokExp = time.Now().Add(ttl - 5*time.Minute) // 留 5 分钟余量
	f.mu.Unlock()
	return out.Tok, nil
}

var feishuMentionRe = regexp.MustCompile(`@_user_\d+`)

// feishuText 从 message.content 里取纯文本（内容是 JSON 字符串，形如 {"text":"..."}），并去掉 @ 占位
func feishuText(content string) string {
	var c struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &c); err != nil {
		return ""
	}
	return strings.TrimSpace(feishuMentionRe.ReplaceAllString(c.Text, ""))
}

// feishuDecrypt 解密飞书加密事件：key = sha256(encryptKey)，AES-256-CBC，IV 取密文头 16 字节
func feishuDecrypt(encryptKey, b64 string) ([]byte, error) {
	key := sha256.Sum256([]byte(encryptKey))
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	if len(data) < aes.BlockSize || (len(data)-aes.BlockSize)%aes.BlockSize != 0 {
		return nil, errors.New("密文长度不对")
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	iv, ct := data[:aes.BlockSize], data[aes.BlockSize:]
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	n := int(out[len(out)-1])
	if n <= 0 || n > aes.BlockSize || n > len(out) {
		return nil, errors.New("padding 不对")
	}
	return out[:len(out)-n], nil
}

/* ---------- 轮询入站（免公网模式） ---------- */

// Poll 轮询监听配置的会话：拉到新消息就回调 onEvent。ctx 取消即停。
// 只处理「用户发的文本」，跳过 bot 自己 / 系统消息，避免回环。
func (f *Feishu) Poll(ctx context.Context, onEvent func(Message)) {
	if len(f.chatIDs) == 0 {
		return
	}
	interval := time.Duration(f.pollSec) * time.Second
	if interval < 2*time.Second {
		interval = 5 * time.Second
	}
	last := map[string]int64{} // 每个会话已读到的最大 create_time（毫秒）
	seen := map[string]bool{}
	now := time.Now().UnixMilli()
	for _, id := range f.chatIDs {
		last[id] = now // 起点 = 现在，别把历史消息一股脑触发
	}
	f.lg.Info("飞书频道开始轮询入站", "channel", f.id, "chats", len(f.chatIDs), "everySec", int(interval.Seconds()))
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if len(seen) > 5000 {
				seen = map[string]bool{}
			}
			for _, id := range f.chatIDs {
				f.pollOnce(ctx, id, last, seen, onEvent)
			}
		}
	}
}

// pollOnce 拉一个会话的新消息，逐条回调
func (f *Feishu) pollOnce(ctx context.Context, chatID string, last map[string]int64, seen map[string]bool, onEvent func(Message)) {
	tok, err := f.tenantToken(ctx)
	if err != nil {
		f.lg.Warn("飞书轮询取 token 失败", "channel", f.id, "err", err)
		return
	}
	// 注意单位：start_time 用秒（飞书要求），而 create_time 是毫秒 —— 下面按毫秒过滤，避免秒级精度带来的重放
	u := fmt.Sprintf("%s/open-apis/im/v1/messages?container_id_type=chat&container_id=%s&start_time=%d&page_size=50",
		f.domain, url.QueryEscape(chatID), last[chatID]/1000)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := f.client.Do(req)
	if err != nil {
		f.lg.Warn("飞书轮询请求失败", "channel", f.id, "err", err)
		return
	}
	defer resp.Body.Close()
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []struct {
				MessageID  string `json:"message_id"`
				MsgType    string `json:"msg_type"`
				CreateTime string `json:"create_time"`
				Body       struct {
					Content string `json:"content"`
				} `json:"body"`
				Sender struct {
					ID         string `json:"id"`
					SenderType string `json:"sender_type"`
				} `json:"sender"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		f.lg.Warn("飞书轮询响应解析失败", "channel", f.id, "err", err)
		return
	}
	if out.Code != 0 {
		f.lg.Warn("飞书轮询返回错误", "channel", f.id, "code", out.Code, "msg", out.Msg)
		return
	}
	for _, it := range out.Data.Items {
		ct, _ := strconv.ParseInt(strings.TrimSpace(it.CreateTime), 10, 64)
		if ct <= last[chatID] {
			continue // 已处理过
		}
		if ct > last[chatID] {
			last[chatID] = ct // 推进游标（非文本 / 机器人消息也推进，免得反复拉）
		}
		if seen[it.MessageID] {
			continue
		}
		seen[it.MessageID] = true
		if it.Sender.SenderType != "user" {
			continue // 跳过 bot 自己 / 系统消息，避免回环
		}
		if it.MsgType != "text" {
			continue
		}
		text := feishuText(it.Body.Content)
		if text == "" {
			continue
		}
		onEvent(Message{
			Channel: f.id,
			Session: "feishu:" + chatID,
			Sender:  it.Sender.ID,
			Text:    text,
			Meta:    map[string]any{"receive_id": chatID, "receive_id_type": "chat_id", "message_id": it.MessageID},
		})
	}
}
