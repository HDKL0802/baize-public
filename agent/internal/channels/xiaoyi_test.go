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

// TestXiaoYiSignature 校验 AK/SK 签名算法：Base64(HMAC-SHA256(SK, 毫秒时间戳))
func TestXiaoYiSignature(t *testing.T) {
	got := xiaoYiSign("secret", "1700000000000")
	want := "T+LOEv1r7F4GSLI7+/8Vi6X8GLOiFLTCBzFGcUSKleE="
	if got != want {
		t.Fatalf("签名算法不对：得到 %q，期望 %q", got, want)
	}
	xy := NewXiaoYi(config.ChannelConfig{AppID: "AK", AppSecret: "secret", AgentID: "aid"}, nil)
	h := xy.authHeaders()
	if h.Get("x-access-key") != "AK" {
		t.Fatalf("x-access-key 不对：%q", h.Get("x-access-key"))
	}
	if h.Get("x-agent-id") != "aid" {
		t.Fatalf("x-agent-id 不对：%q", h.Get("x-agent-id"))
	}
	if h.Get("x-ts") == "" {
		t.Fatal("缺 x-ts")
	}
	if h.Get("x-sign") != xiaoYiSign("secret", h.Get("x-ts")) {
		t.Fatalf("x-sign 与同一时间戳算出的签名不一致：%q", h.Get("x-sign"))
	}
}

// TestXiaoYiChunkText 校验按「字符数」分片（中文不能按字节切坏）
func TestXiaoYiChunkText(t *testing.T) {
	small := "短消息"
	if c := xiaoYiChunkText(small); len(c) != 1 || c[0] != small {
		t.Fatalf("短文本不该分片：%#v", c)
	}
	// 4000 个「中」（每个 3 字节）——按字符数应切两片，且不破坏字符
	big := strings.Repeat("中", xiaoYiTextChunkLimit+10)
	parts := xiaoYiChunkText(big)
	if len(parts) != 2 {
		t.Fatalf("应当切成 2 片，得到 %d 片", len(parts))
	}
	if len([]rune(parts[0])) != xiaoYiTextChunkLimit || len([]rune(parts[1])) != 10 {
		t.Fatalf("分片长度不对：%d / %d", len([]rune(parts[0])), len([]rune(parts[1])))
	}
	if parts[0]+parts[1] != big {
		t.Fatal("拼接后与原文本不一致（切坏了字符）")
	}
	// 两行各 3000 字符：应在换行处断开
	two := strings.Repeat("a", 3000) + "\n" + strings.Repeat("b", 3000)
	parts = xiaoYiChunkText(two)
	if len(parts) != 2 || len([]rune(parts[0])) != 3000 || len([]rune(parts[1])) != 3000 {
		t.Fatalf("按换行分片不对：%#v", parts)
	}
}

// TestXiaoYiHandleInbound 校验入站帧解析（不涉及写连接）
func TestXiaoYiHandleInbound(t *testing.T) {
	xy := NewXiaoYi(config.ChannelConfig{ID: "xy", AppID: "AK", AppSecret: "SK", AgentID: "agent-1"}, nil)
	wc := &xyConn{name: "primary"}
	var got []Message
	onEvent := func(m Message) { got = append(got, m) }

	in := `{"method":"message/stream","id":"req-1","agentId":"agent-1","params":{` +
		`"sessionId":"s-1","id":"t-1","message":{"parts":[` +
		`{"kind":"text","text":"hi"},{"kind":"text","text":"there"},` +
		`{"kind":"file","file":{"uri":"http://x/f","name":"f.txt"}}]}}}`
	xy.handleFrame(wc, []byte(in), onEvent)
	if len(got) != 1 {
		t.Fatalf("应当产出 1 条消息，得到 %d", len(got))
	}
	if got[0].Text != "hi there" {
		t.Fatalf("文本拼装不对：%q", got[0].Text)
	}
	if got[0].Session != "xiaoyi:s-1" {
		t.Fatalf("会话键不对：%q", got[0].Session)
	}
	if got[0].Sender != "s-1" {
		t.Fatalf("发送者不对：%q", got[0].Sender)
	}
	if got[0].Meta["task_id"] != "t-1" || got[0].Meta["message_id"] != "req-1" {
		t.Fatalf("Meta 不对：%#v", got[0].Meta)
	}
	if xy.taskFor("s-1") != "t-1" {
		t.Fatalf("task 映射没记下：%q", xy.taskFor("s-1"))
	}

	// agentId 不匹配：忽略
	mismatch := `{"method":"message/stream","agentId":"other","params":{"sessionId":"s-2",` +
		`"id":"t-2","message":{"parts":[{"kind":"text","text":"x"}]}}}`
	xy.handleFrame(wc, []byte(mismatch), onEvent)
	// 空正文：忽略
	empty := `{"method":"message/stream","agentId":"agent-1","params":{"sessionId":"s-3",` +
		`"id":"t-3","message":{"parts":[]}}}`
	xy.handleFrame(wc, []byte(empty), onEvent)
	// 非消息方法：忽略
	xy.handleFrame(wc, []byte(`{"method":"something/else"}`), onEvent)
	// 非法 JSON：忽略
	xy.handleFrame(wc, []byte(`not json`), onEvent)

	if len(got) != 1 {
		t.Fatalf("以上都应被忽略，仍应只有 1 条消息，得到 %d", len(got))
	}
}

