package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// CronHooks 定时任务的后端能力（由 agentsvc 注入）。
//
// 为什么用回调而不是直接调 agentsvc：tools 是被 agentsvc 引用的下层包，
// 反过来引用会成环。这里只声明"需要什么能力"，谁来提供由上层决定。
type CronHooks struct {
	List   func() []CronJobInfo
	Add    func(expr, goal, recipe string, autoApprove bool) (CronJobInfo, error)
	Update func(id, expr, goal, recipe string, enabled *bool) (CronJobInfo, error)
	Remove func(id string) error
}

// CronJobInfo 一个定时任务（给模型/界面看的形状）
type CronJobInfo struct {
	ID          string `json:"id"`
	Expr        string `json:"expr"`
	Goal        string `json:"goal"`
	Recipe      string `json:"recipe,omitempty"`
	Enabled     bool   `json:"enabled"`
	AutoApprove bool   `json:"autoApprove,omitempty"`
	NextAt      int64  `json:"nextAt,omitempty"`
	ParseErr    string `json:"parseError,omitempty"`
	LastRunAt   int64  `json:"lastRunAt,omitempty"`
	LastStatus  string `json:"lastStatus,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	Runs        int    `json:"runs,omitempty"`
}

// CronTool 让 Agent 自己管理定时任务（看 / 加 / 改 / 启停）。
//
// 删除单独成 cron_remove（走人工审批）：删除类动作按项目口径要人放行，
// 而"看一眼列表"显然不该弹审批——所以拆成两个工具。
type CronTool struct{ hooks CronHooks }

// NewCronTool 创建 cron 工具
func NewCronTool(h CronHooks) *CronTool { return &CronTool{hooks: h} }

// Name 工具名
func (t *CronTool) Name() string { return "cron" }

// Description 说明
func (t *CronTool) Description() string {
	return "定时任务：列出（list）、新增（add，cron 表达式 + 一句目标）、修改（update）、启停（toggle）。" +
		"到点后会按目标自动派一次活；危险操作仍会走人工审批（除任务显式设了 autoApprove）。"
}

// Mutating 会改配置
func (t *CronTool) Mutating() bool { return true }

// Schema 参数说明
func (t *CronTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"list", "add", "update", "toggle"},
				"description": "list=列出全部；add=新增；update=改某条；toggle=启停某条",
			},
			"id":     map[string]any{"type": "string", "description": "update / toggle：任务 id（list 结果里带）"},
			"expr":   map[string]any{"type": "string", "description": "五段 cron（如 0 8 * * * = 每天 8 点）或 @every 30m / @daily"},
			"goal":   map[string]any{"type": "string", "description": "到点后要白泽做什么（一句话目标）"},
			"recipe": map[string]any{"type": "string", "description": "可选的执行配方（一般不用填）"},
			"enabled": map[string]any{"type": "boolean",
				"description": "toggle：true 启用 / false 停用；update 时也可用来一并改启停"},
			"autoApprove": map[string]any{"type": "boolean",
				"description": "add：该任务里的危险操作是否免审批，默认 false（强烈建议保持 false）"},
		},
		"required": []string{"action"},
	}
}

// Run 执行
func (t *CronTool) Run(_ context.Context, args map[string]any) (any, error) {
	if t.hooks.List == nil {
		return nil, errors.New("定时任务能力未接入")
	}
	action := strings.ToLower(ArgString(args, "action"))
	switch action {
	case "list", "":
		jobs := t.hooks.List()
		return map[string]any{"count": len(jobs), "jobs": jobs}, nil

	case "add":
		expr, goal := ArgString(args, "expr"), ArgString(args, "goal")
		if expr == "" || goal == "" {
			return nil, errors.New("add 需要 expr 与 goal")
		}
		if t.hooks.Add == nil {
			return nil, errors.New("定时任务能力未接入")
		}
		job, err := t.hooks.Add(expr, goal, ArgString(args, "recipe"), ArgBool(args, "autoApprove"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "job": job}, nil

	case "update":
		id := ArgString(args, "id")
		if id == "" {
			return nil, errors.New("update 需要 id")
		}
		if t.hooks.Update == nil {
			return nil, errors.New("定时任务能力未接入")
		}
		var enabled *bool
		if _, ok := args["enabled"]; ok {
			v := ArgBool(args, "enabled")
			enabled = &v
		}
		job, err := t.hooks.Update(id, ArgString(args, "expr"), ArgString(args, "goal"), ArgString(args, "recipe"), enabled)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "job": job}, nil

	case "toggle":
		id := ArgString(args, "id")
		if id == "" {
			return nil, errors.New("toggle 需要 id")
		}
		if _, ok := args["enabled"]; !ok {
			return nil, errors.New("toggle 需要 enabled（true 启用 / false 停用）")
		}
		if t.hooks.Update == nil {
			return nil, errors.New("定时任务能力未接入")
		}
		v := ArgBool(args, "enabled")
		job, err := t.hooks.Update(id, "", "", "", &v)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "job": job}, nil
	}
	return nil, fmt.Errorf("不支持的 action：%q（可用：list、add、update、toggle；删除请用 cron_remove）", action)
}

// CronRemoveTool 删除一个定时任务。删除必须人工审批，所以单独成一个工具。
type CronRemoveTool struct{ hooks CronHooks }

// NewCronRemoveTool 创建 cron_remove
func NewCronRemoveTool(h CronHooks) *CronRemoveTool { return &CronRemoveTool{hooks: h} }

// Name 工具名
func (t *CronRemoveTool) Name() string { return "cron_remove" }

// Description 说明
func (t *CronRemoveTool) Description() string {
	return "删掉一个定时任务。需要人工审批；删掉后就不再按点自动跑。"
}

// Dangerous 需要人工审批
func (t *CronRemoveTool) Dangerous() bool { return true }

// Mutating 会改配置
func (t *CronRemoveTool) Mutating() bool { return true }

// Schema 参数说明
func (t *CronRemoveTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "string", "description": "要删的任务 id（list 结果里带）"},
		},
		"required": []string{"id"},
	}
}

// Run 执行
func (t *CronRemoveTool) Run(_ context.Context, args map[string]any) (any, error) {
	if t.hooks.Remove == nil {
		return nil, errors.New("定时任务能力未接入")
	}
	id := ArgString(args, "id")
	if id == "" {
		return nil, errors.New("请给 id，说清要删哪个定时任务")
	}
	if err := t.hooks.Remove(id); err != nil {
		return nil, err
	}
	return map[string]any{"removed": id}, nil
}

// RegisterCron 注册定时任务工具（cron + cron_remove）
func RegisterCron(r *Registry, hooks CronHooks) {
	if r == nil || hooks.List == nil {
		return
	}
	r.Register(NewCronTool(hooks))
	r.Register(NewCronRemoveTool(hooks))
}
