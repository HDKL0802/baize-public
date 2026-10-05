package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"baize/internal/config"
)

// WSChannel 支持"反向 WebSocket"接入的频道（如 OneBot）：由外部实现端主动连进来，
// 事件从这条连接进来，回复也从这条连接回去。
type WSChannel interface {
	Channel
	// Serve 接管一条已升级的连接：读事件 → 回调 onEvent；连接断开时返回（阻塞）。
	Serve(c *websocket.Conn, onEvent func(Message))
}

// OneBot OneBot V11（反向 WebSocket）频道：QQ 机器人实现端（NapCat / go-cqhttp 等）连进来，
// 消息按 V11 事件格式下发，回复用 send_group_msg / send_private_msg 发回同一条连接。
//
// 为什么选反向 WS：LAN 上的 NAS 收不到公网回调，但实现端可以主动连进来；
// 且协议自包含、无需额外依赖。
type OneBot struct {
	id     string
	prefix string

	mu    sync.Mutex
	conns map[string]*wsConn
	seq   uint64
}

// wsConn 一条连接 + 写锁（WebSocket 不允许并发写）
type wsConn struct {
	c  *websocket.Conn
	mu sync.Mutex
}

func (w *wsConn) write(b []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return w.c.WriteMessage(websocket.TextMessage, b)
}

// NewOneBot 造一个 OneBot 频道
func NewOneBot(cfg config.ChannelConfig) *OneBot {
	return &OneBot{id: strings.TrimSpace(cfg.ID), prefix: cfg.BotPrefix, conns: map[string]*wsConn{}}
}

// Serve 接管一条反向 WS 连接：读到消息事件就组一条 Message 回调出去；断开即返回。
func (ob *OneBot) Serve(c *websocket.Conn, onEvent func(Message)) {
	cid := ob.attach(c)
	defer ob.detach(cid)
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return // 断开 / 读失败：收工（调用方会在日志里看到）
		}
		var ev oneBotEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			continue // 不是 JSON：跳过
		}
		if ev.PostType != "message" {
			continue // 只处理消息事件；meta_event（心跳）/ notice / request 先不管
		}
		text := strings.TrimSpace(ev.RawMessage)
		if text == "" {
			text = strings.TrimSpace(ev.Message)
		}
		if text == "" {
			continue
		}
		onEvent(Message{
			Channel: ob.id,
			Session: ob.session(ev),
			Sender:  ob.sender(ev),
			Text:    text,
			Meta: map[string]any{
				"conn": cid, "message_type": ev.MessageType,
				"group_id": ev.GroupID, "user_id": ev.UserID,
			},
		})
	}
}

// Send 把回复发回消息来源的那条连接
func (ob *OneBot) Send(_ context.Context, msg Message, reply string) error {
	cid, _ := msg.Meta["conn"].(string)
	c := ob.conn(cid)
	if c == nil {
		return errors.New("OneBot 频道没有可用的实现端连接（等 NapCat / go-cqhttp 连上来）")
	}
	mt, _ := msg.Meta["message_type"].(string)
	gid, _ := msg.Meta["group_id"].(int64)
	uid, _ := msg.Meta["user_id"].(int64)

	params := map[string]any{"message": ob.prefix + reply}
	var action string
	switch {
	case mt == "group" && gid != 0:
		action = "send_group_msg"
		params["group_id"] = gid
	case uid != 0:
		action = "send_private_msg"
		params["user_id"] = uid
	default:
		return errors.New("这条消息没有可回发的地点（缺群号 / 用户号）")
	}
	payload, err := json.Marshal(map[string]any{"action": action, "params": params, "echo": ob.echo()})
	if err != nil {
		return err
	}
	if err := c.write(payload); err != nil {
		return fmt.Errorf("回发到 OneBot 连接失败：%w", err)
	}
	return nil
}

// session 会话键：群聊按群、私聊按人（同一群/同一人的多轮消息落在一起）
func (ob *OneBot) session(ev oneBotEvent) string {
	if ev.MessageType == "group" && ev.GroupID != 0 {
		return fmt.Sprintf("onebot:group:%d", ev.GroupID)
	}
	return fmt.Sprintf("onebot:private:%d", ev.UserID)
}

// sender 显示用的发送者名（优先群名片，其次昵称，再退 id）
func (ob *OneBot) sender(ev oneBotEvent) string {
	if n := strings.TrimSpace(ev.Sender.Card); n != "" {
		return n
	}
	if n := strings.TrimSpace(ev.Sender.Nickname); n != "" {
		return n
	}
	if ev.UserID != 0 {
		return fmt.Sprintf("%d", ev.UserID)
	}
	return ""
}

func (ob *OneBot) attach(c *websocket.Conn) string {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	ob.seq++
	cid := fmt.Sprintf("c%d", ob.seq)
	ob.conns[cid] = &wsConn{c: c}
	return cid
}

func (ob *OneBot) detach(cid string) {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	delete(ob.conns, cid)
}

func (ob *OneBot) conn(cid string) *wsConn {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	return ob.conns[cid]
}

func (ob *OneBot) echo() string {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	return fmt.Sprintf("baize-%d", ob.seq)
}

// oneBotEvent OneBot V11 消息事件（只取我们需要的字段）
type oneBotEvent struct {
	PostType    string `json:"post_type"`
	MessageType string `json:"message_type"` // group | private
	UserID      int64  `json:"user_id"`
	GroupID     int64  `json:"group_id"`
	RawMessage  string `json:"raw_message"`
	Message     string `json:"message"`
	SelfID      int64  `json:"self_id"`
	MessageID   int64  `json:"message_id"`
	Sender      struct {
		UserID   int64  `json:"user_id"`
		Nickname string `json:"nickname"`
		Card     string `json:"card"`
	} `json:"sender"`
}
