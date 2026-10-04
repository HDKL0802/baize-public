// Package llm 是模型接入层（OpenHuman「模型路由」的 Go 重写）：
// 统一 Provider 抽象，支持 OpenAI 兼容协议与 Anthropic 协议两条通道，
// 按任务类型路由到不同模型，并支持失败回退。
//
// 按开发文档 §6.1 的口径，后端默认只接本地模型（Ollama / vLLM / llama.cpp 都走
// OpenAI 兼容协议）；确实要用远端接口时必须显式打开 AllowRemote，避免"悄悄上云"。
package llm

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Role 消息角色
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// ToolCall 模型请求调用某个工具
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// Message 一条对话消息
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`  // role=assistant 时：要调用的工具
	ToolCallID string     `json:"toolCallId,omitempty"` // role=tool 时：对应哪次调用
	Name       string     `json:"name,omitempty"`       // role=tool 时：工具名
}

// ToolSpec 工具说明（给模型看的）
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"` // JSON Schema（object）
}

// Usage 用量
type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// Response 模型回复
type Response struct {
	Text       string     `json:"text,omitempty"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	Usage      Usage      `json:"usage"`
	Model      string     `json:"model,omitempty"`
	StopReason string     `json:"stopReason,omitempty"`
}

// Request 一次请求
type Request struct {
	System      string
	Messages    []Message
	Tools       []ToolSpec
	Temperature float64
	MaxTokens   int
	Model       string // 覆盖 provider 默认模型（模型路由用）
}

// Provider 模型通道
type Provider interface {
	Name() string
	Chat(ctx context.Context, req Request) (Response, error)
}

// ErrNoProvider 没有可用通道
var ErrNoProvider = errors.New("没有可用的模型通道，请先配置一个本地模型（Ollama / vLLM / llama.cpp）")

// ToolNameRe 模型接口对函数名的硬性要求（DeepSeek / OpenAI 都只接受 [a-zA-Z0-9_-]，
// 长度上限 64；带点号的名字会被直接 400 拒绝）
var ToolNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// ValidToolName 判断工具名是否符合模型接口要求
func ValidToolName(name string) bool { return ToolNameRe.MatchString(name) }

// CheckToolNames 发请求前校验工具名；不合法就明确报错，而不是等远端返回一句看不懂的 400
func CheckToolNames(tools []ToolSpec) error {
	for _, t := range tools {
		if !ValidToolName(t.Name) {
			return fmt.Errorf("工具名 %q 不合规范：模型接口只接受 [a-zA-Z0-9_-] 且不超过 64 个字符（请把工具名里的点号等字符换成下划线）", t.Name)
		}
	}
	return nil
}

// PingPrompt 探活用的那句话（越短越省额度）
const PingPrompt = "只回复两个字：能通"

// Ping 对一条通道做一次极小的真实调用，用来验证连通（会消耗一点点额度）
func Ping(ctx context.Context, cfg Config) (Response, error) {
	prov, err := New(cfg)
	if err != nil {
		return Response{}, err
	}
	return prov.Chat(ctx, Request{
		Messages:    []Message{{Role: RoleUser, Content: PingPrompt}},
		MaxTokens:   16,
		Temperature: 0,
	})
}

// Config 单个通道的配置
type Config struct {
	Name        string  // 通道名，如 "ollama-qwen"
	Protocol    string  // openai | anthropic
	BaseURL     string  // 如 http://127.0.0.1:11434/v1 或 https://api.anthropic.com
	APIKey      string  //
	Model       string  // 默认模型
	Temperature float64 // 0 表示用默认
	MaxTokens   int     // 0 表示用默认
	TimeoutSec  int     // 0 表示用默认 120s
	Dim         int     // 仅 embedding 通道用：向量维度（0 = 首次调用后自动记录）
}

// New 按协议创建通道
func New(cfg Config) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Protocol)) {
	case "", "openai":
		return NewOpenAI(cfg), nil
	case "anthropic", "claude":
		return NewAnthropic(cfg), nil
	}
	return nil, fmt.Errorf("不支持的协议：%s（可选 openai | anthropic）", cfg.Protocol)
}

/* ---------- 模型路由 ---------- */

// Route 一条路由规则
type Route struct {
	Kinds    []string // 任务类型（chat / plan / summarize / extract / code …），空表示通吃
	Provider Provider
}

// Router 按任务类型选模型，并保存回退链
type Router struct {
	routes      []Route
	fallbacks   []Provider
	allowRemote bool
}

// NewRouter 创建路由器；allowRemote=false 时拒绝非本地地址
func NewRouter(allowRemote bool) *Router {
	return &Router{allowRemote: allowRemote}
}

// Add 注册一条路由
func (r *Router) Add(kinds []string, p Provider) {
	r.routes = append(r.routes, Route{Kinds: kinds, Provider: p})
}

// AddFallback 注册回退通道（主通道失败时按顺序尝试）
func (r *Router) AddFallback(p Provider) {
	r.fallbacks = append(r.fallbacks, p)
}

// Providers 已注册的全部通道
func (r *Router) Providers() []Provider {
	out := make([]Provider, 0, len(r.routes)+len(r.fallbacks))
	for _, rt := range r.routes {
		out = append(out, rt.Provider)
	}
	return append(out, r.fallbacks...)
}

// Pick 选一条通道
func (r *Router) Pick(kind string) (Provider, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	for _, rt := range r.routes {
		if len(rt.Kinds) == 0 {
			if kind == "" {
				return rt.Provider, nil
			}
			continue
		}
		for _, k := range rt.Kinds {
			if strings.EqualFold(k, kind) {
				return rt.Provider, nil
			}
		}
	}
	// 没有精确匹配就用第一条兜底路由
	for _, rt := range r.routes {
		if len(rt.Kinds) == 0 {
			return rt.Provider, nil
		}
	}
	if len(r.routes) > 0 {
		return r.routes[0].Provider, nil
	}
	return nil, ErrNoProvider
}

// Fallbacks 回退链
func (r *Router) Fallbacks() []Provider { return r.fallbacks }

// AllowRemote 是否允许远端地址
func (r *Router) AllowRemote() bool { return r.allowRemote }

// CheckEndpoint 校验通道地址是否符合本地优先策略
func (r *Router) CheckEndpoint(cfg Config) error {
	if r.allowRemote || isLocalURL(cfg.BaseURL) {
		return nil
	}
	return fmt.Errorf("通道 %s 的地址 %s 看起来不是本机（按文档只接本地模型）；确实要用远端请显式加 --allow-remote", cfg.Name, cfg.BaseURL)
}

func isLocalURL(u string) bool {
	s := strings.ToLower(strings.TrimSpace(u))
	host := s
	if i := strings.Index(s, "://"); i >= 0 {
		host = s[i+3:]
	}
	if i := strings.IndexAny(host, "/?"); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimSuffix(host, ":")
	host = strings.Split(host, ":")[0]
	switch host {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0", "host.docker.internal":
		return true
	}
	// 192.168.x / 10.x / 172.16-31.x 也算内网（NAS 场景）
	if strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") {
		return true
	}
	if strings.HasPrefix(host, "172.") {
		parts := strings.Split(host, ".")
		if len(parts) > 1 {
			switch parts[1] {
			case "16", "17", "18", "19", "20", "21", "22", "23", "24", "25", "26", "27", "28", "29", "30", "31":
				return true
			}
		}
	}
	return false
}

/* ---------- 小工具 ---------- */

func joinURL(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return path
	}
	if strings.HasSuffix(base, path) {
		return base
	}
	return base + path
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
