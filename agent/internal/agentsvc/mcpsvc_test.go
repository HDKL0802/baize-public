package agentsvc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"baize/internal/backup"
	"baize/internal/config"
	"baize/internal/mcp"
)

// fakeMCP 一个走 HTTP 的最小 MCP 服务（本地地址，默认策略就放行）
func fakeMCP(t *testing.T, serverName string, toolName string, answer string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		if req.ID == 0 {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0"}`))
			return
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			resp["result"] = map[string]any{
				"protocolVersion": mcp.ProtocolVersion,
				"serverInfo":      map[string]any{"name": serverName, "version": "9.9"},
			}
		case "tools/list":
			resp["result"] = map[string]any{"tools": []map[string]any{{
				"name":        toolName,
				"description": "回显文本",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
			}}}
		case "tools/call":
			resp["result"] = map[string]any{
				"content": []map[string]any{{"type": "text", "text": answer}},
			}
		default:
			resp["error"] = map[string]any{"code": -32601, "message": "不支持 " + req.Method}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 热插拔生命周期：加 → 连上（工具动态注册）→ 改（安全名单）→ 删
func TestMCPHotPlugLifecycle(t *testing.T) {
	srv := fakeMCP(t, "demo-mcp", "echo", "echo:hi")
	s := newService(t, nil, func(c *config.Config) { c.ApprovalTimeoutSec = 1 })

	states, err := s.MCPUpsert(mcp.ServerConfig{
		Name: "demo", Transport: "http", URL: srv.URL, Enabled: true,
	})
	if err != nil {
		t.Fatalf("加 MCP 服务失败：%v", err)
	}
	if len(states) != 1 || states[0].Status != "connected" || states[0].ToolCount != 1 {
		t.Fatalf("状态不对：%+v", states)
	}
	if states[0].ServerName != "demo-mcp" || states[0].Protocol == "" {
		t.Fatalf("应记录服务端信息：%+v", states[0])
	}
	if !strings.Contains(s.MCPHints(), "demo=已连接") {
		t.Fatalf("状态提示不对：%q", s.MCPHints())
	}

	// 配置要落盘：重新读配置也还在
	cfg, err := config.Load(s.DataDir())
	if err != nil {
		t.Fatalf("读配置失败：%v", err)
	}
	if len(cfg.MCPServers) != 1 || cfg.MCPServers[0].Name != "demo" {
		t.Fatalf("MCP 配置没保存：%+v", cfg.MCPServers)
	}

	// 手动调用
	res, err := s.MCPCall(context.Background(), "demo", "echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("手动调用失败：%v", err)
	}
	if res.Text() != "echo:hi" {
		t.Fatalf("返回不对：%q", res.Text())
	}
	if _, err := s.MCPCall(context.Background(), "nope", "echo", nil); err == nil {
		t.Fatal("调用不存在的服务必须报错")
	}

	// 重连
	if _, err := s.MCPReload("demo"); err != nil {
		t.Fatalf("重连失败：%v", err)
	}
	// 删掉
	states, err = s.MCPRemove("demo")
	if err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if len(states) != 0 {
		t.Fatalf("删除后不该还有服务：%+v", states)
	}
	if _, err := s.MCPRemove("demo"); err == nil {
		t.Fatal("删不存在的服务必须报错")
	}
}

// 端到端：模型调用 MCP 工具。默认必须走审批；列进 safeTools 后才免批
func TestMCPToolCallEndToEnd(t *testing.T) {
	srv := fakeMCP(t, "demo-mcp", "echo", "echo:hi")
	// 脚本按上下文决定：已经拿到工具结果就收工，否则调 MCP 工具
	script := func(req map[string]any) map[string]any {
		if msgs, ok := req["messages"].([]any); ok {
			for _, m := range msgs {
				if mm, ok := m.(map[string]any); ok && mm["role"] == "tool" {
					return sayBody("已经调过 MCP 工具了")
				}
			}
		}
		return toolBody("c1", "mcp_demo_echo", map[string]any{"text": "hi"})
	}
	f := newFakeLLM(t, script)
	s := newService(t, f, func(c *config.Config) {
		c.ApprovalTimeoutSec = 1
		c.MCPServers = []mcp.ServerConfig{{Name: "demo", Transport: "http", URL: srv.URL, Enabled: true}}
	})

	// 1) 默认（不在 safeTools、没信任）：应被审批闸门拦下
	res, _ := s.Run(context.Background(), "用 MCP 工具回显 hi", "chat", "")
	if len(res.Errors) == 0 || !strings.Contains(strings.Join(res.Errors, " "), "审批") {
		t.Fatalf("默认应该走审批并被拦下：%+v", res.Errors)
	}

	// 2) 列进 safeTools：免审批，工具真的被调用
	if _, err := s.MCPUpsert(mcp.ServerConfig{
		Name: "demo", Transport: "http", URL: srv.URL, Enabled: true, SafeTools: []string{"echo"},
	}); err != nil {
		t.Fatalf("改配置失败：%v", err)
	}
	res, err := s.Run(context.Background(), "用 MCP 工具回显 hi", "chat", "")
	if err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	if res.ToolCalls != 1 || len(res.Trace) != 1 {
		t.Fatalf("应该调用了一次 MCP 工具：%+v", res.Trace)
	}
	if res.Trace[0].Error != "" || !strings.Contains(res.Trace[0].Result, "echo:hi") {
		t.Fatalf("MCP 工具调用轨迹不对：%+v", res.Trace[0])
	}
	if !strings.Contains(res.Text, "MCP") {
		t.Fatalf("最终结论不对：%q", res.Text)
	}
	// 系统提示里要能看见这些外部工具（模型才知道能调）
	var sysPrompt string
	f.mu.Lock()
	if len(f.bodies) > 0 {
		if msgs, ok := f.bodies[0]["messages"].([]any); ok && len(msgs) > 0 {
			if m, ok := msgs[0].(map[string]any); ok {
				sysPrompt, _ = m["content"].(string)
			}
		}
	}
	f.mu.Unlock()
	if !strings.Contains(sysPrompt, "mcp_demo_echo") {
		t.Fatalf("系统提示里应列出 MCP 工具：%q", briefText(sysPrompt))
	}
}

// MCP 连不上时必须明确报错状态，绝不静默
func TestMCPConnectFailureIsVisible(t *testing.T) {
	s := newService(t, nil, nil)
	states, err := s.MCPUpsert(mcp.ServerConfig{
		Name: "dead", Transport: "http", URL: "http://127.0.0.1:1/mcp", Enabled: true, TimeoutSec: 1,
	})
	if err != nil {
		t.Fatalf("保存配置不该失败（配置本身合法）：%v", err)
	}
	if len(states) != 1 || states[0].Status != "error" || states[0].Error == "" {
		t.Fatalf("连接失败必须显示 error 与原因：%+v", states)
	}
	if len(s.MCPTools()) != 0 {
		t.Fatal("连不上的服务不该产出工具")
	}
}

/* ---------- 备份 ---------- */

func TestBackupLifecycle(t *testing.T) {
	s := newService(t, nil, nil)
	// 造点数据
	cfg := s.Config()
	cfg.Workdir = ""
	if err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBackup(backup.Selection{}, "单测全选"); err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	list, err := s.Backups()
	if err != nil || len(list) != 1 {
		t.Fatalf("备份列表不对：%v %+v", err, list)
	}
	name := list[0].Name
	if _, err := s.VerifyBackup(name); err != nil {
		t.Fatalf("校验失败：%v", err)
	}
	// 试恢复不改数据
	res, err := s.RestoreBackup(name, backup.RestoreOptions{DryRun: true})
	if err != nil || !res.DryRun || len(res.Restored) == 0 {
		t.Fatalf("试恢复不对：%v %+v", err, res)
	}
	// 真恢复：后端正在运行、数据库文件被锁 → 必须明确报错且不破坏数据（并留下安全点）
	res, err = s.RestoreBackup(name, backup.RestoreOptions{})
	if err == nil {
		t.Fatalf("后端运行时数据库被占用，恢复应当明确报错：%+v", res)
	}
	if !strings.Contains(err.Error(), "占用") {
		t.Fatalf("错误信息要说明原因和怎么办：%v", err)
	}
	if res.SafePoint == "" {
		t.Fatal("即使恢复失败，也该留下安全点")
	}
	if list2, _ := s.Backups(); len(list2) != 2 {
		t.Fatalf("安全点也是一份备份，应有 2 份：%+v", list2)
	}
	// 配置仍然完好（没被半截覆盖）
	if _, err := os.Stat(filepath.Join(s.DataDir(), "config.json")); err != nil {
		t.Fatalf("恢复失败不该破坏现有数据：%v", err)
	}
	// 越界与不存在都要明确报错
	if _, err := s.VerifyBackup(filepath.Join(s.DataDir(), "no-such.zip")); err == nil {
		t.Fatal("不存在的备份应报错")
	}
	if _, err := s.VerifyBackup(filepath.Join(os.TempDir(), "x.zip")); err == nil {
		t.Fatal("备份目录之外的路径必须拒绝")
	}
	if err := s.DeleteBackup(filepath.Join(s.DataDir(), "config.json")); err == nil {
		t.Fatal("只允许删备份目录里的 zip")
	}
	if err := s.DeleteBackup(name); err != nil {
		t.Fatalf("删除备份失败：%v", err)
	}
}

func briefText(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) > 300 {
		return string([]rune(s)[:300]) + "…"
	}
	return s
}
