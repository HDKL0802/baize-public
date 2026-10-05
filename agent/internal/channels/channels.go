// Package channels 是「频道」——你和白泽在"哪里"对话的接入点（QwenPaw 频道机制的 Go 版）。
//
// 一个频道负责两件事：
//   - 收：把外部消息（IM 回调 / webhook / 平台事件）变成一条统一的 Message，交给 Agent；
//   - 发：把 Agent 的结论按平台的方式发回去。
//
// 目前实现 kind = "webhook"：出站 POST 到一个 URL（可直接对接飞书/钉钉/Slack 的群机器人 webhook），
// 入站走 POST /api/channels/{id}/inbound（用频道自己的令牌校验）。真实平台适配器
// （飞书长连接、Discord 网关、QQ OneBot 反向 WS）后续照 Channel 接口实现即可，不用改本包。
//
// 刻意不依赖 agentsvc：跑 Agent 的能力由调用方以 Runner 注入，避免循环依赖
// （agentsvc 会 import channels 来装配）。
package channels

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"baize/internal/config"
)

// Message 一条统一的频道消息（进站或出站共用同一形状）
type Message struct {
	Channel string         `json:"channel"`           // 频道 id
	Session string         `json:"session,omitempty"` // 会话键（群 / 私聊）；缺省按频道+发送者算
	Sender  string         `json:"sender,omitempty"`  // 发送者（名字或平台 id）
	Text    string         `json:"text"`              // 正文
	Meta    map[string]any `json:"meta,omitempty"`    // 平台附加信息（原样带给下层）
}

// Runner 跑一次 Agent 并返回回复文本。由 agentsvc 注入，避免 channels 反向依赖 agentsvc。
type Runner func(ctx context.Context, session, text string) (string, error)

// Channel 一个频道实例：只需要会"发"。
// "收"由外部驱动（HTTP 回调 / 平台 SDK 线程）在拿到消息后调用 Manager.Inbound。
type Channel interface {
	Send(ctx context.Context, msg Message, reply string) error
}

// Info 频道的对外描述（控制台 / API 用；不含令牌明文）
type Info struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Enabled     bool   `json:"enabled"`
	OutboundURL string `json:"outboundUrl,omitempty"`
	Format      string `json:"format,omitempty"`
	HasToken    bool   `json:"hasToken"`
	BotPrefix   string `json:"botPrefix,omitempty"`
}

// entry 一个已登记的频道：配置快照 + 活对象
type entry struct {
	cfg config.ChannelConfig
	ch  Channel
}

// Manager 频道管理器：登记 / 入站分发 / 主动发送 / 最近频道记录。
// 可并发使用；Register 语义是"整体替换"（装配或改配置时整体重建）。
type Manager struct {
	mu      sync.RWMutex
	order   []string
	entries map[string]*entry

	runner Runner
	lg     *slog.Logger

	lastMu sync.Mutex
	last   string // 最近收到过消息的频道 id（心跳 target=last 用）
}

// NewManager 建一个空的管理器
func NewManager(runner Runner, lg *slog.Logger) *Manager {
	if lg == nil {
		lg = slog.Default()
	}
	return &Manager{entries: map[string]*entry{}, runner: runner, lg: lg}
}

// Register 登记一个频道（同 id 覆盖）。cfg.ID 为空的一律忽略。
func (m *Manager) Register(cfg config.ChannelConfig, ch Channel) {
	id := strings.TrimSpace(cfg.ID)
	if id == "" || ch == nil {
		return
	}
	cfg.ID = id
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[id]; !ok {
		m.order = append(m.order, id)
	}
	m.entries[id] = &entry{cfg: cfg, ch: ch}
}

// List 全部频道（按登记顺序）
func (m *Manager) List() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Info, 0, len(m.order))
	for _, id := range m.order {
		e := m.entries[id]
		if e == nil {
			continue
		}
		out = append(out, Info{
			ID: e.cfg.ID, Kind: e.cfg.Kind, Enabled: e.cfg.Enabled,
			OutboundURL: e.cfg.OutboundURL, Format: NormalizeFormat(e.cfg.Format),
			HasToken: strings.TrimSpace(e.cfg.Token) != "", BotPrefix: e.cfg.BotPrefix,
		})
	}
	return out
}

// Get 取一个频道
func (m *Manager) Get(id string) (Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.entries[id]
	if e == nil {
		return nil, false
	}
	return e.ch, true
}

