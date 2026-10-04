package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"baize/internal/llm"
)

// TestMain 让同一个测试二进制可以扮演"一个最小 MCP 服务"（stdio 用）
func TestMain(m *testing.M) {
	if os.Getenv("BAIZE_MCP_HELPER") == "1" {
		runHelperStdioServer()
		return
	}
	os.Exit(m.Run())
}

// runHelperStdioServer 最小 MCP 服务：initialize / tools.list / tools.call，一行一条 JSON
func runHelperStdioServer() {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req rpcRequest
		if err := dec.Decode(&req); err != nil {
			return
		}
		if req.ID == 0 {
			continue // 通知，不回
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			resp["result"] = map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake-mcp", "version": "1.0.0"},
			}
		case "tools/list":
			resp["result"] = map[string]any{"tools": []map[string]any{
				{
					"name": "echo", "description": "原样返回 text",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"text": map[string]any{"type": "string"}},
					},
				},
				{"name": "boom", "description": "总是失败", "inputSchema": map[string]any{"type": "object"}},
			}}
		case "tools/call":
			params, _ := req.Params.(map[string]any)
			name := strings.TrimSpace(fmt.Sprint(params["name"]))
			args, _ := params["arguments"].(map[string]any)
			switch name {
			case "echo":
				resp["result"] = map[string]any{
					"content": []map[string]any{{"type": "text", "text": "echo:" + strings.TrimSpace(fmt.Sprint(args["text"]))}},
				}
			case "boom":
				resp["result"] = map[string]any{
					"isError": true,
					"content": []map[string]any{{"type": "text", "text": "远端炸了"}},
				}
			default:
				resp["error"] = &RPCError{Code: codeMethodNotFound, Message: "没有这个工具：" + name}
			}
		default:
			resp["error"] = &RPCError{Code: codeMethodNotFound, Message: "不支持的方法：" + req.Method}
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func helperConfig() ServerConfig {
	return ServerConfig{
		Name: "fake", Transport: "stdio",
		Command: os.Args[0], Env: []string{"BAIZE_MCP_HELPER=1"},
		Enabled: true, TimeoutSec: 10,
	}
}

func TestClientStdio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := Connect(ctx, helperConfig(), false)
	if err != nil {
		t.Fatalf("连接 stdio MCP 失败：%v", err)
	}
	defer c.Close()

	info := c.Info()
	if info.ServerInfo.Name != "fake-mcp" {
		t.Fatalf("服务名不对：%+v", info)
	}
	tools, err := c.RefreshTools(ctx)
	if err != nil {
		t.Fatalf("拉工具清单失败：%v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("工具数应为 2，实际 %d（%+v）", len(tools), tools)
	}

	res, err := c.CallTool(ctx, "echo", map[string]any{"text": "你好"})
	if err != nil {
		t.Fatalf("调 echo 失败：%v", err)
	}
	if got := res.Text(); got != "echo:你好" {
		t.Fatalf("echo 返回不对：%q", got)
	}

	res, err = c.CallTool(ctx, "boom", nil)
	if err != nil {
		t.Fatalf("调 boom 不应该返回 RPC 错误：%v", err)
	}
	if !res.IsError {
		t.Fatalf("boom 应该标记 isError=true：%+v", res)
	}

	if _, err := c.CallTool(ctx, "nope", nil); err == nil {
		t.Fatal("调用不存在的工具应该报错")
	} else if !isMethodNotFound(err) {
		t.Fatalf("错误应该是方法不存在：%v", err)
	}
}

func TestManagerHotPlug(t *testing.T) {
	m := NewManager(nil, false)
	defer m.Close()

	st := m.Apply([]ServerConfig{helperConfig()})
	if len(st) != 1 || st[0].Status != "connected" {
		t.Fatalf("应该连接成功：%+v", st)
	}
	if st[0].ToolCount != 2 {
		t.Fatalf("工具数应为 2：%+v", st[0])
	}

	refs := m.ToolRefs()
	if len(refs) != 2 {
		t.Fatalf("应产出 2 个工具引用：%+v", refs)
	}
	names := map[string]ToolRef{}
	for _, r := range refs {
		names[r.Name] = r
	}
	echo, ok := names["mcp_fake_echo"]
	if !ok {
		t.Fatalf("工具名不对（应有 mcp_fake_echo）：%+v", names)
	}
	if !echo.Dangerous {
		t.Fatal("默认情况下 MCP 工具必须走审批（Dangerous=true）")
	}

	// 显式信任 + echo 列进安全名单
	cfg := helperConfig()
	cfg.SafeTools = []string{"echo"}
	st = m.Apply([]ServerConfig{cfg})
	if len(st) != 1 || st[0].Status != "connected" {
		t.Fatalf("改配置后应仍是连接状态：%+v", st)
	}
	refs = m.ToolRefs()
	for _, r := range refs {
		if r.Remote == "echo" && r.Dangerous {
			t.Fatal("列进 safeTools 的 echo 应该免审批")
		}
		if r.Remote == "boom" && !r.Dangerous {
			t.Fatal("没列进 safeTools 的 boom 仍要审批")
		}
	}

	// 同一份配置再 Apply 一次：不应重连
	before := m.Status()[0].ConnectedAt
	_ = m.Apply([]ServerConfig{cfg})
	if after := m.Status()[0].ConnectedAt; after != before {
		t.Fatalf("配置没变不应该重连（%d → %d）", before, after)
	}

	// 热拔：移除服务
	st = m.Apply(nil)
	if len(st) != 0 {
		t.Fatalf("移除后不该还有服务：%+v", st)
	}
	if got := m.ToolRefs(); len(got) != 0 {
		t.Fatalf("移除后不该还有工具：%+v", got)
	}
}

