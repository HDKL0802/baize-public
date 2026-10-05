// Package mcp 是 MCP（Model Context Protocol）客户端的 Go 实现。
//
// 支持两种传输：
//   - stdio：把 MCP 服务当子进程拉起，JSON-RPC 走 stdin/stdout（一行一条）
//   - http：Streamable HTTP（响应既可能是 application/json，也可能是 text/event-stream）
//
// 远端工具会被包装成本地工具注册进 tools.Registry，并支持热插拔：
// 加/删/改一个 MCP 服务后调 Manager.Apply 即可，不用重启后端。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Version 客户端版本（给 MCP 服务看的 clientInfo.version）。
// 默认值可被 SetVersion 覆盖，由 cmd/backend 在启动时对齐到后端版本号。
var Version = "0.8.0"

// SetVersion 让 MCP 客户端版本与后端版本保持一致。
func SetVersion(v string) {
	if strings.TrimSpace(v) != "" {
		Version = v
	}
}

// ProtocolVersion 我们主动声明的 MCP 协议版本（服务端可回它自己支持的版本）
const ProtocolVersion = "2025-06-18"

// rpcRequest JSON-RPC 2.0 请求
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse JSON-RPC 2.0 响应
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError JSON-RPC 错误
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error 实现 error
func (e *RPCError) Error() string {
	if e == nil {
		return "MCP 调用失败"
	}
	msg := fmt.Sprintf("MCP 错误 %d：%s", e.Code, e.Message)
	if len(e.Data) > 0 {
		msg += "（" + brief(string(e.Data), 300) + "）"
	}
	return msg
}

// 常见 JSON-RPC 错误码
const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// isMethodNotFound 判断是不是"该服务不支持这个方法"
func isMethodNotFound(err error) bool {
	re, ok := err.(*RPCError)
	return ok && re.Code == codeMethodNotFound
}

// transport 传输层：一次请求对应一次响应
type transport interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	Notify(ctx context.Context, method string, params any) error
	Close() error
}

// normalizeRPCID 把 JSON-RPC 的 id（可能是数字或字符串）归一成字符串，便于比对
func normalizeRPCID(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	s = strings.Trim(s, `"`)
	return s
}

// brief 截断长文本（日志/错误里用）
func brief(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// idGen 线程安全的递增 id
type idGen struct {
	mu   sync.Mutex
	next int64
}

func (g *idGen) New() (int64, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return g.next, strconv.FormatInt(g.next, 10)
}