// InboundError 入站校验失败，带 HTTP 语义的错误码（让 HTTP 层能直接映射状态码）
type InboundError struct {
	Code int
	Msg  string
}

func (e *InboundError) Error() string { return e.Msg }

// CheckInbound 校验"这个频道此刻能不能收消息"（不改状态）。HTTP / WS 入口先调它，
// 好在升级连接 / 收下请求之前就能给出干净的状态码。
func (m *Manager) CheckInbound(id, token string) error {
	id = strings.TrimSpace(id)
	m.mu.RLock()
	e := m.entries[id]
	m.mu.RUnlock()
	if e == nil {
		return &InboundError{Code: 404, Msg: "没有这个频道：" + id}
	}
	if !e.cfg.Enabled {
		return &InboundError{Code: 409, Msg: "频道已停用：" + id}
	}
	if strings.TrimSpace(e.cfg.Token) == "" {
		return &InboundError{Code: 409, Msg: "频道 " + id + " 没设入站令牌，拒绝入站（先在控制台给它设一个）"}
	}
	if token != e.cfg.Token {
		return &InboundError{Code: 401, Msg: "入站令牌不对"}
	}
	return nil
}

// Inbound 处理一条入站消息：校验频道与令牌 → 起后台任务跑 Agent → 回发。
// 立即返回（入站回调要快速 ACK），跑与回发在后台完成。
func (m *Manager) Inbound(msg Message, token string) error {
	id := strings.TrimSpace(msg.Channel)
	if err := m.CheckInbound(id, token); err != nil {
		return err
	}
	if strings.TrimSpace(msg.Text) == "" {
		return &InboundError{Code: 400, Msg: "消息内容为空"}
	}
	if m.runner == nil {
		return &InboundError{Code: 503, Msg: "Agent 还没接上，暂时收不了频道消息"}
	}
	m.mu.RLock()
	e := m.entries[id]
	m.mu.RUnlock()
	if e == nil {
		return &InboundError{Code: 404, Msg: "没有这个频道：" + id}
	}
	if strings.TrimSpace(msg.Session) == "" {
		msg.Session = id + ":" + strings.TrimSpace(msg.Sender)
	}
	m.NoteLast(id)
	go m.process(e, msg)
	return nil
}

// process 跑一次 Agent 并把结论回发；全程记日志，绝不静默失败
func (m *Manager) process(e *entry, msg Message) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	reply, err := m.runner(ctx, msg.Session, msg.Text)
	if err != nil {
		m.lg.Warn("频道消息处理失败", "channel", e.cfg.ID, "session", msg.Session, "err", err)
		reply = "这次没跑成：" + err.Error()
	}
	if strings.TrimSpace(reply) == "" {
		reply = "（白泽没有产出正文）"
	}
	if err := e.ch.Send(ctx, msg, reply); err != nil {
		m.lg.Warn("频道回复发送失败", "channel", e.cfg.ID, "err", err)
		return
	}
	m.lg.Info("频道已回复", "channel", e.cfg.ID, "session", msg.Session)
}

// Send 主动往某个频道发一条（心跳 / 通知用）。频道不存在或停用会明确报错。
func (m *Manager) Send(ctx context.Context, channelID string, msg Message, text string) error {
	id := strings.TrimSpace(channelID)
	m.mu.RLock()
	e := m.entries[id]
	m.mu.RUnlock()
	if e == nil {
		return fmt.Errorf("没有这个频道：%s", id)
	}
	if !e.cfg.Enabled {
		return fmt.Errorf("频道已停用：%s", id)
	}
	if msg.Channel == "" {
		msg.Channel = id
	}
	return e.ch.Send(ctx, msg, text)
}

// LastChannel 最近收到过消息的频道 id（没有则空）
func (m *Manager) LastChannel() string {
	m.lastMu.Lock()
	defer m.lastMu.Unlock()
	return m.last
}

// HasChannel 是否有这个 id 的频道
func (m *Manager) HasChannel(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.entries[strings.TrimSpace(id)]
	return ok
}

// Count 已登记频道数
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.order)
}

// NoteLast 记下最近收到过消息的频道 id（供心跳 target=last 使用）
func (m *Manager) NoteLast(id string) {
	m.lastMu.Lock()
	m.last = id
	m.lastMu.Unlock()
}
