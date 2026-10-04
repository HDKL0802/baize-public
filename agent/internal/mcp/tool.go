package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"baize/internal/tools"
)

// Tool 远端工具在本地的统一形态
type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any
	Run(ctx context.Context, args map[string]any) (any, error)
}

// ServerTool 把一个远端 MCP 工具包装成本地工具
type ServerTool struct {
	mgr *Manager
	ref ToolRef
}

// NewServerTool 创建包装
func NewServerTool(mgr *Manager, ref ToolRef) *ServerTool {
	return &ServerTool{mgr: mgr, ref: ref}
}

// Name 本地工具名（带前缀，避免和内置工具撞名）
func (t *ServerTool) Name() string { return t.ref.Name }

// Description 给模型看的说明（标明来自哪个 MCP 服务）
func (t *ServerTool) Description() string { return t.ref.Description }

// Schema 参数说明（直接透传远端 inputSchema）
func (t *ServerTool) Schema() map[string]any {
	if t.ref.Schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return t.ref.Schema
}

// Dangerous 是否需要人工审批（默认需要；配置里显式信任才免批）
func (t *ServerTool) Dangerous() bool { return t.ref.Dangerous }

// Run 调远端工具；服务掉线、超时、远端报错都明确返回错误，绝不伪装成功
func (t *ServerTool) Run(ctx context.Context, args map[string]any) (any, error) {
	if t.mgr == nil {
		return nil, fmt.Errorf("MCP 工具 %s 没有绑定服务管理器", t.ref.Name)
	}
	cfg, ok := t.mgr.ServerConfig(t.ref.Server)
	if !ok {
		return nil, fmt.Errorf("MCP 服务 %s 已不存在（工具 %s 可能已被热插拔下线）", t.ref.Server, t.ref.Name)
	}
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := t.mgr.CallTool(callCtx, t.ref.Server, t.ref.Remote, args)
	if err != nil {
		return nil, fmt.Errorf("调用 MCP 工具 %s（%s/%s）失败：%w", t.ref.Name, t.ref.Server, t.ref.Remote, err)
	}
	text := res.Text()
	out := map[string]any{
		"server": t.ref.Server,
		"tool":   t.ref.Remote,
		"text":   text,
	}
	if len(res.StructuredContent) > 0 {
		out["structured"] = res.StructuredContent
	}
	if res.IsError {
		msg := text
		if strings.TrimSpace(msg) == "" {
			msg = "（远端没有给出错误说明）"
		}
		return out, fmt.Errorf("MCP 工具 %s 返回错误：%s", t.ref.Name, brief(msg, 500))
	}
	return out, nil
}

// RegisterInto 把远端工具注册进本地工具中心（每次运行前调一次，热插拔立刻生效）
func (m *Manager) RegisterInto(reg *tools.Registry) int {
	if m == nil || reg == nil {
		return 0
	}
	n := 0
	for _, t := range m.Tools() {
		reg.Register(t)
		n++
	}
	return n
}

// Hint 给系统提示用的一句话（有哪些 MCP 工具可用）
func (m *Manager) Hint() string {
	if m == nil {
		return ""
	}
	refs := m.ToolRefs()
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("【MCP 外部工具】\n")
	for _, r := range refs {
		flag := "需审批"
		if !r.Dangerous {
			flag = "免审批"
		}
		b.WriteString(fmt.Sprintf("- %s（%s/%s，%s）：%s\n", r.Name, r.Server, r.Remote, flag, brief(r.Description, 120)))
	}
	return b.String()
}

// StatusHint 给控制台/CLI 的一句话状态
func (m *Manager) StatusHint() string {
	if m == nil {
		return ""
	}
	st := m.Status()
	if len(st) == 0 {
		return ""
	}
	parts := make([]string, 0, len(st))
	for _, s := range st {
		switch s.Status {
		case "connected":
			parts = append(parts, fmt.Sprintf("%s=已连接(%d 个工具)", s.Name, s.ToolCount))
		case "disabled":
			parts = append(parts, s.Name+"=已禁用")
		default:
			parts = append(parts, s.Name+"=失败("+brief(s.Error, 80)+")")
		}
	}
	return strings.Join(parts, "；")
}

// MarshalSchema 便于测试/日志展示
func MarshalSchema(s map[string]any) string {
	if len(s) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
