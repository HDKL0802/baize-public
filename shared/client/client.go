// Package client 是各端共用的后端连接客户端：
// 注册 → 心跳 → 上报事件 → 接收指令 → 回执，断开后由调用方重连。
//
// 桌面端（agent/cmd/desktop）和手机内核（core/cmd/bzcore）都用这一份，
// 差别只在于传进来的 Hello（设备信息/能力）和 CommandHandler（本端能干什么）。
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"baize/shared/proto"
)

const (
	readTimeout  = 90 * time.Second
	writeTimeout = 10 * time.Second
	pingInterval = 25 * time.Second
	maxMsgSize   = 4 << 20
)

// ErrFatal 不可重试的错误（如配对令牌不对、服务端拒绝注册）
var ErrFatal = errors.New("不可重试的连接错误")

// Config 连接配置
type Config struct {
	URL          string // http(s)://host:port 或 ws(s)://host:port/ws/device
	Token        string // 配对令牌
	Info         proto.Hello
	Logger       *slog.Logger
	Heartbeat    time.Duration
	HelloTimeout time.Duration
	Busy         func() bool // 供心跳上报忙闲
}

// CommandHandler 收到指令后的处理函数（返回回执）
type CommandHandler func(cmd proto.Command) proto.Receipt

// Session 一次成功连接的会话（采集协程用它在连接期内发送事件）
type Session struct {
	c    *wsConn
	ack  proto.HelloAck
	done chan struct{}
	once sync.Once
}

// Send 发送一条消息
func (s *Session) Send(env proto.Envelope) error { return s.c.send(env) }

// Done 连接结束信号
func (s *Session) Done() <-chan struct{} { return s.done }

// Ack 注册确认
func (s *Session) Ack() proto.HelloAck { return s.ack }

type wsConn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (c *wsConn) send(env proto.Envelope) error {
	b, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("消息编码失败：%w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	return c.ws.WriteMessage(websocket.TextMessage, b)
}

// Run 连接一次后端并阻塞在读写循环里；返回错误后由调用方决定是否重连。
//   - onCommand：收到指令时调用（内部已负责发"执行中/最终"回执）
//   - onReady：注册成功后调用（可用于启动采集协程，连接结束时会通过 Session.Done 通知）
func Run(ctx context.Context, cfg Config, onCommand CommandHandler, onReady func(*Session)) error {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HelloTimeout <= 0 {
		cfg.HelloTimeout = 15 * time.Second
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = 10 * time.Second
	}

	wsURL, err := normalizeURL(cfg.URL, cfg.Token)
	if err != nil {
		return fmt.Errorf("%w：%v", ErrFatal, err)
	}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, resp, err := dialer.DialContext(ctx, wsURL, http.Header{})
	if err != nil {
		if resp != nil {
			return fmt.Errorf("连接后端失败（HTTP %d）：%w", resp.StatusCode, err)
		}
		return fmt.Errorf("连接后端失败：%w", err)
	}
	defer ws.Close()
	ws.SetReadLimit(maxMsgSize)

	c := &wsConn{ws: ws}
	sess := &Session{c: c, done: make(chan struct{})}
	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer sess.once.Do(func() { close(sess.done) })

	hello := cfg.Info
	hello.Token = cfg.Token
	hello.Protocol = proto.Version
	if err := c.send(proto.MustNew(proto.TypeHello, hello.DeviceID, "", hello)); err != nil {
		return err
	}
	cfg.Logger.Info("已连接后端，等待注册确认", "url", wsURL, "device", hello.DeviceID)

	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(readTimeout))
	})
	ws.SetPingHandler(func(data string) error {
		_ = ws.SetReadDeadline(time.Now().Add(readTimeout))
		return ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(writeTimeout))
	})

	// 保活：定时 ping，维持服务端读超时
	go func() {
		t := time.NewTicker(pingInterval)
		defer t.Stop()
		for {
			select {
			case <-sessCtx.Done():
				return
			case <-t.C:
				c.mu.Lock()
				err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout))
				c.mu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	// 心跳
	startedAt := time.Now()
	go func() {
		t := time.NewTicker(cfg.Heartbeat)
		defer t.Stop()
		for {
			select {
			case <-sessCtx.Done():
				return
			case <-t.C:
				busy := false
				if cfg.Busy != nil {
					busy = cfg.Busy()
				}
				env, err := proto.New(proto.TypeHeartbeat, hello.DeviceID, "", proto.Heartbeat{
					At: time.Now().UnixMilli(), UptimeSec: int64(time.Since(startedAt).Seconds()), Busy: busy,
				})
				if err != nil {
					continue
				}
				if err := c.send(env); err != nil {
					cfg.Logger.Debug("心跳发送失败", "err", err)
					return
				}
			}
		}
	}()

	registered := false
	_ = ws.SetReadDeadline(time.Now().Add(cfg.HelloTimeout))
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return fmt.Errorf("连接中断：%w", err)
		}
		var env proto.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			cfg.Logger.Warn("收到无法解析的消息，已忽略", "err", err)
			continue
		}
		switch env.Type {
		case proto.TypeHelloAck:
			if !registered {
				if err := env.Decode(&sess.ack); err != nil {
					return fmt.Errorf("解析注册确认失败：%w", err)
				}
				registered = true
				_ = ws.SetReadDeadline(time.Now().Add(readTimeout))
				cfg.Logger.Info("注册成功", "device", sess.ack.DeviceID, "server", sess.ack.Server,
					"protocol", sess.ack.Protocol, "queuedTasks", sess.ack.QueuedTasks)
				if onReady != nil {
					onReady(sess)
				}
			}
		case proto.TypeCommand:
			if !registered {
				cfg.Logger.Warn("未注册就收到指令，已忽略")
				continue
			}
			var cmd proto.Command
			if err := env.Decode(&cmd); err != nil {
				cfg.Logger.Warn("指令解析失败", "err", err)
				continue
			}
			go handleCommand(cfg.Logger, sess, cmd, onCommand)
		case proto.TypeError:
			var em proto.ErrorMsg
			_ = env.Decode(&em)
			return fmt.Errorf("%w：后端拒绝（%s）：%s", ErrFatal, em.Code, em.Message)
		default:
			cfg.Logger.Debug("收到未处理的消息类型", "type", env.Type)
		}
	}
}

