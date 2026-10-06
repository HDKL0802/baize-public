package channels

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"baize/internal/config"
)

// isolateUserConfig 把 os.UserConfigDir() 指到一个临时目录，避免测试碰到真实用户配置。
func isolateUserConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AppData", dir)         // Windows：os.UserConfigDir() 用 %AppData%
	t.Setenv("XDG_CONFIG_HOME", dir) // Linux
	t.Setenv("HOME", dir)            // 兜底
	return dir
}

func weChatTextItem(text string) weChatItem {
	it := weChatItem{Type: weChatItemText}
	it.TextItem.Text = text
	return it
}

// TestWeChatQRLoginPersistsCredential 走完整二维码登录：取码 → 轮询（先 scanned 后 confirmed）
// → 拿到 bot_token 并落盘；新建实例能自动恢复。
func TestWeChatQRLoginPersistsCredential(t *testing.T) {
	isolateUserConfig(t)

	var (
		mu       sync.Mutex
		statusN  int
		srvURL   string
		noAuthOn int // 登录前的请求不应带 Authorization
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/ilink/bot/get_bot_qrcode", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Query().Get("bot_type") != "3" {
			http.Error(w, "bot_type", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") == "" {
			mu.Lock()
			noAuthOn++
			mu.Unlock()
		}
		if r.Header.Get("AuthorizationType") != "ilink_bot_token" {
			http.Error(w, "auth type", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"qrcode": "qr-login", "qrcode_img_content": "",
		})
	})
	mux.HandleFunc("/ilink/bot/get_qrcode_status", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("qrcode") != "qr-login" {
			http.Error(w, "qrcode", http.StatusBadRequest)
			return
		}
		mu.Lock()
		statusN++
		n := statusN
		mu.Unlock()
		if n < 2 {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "scanned"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "confirmed", "bot_token": "tk-login", "baseurl": srvURL,
			"ilink_bot_id": "bot-1", "ilink_user_id": "u-1",
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	w := NewWeChat(config.ChannelConfig{ID: "wx", Domain: srv.URL}, nil)
	w.qrPoll = 5 * time.Millisecond // 测试里别等 1.5s

	if w.currentToken() != "" {
		t.Fatalf("未登录时不该有 token：%q", w.currentToken())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Login(ctx); err != nil {
		t.Fatalf("扫码登录失败：%v", err)
	}
	if w.currentToken() != "tk-login" {
		t.Fatalf("登录后 token 不对：%q", w.currentToken())
	}
	if w.currentBaseURL() != srv.URL {
		t.Fatalf("登录后 baseURL 应从 baseurl 覆盖：%q", w.currentBaseURL())
	}
	mu.Lock()
	na := noAuthOn
	mu.Unlock()
	if na == 0 {
		t.Fatal("登录前的取码请求不该带 Authorization")
	}

	if _, err := os.Stat(w.credentialPath()); err != nil {
		t.Fatalf("登录凭证没落盘：%v", err)
	}
	cr, err := w.loadCredential()
	if err != nil || cr.BotToken != "tk-login" || cr.BotID != "bot-1" {
		t.Fatalf("落盘凭证不对：%+v err=%v", cr, err)
	}

	// 新实例应从本地凭证恢复
	w2 := NewWeChat(config.ChannelConfig{ID: "wx", Domain: srv.URL}, nil)
	if w2.currentToken() != "tk-login" {
		t.Fatalf("新实例没恢复 token：%q", w2.currentToken())
	}
	if w2.currentBaseURL() != srv.URL {
		t.Fatalf("新实例没恢复 baseURL：%q", w2.currentBaseURL())
	}
}

// TestWeChatPollAndSend 长轮询收到一条用户消息并回发：校验 session/正文/context_token 与出站体。
func TestWeChatPollAndSend(t *testing.T) {
	isolateUserConfig(t)

	var (
		mu      sync.Mutex
		sent    map[string]any
		updates int
		auth    string
		uinSet  bool
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/ilink/bot/getupdates", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bi, _ := body["base_info"].(map[string]any)
		if bi["channel_version"] != weChatChannelVersion {
			http.Error(w, "base_info", http.StatusBadRequest)
			return
		}
		mu.Lock()
		updates++
		n := updates
		auth = r.Header.Get("Authorization")
		uinSet = r.Header.Get("X-WECHAT-UIN") != ""
		mu.Unlock()
		if n == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ret": 0, "get_updates_buf": "cur-1",
				"msgs": []any{map[string]any{
					"from_user_id": "u1", "to_user_id": "bot", "context_token": "ct1",
					"message_type": weChatMsgTypeUser, "msg_id": "m1",
					"item_list": []any{map[string]any{"type": weChatItemText,
						"text_item": map[string]any{"text": "你好白泽"}}},
				}},
			})
			return
		}
		// 模拟长轮询的 hold，避免测试里忙轮询
		select {
		case <-r.Context().Done():
		case <-time.After(150 * time.Millisecond):
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ret": -1, "get_updates_buf": "cur-1"})
	})
	mux.HandleFunc("/ilink/bot/sendmessage", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = body
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ret": 0, "errcode": 0})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	w := NewWeChat(config.ChannelConfig{ID: "wx", Token: "tk", Domain: srv.URL}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msgCh := make(chan Message, 4)
	go w.Poll(ctx, func(m Message) { msgCh <- m })

	var msg Message
	select {
	case msg = <-msgCh:
	case <-time.After(5 * time.Second):
		t.Fatal("没收到微信入站消息")
	}
	if msg.Session != "wechat:u1" {
		t.Fatalf("会话键不对：%s", msg.Session)
	}
	if msg.Text != "你好白泽" {
		t.Fatalf("正文不对：%s", msg.Text)
	}
	if msg.Sender != "u1" {
		t.Fatalf("发送者不对：%s", msg.Sender)
	}
	if got, _ := msg.Meta["context_token"].(string); got != "ct1" {
		t.Fatalf("没带上 context_token：%v", msg.Meta["context_token"])
	}

	if err := w.Send(ctx, msg, "收到"); err != nil {
		t.Fatalf("回发失败：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !uinSet {
		t.Fatal("请求没带 X-WECHAT-UIN")
	}
	if auth != "Bearer tk" {
		t.Fatalf("Authorization 不对：%q", auth)
	}
	if sent == nil {
		t.Fatal("sendmessage 没收到")
	}
	m, _ := sent["msg"].(map[string]any)
	if m["to_user_id"] != "u1" {
		t.Fatalf("收件人不对：%v", m["to_user_id"])
	}
	if m["context_token"] != "ct1" {
		t.Fatalf("回复没带上 context_token：%v", m["context_token"])
	}
	if m["message_type"] != float64(weChatMsgTypeBot) {
		t.Fatalf("message_type 应为 2：%v", m["message_type"])
	}
	if m["message_state"] != float64(weChatMsgStateFinish) {
		t.Fatalf("message_state 应为 2：%v", m["message_state"])
	}
	items, _ := m["item_list"].([]any)
	if len(items) != 1 {
		t.Fatalf("item_list 长度不对：%v", m["item_list"])
	}
	first, _ := items[0].(map[string]any)
	ti, _ := first["text_item"].(map[string]any)
	if ti["text"] != "收到" {
		t.Fatalf("回复正文不对：%v", ti["text"])
	}
}

// TestWeChatParseInbound 单测解析：单聊 / 群聊 / 去重 / 非用户消息 / 空文本 / 非文本项。
func TestWeChatParseInbound(t *testing.T) {
	w := NewWeChat(config.ChannelConfig{ID: "wx"}, nil)
	seen := map[string]struct{}{}

	m1 := weChatMessage{FromUserID: "u1", ContextToken: "ct1",
		MessageType: weChatMsgTypeUser, ItemList: []weChatItem{weChatTextItem("  hi  ")}}
	msg, ok := w.parseInbound(m1, seen)
	if !ok {
		t.Fatal("用户文本消息应当产出")
	}
	if msg.Session != "wechat:u1" || msg.Text != "hi" || msg.Sender != "u1" {
		t.Fatalf("解析不对：%+v", msg)
	}
	if _, ok := w.parseInbound(m1, seen); ok {
		t.Fatal("同一 context_token 不该重复产出")
	}

	m2 := weChatMessage{FromUserID: "u2", ContextToken: "ct2", GroupID: "g1",
		MessageType: weChatMsgTypeUser, ItemList: []weChatItem{weChatTextItem("群消息")}}
	msg2, ok := w.parseInbound(m2, seen)
	if !ok || msg2.Session != "wechat:group:g1" {
		t.Fatalf("群聊会话键不对：%+v", msg2)
	}

	if _, ok := w.parseInbound(weChatMessage{MessageType: weChatMsgTypeBot, ContextToken: "ct3",
		ItemList: []weChatItem{weChatTextItem("bot")}}, seen); ok {
		t.Fatal("message_type=2（机器人自己）不该产出")
	}
	if _, ok := w.parseInbound(weChatMessage{MessageType: weChatMsgTypeUser, ContextToken: "ct4"}, seen); ok {
		t.Fatal("空文本不该产出")
	}
	if _, ok := w.parseInbound(weChatMessage{MessageType: weChatMsgTypeUser, ContextToken: "ct5",
		ItemList: []weChatItem{{Type: 2}}}, seen); ok {
		t.Fatal("非文本项不该产出")
	}
}

// TestWeChatSendErrors 未登录 / 缺收件人 / 缺 context_token 都要明确报错。
func TestWeChatSendErrors(t *testing.T) {
	ctx := context.Background()
	w := NewWeChat(config.ChannelConfig{ID: "wx"}, nil)
	w.token = "" // 明确未登录（防止本地恰好有凭证）

	if err := w.Send(ctx, Message{Channel: "wx"}, "hi"); err == nil || !strings.Contains(err.Error(), "未登录") {
		t.Fatalf("未登录应明确报错，得到：%v", err)
	}

	w2 := NewWeChat(config.ChannelConfig{ID: "wx", Token: "tk"}, nil)
	if err := w2.Send(ctx, Message{Channel: "wx"}, "hi"); err == nil || !strings.Contains(err.Error(), "from_user_id") {
		t.Fatalf("缺收件人应明确报错，得到：%v", err)
	}
	err := w2.Send(ctx, Message{Channel: "wx", Meta: map[string]any{"from_user_id": "u1"}}, "hi")
	if err == nil || !strings.Contains(err.Error(), "context_token") {
		t.Fatalf("缺 context_token 应明确报错，得到：%v", err)
	}
}

// TestWeChatGetUpdatesRetHandling ret=-1 正常、ret=-14 会话过期、发信 ret=-2 明确报错。
func TestWeChatGetUpdatesRetHandling(t *testing.T) {
	var ret int
	mux := http.NewServeMux()
	mux.HandleFunc("/ilink/bot/getupdates", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ret": ret})
	})
	mux.HandleFunc("/ilink/bot/sendmessage", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ret": -2})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	w := NewWeChat(config.ChannelConfig{ID: "wx", Token: "tk", Domain: srv.URL}, nil)
	ctx := context.Background()

	ret = -1
	msgs, cur, err := w.getUpdates(ctx, "c0")
	if err != nil || len(msgs) != 0 || cur != "c0" {
		t.Fatalf("ret=-1 应视为正常超时：msgs=%v cur=%q err=%v", msgs, cur, err)
	}

	ret = -14
	if _, _, err := w.getUpdates(ctx, "c0"); !errors.Is(err, errWeChatSessionExpired) {
		t.Fatalf("ret=-14 应为会话过期，得到：%v", err)
	}

	ret = -2
	if _, _, err := w.getUpdates(ctx, "c0"); err == nil || errors.Is(err, errWeChatSessionExpired) {
		t.Fatalf("ret=-2 应是普通错误，得到：%v", err)
	}

	err = w.Send(ctx, Message{Channel: "wx",
		Meta: map[string]any{"from_user_id": "u1", "context_token": "ct1"}}, "hi")
	if err == nil || !strings.Contains(err.Error(), "context_token") {
		t.Fatalf("发信 ret=-2 应提示 context_token 失效，得到：%v", err)
	}
}
