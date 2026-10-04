// Command mockllm 是**自检 / 演示用的最小 OpenAI 兼容通道**：
// 不联网、按剧本依次回复，并回放收到过的请求。
//
// 为什么需要它：后端的模型通道只支持 openai / anthropic 协议（没有内置 fake），
// 而用真模型测 Agent 全链路既花钱又不确定（同样的 goal，模型每次调的工具都可能不同）。
// 有了它就能在**零成本 + 结果确定**的前提下把主循环、工具、检查点、记忆、审批、
// 跨端派发这些路都走一遍。
//
// 用法：
//
//	mockllm --addr 127.0.0.1:8901 --script script.json
//
// 剧本文件是 JSON 数组，按次序消费（用得最多的是「先调工具、再总结」两步）：
//
//	[
//	  {"tool":"fs_write","args":{"path":"notes.md","content":"hello"}},
//	  {"text":"已经写好了"}
//	]
//
// 另外两个自检接口：
//
//	GET /_mock/state     当前进度 + 收到过的请求（可用来断言「模型到底看到了什么」）
//	POST /_mock/script   换一份剧本（不用重启）
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type step struct {
	Text string          `json:"text"`
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

type mock struct {
	mu       sync.Mutex
	script   []step
	idx      int
	served   int
	requests []requestDigest
}

// requestDigest 记下模型「看到了什么」，供自检断言（例如工具结果是否被截断）
type requestDigest struct {
	At       int64    `json:"at"`
	Model    string   `json:"model"`
	Tools    []string `json:"tools"`
	Messages []struct {
		Role    string `json:"role"`
		Name    string `json:"name"`
		Content string `json:"content"`
	} `json:"messages"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8901", "监听地址")
	scriptPath := flag.String("script", "", "剧本文件（JSON 数组，可省略）")
	showVersion := flag.Bool("version", false, "打印版本后退出")
	flag.Parse()

	if *showVersion {
		fmt.Println("mockllm 1.0（自检用的最小 OpenAI 兼容通道）")
		return
	}

	m := &mock{}
	if *scriptPath != "" {
		raw, err := os.ReadFile(*scriptPath)
		if err != nil {
			log.Fatalf("读剧本失败：%v", err)
		}
		if err := json.Unmarshal(raw, &m.script); err != nil {
			log.Fatalf("剧本解析失败（应是 JSON 数组）：%v", err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"object": "list",
			"data":   []map[string]any{{"id": "mock-model", "object": "model"}},
		})
	})
	mux.HandleFunc("POST /v1/chat/completions", m.handleChat)
	mux.HandleFunc("GET /_mock/state", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		writeJSON(w, map[string]any{
			"served": m.served, "cursor": m.idx, "total": len(m.script),
			"requests": m.requests,
		})
	})
	mux.HandleFunc("POST /_mock/script", func(w http.ResponseWriter, r *http.Request) {
		var next []step
		if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
			writeJSON(w, map[string]any{"error": "剧本解析失败：" + err.Error()})
			return
		}
		m.mu.Lock()
		m.script, m.idx = next, 0
		m.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true, "total": len(next)})
	})

	log.Printf("mockllm 已启动：http://%s/  剧本 %d 步", *addr, len(m.script))
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务异常退出：%v", err)
	}
}

func (m *mock) handleChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Tools    []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Name    string `json:"name"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]any{"error": map[string]string{"message": "请求体解析失败：" + err.Error()}})
		return
	}

	m.mu.Lock()
	d := requestDigest{At: time.Now().UnixMilli(), Model: req.Model}
	for _, t := range req.Tools {
		d.Tools = append(d.Tools, t.Function.Name)
	}
	for _, msg := range req.Messages {
		d.Messages = append(d.Messages, struct {
			Role    string `json:"role"`
			Name    string `json:"name"`
			Content string `json:"content"`
		}{msg.Role, msg.Name, msg.Content})
	}
	m.requests = append(m.requests, d)
	if len(m.requests) > 40 {
		m.requests = m.requests[len(m.requests)-40:]
	}

	var cur step
	if m.idx < len(m.script) {
		cur = m.script[m.idx]
		m.idx++
	}
	m.served++
	done := m.idx >= len(m.script)
	m.mu.Unlock()

	msg := map[string]any{"role": "assistant"}
	finish := "stop"
	if cur.Tool != "" {
		args := strings.TrimSpace(string(cur.Args))
		if args == "" || args == "null" {
			args = "{}"
		}
		msg["content"] = cur.Text
		msg["tool_calls"] = []map[string]any{{
			"id":   fmt.Sprintf("call_mock_%d", m.served),
			"type": "function",
			"function": map[string]any{
				"name":      cur.Tool,
				"arguments": args,
			},
		}}
		finish = "tool_calls"
	} else if cur.Text != "" {
		msg["content"] = cur.Text
	} else if done {
		msg["content"] = "（mock 通道：剧本已用完）"
	} else {
		msg["content"] = "（mock 通道：这一步没有内容）"
	}

	prompt := 0
	for _, x := range req.Messages {
		prompt += len([]rune(x.Content))/3 + 4
	}
	writeJSON(w, map[string]any{
		"id":      fmt.Sprintf("chatcmpl-mock-%d", m.served),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]any{{"index": 0, "finish_reason": finish, "message": msg}},
		"usage": map[string]int{
			"prompt_tokens": prompt, "completion_tokens": 8, "total_tokens": prompt + 8,
		},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
