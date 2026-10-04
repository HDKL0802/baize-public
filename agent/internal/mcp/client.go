package mcp

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ServerConfig 一个 MCP 服务的配置（存在 config.json 的 mcpServers 里）
type ServerConfig struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"` // stdio | http
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       []string          `json:"env,omitempty"` // "K=V" 形式
	Dir       string            `json:"dir,omitempty"` // 子进程工作目录
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Enabled   bool              `json:"enabled"`
	// AutoApprove 信任该服务：它的工具默认不需要人工审批（默认 false，一律要走审批闸门）
	AutoApprove bool `json:"autoApprove,omitempty"`
	// SafeTools 例外名单：即便 AutoApprove=false，这些远端工具也视为安全（只读类可以放这里）
	SafeTools  []string `json:"safeTools,omitempty"`
	Prefix     string   `json:"prefix,omitempty"` // 工具名前缀，默认 mcp.<服务名>
	TimeoutSec int      `json:"timeoutSec,omitempty"`
}

// normalize 补默认值并做基本校验（缺什么就明确报什么，不猜）
func (c ServerConfig) normalize(allowRemote bool) (ServerConfig, error) {
	c.Name = strings.TrimSpace(c.Name)
	c.Transport = strings.ToLower(strings.TrimSpace(c.Transport))
	c.Command = strings.TrimSpace(c.Command)
	c.URL = strings.TrimSpace(c.URL)
	if c.Name == "" {
		return c, errors.New("MCP 服务缺少 name")
	}
	if c.Transport == "" {
		switch {
		case c.Command != "":
			c.Transport = "stdio"
		case c.URL != "":
			c.Transport = "http"
		default:
			return c, errors.New("MCP 服务 " + c.Name + " 既没有 command（stdio）也没有 url（http）")
		}
	}
	switch c.Transport {
	case "stdio":
		if c.Command == "" {
			return c, fmt.Errorf("MCP 服务 %s 用 stdio 传输时必须给 command", c.Name)
		}
	case "http", "sse", "streamable-http":
		c.Transport = "http"
		if c.URL == "" {
			return c, fmt.Errorf("MCP 服务 %s 用 http 传输时必须给 url", c.Name)
		}
		if !allowRemote && !isLocalURL(c.URL) {
			return c, fmt.Errorf("MCP 服务 %s 的地址不是本机（%s）；要连远端服务请把配置里的 allowRemote 设为 true", c.Name, c.URL)
		}
	default:
		return c, fmt.Errorf("MCP 服务 %s 的 transport 只支持 stdio / http，收到：%s", c.Name, c.Transport)
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 20
	}
	if strings.TrimSpace(c.Prefix) == "" {
		c.Prefix = "mcp_" + sanitizeName(c.Name)
	}
	c.Prefix = strings.Trim(strings.TrimSpace(c.Prefix), "._-")
	return c, nil
}

// Normalized 返回补齐默认值并校验后的配置（调用方保存前先规范化，存下来的就是可用的形态）
func (c ServerConfig) Normalized(allowRemote bool) (ServerConfig, error) {
	return c.normalize(allowRemote)
}

// Clone 深拷贝（避免调用方改到内部切片）
func (c ServerConfig) Clone() ServerConfig {
	out := c
	out.Args = append([]string{}, c.Args...)
	out.Env = append([]string{}, c.Env...)
	out.SafeTools = append([]string{}, c.SafeTools...)
	if c.Headers != nil {
		out.Headers = make(map[string]string, len(c.Headers))
		for k, v := range c.Headers {
			out.Headers[k] = v
		}
	}
	return out
}

// Implementation MCP 客户端/服务端自我介绍
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Title   string `json:"title,omitempty"`
}

// ServerInfo initialize 的结果
type ServerInfo struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      Implementation `json:"serverInfo"`
	Instructions    string         `json:"instructions,omitempty"`
}

// ToolInfo 远端工具描述（tools/list 的条目）
type ToolInfo struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