func TestManagerBadConfig(t *testing.T) {
	m := NewManager(nil, false)
	defer m.Close()

	st := m.Apply([]ServerConfig{{Name: "bad", Enabled: true}})
	if len(st) != 1 || st[0].Status != "error" {
		t.Fatalf("坏配置应该显示 error 状态（不能静默消失）：%+v", st)
	}
	if !strings.Contains(st[0].Error, "command") {
		t.Fatalf("错误信息应说明缺什么：%q", st[0].Error)
	}

	// 远端地址默认不放行
	remote := ServerConfig{Name: "remote", Transport: "http", URL: "http://mcp.example.com/mcp", Enabled: true}
	st = m.Apply([]ServerConfig{remote})
	if st[0].Status != "error" || !strings.Contains(st[0].Error, "allowRemote") {
		t.Fatalf("远端 MCP 默认应该被拒并提示 allowRemote：%+v", st[0])
	}

	// 禁用状态要保留配置
	off := helperConfig()
	off.Enabled = false
	st = m.Apply([]ServerConfig{off})
	if st[0].Status != "disabled" {
		t.Fatalf("禁用状态不对：%+v", st[0])
	}
	if cfgs := m.Configs(); len(cfgs) != 1 || cfgs[0].Enabled {
		t.Fatalf("禁用的配置也要保留下来：%+v", cfgs)
	}
}

func TestManagerUpsertAndRemove(t *testing.T) {
	m := NewManager(nil, false)
	defer m.Close()

	if _, err := m.Upsert(ServerConfig{Name: "x", Command: "nope.exe", Enabled: true}); err != nil {
		t.Fatalf("Upsert 失败：%v", err)
	}
	if len(m.Configs()) != 1 {
		t.Fatalf("应该有 1 个服务：%+v", m.Configs())
	}
	if _, err := m.Upsert(helperConfig()); err != nil {
		t.Fatalf("Upsert 第二个失败：%v", err)
	}
	if got := len(m.Configs()); got != 2 {
		t.Fatalf("应该有 2 个服务，实际 %d", got)
	}
	if st := m.Remove("x"); len(st) != 1 {
		t.Fatalf("移除后应剩 1 个：%+v", st)
	}
	if _, err := m.Upsert(ServerConfig{}); err == nil {
		t.Fatal("没有 name 的 Upsert 应该报错")
	}
}

/* ---------- HTTP 传输 ---------- */

func TestClientHTTPJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "sess-1")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": ProtocolVersion, "serverInfo": map[string]any{"name": "http-mcp"}},
			})
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "sess-1" {
				t.Errorf("后续请求应带上会话 id，实际 %q", r.Header.Get("Mcp-Session-Id"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"tools": []map[string]any{{"name": "ping", "description": "探活"}}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "不支持"},
			})
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	c, err := Connect(ctx, ServerConfig{Name: "http", Transport: "http", URL: srv.URL, Enabled: true, TimeoutSec: 5}, false)
	if err != nil {
		t.Fatalf("连接 HTTP MCP 失败：%v", err)
	}
	defer c.Close()
	if c.Info().ServerInfo.Name != "http-mcp" {
		t.Fatalf("服务信息不对：%+v", c.Info())
	}
	tools, err := c.RefreshTools(ctx)
	if err != nil || len(tools) != 1 {
		t.Fatalf("工具清单不对：%v %+v", err, tools)
	}
}