// RunForever 反复重连直到 ctx 结束：不可重试的错误（令牌不对等）直接返回；
// 其它错误按退避重试（1s→30s 封顶）。手机端内核常驻用这个。
func RunForever(ctx context.Context, cfg Config, onCommand CommandHandler, onReady func(*Session)) error {
	lg := cfg.Logger
	if lg == nil {
		lg = slog.Default()
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := Run(ctx, cfg, onCommand, onReady)
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrFatal) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lg.Warn("与后端的连接断了，稍后重连", "err", err, "retryIn", backoff.String())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func handleCommand(lg *slog.Logger, sess *Session, cmd proto.Command, onCommand CommandHandler) {
	started := time.Now().UnixMilli()
	lg.Info("收到指令", "task", cmd.TaskID, "action", cmd.Action,
		"needApproval", cmd.NeedApproval, "approvedBy", cmd.ApprovedBy)

	// 先回"执行中"
	if env, err := proto.New(proto.TypeReceipt, "", cmd.TaskID, proto.Receipt{
		TaskID: cmd.TaskID, Status: proto.ReceiptRunning, StartedAt: started,
	}); err == nil {
		_ = sess.Send(env)
	}

	var rc proto.Receipt
	if onCommand == nil {
		rc = proto.Receipt{TaskID: cmd.TaskID, Status: proto.ReceiptRejected, Error: "本端未实现指令处理"}
	} else {
		rc = onCommand(cmd)
	}
	if rc.TaskID == "" {
		rc.TaskID = cmd.TaskID
	}
	if rc.StartedAt == 0 {
		rc.StartedAt = started
	}
	if rc.FinishedAt == 0 {
		rc.FinishedAt = time.Now().UnixMilli()
	}
	if rc.Status == "" {
		rc.Status = proto.ReceiptDone
	}

	env, err := proto.New(proto.TypeReceipt, "", cmd.TaskID, rc)
	if err != nil {
		lg.Error("回执编码失败", "task", cmd.TaskID, "err", err)
		return
	}
	if err := sess.Send(env); err != nil {
		lg.Error("回执发送失败", "task", cmd.TaskID, "err", err)
		return
	}
	switch rc.Status {
	case proto.ReceiptDone:
		lg.Info("指令执行完成，回执已发送", "task", cmd.TaskID, "action", cmd.Action,
			"costMs", rc.FinishedAt-rc.StartedAt)
	case proto.ReceiptFailed:
		lg.Error("指令执行失败，回执已发送", "task", cmd.TaskID, "action", cmd.Action, "err", rc.Error)
	default:
		lg.Warn("指令被拒绝，回执已发送", "task", cmd.TaskID, "action", cmd.Action, "reason", rc.Error)
	}
}

// normalizeURL 把 http(s):// 地址转换成 ws(s)://.../ws/device
func normalizeURL(raw, token string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("缺少后端地址（--server）")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("后端地址不合法：%v", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("不支持的协议：%s", u.Scheme)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/ws/device"
	}
	if token != "" {
		q := u.Query()
		q.Set("token", token)
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}
