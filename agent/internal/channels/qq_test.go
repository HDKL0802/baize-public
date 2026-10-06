package channels

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"baize/internal/config"
)

/* 官方 QQ 机器人频道单元测试：网关长连接的 Identify / 事件解析、出站 OpenAPI 发消息、
   会话键、去重与错误路径。 */

func TestQQGatewayInboundAndSend(t *testing.T) {
	var (
		mu      sync.Mutex
		gotSend map[string]any
		gotAuth string
		gotPath string
	)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	mux := http.NewServeMux()
	var srvURL string

	mux.HandleFunc("/app/getAppAccessToken", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["appId"] != "app1" || body["clientSecret"] != "sec1" {
			http.Error(w, "bad creds", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"tk","expires_in":"7200"}`))
	})

	mux.HandleFunc("/gateway", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "QQBot tk" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		wsURL := "ws" + strings.TrimPrefix(srvURL, "http") + "/ws"
		_ = json.NewEncoder(w).Encode(map[string]any{"url": wsURL})
	})

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// 先推 Hello（客户端据此发 Identify 并起心跳）
		_ = conn.WriteJSON(map[string]any{"op": qqOpHello, "d": map[string]any{"heartbeat_interval": 45000}})
		// 读 Identify 并校验
		_, b, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var idt struct {
			Op int `json:"op"`
			D  struct {
				Token   string `json:"token"`
				Intents int    `json:"intents"`
				Shard   []int  `json:"shard"`
			} `json:"d"`
		}
		_ = json.Unmarshal(b, &idt)
		if idt.Op != qqOpIdentify || idt.D.Token != "QQBot tk" || idt.D.Intents == 0 || len(idt.D.Shard) != 2 {
			return // Identify 不对：直接断开，测试会在超时处失败
		}
		// 推一条群 @ 消息事件
		_ = conn.WriteJSON(map[string]any{
			"op": qqOpDispatch, "s": 2, "t": "GROUP_AT_MESSAGE_CREATE",
			"d": map[string]any{
				"id": "m1", "content": "你好白泽",
				"author":       map[string]any{"member_openid": "mo1", "id": "u1"},
				"group_openid": "g1",
			},
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	mux.HandleFunc("/v2/groups/g1/messages", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_ = json.Unmarshal(b, &gotSend)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"srv-1","timestamp":"2026-07-21T10:30:00+08:00"}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	q := NewQQ(config.ChannelConfig{
		ID: "qq", Kind: "qq", Enabled: true,
		AppID: "app1", AppSecret: "sec1",
		Domain: srv.URL, BotPrefix: "[泽] ",
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msgCh := make(chan Message, 4)
	go q.Poll(ctx, func(m Message) { msgCh <- m })

	var msg Message
	select {
	case msg = <-msgCh:
	case <-time.After(5 * time.Second):
		t.Fatal("没收到 QQ 入站消息")
	}
	if msg.Session != "qq:group:g1" {
		t.Fatalf("群聊会话键不对：%s", msg.Session)
	}
	if msg.Text != "你好白泽" {
		t.Fatalf("正文不对：%s", msg.Text)
	}
	if msg.Meta["message_type"] != "group" || msg.Meta["group_openid"] != "g1" {
		t.Fatalf("Meta 不对：%+v", msg.Meta)
	}
	if v, _ := msg.Meta["is_group"].(bool); !v {
		t.Fatal("群聊应当标成群")
	}

	if err := q.Send(ctx, msg, "收到"); err != nil {
		t.Fatalf("回发失败：%v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		sent, auth, path := gotSend, gotAuth, gotPath
		mu.Unlock()
		if sent != nil {
			if auth != "QQBot tk" {
				t.Fatalf("Authorization 不对：%q", auth)
			}
			if path != "/v2/groups/g1/messages" {
				t.Fatalf("回发路径不对：%q", path)
			}
			if sent["msg_type"] != float64(0) {
				t.Fatalf("msg_type 应为 0：%v", sent["msg_type"])
			}
			if _, ok := sent["msg_seq"]; !ok {
				t.Fatalf("群聊回发应带 msg_seq：%+v", sent)
			}
			if sent["msg_id"] != "m1" {
				t.Fatalf("msg_id 不对：%v", sent["msg_id"])
			}
			if c, _ := sent["content"].(string); c != "[泽] 收到" {
				t.Fatalf("回发正文不对：%q", c)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("QQ 回发没到")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestQQParseEvent(t *testing.T) {
	q := NewQQ(config.ChannelConfig{ID: "qq"}, nil)

	// 群 @：member_openid 作发送者，session 按群
	msg, ok := q.parseEvent("GROUP_AT_MESSAGE_CREATE", []byte(
		`{"id":"m1","content":"你好","author":{"member_openid":"mo1","id":"u1"},"group_openid":"g1"}`))
	if !ok {
		t.Fatal("群消息应当产出")
	}
	if msg.Session != "qq:group:g1" || msg.Text != "你好" || msg.Sender != "mo1" {
		t.Fatalf("群消息解析不对：%+v", msg)
	}
	if msg.Meta["group_openid"] != "g1" {
		t.Fatalf("group_openid 不对：%+v", msg.Meta)
	}

	// 单聊：user_openid 作发送者，session 按人
	msg, ok = q.parseEvent("C2C_MESSAGE_CREATE", []byte(
		`{"id":"m2","content":"hi","author":{"user_openid":"uo1","id":"u2"}}`))
	if !ok || msg.Session != "qq:c2c:uo1" {
		t.Fatalf("单聊解析不对：%+v ok=%v", msg, ok)
	}
	if v, _ := msg.Meta["is_group"].(bool); v {
		t.Fatal("单聊不该标成群")
	}

	// 频道 @：取 username 当显示名，session 按子频道
	msg, ok = q.parseEvent("AT_MESSAGE_CREATE", []byte(
		`{"id":"m3","content":"hey","author":{"id":"a1","username":"小明"},"channel_id":"c1","guild_id":"gd1"}`))
	if !ok || msg.Session != "qq:guild:c1" || msg.Sender != "小明" {
		t.Fatalf("频道消息解析不对：%+v ok=%v", msg, ok)
	}
	if msg.Meta["channel_id"] != "c1" || msg.Meta["guild_id"] != "gd1" {
		t.Fatalf("频道 meta 不对：%+v", msg.Meta)
	}

	// 频道私信：session 按频道
	msg, ok = q.parseEvent("DIRECT_MESSAGE_CREATE", []byte(
		`{"id":"m4","content":"dm","author":{"id":"a2","username":"甲"},"channel_id":"c2","guild_id":"gd2"}`))
	if !ok || msg.Session != "qq:dm:gd2" {
		t.Fatalf("频道私信解析不对：%+v ok=%v", msg, ok)
	}

	// 空文本 / 非 JSON / 未知事件类型 / 无发送者 → 不产出
	if _, ok := q.parseEvent("C2C_MESSAGE_CREATE", []byte(`{"id":"m5","content":"   ","author":{"user_openid":"uo9"}}`)); ok {
		t.Fatal("空文本不该产出")
	}
	if _, ok := q.parseEvent("GROUP_AT_MESSAGE_CREATE", []byte(`not json`)); ok {
		t.Fatal("非 JSON 不该产出")
	}
	if _, ok := q.parseEvent("READY", []byte(`{}`)); ok {
		t.Fatal("非消息事件不该产出")
	}
	if _, ok := q.parseEvent("C2C_MESSAGE_CREATE", []byte(`{"id":"m6","content":"hi","author":{}}`)); ok {
		t.Fatal("无发送者不该产出")
	}

	// 去重：同一 msg_id 第二次不再产出（网关恢复会话会重投）
	dup := []byte(`{"id":"dup1","content":"again","author":{"user_openid":"uo1"}}`)
	if _, ok := q.parseEvent("C2C_MESSAGE_CREATE", dup); !ok {
		t.Fatal("首次事件应当产出")
	}
	if _, ok := q.parseEvent("C2C_MESSAGE_CREATE", dup); ok {
		t.Fatal("重复事件应当被去重")
	}

	// 前缀过滤：机器人自己带前缀回显的内容不回环
	qp := NewQQ(config.ChannelConfig{ID: "qq", BotPrefix: "[泽] "}, nil)
	if _, ok := qp.parseEvent("C2C_MESSAGE_CREATE", []byte(
		`{"id":"m7","content":"[泽] 我在","author":{"user_openid":"uo1"}}`)); ok {
		t.Fatal("带前缀的回显应当被过滤")
	}
}

func TestQQSendErrors(t *testing.T) {
	// 没配凭证 → 明确报错
	q := NewQQ(config.ChannelConfig{ID: "qq"}, nil)
	err := q.Send(context.Background(), Message{Meta: map[string]any{"message_type": "c2c", "sender_id": "uo"}}, "x")
	if err == nil || !strings.Contains(err.Error(), "appId") {
		t.Fatalf("没配凭证应明确报错，得到：%v", err)
	}

	// 有凭证但缺回发目标 → 明确报错
	q2 := NewQQ(config.ChannelConfig{ID: "qq", AppID: "a", AppSecret: "b"}, nil)
	err = q2.Send(context.Background(), Message{Meta: map[string]any{"message_type": "c2c"}}, "x")
	if err == nil || !strings.Contains(err.Error(), "回发的地点") {
		t.Fatalf("缺目标应明确报错，得到：%v", err)
	}
	if err = q2.Send(context.Background(), Message{Meta: map[string]any{"message_type": "group"}}, "x"); err == nil {
		t.Fatal("群聊缺 group_openid 应报错")
	}
}

func TestQQDefaults(t *testing.T) {
	q := NewQQ(config.ChannelConfig{ID: "qq"}, nil)
	if q.apiBase != qqDefaultAPIBase {
		t.Fatalf("默认 OpenAPI 根地址不对：%s", q.apiBase)
	}
	if q.tokenURL != qqDefaultTokenURL {
		t.Fatalf("默认令牌地址不对：%s", q.tokenURL)
	}

	// Domain 覆盖（沙箱 / 测试）：令牌端点跟随同一根地址
	q2 := NewQQ(config.ChannelConfig{ID: "qq", Domain: "https://sandbox.api.sgroup.qq.com/"}, nil)
	if q2.apiBase != "https://sandbox.api.sgroup.qq.com" {
		t.Fatalf("Domain 覆盖后根地址不对：%s", q2.apiBase)
	}
	if q2.tokenURL != "https://sandbox.api.sgroup.qq.com/app/getAppAccessToken" {
		t.Fatalf("Domain 覆盖后令牌地址不对：%s", q2.tokenURL)
	}
}

func TestQQStripURLs(t *testing.T) {
	got := qqStripURLs("见 https://example.com/a?b=1 与 www.demo.cn 谢谢")
	if got != "见 [链接已省略] 与 [链接已省略] 谢谢" {
		t.Fatalf("剥链接不对：%q", got)
	}
	if s := qqStripURLs("没有链接"); s != "没有链接" {
		t.Fatalf("无链接不该改动：%q", s)
	}
}

func TestQQPollWithoutCredentials(t *testing.T) {
	// 没配凭证时 Poll 直接返回（不 panic、不发请求）
	q := NewQQ(config.ChannelConfig{ID: "qq", Kind: "qq"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q.Poll(ctx, func(Message) {})
}
