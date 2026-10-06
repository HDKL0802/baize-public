package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeCaller struct {
	lastID   string
	lastGoal string
	res      ExternalAgentResult
	err      error
}

func (f *fakeCaller) ListExternalAgents() ([]ExternalAgentBrief, error) { return nil, nil }
func (f *fakeCaller) CallExternalAgent(_ context.Context, id, goal string) (ExternalAgentResult, error) {
	f.lastID, f.lastGoal = id, goal
	return f.res, f.err
}

func TestAgentCallTool(t *testing.T) {
	c := &fakeCaller{res: ExternalAgentResult{Target: "remote", Status: "done", Text: "结论", LatencyMs: 12}}
	tool := NewAgentCall(c)

	if tool.Name() != "agent_call" {
		t.Fatalf("工具名不对：%s", tool.Name())
	}
	// 委托出去 = 危险操作（走人工审批）；但快照没意义
	if !tool.Dangerous() {
		t.Fatal("agent_call 必须声明为危险操作")
	}
	if tool.Mutating() {
		t.Fatal("agent_call 不该声明为会改本端工作目录")
	}

	// 参数校验
	if _, err := tool.Run(context.Background(), map[string]any{"goal": "x"}); err == nil {
		t.Fatal("缺 target 必须报错")
	}
	if _, err := tool.Run(context.Background(), map[string]any{"target": "remote"}); err == nil {
		t.Fatal("缺 goal 必须报错")
	}

	// 正常：回调被调用，结果如实带回
	out, err := tool.Run(context.Background(), map[string]any{"target": "remote", "goal": "写首诗"})
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}
	if c.lastID != "remote" || c.lastGoal != "写首诗" {
		t.Fatalf("回调参数没传对：%q %q", c.lastID, c.lastGoal)
	}
	m, ok := out.(map[string]any)
	if !ok || m["status"] != "done" || m["text"] != "结论" {
		t.Fatalf("返回形状不对：%+v", out)
	}

	// 失败：如实回报错误，不伪装成功
	c2 := &fakeCaller{res: ExternalAgentResult{Target: "remote", Status: "failed"}, err: errors.New("连不上对端")}
	out, err = NewAgentCall(c2).Run(context.Background(), map[string]any{"target": "remote", "goal": "x"})
	if err == nil {
		t.Fatal("回调报错时工具必须也报错")
	}
	if m := out.(map[string]any); m["status"] != "failed" || !strings.Contains(m["error"].(string), "连不上对端") {
		t.Fatalf("失败信息不对：%+v", out)
	}

	// 没有委托层不注册（不给摆设工具）
	r := NewRegistry()
	RegisterExternalAgents(r, nil)
	if _, ok := r.Get("agent_call"); ok {
		t.Fatal("caller 为 nil 时不该注册 agent_call")
	}
	r2 := NewRegistry()
	RegisterExternalAgents(r2, c)
	if _, ok := r2.Get("agent_call"); !ok {
		t.Fatal("有委托层时应注册 agent_call")
	}

	// 提示串：空清单一个字都不提
	if ExternalAgentsHint(nil) != "" {
		t.Fatal("空清单不该有提示")
	}
	hint := ExternalAgentsHint([]ExternalAgentBrief{{ID: "peer", Name: "对端", Type: "baize", Note: "查资料"}})
	if !strings.Contains(hint, "peer") || !strings.Contains(hint, "baize") {
		t.Fatalf("提示串没带上目标：%s", hint)
	}
}
