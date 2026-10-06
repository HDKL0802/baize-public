package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 外部 Agent 接口：配置增删 + 手动委托 + 坏请求 400
func TestExternalAgentEndpoint(t *testing.T) {
	e, _ := newAgentEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "你好，外部的"})
	}))
	t.Cleanup(srv.Close)

	// 没令牌 → 401
	if code := e.do("GET", "/api/agent/external-agents", nil, false, nil); code != 401 {
		t.Fatalf("没有令牌应 401，实际 %d", code)
	}

	// 初始：空清单，但类型清单要给出来（界面下拉用），agents/recent 是空数组不是 null
	var got struct {
		Agents      []map[string]any `json:"agents"`
		Types       []string         `json:"types"`
		Recent      []map[string]any `json:"recent"`
		AllowRemote bool             `json:"allowRemote"`
	}
	if code := e.do("GET", "/api/agent/external-agents", nil, true, &got); code != 200 {
		t.Fatalf("读外部 Agent 失败：%d", code)
	}
	if got.Agents == nil || got.Recent == nil {
		t.Fatal("agents/recent 应当是空数组而不是 null")
	}
	if len(got.Types) == 0 || got.AllowRemote {
		t.Fatalf("初始类型清单/放行状态不对：%+v", got)
	}

	// 坏 action → 400
	if code := e.do("POST", "/api/agent/external-agents", map[string]any{"action": "wat"}, true, nil); code != 400 {
		t.Fatalf("坏 action 应 400，实际 %d", code)
	}
	// upsert 缺 config → 400
	if code := e.do("POST", "/api/agent/external-agents", map[string]any{"action": "upsert"}, true, nil); code != 400 {
		t.Fatalf("upsert 缺 config 应 400，实际 %d", code)
	}

	// upsert 一个本机端点（httptest 是 127.0.0.1，不算远端）
	var up struct {
		Agents []map[string]any `json:"agents"`
		Saved  string           `json:"saved"`
	}
	code := e.do("POST", "/api/agent/external-agents", map[string]any{
		"action": "upsert",
		"config": map[string]any{
			"id": "peer", "name": "对端", "type": "http", "url": srv.URL, "token": "t1", "enabled": true,
		},
	}, true, &up)
	if code != 200 || up.Saved != "peer" || len(up.Agents) != 1 {
		t.Fatalf("upsert 失败：%d %+v", code, up)
	}

	// call 缺 goal → 400
	if code := e.do("POST", "/api/agent/external-agents", map[string]any{"action": "call", "id": "peer"}, true, nil); code != 400 {
		t.Fatalf("call 缺 goal 应 400，实际 %d", code)
	}
	// test 缺 id → 400
	if code := e.do("POST", "/api/agent/external-agents", map[string]any{"action": "test"}, true, nil); code != 400 {
		t.Fatalf("test 缺 id 应 400，实际 %d", code)
	}

	// call 成功
	var call struct {
		Result map[string]any `json:"result"`
		Error  string         `json:"error"`
	}
	code = e.do("POST", "/api/agent/external-agents", map[string]any{"action": "call", "id": "peer", "goal": "打个招呼"}, true, &call)
	if code != 200 || call.Result["text"] != "你好，外部的" {
		t.Fatalf("call 失败：%d %+v", code, call)
	}

	// 记录里应该能看到这一次委托
	var after struct {
		Recent []map[string]any `json:"recent"`
	}
	if code := e.do("GET", "/api/agent/external-agents", nil, true, &after); code != 200 || len(after.Recent) == 0 {
		t.Fatalf("委托记录没落下来：%d %+v", code, after)
	}

	// remove
	var rm struct {
		Agents  []map[string]any `json:"agents"`
		Removed string           `json:"removed"`
	}
	if code := e.do("POST", "/api/agent/external-agents", map[string]any{"action": "remove", "id": "peer"}, true, &rm); code != 200 || rm.Removed != "peer" || len(rm.Agents) != 0 {
		t.Fatalf("remove 失败：%d %+v", code, rm)
	}
	// 删不存在 → 400
	if code := e.do("POST", "/api/agent/external-agents", map[string]any{"action": "remove", "id": "peer"}, true, nil); code != 400 {
		t.Fatalf("删不存在应 400，实际 %d", code)
	}
}
