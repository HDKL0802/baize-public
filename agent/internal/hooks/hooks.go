// Package hooks 是生命周期事件钩子（Hermes「事件钩子 + Plugin hooks」的 Go 重写）。
package hooks

import (
	"context"
	"sync"
)

// Event 事件名
type Event string

// 生命周期事件
const (
	EventRunStart    Event = "run.start"
	EventLLMRequest  Event = "llm.request"
	EventLLMResponse Event = "llm.response"
	EventLLMRetry    Event = "llm.retry"
	EventToolBefore  Event = "tool.before"
	EventToolAfter   Event = "tool.after"
	EventToolError   Event = "tool.error"
	EventToolBlocked Event = "tool.blocked"
	EventCompress    Event = "context.compress"
	EventCheckpoint  Event = "checkpoint.create"
	EventMemoryWrite Event = "memory.write"
	EventRunFinish   Event = "run.finish"
	EventError       Event = "error"
)

// Payload 事件负载（键值对，便于插件/日志按需取用）
type Payload map[string]any

// Handler 处理函数
type Handler func(ctx context.Context, ev Event, p Payload)

// Bus 事件总线
type Bus struct {
	mu       sync.RWMutex
	handlers map[Event][]Handler
	any      []Handler
}

// NewBus 创建总线
func NewBus() *Bus {
	return &Bus{handlers: map[Event][]Handler{}}
}

// On 订阅某个事件
func (b *Bus) On(ev Event, h Handler) {
	if h == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[ev] = append(b.handlers[ev], h)
}

// OnAny 订阅所有事件（插件常用）
func (b *Bus) OnAny(h Handler) {
	if h == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.any = append(b.any, h)
}

// Emit 触发事件：先发给专属订阅者，再发给全体订阅者
func (b *Bus) Emit(ctx context.Context, ev Event, p Payload) {
	if b == nil {
		return
	}
	b.mu.RLock()
	specific := append([]Handler{}, b.handlers[ev]...)
	all := append([]Handler{}, b.any...)
	b.mu.RUnlock()
	for _, h := range specific {
		h(ctx, ev, p)
	}
	for _, h := range all {
		h(ctx, ev, p)
	}
}

// Plugin 可插拔扩展（注册自己的钩子）
type Plugin interface {
	Name() string
	Register(bus *Bus)
}
