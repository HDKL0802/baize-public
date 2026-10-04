// Package tools 是工具注册中心（文档 §6.1「技能/工具注册中心」的最小实现）。
//
// 所有工具统一走 Name/Description/Schema/Run 四件套，
// 危险工具额外实现 Dangerous()，由 Agent 主循环交给审批闸门判断。
package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"baize/internal/llm"
)

// Tool 一个可被模型调用的工具
type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any // JSON Schema（object）
	Run(ctx context.Context, args map[string]any) (any, error)
}

// Dangerous 需要人工审批的工具（删除 / 执行命令 / 跨端下发这类，
// 口径见开发文档 §6.1：删除、支付、发布、发消息、改权限、正式提交需人工确认）
type Dangerous interface {
	Dangerous() bool
}

// Mutating 会改动工作目录的工具：这类工具在执行前要自动打快照（不一定需要人工审批）
type Mutating interface {
	Mutating() bool
}

// ErrUnknownTool 未知工具
var ErrUnknownTool = errors.New("未知工具")

// Registry 工具注册中心
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

// NewRegistry 创建注册中心
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register 注册工具（同名会覆盖并告警由调用方负责）
func (r *Registry) Register(t Tool) {
	if t == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	name := t.Name()
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
}

// Get 取工具
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names 全部工具名（按注册顺序）
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string{}, r.order...)
}

// Specs 给模型看的工具说明
func (r *Registry) Specs() []llm.ToolSpec {
	r.mu.RLock()
	names := append([]string{}, r.order...)
	tools := r.tools
	r.mu.RUnlock()
	out := make([]llm.ToolSpec, 0, len(names))
	for _, n := range names {
		t := tools[n]
		schema := t.Schema()
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, llm.ToolSpec{Name: t.Name(), Description: t.Description(), Schema: schema})
	}
	return out
}

// IsDangerous 是否需要审批
func (r *Registry) IsDangerous(name string) bool {
	t, ok := r.Get(name)
	if !ok {
		return false
	}
	if d, ok := t.(Dangerous); ok {
		return d.Dangerous()
	}
	return false
}

// IsMutating 是否需要"变更前快照"
func (r *Registry) IsMutating(name string) bool {
	t, ok := r.Get(name)
	if !ok {
		return false
	}
	if m, ok := t.(Mutating); ok {
		return m.Mutating()
	}
	// 没显式声明的：按是否危险兜底（危险的默认会改现场）
	return r.IsDangerous(name)
}

// Call 调用工具；未知工具返回明确错误（不伪装成功）
func (r *Registry) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	t, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("%w：%s（可用：%s）", ErrUnknownTool, name, strings.Join(r.Names(), ", "))
	}
	res, err := t.Run(ctx, args)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// SortedNames 排序后的名字（日志/提示词里用）
func (r *Registry) SortedNames() []string {
	names := r.Names()
	sort.Strings(names)
	return names
}

/* ---------- 参数取值助手 ---------- */

// ArgString 取字符串参数
func ArgString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// ArgInt 取整型参数
func ArgInt(args map[string]any, key string, def int) int {
	s := ArgString(args, key)
	if s == "" {
		return def
	}
	n := 0
	neg := false
	for i, r := range s {
		if i == 0 && r == '-' {
			neg = true
			continue
		}
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	if neg {
		return -n
	}
	return n
}

// ArgFloat 取浮点参数
func ArgFloat(args map[string]any, key string, def float64) float64 {
	s := strings.TrimSpace(ArgString(args, key))
	if s == "" {
		return def
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, fracPart := s, ""
	if i := strings.Index(s, "."); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	val := 0.0
	for _, r := range intPart {
		if r < '0' || r > '9' {
			return def
		}
		val = val*10 + float64(r-'0')
	}
	scale := 0.1
	for _, r := range fracPart {
		if r < '0' || r > '9' {
			return def
		}
		val += float64(r-'0') * scale
		scale /= 10
	}
	if neg {
		return -val
	}
	return val
}

// ArgBool 取布尔参数
func ArgBool(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	v, ok := args[key]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "true" || s == "1" || s == "yes" || s == "是"
	}
	return false
}
