package tools

import "context"

type runIDKey struct{}

// WithRunID 把「当前运行 id」放进 ctx。
//
// 工具本身是无状态的、注册一次全局复用，但有些工具在运行时需要知道
// "我现在是哪一次运行"（典型：上下文回放要按 run 去取被滚出窗口的原文）。
func WithRunID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, runIDKey{}, id)
}

// RunIDFrom 取当前运行 id；不在运行里（或没设置）时返回空串
func RunIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(runIDKey{}).(string); ok {
		return v
	}
	return ""
}