// TestXiaoYiStreamInboundAndSend 起一个假小艺服务端，走完整的握手 / 入站 / 出站
func TestXiaoYiStreamInboundAndSend(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	var (
		hdrMu  sync.Mutex
		gotHdr http.Header
	)
	initCh := make(chan map[string]any, 1)
	textCh := make(chan string, 4)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		hdrMu.Lock()
		gotHdr = r.Header.Clone()
		hdrMu.Unlock()

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// 握手后客户端应发 init
		var initMsg map[string]any
		if err := conn.ReadJSON(&initMsg); err != nil {
			return
		}
		select {
		case initCh <- initMsg:
		default:
		}

		// 推一条 A2A 入站消息
		_ = conn.WriteJSON(map[string]any{
			"method":  "message/stream",
			"id":      "req-1",
			"agentId": "agent-1",
			"params": map[string]any{
				"sessionId": "sess-1",
				"id":        "task-1",
				"message": map[string]any{
					"parts": []map[string]any{
						{"kind": "text", "text": "你好小艺"},
						{"kind": "file", "file": map[string]any{"uri": "http://x/f", "name": "f"}},
					},
				},
			},
		})

		// 读客户端回发的帧
		for {
			var m map[string]any
			if err := conn.ReadJSON(&m); err != nil {
				return
			}
			if m["msgType"] != "agent_response" {
				continue
			}
			detail, _ := m["msgDetail"].(string)
			var inner map[string]any
			if err := json.Unmarshal([]byte(detail), &inner); err != nil {
				continue
			}
			res, _ := inner["result"].(map[string]any)
			if res == nil || res["kind"] != "artifact-update" {
				continue
			}
			art, _ := res["artifact"].(map[string]any)
			parts, _ := art["parts"].([]any)
			if len(parts) == 0 {
				continue
			}
			p0, _ := parts[0].(map[string]any)
			if txt, _ := p0["text"].(string); txt != "" {
				select {
				case textCh <- txt:
				default:
				}
			}
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	xy := NewXiaoYi(config.ChannelConfig{
		ID: "xy", Kind: "xiaoyi", Enabled: true,
		AppID: "AK", AppSecret: "SK", AgentID: "agent-1", Domain: wsURL,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msgCh := make(chan Message, 4)
	go xy.Poll(ctx, func(m Message) { msgCh <- m })

	// 握手：init 帧
	select {
	case initMsg := <-initCh:
		if initMsg["msgType"] != "clawd_bot_init" {
			t.Fatalf("init 类型不对：%v", initMsg["msgType"])
		}
		if initMsg["agentId"] != "agent-1" {
			t.Fatalf("init agentId 不对：%v", initMsg["agentId"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("没收到小艺 init 帧")
	}

	// 鉴权头
	hdrMu.Lock()
	h := gotHdr
	hdrMu.Unlock()
	if h.Get("x-access-key") != "AK" {
		t.Fatalf("x-access-key 不对：%q", h.Get("x-access-key"))
	}
	if h.Get("x-agent-id") != "agent-1" {
		t.Fatalf("x-agent-id 不对：%q", h.Get("x-agent-id"))
	}
	if h.Get("x-ts") == "" || h.Get("x-sign") != xiaoYiSign("SK", h.Get("x-ts")) {
		t.Fatalf("签名头不对：ts=%q sign=%q", h.Get("x-ts"), h.Get("x-sign"))
	}

	// 入站消息
	var msg Message
	select {
	case msg = <-msgCh:
	case <-time.After(5 * time.Second):
		t.Fatal("没收到小艺入站消息")
	}
	if msg.Session != "xiaoyi:sess-1" {
		t.Fatalf("会话键不对：%s", msg.Session)
	}
	if msg.Text != "你好小艺" {
		t.Fatalf("正文不对：%s", msg.Text)
	}
	if msg.Meta["task_id"] != "task-1" {
		t.Fatalf("没带上 task_id：%v", msg.Meta["task_id"])
	}

	// 回发
	if err := xy.Send(ctx, msg, "收到"); err != nil {
		t.Fatalf("回发失败：%v", err)
	}
	select {
	case txt := <-textCh:
		if txt != "收到" {
			t.Fatalf("回发文本不对：%q", txt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("小艺没收到回发文本")
	}
}

// TestXiaoYiMissingCreds 没配凭据：Send 明确报错，Poll 直接返回（不 panic）
func TestXiaoYiMissingCreds(t *testing.T) {
	xy := NewXiaoYi(config.ChannelConfig{ID: "xy"}, nil)
	err := xy.Send(context.Background(), Message{Channel: "xy", Session: "xiaoyi:s-1"}, "hi")
	if err == nil || !strings.Contains(err.Error(), "AK") {
		t.Fatalf("没配凭据时 Send 应当明确报错，得到：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	xy.Poll(ctx, func(Message) {}) // 直接返回，不 panic
}

// TestXiaoYiSendNeedsTask 有连接前必须先有 task_id：缺 task_id 明确报错
func TestXiaoYiSendNeedsTask(t *testing.T) {
	xy := NewXiaoYi(config.ChannelConfig{ID: "xy", AppID: "AK", AppSecret: "SK", AgentID: "aid"}, nil)
	err := xy.Send(context.Background(), Message{Channel: "xy", Session: "xiaoyi:s-1"}, "hi")
	if err == nil || !strings.Contains(err.Error(), "task_id") {
		t.Fatalf("缺 task_id 时应当明确报错，得到：%v", err)
	}
}

// TestXiaoYiSendNoConn 有 task_id 但没连上服务端：明确报错
func TestXiaoYiSendNoConn(t *testing.T) {
	xy := NewXiaoYi(config.ChannelConfig{ID: "xy", AppID: "AK", AppSecret: "SK", AgentID: "aid"}, nil)
	err := xy.Send(context.Background(),
		Message{Channel: "xy", Session: "xiaoyi:s-1", Meta: map[string]any{"task_id": "t-1"}}, "hi")
	if err == nil || !strings.Contains(err.Error(), "连接") {
		t.Fatalf("没连接时应当明确报错，得到：%v", err)
	}
}
