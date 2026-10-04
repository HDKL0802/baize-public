package llm

import (
	"context"
	"fmt"
	"sync"
)

// ScriptFunc 按请求给出回复（测试与演示用）
type ScriptFunc func(ctx context.Context, call int, req Request) (Response, error)

// Fake 脚本化的假通道：不联网，按调用次序给出预设回复。
// 用途：单测里驱动 Agent 主循环；也用 `--provider fake` 做确定性的端到端演示。
type Fake struct {
	naam  string
	fn    ScriptFunc
	mu    sync.Mutex
	calls int
	log   []Request
}

// NewFake 创建假通道
func NewFake(name string, fn ScriptFunc) *Fake {
	if name == "" {
		name = "fake"
	}
	return &Fake{naam: name, fn: fn}
}

// Name 通道名
func (f *Fake) Name() string { return f.naam }

// Chat 第 n 次调用交给脚本决定
func (f *Fake) Chat(ctx context.Context, req Request) (Response, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.log = append(f.log, req)
	fn := f.fn
	f.mu.Unlock()
	if fn == nil {
		return Response{Text: "（fake 通道没有设置脚本）", StopReason: "stop"}, nil
	}
	return fn(ctx, call, req)
}

// Calls 已经收到的请求（便于断言「提示词里带上了工具/记忆」）
func (f *Fake) Calls() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request{}, f.log...)
}

// CallCount 调用次数
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Script 按顺序返回预设回复，超出范围的调用返回最后一条
func Script(responses ...Response) ScriptFunc {
	return func(_ context.Context, call int, _ Request) (Response, error) {
		if call-1 < len(responses) {
			return responses[call-1], nil
		}
		if len(responses) == 0 {
			return Response{}, fmt.Errorf("脚本为空")
		}
		return responses[len(responses)-1], nil
	}
}

// CallTool 生成一次「调用工具」的回复
func CallTool(id, name string, args map[string]any) Response {
	return Response{
		ToolCalls:  []ToolCall{{ID: id, Name: name, Args: args}},
		StopReason: "tool_use",
	}
}

// Say 生成一次「直接回答」的回复
func Say(text string) Response {
	return Response{Text: text, StopReason: "stop"}
}
