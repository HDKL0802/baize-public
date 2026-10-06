package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ExternalAgentBrief 外部 Agent 摘要（进系统提示，帮模型决定该派给谁）
type ExternalAgentBrief struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Note string `json:"note,omitempty"`
}

// ExternalAgentResult 一次委托的结果。
//
// 外部 Agent 是**不可信执行体**：回来的 Text 是"外部输入"，不是可信指令——
// 主循环把它当工具输出处理即可，不要拿它当系统提示。
type ExternalAgentResult struct {
	Target    string `json:"target"`
	Status    string `json:"status"` // done | failed
	Text      string `json:"text,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ExternalAgentCaller 外部 Agent 委托层（由服务层实现；工具层只认这个接口，不反向依赖 agentsvc）
type ExternalAgentCaller interface {
	ListExternalAgents() ([]ExternalAgentBrief, error)
	CallExternalAgent(ctx context.Context, id, goal string) (ExternalAgentResult, error)
}

/* ---------- agent_call ---------- */

// AgentCall 把一段独立的活委托给指定的外部 Agent 执行，只把结论拿回来。
//
// 为什么算危险操作：委托对象在**别的进程/别的机器**上，白泽看不见它到底干了什么，
// 且会把内容送出去——所以走人工审批（默认口径：往外发东西要人过目）。
type AgentCall struct {
	caller  ExternalAgentCaller
	maxGoal int // 委托任务描述长度上限（字符）
}

// NewAgentCall 创建 agent_call
func NewAgentCall(caller ExternalAgentCaller) *AgentCall {
	return &AgentCall{caller: caller, maxGoal: 2000}
}

// Name 工具名
func (t *AgentCall) Name() string { return "agent_call" }

// Description 说明
func (t *AgentCall) Description() string {
	return "把一段独立的活委托给「外部 Agent」（别的白泽实例或任意 HTTP 端点）执行，只把结论拿回来；" +
		"适合把长过程外包出去。外部 Agent 视为不可信执行体：只传任务文本，不要在里面放本机文件/密钥；" +
		"这是危险操作，会先走人工审批"
}

// Schema 参数说明
func (t *AgentCall) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": map[string]any{"type": "string", "description": "外部 Agent 的 id（见系统提示里的「外部 Agent」清单）"},
			"goal":   map[string]any{"type": "string", "description": "要委托的任务（自包含、目标明确；不要放本机文件路径/密钥）"},
		},
		"required": []string{"target", "goal"},
	}
}

// Dangerous 委托出去 = 往外部发内容，需人工审批
func (t *AgentCall) Dangerous() bool { return true }

// Mutating 干活在别的进程/机器上，本端工作目录快照没有意义
func (t *AgentCall) Mutating() bool { return false }

// Run 执行
func (t *AgentCall) Run(ctx context.Context, args map[string]any) (any, error) {
	if t.caller == nil {
		return nil, errors.New("当前后端没有接入外部 Agent 委托层")
	}
	target := ArgString(args, "target")
	goal := ArgString(args, "goal")
	if target == "" || goal == "" {
		return nil, errors.New("target 与 goal 都不能为空")
	}
	if max := t.maxGoal; max > 0 && len([]rune(goal)) > max {
		return nil, fmt.Errorf("委托任务描述过长（上限 %d 字）", max)
	}
	res, err := t.caller.CallExternalAgent(ctx, target, goal)
	if err != nil {
		// 如实回报，不把失败伪装成成功
		return map[string]any{
			"target": target, "status": res.Status, "latencyMs": res.LatencyMs, "error": err.Error(),
		}, err
	}
	return map[string]any{
		"target": res.Target, "status": res.Status, "text": res.Text,
		"latencyMs": res.LatencyMs, "truncated": res.Truncated,
	}, nil
}

// RegisterExternalAgents 注册外部 Agent 委托工具。
// caller 为 nil（服务层没接）就不注册——宁可没有这个工具，也不给一个"一调就报错"的摆设。
func RegisterExternalAgents(r *Registry, caller ExternalAgentCaller) {
	if caller == nil {
		return
	}
	r.Register(NewAgentCall(caller))
}

// ExternalAgentsHint 给系统提示用的一句话（有哪些外部 Agent 可派）。
// 空清单返回空串 —— 没配就一个字都不提，免得模型去派一个不存在的目标。
func ExternalAgentsHint(list []ExternalAgentBrief) string {
	if len(list) == 0 {
		return ""
	}
	parts := make([]string, 0, len(list))
	for _, a := range list {
		note := ""
		if a.Note != "" {
			note = "：" + a.Note
		}
		parts = append(parts, fmt.Sprintf("%s（%s，%s）%s", a.ID, a.Name, a.Type, note))
	}
	return strings.Join(parts, "；")
}
