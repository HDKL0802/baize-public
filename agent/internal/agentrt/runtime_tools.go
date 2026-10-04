package agentrt

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"baize/internal/skills"
	"baize/internal/tools"
)

// DelegateTool 多代理协作（Hermes delegate 的 Go 重写）：
// 让当前 Agent 把一段子任务交给一个"子 Agent"去跑，只把结论拿回来，避免主上下文被过程撑爆。
type DelegateTool struct {
	RunSub     func(ctx context.Context, goal, recipe string) (string, error)
	Depth      int // 当前深度（0 = 主 Agent）
	MaxDepth   int
	MaxSubGoal int // 子任务目标长度上限
}

// Name 工具名
func (t *DelegateTool) Name() string { return "agent_delegate" }

// Description 说明
func (t *DelegateTool) Description() string {
	return "把一段独立的子任务交给子 Agent 去处理，返回它的结论（适合拆解、查资料、批量处理这类过程很长的活）"
}

// Schema 参数说明
func (t *DelegateTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"goal":   map[string]any{"type": "string", "description": "交给子 Agent 的子任务（要自包含、目标明确）"},
			"recipe": map[string]any{"type": "string", "description": "子任务类型：chat|plan|code，默认 chat"},
		},
		"required": []string{"goal"},
	}
}

// Run 执行
func (t *DelegateTool) Run(ctx context.Context, args map[string]any) (any, error) {
	if t.RunSub == nil {
		return nil, errors.New("当前运行时不支持派发子 Agent")
	}
	if t.MaxDepth > 0 && t.Depth >= t.MaxDepth {
		return nil, fmt.Errorf("已达最大派发深度（%d），请在本层自己完成", t.MaxDepth)
	}
	goal := tools.ArgString(args, "goal")
	if goal == "" {
		return nil, errors.New("goal 不能为空")
	}
	if max := t.MaxSubGoal; max > 0 && len([]rune(goal)) > max {
		return nil, fmt.Errorf("子任务描述过长（上限 %d 字）", max)
	}
	recipe := tools.ArgString(args, "recipe")
	if recipe == "" {
		recipe = "chat"
	}
	text, err := t.RunSub(ctx, goal, recipe)
	if err != nil {
		return nil, fmt.Errorf("子 Agent 执行失败：%w", err)
	}
	return map[string]any{"subAgent": true, "goal": goal, "result": text}, nil
}

// SkillLoadTool 技能加载（渐进式披露：系统提示里只有清单，正文按需取）
type SkillLoadTool struct{ Lib *skills.Library }

// Name 工具名
func (t *SkillLoadTool) Name() string { return "skill_load" }

// Description 说明
func (t *SkillLoadTool) Description() string {
	return "加载某个技能的完整说明（技能清单已在系统提示里给出，需要照做时再取全文）"
}

// Schema 参数说明
func (t *SkillLoadTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "技能名"},
		},
		"required": []string{"name"},
	}
}

// Run 执行
func (t *SkillLoadTool) Run(_ context.Context, args map[string]any) (any, error) {
	name := tools.ArgString(args, "name")
	if name == "" {
		return nil, errors.New("name 不能为空")
	}
	sk, ok := t.Lib.Get(name)
	if !ok {
		return nil, fmt.Errorf("没有这个技能：%s（可用：%s）", name, strings.Join(t.Lib.Names(), "、"))
	}
	return map[string]any{"name": sk.Name, "path": sk.Path, "tokens": sk.Tokens, "body": sk.Body}, nil
}

// RegisterRuntimeTools 注册运行时自带的工具（派发子 Agent、加载技能）
//
// skill_load 一律注册，不按"当前有没有技能"来决定：Agent 现在能自己新建技能（skill_manage），
// 技能库是原地刷新的，若因为开始时是空库就不给这个工具，它会建完却读不回来。
func RegisterRuntimeTools(r *tools.Registry, delegate *DelegateTool, lib *skills.Library) {
	if delegate != nil {
		r.Register(delegate)
	}
	if lib != nil {
		r.Register(&SkillLoadTool{Lib: lib})
	}
}
