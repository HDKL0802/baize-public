package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"baize/internal/config"
)

func TestDingTalkStreamInboundAndSend(t *testing.T) {
	var (
		mu      sync.Mutex
		gotHook map[string]any
		ackN    int
	)

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	mux := http.NewServeMux()
	var srvURL string

	mux.HandleFunc("/v1.0/gateway/connections/open", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["clientId"] != "appid" || req["clientSecret"] != "secret" {
			http.Error(w, "bad creds", http.StatusUnauthorized)
			return
		}
		wsURL := "ws" + strings.TrimPrefix(srvURL, "http") + "/ws"
		_ = json.NewEncoder(w).Encode(map[string]any{"endpoint": wsURL, "ticket": "tk-test"})
	})

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ticket") != "tk-test" {
			http.Error(w, "bad ticket", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		data := `{"conversationId":"cid-1","conversationType":"2","senderId":"u1",` +
			`"senderStaffId":"staff1","senderNick":"阿泽","msgId":"m1","msgtype":"text",` +
			`"text":{"content":"你好白泽"},"sessionWebhook":"` + srvURL + `/hook","robotCode":"bot1"}`
		_ = conn.WriteJSON(map[string]any{
			"specVersion": "1.0", "type": "CALLBACK",
			"headers": map[string]string{"topic": dingTalkBotTopic, "messageId": "m1", "contentType": "application/json"},
			"data":    data,
		})
		if _, b, err := conn.ReadMessage(); err == nil {
			var ack map[string]any
			_ = json.Unmarshal(b, &ack)
			hdrs, _ := ack["headers"].(map[string]any)
			if hdrs["messageId"] == "m1" {
				mu.Lock()
				ackN++
				mu.Unlock()
			}
		}

		// 系统 ping：期待回一个 ACK（pong）
		_ = conn.WriteJSON(map[string]any{
			"specVersion": "1.0", "type": "SYSTEM",
			"headers": map[string]string{"topic": "ping", "messageId": "sys1"},
			"data":    "",
		})
		if _, b, err := conn.ReadMessage(); err == nil {
			var ack map[string]any
			_ = json.Unmarshal(b, &ack)
			hdrs, _ := ack["headers"].(map[string]any)
			if hdrs["messageId"] == "sys1" {
				mu.Lock()
				ackN++
				mu.Unlock()
			}
		}

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	hookCh := make(chan struct{}, 1)
	mux.HandleFunc("/hook", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		gotHook = body
		mu.Unlock()
		select {
		case hookCh <- struct{}{}:
		default:
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	dt := NewDingTalk(config.ChannelConfig{
		ID: "ding", Kind: "dingtalk", Enabled: true,
		AppID: "appid", AppSecret: "secret",
		Domain: srv.URL + "/v1.0/gateway/connections/open",
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msgCh := make(chan Message, 4)
	go dt.Poll(ctx, func(m Message) { msgCh <- m })

	var msg Message
	select {
	case msg = <-msgCh:
	case <-time.After(5 * time.Second):
		t.Fatal("没收到钉钉入站消息")
	}
	if msg.Session != "dingtalk:group:cid-1" {
		t.Fatalf("群聊会话键不对：%s", msg.Session)
	}
	if msg.Text != "你好白泽" {
		t.Fatalf("正文不对：%s", msg.Text)
	}
	if msg.Sender != "阿泽" {
		t.Fatalf("发送者不对：%s", msg.Sender)
	}
	if msg.Meta["session_webhook"] != srv.URL+"/hook" {
		t.Fatalf("没带上 sessionWebhook：%v", msg.Meta["session_webhook"])
	}

	// 等 ACK 都回来（回调帧 + 系统 ping 各一次）
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := ackN
		mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ACK 没收全（收到 %d 条）", n)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := dt.Send(ctx, msg, "收到"); err != nil {
		t.Fatalf("回发失败：%v", err)
	}
	select {
	case <-hookCh:
	case <-time.After(3 * time.Second):
		t.Fatal("sessionWebhook 没收到回发")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotHook["msgtype"] != "text" {
		t.Fatalf("回发类型不对：%v", gotHook["msgtype"])
	}
	text, _ := gotHook["text"].(map[string]any)
	if cnt, _ := text["content"].(string); cnt != "收到" {
		t.Fatalf("回发正文不对：%v", text)
	}
}

func TestDingTalkParseInbound(t *testing.T) {
	dt := NewDingTalk(config.ChannelConfig{ID: "d"}, nil)

	msg, ok := dt.parseInbound(`{"conversationType":"1","senderStaffId":"s1","senderNick":"甲",` +
		`"text":{"content":"hi"},"sessionWebhook":"http://x/hook"}`)
	if !ok {
		t.Fatal("单聊消息应当产出")
	}
	if msg.Session != "dingtalk:single:s1" {
		t.Fatalf("单聊会话键不对：%s", msg.Session)
	}
	if msg.Text != "hi" || msg.Sender != "甲" {
		t.Fatalf("解析不对：%+v", msg)
	}
	if v, _ := msg.Meta["is_group"].(bool); v {
		t.Fatal("单聊不该标成群")
	}

	if _, ok := dt.parseInbound(`{"conversationType":"2","text":{"content":"   "}}`); ok {
		t.Fatal("空文本不该产出消息")
	}
	if _, ok := dt.parseInbound(`not json`); ok {
		t.Fatal("非 JSON 不该产出消息")
	}

	// 没配凭证时不连（Poll 直接返回，不 panic）
	empty := NewDingTalk(config.ChannelConfig{ID: "d2", Kind: "dingtalk"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	empty.Poll(ctx, func(Message) {})
}

func TestDingTalkSendWithoutWebhook(t *testing.T) {
	dt := NewDingTalk(config.ChannelConfig{ID: "d", BotPrefix: "[白泽] "}, nil)
	err := dt.Send(context.Background(), Message{Channel: "d"}, "hi")
	if err == nil || !strings.Contains(err.Error(), "sessionWebhook") {
		t.Fatalf("没有 sessionWebhook 时应当明确报错，得到：%v", err)
	}
}
