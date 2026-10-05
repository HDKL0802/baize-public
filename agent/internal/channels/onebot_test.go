package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"baize/internal/config"
)

/* OneBot V11 反向 WebSocket 频道测试。
   用真连一条 WS 做集成：实现端发消息事件 → 解析成 Message；Send → 从同一条连接收到 send_*_msg。 */

func TestOneBotServeAndSend(t *testing.T) {
	ob := NewOneBot(config.ChannelConfig{ID: "qq", BotPrefix: "[白泽] "})

	got := make(chan Message, 1)
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ob.Serve(c, func(m Message) { got <- m })
	}))
	defer srv.Close()

	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer c.Close()

	// 实现端推一条群消息事件
	ev := `{"post_type":"message","message_type":"group","group_id":42,"user_id":7,` +
		`"raw_message":"你好","sender":{"user_id":7,"nickname":"小明"}}`
	if err := c.WriteMessage(websocket.TextMessage, []byte(ev)); err != nil {
		t.Fatalf("发事件失败：%v", err)
	}

	var m Message
	select {
	case m = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("没收到 OneBot 消息事件")
	}
	if m.Channel != "qq" || m.Text != "你好" || m.Session != "onebot:group:42" || m.Sender != "小明" {
		t.Fatalf("事件解析不对：%+v", m)
	}

	// 回发：应在这条连接上收到 send_group_msg
	if err := ob.Send(context.Background(), m, "收到"); err != nil {
		t.Fatalf("回发失败：%v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("没收到回发：%v", err)
	}
	var action struct {
		Action string         `json:"action"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(data, &action); err != nil {
		t.Fatalf("回发不是 JSON：%s", data)
	}
	if action.Action != "send_group_msg" || action.Params["group_id"] != float64(42) {
		t.Fatalf("回发动作不对：%s", data)
	}
	if action.Params["message"] != "[白泽] 收到" {
		t.Fatalf("回发内容/前缀不对：%s", data)
	}
}

func TestOneBotSessionAndSender(t *testing.T) {
	ob := NewOneBot(config.ChannelConfig{ID: "qq"})
	if ob.session(oneBotEvent{MessageType: "group", GroupID: 42}) != "onebot:group:42" {
		t.Fatal("群会话键不对")
	}
	if ob.session(oneBotEvent{MessageType: "private", UserID: 7}) != "onebot:private:7" {
		t.Fatal("私聊会话键不对")
	}
	e := oneBotEvent{}
	e.Sender.Card = "名片"
	if ob.sender(e) != "名片" {
		t.Fatal("应优先用群名片")
	}
	e.Sender.Card = ""
	e.Sender.Nickname = "昵称"
	if ob.sender(e) != "昵称" {
		t.Fatal("其次用昵称")
	}
	e.Sender.Nickname = ""
	e.UserID = 9
	if ob.sender(e) != "9" {
		t.Fatal("都没有时退 id")
	}
}

func TestOneBotSendWithoutConn(t *testing.T) {
	ob := NewOneBot(config.ChannelConfig{ID: "qq"})
	if err := ob.Send(context.Background(), Message{Meta: map[string]any{"user_id": int64(1)}}, "x"); err == nil {
		t.Fatal("没有实现端连接时回发应报错")
	}
}