func TestClientHTTPSSE(t *testing.T) {
	var sse bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		_ = json.Unmarshal(body, &req)
		if req.Method == "initialize" && !sse {
			// 第一次用 SSE 回，验证流式响应也能解析
			sse = true
			w.Header().Set("Content-Type", "text/event-stream")
			payload, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": ProtocolVersion, "serverInfo": map[string]any{"name": "sse-mcp"}},
			})
			_, _ = w.Write([]byte("event: message\ndata: " + string(payload) + "\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{"tools": []map[string]any{}},
		})
	}))
	defer srv.Close()

	c, err := Connect(context.Background(),
		ServerConfig{Name: "sse", Transport: "http", URL: srv.URL, Enabled: true, TimeoutSec: 5}, false)
	if err != nil {
		t.Fatalf("SSE 连接失败：%v", err)
	}
	defer c.Close()
	if c.Info().ServerInfo.Name != "sse-mcp" {
		t.Fatalf("SSE 服务信息不对：%+v", c.Info())
	}
}

func TestServerToolRunAndUnknownServer(t *testing.T) {
	m := NewManager(nil, false)
	defer m.Close()
	_ = m.Apply([]ServerConfig{helperConfig()})

	var ref ToolRef
	for _, r := range m.ToolRefs() {
		if r.Remote == "echo" {
			ref = r
		}
	}
	tool := NewServerTool(m, ref)
	if tool.Name() != "mcp_fake_echo" {
		t.Fatalf("工具名不对：%s", tool.Name())
	}
	out, err := tool.Run(context.Background(), map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}
	if s, _ := out.(map[string]any)["text"].(string); s != "echo:hi" {
		t.Fatalf("返回不对：%+v", out)
	}

	// boom 是远端失败：必须返回错误，不能假装成功
	for _, r := range m.ToolRefs() {
		if r.Remote != "boom" {
			continue
		}
		if _, err := NewServerTool(m, r).Run(context.Background(), nil); err == nil {
			t.Fatal("远端 isError=true 时必须返回错误")
		}
	}

	// 热拔之后旧包装必须明确报错，而不是静默成功
	m.Remove("fake")
	if _, err := tool.Run(context.Background(), nil); err == nil {
		t.Fatal("服务已下线，旧工具包装调用必须报错")
	}
}

func TestLocalURL(t *testing.T) {
	local := []string{"http://127.0.0.1:3000/mcp", "http://localhost/mcp", "http://192.168.1.9:8080/sse", "https://10.1.2.3/x"}
	for _, u := range local {
		if !isLocalURL(u) {
			t.Fatalf("%s 应判定为本机/内网", u)
		}
	}
	remote := []string{"https://mcp.example.com/mcp", "http://8.8.8.8:80/x", "https://api.githubcopilot.com/mcp"}
	for _, u := range remote {
		if isLocalURL(u) {
			t.Fatalf("%s 不该判定为本机", u)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"github mcp": "github_mcp",
		"a/b":        "a_b",
		"   ":        "server",
		"ok.name-1":  "ok_name-1", // 点号必须换掉：DeepSeek/OpenAI 只接受 [a-zA-Z0-9_-]
		"read.file":  "read_file",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Fatalf("sanitizeName(%q)=%q，期望 %q", in, got, want)
		}
	}
}

// 拼出来的工具名必须永远符合模型接口的要求（含超长截断）
func TestToolNameIsAlwaysValid(t *testing.T) {
	cfg, err := ServerConfig{Name: "my server", Command: "x", Enabled: true}.normalize(false)
	if err != nil {
		t.Fatalf("规范化失败：%v", err)
	}
	cases := []string{"echo", "read.file", "a/b/c", strings.Repeat("long", 40)}
	for _, remote := range cases {
		name := cfg.ToolName(remote)
		if !llm.ValidToolName(name) {
			t.Fatalf("工具名 %q 不合法（远端 %q）", name, remote)
		}
	}
	if name := cfg.ToolName("echo"); name != "mcp_my_server_echo" {
		t.Fatalf("工具名拼法不对：%s", name)
	}
	long := cfg.ToolName(strings.Repeat("long", 40))
	if len(long) > 64 || !llm.ValidToolName(long) {
		t.Fatalf("超长工具名应截断到 64 以内：%q（%d）", long, len(long))
	}
}

func TestApplyIsConcurrentSafe(t *testing.T) {
	m := NewManager(nil, false)
	defer m.Close()
	_ = m.Apply([]ServerConfig{helperConfig()})
	var done int32
	go func() {
		for i := 0; i < 20; i++ {
			_ = m.Status()
			_ = m.ToolRefs()
			_ = m.Configs()
		}
		atomic.AddInt32(&done, 1)
	}()
	for i := 0; i < 5; i++ {
		_ = m.Apply([]ServerConfig{helperConfig()})
	}
	for atomic.LoadInt32(&done) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
}
