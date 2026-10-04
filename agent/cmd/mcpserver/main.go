// Command mcpserver 是一个最小的 MCP 服务（stdio），只用于演示与自检：
// 暴露两个工具 —— hello（回显名字）、now（返回当前时间）。
//
// 用法（由白泽后端当子进程拉起，一般不用手工跑）：
//
//	mcpserver
//
// 手工试一下：
//
//	echo {"jsonrpc":"2.0","id":1,"method":"initialize"} | mcpserver
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  map[string]any  `json:"params"`
}

type errorObj struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req request
		if err := dec.Decode(&req); err != nil {
			return // 输入结束：退出
		}
		if len(req.ID) == 0 {
			continue // 通知，不回
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID)}
		switch req.Method {
		case "initialize":
			resp["result"] = map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "baize-demo-mcp", "version": "0.2.0"},
				"instructions":    "演示用的最小 MCP 服务：hello 回显名字，now 返回当前时间。",
			}
		case "tools/list":
			resp["result"] = map[string]any{"tools": []map[string]any{
				{
					"name": "hello", "description": "回显问候（只读，安全）",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"name": map[string]any{"type": "string", "description": "要问候的名字"}},
					},
				},
				{
					"name": "now", "description": "返回服务器当前时间（只读，安全）",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				},
			}}
		case "tools/call":
			name := str(req.Params["name"])
			args, _ := req.Params["arguments"].(map[string]any)
			switch name {
			case "hello":
				resp["result"] = text("你好，" + orDefault(str(args["name"]), "世界") + "！来自白泽演示 MCP 服务。")
			case "now":
				resp["result"] = text("服务器时间：" + time.Now().Format("2006-01-02 15:04:05"))
			default:
				resp["error"] = errorObj{Code: -32602, Message: "没有这个工具：" + name}
			}
		default:
			resp["error"] = errorObj{Code: -32601, Message: "不支持的方法：" + req.Method}
		}
		if err := enc.Encode(resp); err != nil {
			fmt.Fprintln(os.Stderr, "写响应失败：", err)
			return
		}
	}
}

func text(s string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": s}}}
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
