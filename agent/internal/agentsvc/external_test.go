package agentsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"baize/internal/config"
)

// 外部 Agent：http / baize 两种类型都能派、令牌按类型放对地方、记录与打点都落下来
func TestExternalAgentCallHTTPAndBaize(t *testing.T) {
	var mu sync.Mutex
	var httpAuth, baizeToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/agent/run" {
			baizeToken = r.Header.Get("X-Baize-Token")
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"text": "来自另一个白泽"}})
			return
		}
		httpAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"reply": "来自通用端点"})
	}))
	defer srv.Close()

	s := newService(t, nil, nil)

	// http 类型
	if _, err := s.ExternalAgentSave(config.ExternalAgent{
		ID: "generic", Name: "通用端点", Type: "http", URL: srv.URL, Token: "tok-1", Enabled: true,
	}, false); err != nil {
		t.Fatalf("保存 http 类型失败：%v", err)
	}
	res, err := s.CallExternalAgent(context.Background(), "generic", "写首诗")
	if err != nil {
		t.Fatalf("http 委托失败：%v", err)
	}
	if res.Status != "done" || res.Text != "来自通用端点" || res.Target != "generic" {
		t.Fatalf("http 委托结果不对：%+v", res)
	}
	mu.Lock()
	gotHTTP := httpAuth
	mu.Unlock()
	if gotHTTP != "Bearer tok-1" {
		t.Fatalf("http 类型应把令牌放 Authorization: Bearer，实际 %q", gotHTTP)
	}

	// baize 类型
	if _, err := s.ExternalAgentSave(config.ExternalAgent{
		ID: "peer", Name: "对端白泽", Type: "baize", URL: srv.URL, Token: "pair-2", Enabled: true,
	}, false); err != nil {
		t.Fatalf("保存 baize 类型失败：%v", err)
	}
	res, err = s.CallExternalAgent(context.Background(), "peer", "查件事")
	if err != nil {
		t.Fatalf("baize 委托失败：%v", err)
	}
	if res.Text != "来自另一个白泽" {
		t.Fatalf("baize 委托结果不对：%+v", res)
	}
	mu.Lock()
	gotBaize := baizeToken
	mu.Unlock()
	if gotBaize != "pair-2" {
		t.Fatalf("baize 类型应把令牌放 X-Baize-Token，实际 %q", gotBaize)
	}

	// 最近委托记录：新记录在最前
	dels := s.Delegations()
	if len(dels) != 2 || dels[0].Target != "peer" || dels[1].Target != "generic" {
		t.Fatalf("委托记录不对：%+v", dels)
	}
	if dels[0].Status != "done" || dels[0].Preview == "" {
		t.Fatalf("委托记录应带状态与预览：%+v", dels[0])
	}

	// 观测打点：按 target 统计
	snap := s.metrics.TargetSnapshot()
	if len(snap) != 2 {
		t.Fatalf("应统计到 2 个委托目标，实际 %d：%+v", len(snap), snap)
	}
	for _, st := range snap {
		if st.Calls != 1 || st.Failed != 0 {
			t.Fatalf("目标统计不对：%+v", st)
		}
	}
}

// 输出上限：超长回文要截断并标 truncated
func TestExternalAgentOutputTruncated(t *testing.T) {
	big := strings.Repeat("字", 9000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": big})
	}))
	defer srv.Close()

	s := newService(t, nil, nil)
	if _, err := s.ExternalAgentSave(config.ExternalAgent{ID: "big", Type: "http", URL: srv.URL, Enabled: true}, false); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	res, err := s.CallExternalAgent(context.Background(), "big", "说很多")
	if err != nil {
		t.Fatalf("委托失败：%v", err)
	}
	if !res.Truncated {
		t.Fatal("超长输出应被标记为截断")
	}
	if n := len([]rune(res.Text)); n != externalMaxOut {
		t.Fatalf("截断后应为 %d 字，实际 %d", externalMaxOut, n)
	}
}

