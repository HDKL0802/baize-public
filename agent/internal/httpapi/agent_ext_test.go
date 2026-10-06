package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"baize/internal/agentsvc"
	"baize/internal/httpapi"
	"baize/internal/hub"
	"baize/internal/logx"
	"baize/internal/mcp"
	"baize/internal/memory"
	"baize/internal/store"
)

// writeFile 造一个文件（含父目录）
func writeFile(p, content string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// newAgentEnv 造一个挂了 Agent 服务的后端（MCP / 备份接口都要它）
func newAgentEnv(t *testing.T) (*env, *agentsvc.Service) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/agent.db")
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() { st.Close() })
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(st, lg, testToken, 5*time.Second)
	svc, err := agentsvc.New(t.TempDir(), lg, agentsvc.WithDevices(h))
	if err != nil {
		t.Fatalf("启动 Agent 服务失败：%v", err)
	}
	t.Cleanup(svc.Close)

	s := httpapi.New(h, lg, logx.NewRing(200), "test")
	s.SetAgent(svc)
	s.SetKB(svc.KB()) // 与 cmd/backend 的装配一致：知识库接口也要挂上
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, hub: h, store: st, token: testToken}, svc
}

// fakeMCPHTTP 一个最小的 MCP HTTP 服务（本地地址）
func fakeMCPHTTP(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		if req.ID == 0 {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			resp["result"] = map[string]any{
				"protocolVersion": mcp.ProtocolVersion,
				"serverInfo":      map[string]any{"name": "api-mcp", "version": "1.0"},
			}
		case "tools/list":
			resp["result"] = map[string]any{"tools": []map[string]any{{"name": "ping", "description": "探活"}}}
		case "tools/call":
			resp["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": "pong"}}}
		default:
			resp["error"] = map[string]any{"code": -32601, "message": "不支持"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeOpenAI 一个最小的 OpenAI 兼容服务（localhost，默认策略就放行）
func fakeOpenAI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		var req struct {
			ID int64 `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "fake-openai",
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "能通"},
			}},
			"usage": map[string]any{"prompt_tokens": 9, "completion_tokens": 2, "total_tokens": 11},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAgentProviderAPI(t *testing.T) {
	e, _ := newAgentEnv(t)
	openai := fakeOpenAI(t)

	if code := e.do("GET", "/api/agent/providers", nil, false, nil); code != 401 {
		t.Fatalf("没有令牌应 401，实际 %d", code)
	}

	var got struct {
		Providers   []map[string]any `json:"providers"`
		AllowRemote bool             `json:"allowRemote"`
	}
	if code := e.do("GET", "/api/agent/providers", nil, true, &got); code != 200 {
		t.Fatalf("读通道失败：%d", code)
	}
	if len(got.Providers) != 0 || got.AllowRemote {
		t.Fatalf("初始应该没有通道、也没开远端：%+v", got)
	}

	// 加一条本机通道
	var up struct {
		Providers []map[string]any `json:"providers"`
		Saved     string           `json:"saved"`
	}
	code := e.do("POST", "/api/agent/providers", map[string]any{
		"action": "upsert",
		"config": map[string]any{
			"name": "local", "protocol": "openai",
			"baseUrl": openai.URL + "/v1", "model": "fake-openai", "apiKey": "sk-x",
		},
	}, true, &up)
	if code != 200 || up.Saved != "local" || len(up.Providers) != 1 {
		t.Fatalf("upsert 失败：%d %+v", code, up)
	}
	if up.Providers[0]["usable"] != true {
		t.Fatalf("通道应可用：%+v", up.Providers[0])
	}
	// key 只回"是否配置"，绝不回显
	if raw, _ := json.Marshal(up.Providers[0]); strings.Contains(string(raw), "sk-x") {
		t.Fatalf("接口把 API Key 回显出来了：%s", raw)
	}

	// 探测（打到本地假模型，不花额度）
	var test struct {
		Result map[string]any `json:"result"`
		Error  string         `json:"error"`
	}
	if code := e.do("POST", "/api/agent/providers", map[string]any{"action": "test", "name": "local"}, true, &test); code != 200 {
		t.Fatalf("探测失败：%d", code)
	}
	if test.Error != "" || test.Result["reply"] != "能通" || test.Result["totalTokens"] != float64(11) {
		t.Fatalf("探测结果不对：%+v", test)
	}

	// 远端地址必须显式放行
	code = e.do("POST", "/api/agent/providers", map[string]any{
		"action": "upsert",
		"config": map[string]any{"name": "cloud", "baseUrl": "https://api.deepseek.com/v1", "model": "deepseek-chat"},
	}, true, nil)
	if code != 400 {
		t.Fatalf("远端地址未放行应 400，实际 %d", code)
	}
	code = e.do("POST", "/api/agent/providers", map[string]any{
		"action":      "upsert",
		"allowRemote": true,
		"config":      map[string]any{"name": "cloud", "baseUrl": "https://api.deepseek.com/v1", "model": "deepseek-chat"},
	}, true, &up)
	if code != 200 || len(up.Providers) != 2 {
		t.Fatalf("放行后应该能加：%d %+v", code, up)
	}
	var after struct {
		AllowRemote bool `json:"allowRemote"`
	}
	e.do("GET", "/api/agent/providers", nil, true, &after)
	if !after.AllowRemote {
		t.Fatal("allowRemote 应已打开")
	}

	// 未知 action / 删不存在的都要明确报错
	if code := e.do("POST", "/api/agent/providers", map[string]any{"action": "nope"}, true, nil); code != 400 {
		t.Fatalf("未知 action 应 400，实际 %d", code)
	}
	if code := e.do("POST", "/api/agent/providers", map[string]any{"action": "remove", "name": "nope"}, true, nil); code != 400 {
		t.Fatalf("删不存在的通道应 400，实际 %d", code)
	}
	var removed struct {
		Providers []map[string]any `json:"providers"`
	}
	if code := e.do("POST", "/api/agent/providers", map[string]any{"action": "remove", "name": "cloud"}, true, &removed); code != 200 {
		t.Fatalf("删除失败：%d", code)
	}
	if len(removed.Providers) != 1 {
		t.Fatalf("删除后应剩 1 条：%+v", removed.Providers)
	}
}

// 通道充值记账：加一笔 / 读出来 / 金额非法报 400 / 删掉就没了
func TestAgentProviderRechargeAPI(t *testing.T) {
	e, _ := newAgentEnv(t)
	openai := fakeOpenAI(t)

	// 记一笔充值必须先有这条通道
	if code := e.do("POST", "/api/agent/providers/recharge", map[string]any{
		"action": "add", "name": "ghost", "amount": 10,
	}, true, nil); code != 400 {
		t.Fatalf("给不存在的通道充值应 400，实际 %d", code)
	}

	// 先加一条本机通道
	if code := e.do("POST", "/api/agent/providers", map[string]any{
		"action": "upsert",
		"config": map[string]any{
			"name": "local", "protocol": "openai",
			"baseUrl": openai.URL + "/v1", "model": "fake-openai",
		},
	}, true, nil); code != 200 {
		t.Fatalf("加通道失败：%d", code)
	}

	// 初始没有充值流水
	var empty struct {
		Recharges map[string][]map[string]any `json:"recharges"`
	}
	if code := e.do("GET", "/api/agent/providers/recharge", nil, true, &empty); code != 200 {
		t.Fatalf("读充值流水失败：%d", code)
	}
	if len(empty.Recharges) != 0 {
		t.Fatalf("初始不该有充值流水：%+v", empty.Recharges)
	}

	// 加一笔
	var added struct {
		OK    bool           `json:"ok"`
		Entry map[string]any `json:"entry"`
	}
	if code := e.do("POST", "/api/agent/providers/recharge", map[string]any{
		"action": "add", "name": "local", "amount": 100.5, "note": "首充",
	}, true, &added); code != 200 {
		t.Fatalf("记充值失败：%d", code)
	}
	if !added.OK || added.Entry["id"] == "" || added.Entry["amount"] != 100.5 {
		t.Fatalf("充值回执不对：%+v", added)
	}
	id, _ := added.Entry["id"].(string)

	// GET 要能看到
	var got struct {
		Recharges map[string][]map[string]any `json:"recharges"`
	}
	if code := e.do("GET", "/api/agent/providers/recharge", nil, true, &got); code != 200 {
		t.Fatalf("读充值流水失败：%d", code)
	}
	if len(got.Recharges["local"]) != 1 || got.Recharges["local"][0]["id"] != id {
		t.Fatalf("充值流水没落上：%+v", got.Recharges)
	}

	// 金额 <= 0 一律 400
	for _, amt := range []float64{0, -1} {
		if code := e.do("POST", "/api/agent/providers/recharge", map[string]any{
			"action": "add", "name": "local", "amount": amt,
		}, true, nil); code != 400 {
			t.Fatalf("金额 %v 应 400，实际 %d", amt, code)
		}
	}

	// 删掉就没了
	var removed struct {
		OK      bool   `json:"ok"`
		Removed string `json:"removed"`
	}
	if code := e.do("POST", "/api/agent/providers/recharge", map[string]any{
		"action": "remove", "name": "local", "id": id,
	}, true, &removed); code != 200 {
		t.Fatalf("删充值失败：%d", code)
	}
	if !removed.OK || removed.Removed != id {
		t.Fatalf("删除回执不对：%+v", removed)
	}
	var after struct {
		Recharges map[string][]map[string]any `json:"recharges"`
	}
	e.do("GET", "/api/agent/providers/recharge", nil, true, &after)
	if len(after.Recharges["local"]) != 0 {
		t.Fatalf("删除后不该还有：%+v", after.Recharges)
	}

	// 没有令牌一律 401
	if code := e.do("GET", "/api/agent/providers/recharge", nil, false, nil); code != 401 {
		t.Fatalf("没有令牌应 401，实际 %d", code)
	}
}

func TestAgentMCPAPI(t *testing.T) {
	e, _ := newAgentEnv(t)
	mcpSrv := fakeMCPHTTP(t)

	// 没有令牌一律 401
	if code := e.do("GET", "/api/agent/mcp", nil, false, nil); code != 401 {
		t.Fatalf("没有令牌应 401，实际 %d", code)
	}

	var got struct {
		Servers []mcp.ServerState `json:"servers"`
	}
	if code := e.do("GET", "/api/agent/mcp", nil, true, &got); code != 200 {
		t.Fatalf("读 MCP 状态失败：%d", code)
	}
	if len(got.Servers) != 0 {
		t.Fatalf("初始应该没有 MCP 服务：%+v", got.Servers)
	}

	// 加一个服务：应立刻连上并带上工具
	var up struct {
		Servers []mcp.ServerState `json:"servers"`
	}
	code := e.do("POST", "/api/agent/mcp", map[string]any{
		"action": "upsert",
		"config": map[string]any{"name": "api", "transport": "http", "url": mcpSrv.URL, "enabled": true},
	}, true, &up)
	if code != 200 {
		t.Fatalf("upsert 失败：%d", code)
	}
	if len(up.Servers) != 1 || up.Servers[0].Status != "connected" || up.Servers[0].ToolCount != 1 {
		t.Fatalf("状态不对：%+v", up.Servers)
	}

	// 手动调用远端工具
	var call struct {
		Result map[string]any `json:"result"`
		Error  string         `json:"error"`
	}
	if code := e.do("POST", "/api/agent/mcp", map[string]any{
		"action": "call", "server": "api", "tool": "ping",
	}, true, &call); code != 200 {
		t.Fatalf("调用失败：%d", code)
	}
	if call.Error != "" || call.Result["text"] != "pong" {
		t.Fatalf("调用结果不对：%+v", call)
	}

	// 未知 action 要明确报错
	if code := e.do("POST", "/api/agent/mcp", map[string]any{"action": "nope"}, true, nil); code != 400 {
		t.Fatalf("未知 action 应 400，实际 %d", code)
	}

	// 远端地址默认不放行
	if code := e.do("POST", "/api/agent/mcp", map[string]any{
		"action": "upsert",
		"config": map[string]any{"name": "remote", "transport": "http", "url": "https://mcp.example.com/mcp", "enabled": true},
	}, true, nil); code != 200 {
		t.Fatalf("保存合法配置应 200（连接失败体现在状态里）：%d", code)
	}
	var after struct {
		Servers []mcp.ServerState `json:"servers"`
	}
	e.do("GET", "/api/agent/mcp", nil, true, &after)
	for _, s := range after.Servers {
		if s.Name == "remote" && (s.Status != "error" || s.Error == "") {
			t.Fatalf("远端 MCP 应显示 error 及原因：%+v", s)
		}
	}

	// 下线
	var removed struct {
		Servers []mcp.ServerState `json:"servers"`
	}
	if code := e.do("POST", "/api/agent/mcp", map[string]any{"action": "remove", "server": "api"}, true, &removed); code != 200 {
		t.Fatalf("下线失败：%d", code)
	}
	for _, s := range removed.Servers {
		if s.Name == "api" {
			t.Fatalf("下线后不该还有 api：%+v", removed.Servers)
		}
	}
}

func TestAgentBackupAPI(t *testing.T) {
	e, svc := newAgentEnv(t)

	// 造点数据，保证有东西可备份
	if err := writeFile(svc.DataDir()+"/skills/demo/SKILL.md", "# 演示\n"); err != nil {
		t.Fatal(err)
	}

	if code := e.do("POST", "/api/agent/backups", nil, false, nil); code != 401 {
		t.Fatalf("没有令牌应 401，实际 %d", code)
	}

	var created struct {
		Manifest struct {
			Entries   []map[string]any `json:"entries"`
			TotalSize int64            `json:"totalSize"`
		} `json:"manifest"`
		Dir string `json:"dir"`
	}
	if code := e.do("POST", "/api/agent/backups", map[string]any{"note": "接口单测"}, true, &created); code != 200 {
		t.Fatalf("创建备份失败：%d", code)
	}
	if len(created.Manifest.Entries) == 0 {
		t.Fatal("备份里应该有文件")
	}

	var list struct {
		Backups []struct {
			Name     string `json:"name"`
			Manifest *struct {
				CreatedAt int64 `json:"createdAt"`
			} `json:"manifest"`
		} `json:"backups"`
		Dir string `json:"dir"`
	}
	if code := e.do("GET", "/api/agent/backups", nil, true, &list); code != 200 || len(list.Backups) != 1 {
		t.Fatalf("备份列表不对：%d %+v", code, list)
	}
	name := list.Backups[0].Name

	// 校验
	if code := e.do("POST", "/api/agent/backups/verify", map[string]any{"path": name}, true, nil); code != 200 {
		t.Fatalf("校验应通过：%d", code)
	}
	// 试恢复
	var dry struct {
		Result map[string]any `json:"result"`
		Error  string         `json:"error"`
	}
	if code := e.do("POST", "/api/agent/backups/restore", map[string]any{"path": name, "dryRun": true}, true, &dry); code != 200 {
		t.Fatalf("试恢复失败：%d", code)
	}
	if dry.Error != "" {
		t.Fatalf("试恢复不该报错：%s", dry.Error)
	}
	if files, _ := dry.Result["restored"].([]any); len(files) == 0 {
		t.Fatalf("试恢复应列出文件：%+v", dry.Result)
	}
	// 越界路径必须拒绝
	if code := e.do("POST", "/api/agent/backups/delete", map[string]any{"path": "D:\\evil.zip"}, true, nil); code != 400 {
		t.Fatalf("越界删除应 400，实际 %d", code)
	}
	// 删掉
	if code := e.do("POST", "/api/agent/backups/delete", map[string]any{"path": name}, true, nil); code != 200 {
		t.Fatalf("删除失败：%d", code)
	}
	var after struct {
		Backups []any `json:"backups"`
	}
	e.do("GET", "/api/agent/backups", nil, true, &after)
	if len(after.Backups) != 0 {
		t.Fatalf("删除后应为空：%+v", after.Backups)
	}
}

/* ---------- 记忆检索接口的阈值口径 ---------- */

// 假向量通道：按字符做一个词袋向量，够用来造出"高/低相似度"两种情形
type fakeEmbed struct{}

func (fakeEmbed) Name() string { return "fake-embed" }
func (fakeEmbed) Dim() int     { return 32 }
func (fakeEmbed) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 32)
		for _, r := range t {
			v[int(r)%32] += 1
		}
		out[i] = v
	}
	return out, nil
}

// 搜索框要的是"语义相关"，不是"最近 N 条"：
// 默认按配置里的最低语义相似度卡一道（跟派活时的自动召回同一个口径），
// 全部没够上时要如实说明原因，minScore=0 可以显式关掉。
func TestAgentMemorySearchThreshold(t *testing.T) {
	e, svc := newAgentEnv(t)
	mem := svc.Memory()
	if mem == nil {
		t.Fatal("记忆库没起来")
	}
	mem.SetEmbedder(fakeEmbed{})
	for _, s := range []string{
		"白泽后端部署在 NAS 上，用 systemd 守护，端口 8787",
		"今天天气很好，适合出门散步",
	} {
		if _, err := mem.Ingest(s, memory.IngestOptions{Title: strings.SplitN(s, "，", 2)[0]}); err != nil {
			t.Fatalf("写记忆失败：%v", err)
		}
	}

	type memResp struct {
		Hits       []map[string]any `json:"hits"`
		VectorUsed bool             `json:"vectorUsed"`
		VectorNote string           `json:"vectorNote"`
		Candidates int              `json:"candidates"`
		Filtered   int              `json:"filtered"`
		MinVector  float64          `json:"minVector"`
	}

	// 阈值拉到顶：全被挡掉，但必须说清楚（不能静默返回空）
	var strict memResp
	if code := e.do("GET", "/api/agent/memory?q="+url.QueryEscape("部署")+"&minScore=0.99", nil, true, &strict); code != 200 {
		t.Fatalf("检索失败：%d", code)
	}
	if len(strict.Hits) != 0 {
		t.Fatalf("阈值 0.99 时不该有命中：%+v", strict.Hits)
	}
	if strict.Filtered == 0 || strict.VectorNote == "" {
		t.Fatalf("全被挡掉时要报出条数与原因：%+v", strict)
	}
	if !strict.VectorUsed {
		t.Fatal("配了向量通道就该声称用上了语义检索")
	}

	// minScore=0 显式关掉闸门 → 又能搜到
	var open memResp
	if code := e.do("GET", "/api/agent/memory?q="+url.QueryEscape("部署")+"&minScore=0", nil, true, &open); code != 200 {
		t.Fatalf("检索失败：%d", code)
	}
	if len(open.Hits) == 0 {
		t.Fatalf("关掉阈值后应能搜到：%+v", open)
	}
	if open.MinVector != 0 {
		t.Fatalf("minScore=0 应回显 0：%v", open.MinVector)
	}

	// 空 q = 要最近入库的：两条都要在，且不卡语义阈值
	var recent memResp
	if code := e.do("GET", "/api/agent/memory?q=&limit=10", nil, true, &recent); code != 200 {
		t.Fatalf("读最近记忆失败：%d", code)
	}
	if len(recent.Hits) != 2 {
		t.Fatalf("空 q 应给最近入库的 2 条：%+v", recent.Hits)
	}
	if recent.Filtered != 0 {
		t.Fatalf("空 q 不该卡阈值：%+v", recent)
	}

	// 阈值参数写错要明确报错，不能静默当成 0
	if code := e.do("GET", "/api/agent/memory?q=x&minScore=abc", nil, true, nil); code != 400 {
		t.Fatalf("minScore 非法应 400，实际 %d", code)
	}
}

// 记忆树：接口要如实报"有几天摘要没跟上"，重建接口要能补上。
// 摘要要调模型，没配模型通道时必须明确报错，而不是假装重建成功。
func TestAgentMemoryTreeRebuild(t *testing.T) {
	e, svc := newAgentEnv(t)
	mem := svc.Memory()
	if mem == nil {
		t.Fatal("记忆库没起来")
	}
	if _, err := mem.Ingest("白泽后端部署在 NAS 上，用 systemd 守护", memory.IngestOptions{}); err != nil {
		t.Fatalf("写记忆失败：%v", err)
	}

	var tree struct {
		Nodes      []map[string]any `json:"nodes"`
		StaleDays  []string         `json:"staleDays"`
		StaleCount int              `json:"staleCount"`
	}
	if code := e.do("GET", "/api/agent/memory/tree", nil, true, &tree); code != 200 {
		t.Fatalf("读记忆树失败：%d", code)
	}
	if tree.StaleCount != 1 || len(tree.StaleDays) != 1 {
		t.Fatalf("刚写完记忆，当天摘要应为空 → 应报 1 天没跟上：%+v", tree)
	}
	if len(tree.Nodes) == 0 {
		t.Fatal("应该已经建出当天的节点")
	}

	// 没配模型通道 → 摘要生成不了，必须明确报错
	if code := e.do("POST", "/api/agent/memory/tree/rebuild", map[string]any{}, true, nil); code != 400 {
		t.Fatalf("没有模型通道时重建应 400，实际 %d", code)
	}
}