// Content MCP 工具返回的一段内容
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// CallResult tools/call 的结果
type CallResult struct {
	Content           []Content      `json:"content"`
	StructuredContent map[string]any `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

// Text 把返回内容拼成文本（非文本内容用占位符标出来，不假装是文字）
func (r CallResult) Text() string {
	var parts []string
	for _, c := range r.Content {
		switch {
		case c.Text != "":
			parts = append(parts, c.Text)
		case c.Type == "image":
			parts = append(parts, "[图片 "+c.MimeType+"]")
		case c.Type == "resource":
			parts = append(parts, "[资源]")
		default:
			parts = append(parts, "["+c.Type+"]")
		}
	}
	if len(parts) == 0 && len(r.StructuredContent) > 0 {
		if raw, err := json.Marshal(r.StructuredContent); err == nil {
			parts = append(parts, string(raw))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// Client 一个已连上的 MCP 服务
type Client struct {
	cfg  ServerConfig
	tr   transport
	info ServerInfo

	mu    sync.RWMutex
	tools []ToolInfo
}

// Connect 建传输、initialize、拉一次工具清单
func Connect(ctx context.Context, cfg ServerConfig, allowRemote bool) (*Client, error) {
	ncfg, err := cfg.normalize(allowRemote)
	if err != nil {
		return nil, err
	}
	var tr transport
	switch ncfg.Transport {
	case "stdio":
		tr, err = newStdioTransport(ncfg)
	default:
		tr, err = newHTTPTransport(ncfg)
	}
	if err != nil {
		return nil, err
	}
	c := &Client{cfg: ncfg, tr: tr}

	raw, err := tr.Call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "baize-agent", "version": Version},
	})
	if err != nil {
		_ = tr.Close()
		return nil, fmt.Errorf("initialize 失败：%w", err)
	}
	if err := json.Unmarshal(raw, &c.info); err != nil {
		_ = tr.Close()
		return nil, fmt.Errorf("initialize 返回解析失败：%w", err)
	}
	if err := tr.Notify(ctx, "notifications/initialized", nil); err != nil {
		_ = tr.Close()
		return nil, fmt.Errorf("发送 initialized 通知失败：%w", err)
	}
	return c, nil
}

// Config 配置副本
func (c *Client) Config() ServerConfig { return c.cfg.Clone() }

// Info 服务端信息
func (c *Client) Info() ServerInfo { return c.info }

// Tools 上次拉到的工具清单
func (c *Client) Tools() []ToolInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]ToolInfo{}, c.tools...)
}

// RefreshTools 重新拉工具清单；服务端明确不支持 tools 能力时返回空清单 + ErrNoToolsCapability
func (c *Client) RefreshTools(ctx context.Context) ([]ToolInfo, error) {
	raw, err := c.tr.Call(ctx, "tools/list", map[string]any{})
	if err != nil {
		if isMethodNotFound(err) {
			c.mu.Lock()
			c.tools = nil
			c.mu.Unlock()
			return nil, ErrNoToolsCapability
		}
		return nil, err
	}
	var res struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("tools/list 返回解析失败：%w", err)
	}
	c.mu.Lock()
	c.tools = res.Tools
	c.mu.Unlock()
	return append([]ToolInfo{}, res.Tools...), nil
}

// ErrNoToolsCapability 该 MCP 服务没有暴露工具
var ErrNoToolsCapability = errors.New("该 MCP 服务没有提供工具（不支持 tools 能力）")

// CallTool 调一个远端工具
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.tr.Call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return CallResult{}, err
	}
	var res CallResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return CallResult{}, fmt.Errorf("tools/call 返回解析失败：%w", err)
	}
	return res, nil
}

// Ping 探活（用 tools/list 当心跳；不支持工具的服务的用空参数 initialize 探）
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := c.RefreshTools(ctx); err != nil && !errors.Is(err, ErrNoToolsCapability) {
		return err
	}
	return nil
}

// Close 断开连接
func (c *Client) Close() error { return c.tr.Close() }

/* ---------- 小工具 ---------- */

// sanitizeName 把名字里不适合做工具名的字符换成下划线
// （模型接口对函数名有硬性要求：只允许 [a-zA-Z0-9_-]，DeepSeek/OpenAI 遇到点号会直接 400）
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "._-")
	if out == "" {
		return "server"
	}
	return out
}

// ToolName 拼一个合法的本地工具名：<前缀>_<远端工具名>；
// 太长就截断并加短哈希，保证既合规又基本不重名。
// 前缀没填时按 "mcp_<服务名>" 现算，绝不会拼出 "_echo" 这种半截名字。
func (c ServerConfig) ToolName(remote string) string {
	prefix := strings.Trim(strings.TrimSpace(c.Prefix), "._-")
	if prefix == "" {
		prefix = "mcp_" + sanitizeName(c.Name)
	}
	name := prefix + "_" + sanitizeName(remote)
	if len(name) <= maxToolNameLen {
		return name
	}
	sum := sha1.Sum([]byte(name))
	suffix := hex.EncodeToString(sum[:])[:8]
	keep := maxToolNameLen - len(suffix) - 1
	return name[:keep] + "_" + suffix
}

// maxToolNameLen 模型接口对函数名的长度上限（OpenAI 系是 64）
const maxToolNameLen = 64

// isLocalURL 判断地址是不是本机/内网（和模型通道同一套本地优先策略）
func isLocalURL(u string) bool {
	s := strings.ToLower(strings.TrimSpace(u))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	host := s
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s[i:], "]") {
		host = s[:i]
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0":
		return true
	}
	return strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "172.")
}