// 管理面：令牌留空不改动、远端地址先放行、必填校验、删除
func TestExternalAgentManagement(t *testing.T) {
	s := newService(t, nil, nil)

	base := config.ExternalAgent{ID: "peer", Name: "对端", Type: "baize", URL: "http://127.0.0.1:9/x", Token: "keep-me", Enabled: true}
	if _, err := s.ExternalAgentSave(base, false); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	// token 留空 = 只改其他字段，不把已有令牌清掉
	updated := base
	updated.Token = ""
	updated.Name = "对端改名"
	list, err := s.ExternalAgentSave(updated, false)
	if err != nil {
		t.Fatalf("更新失败：%v", err)
	}
	if len(list) != 1 || list[0].Name != "对端改名" || !list[0].HasToken {
		t.Fatalf("更新后应保留令牌：%+v", list)
	}
	cfg, err := config.Load(s.DataDir())
	if err != nil || len(cfg.ExternalAgents) != 1 || cfg.ExternalAgents[0].Token != "keep-me" {
		t.Fatalf("令牌没落盘或丢了：%v %+v", err, cfg.ExternalAgents)
	}

	// 远端地址默认拒绝，报错要告诉怎么放行
	remote := config.ExternalAgent{ID: "cloud", Type: "http", URL: "https://agent.example.com/run", Enabled: true}
	if _, err := s.ExternalAgentSave(remote, false); err == nil {
		t.Fatal("远端地址未放行必须报错")
	} else if !strings.Contains(err.Error(), "allow-remote") && !strings.Contains(err.Error(), "allowRemote") {
		t.Fatalf("错误信息要说清怎么放行：%v", err)
	}
	if _, err := s.ExternalAgentSave(remote, true); err != nil {
		t.Fatalf("显式放行后应能保存：%v", err)
	}
	if !s.Config().AllowRemote {
		t.Fatal("放行没落到配置里")
	}

	// 必填 / 类型校验
	bad := []config.ExternalAgent{
		{ID: "", Type: "http", URL: "http://127.0.0.1:9/x"},
		{ID: "x", Type: "http", URL: ""},
		{ID: "x", Type: "grpc", URL: "http://127.0.0.1:9/x"},
	}
	for _, b := range bad {
		if _, err := s.ExternalAgentSave(b, true); err == nil {
			t.Fatalf("坏配置必须报错：%+v", b)
		}
	}

	// 删除
	list, err = s.ExternalAgentRemove("cloud")
	if err != nil || len(list) != 1 {
		t.Fatalf("删除失败：%v %+v", err, list)
	}
	if _, err := s.ExternalAgentRemove("cloud"); err == nil {
		t.Fatal("删不存在的必须报错")
	}
	if _, err := s.ExternalAgentRemove(""); err == nil {
		t.Fatal("不给 id 必须报错")
	}
}

// 停用 / 不存在：都要明确报错，且不存在的报错里带上可用清单
func TestExternalAgentCallErrors(t *testing.T) {
	s := newService(t, nil, nil)
	if _, err := s.ExternalAgentSave(config.ExternalAgent{ID: "off", Type: "http", URL: "http://127.0.0.1:9/x", Enabled: false}, false); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if _, err := s.CallExternalAgent(context.Background(), "off", "干点活"); err == nil || !strings.Contains(err.Error(), "停用") {
		t.Fatalf("停用目标必须报错并说明：%v", err)
	}
	if _, err := s.CallExternalAgent(context.Background(), "nope", "干点活"); err == nil || !strings.Contains(err.Error(), "off") {
		t.Fatalf("不存在的目标报错里应带可用清单：%v", err)
	}
	if _, err := s.CallExternalAgent(context.Background(), "off", "  "); err == nil {
		t.Fatal("空任务必须报错")
	}
	// 空环境：提示去配置里加一个
	s2 := newService(t, nil, nil)
	if _, err := s2.CallExternalAgent(context.Background(), "x", "干点活"); err == nil || !strings.Contains(err.Error(), "还没有配置任何外部 Agent") {
		t.Fatalf("空环境报错要指路：%v", err)
	}
}
