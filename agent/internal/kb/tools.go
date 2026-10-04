package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"baize/internal/tools"
)

// 本文件把知识库能力注册成 Agent 工具。
//
// 审批口径（与用户约定一致）：
//   - 增与查：随便用，不拦
//   - 删与"看明文密码"：必须过人工审批闸门
// 另外查密码只回"有没有密码"，绝不把明文交给模型 —— 明文只能由人自己看。

/* ---------- 只读：列待办 ---------- */

type listTodos struct{ s *Service }

// Name 工具名
func (t *listTodos) Name() string { return "kb_list_todos" }

// Description 说明
func (t *listTodos) Description() string {
	return "查询白泽知识库里的待办（正本在后端）。可按负责人(user/agent)、状态(todo/doing/done)、关键词过滤。"
}

// Schema 参数
func (t *listTodos) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"owner":   map[string]any{"type": "string", "description": "负责人：all / user / agent，默认 all"},
			"status":  map[string]any{"type": "string", "description": "状态：all / todo / doing / done，默认 all"},
			"keyword": map[string]any{"type": "string", "description": "按标题、领域、备注模糊匹配"},
			"limit":   map[string]any{"type": "integer", "description": "返回条数，默认 20，上限 100"},
		},
	}
}

// Run 执行
func (t *listTodos) Run(_ context.Context, args map[string]any) (any, error) {
	res, why := t.s.core.RunDeviceAction("todo.list", args)
	if why != "" {
		return nil, errors.New(why)
	}
	return res, nil
}

/* ---------- 写：加待办（免审批） ---------- */

type addTodo struct{ s *Service }

// Name 工具名
func (t *addTodo) Name() string { return "kb_add_todo" }

// Description 说明
func (t *addTodo) Description() string {
	return "往白泽知识库里加一条待办（正本在后端，手机端立刻可见）。这条不需要审批，放心用。"
}

// Schema 参数
func (t *addTodo) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":    map[string]any{"type": "string", "description": "任务标题（必填）"},
			"category": map[string]any{"type": "string", "description": "所属领域，例如 工作开发 / 创作 / 日常生活"},
			"priority": map[string]any{"type": "string", "description": "优先级：high / mid / low，默认 mid"},
			"form":     map[string]any{"type": "string", "description": "schedule=有截止时间的排期任务；leisure=闲暇待办。默认按有没有 due 自动判断"},
			"due":      map[string]any{"type": "string", "description": "截止时间，格式 2026-09-28T15:30（本地时间）"},
			"estimate": map[string]any{"type": "integer", "description": "预计耗时（分钟）"},
			"weekly":   map[string]any{"type": "boolean", "description": "是否每周重复"},
			"note":     map[string]any{"type": "string", "description": "备注"},
			"owner":    map[string]any{"type": "string", "description": "归谁：user（默认）=用户自己的待办；agent=Agent 自己领的任务"},
		},
		"required": []string{"title"},
	}
}

// Run 执行
func (t *addTodo) Run(_ context.Context, args map[string]any) (any, error) {
	title := tools.ArgString(args, "title")
	if title == "" {
		return nil, errors.New("title 不能为空")
	}
	form := tools.ArgString(args, "form")
	if form == "" {
		if tools.ArgString(args, "due") != "" {
			form = "schedule"
		} else {
			form = "leisure"
		}
	}
	payload := map[string]any{
		"title": title, "form": form,
		"category": tools.ArgString(args, "category"),
		"priority": tools.ArgString(args, "priority"),
		"due":      tools.ArgString(args, "due"),
		"note":     tools.ArgString(args, "note"),
		"owner":    tools.ArgString(args, "owner"),
		"estimate": tools.ArgInt(args, "estimate", 0),
		"weekly":   tools.ArgBool(args, "weekly"),
	}
	res, err := t.s.runOp("todo.add", payload)
	if err != nil {
		return nil, err
	}
	return map[string]any{"added": true, "todo": res}, nil
}

/* ---------- 只读：找密码（不回明文） ---------- */

type searchPasswords struct{ s *Service }

// Name 工具名
func (t *searchPasswords) Name() string { return "kb_search_passwords" }

// Description 说明
func (t *searchPasswords) Description() string {
	return "在白泽知识库里按平台/账号/备注找密码记录。只返回平台、账号、归属和历史条数，" +
		"不返回密码明文（要看明文得用 kb_reveal_password，那条需要人工审批）。"
}

// Schema 参数
func (t *searchPasswords) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "搜索词：平台名、账号或备注"},
		},
		"required": []string{"query"},
	}
}

// Run 执行
func (t *searchPasswords) Run(_ context.Context, args map[string]any) (any, error) {
	q := tools.ArgString(args, "query")
	if q == "" {
		return nil, errors.New("query 不能为空")
	}
	res, err := t.s.runOp("vault.search", map[string]any{"query": q})
	if err != nil {
		return nil, err
	}
	items, _ := res.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		hist, _ := m["history"].([]any)
		out = append(out, map[string]any{
			"id": m["id"], "title": m["title"], "account": m["account"],
			"group": m["group"], "note": m["note"],
			"hasPassword": fmt.Sprint(m["password"]) != "",
			"historyCount": len(hist),
			"updatedAt":    m["updatedAt"],
		})
	}
	return map[string]any{"query": q, "count": len(out), "items": out}, nil
}

/* ---------- 写：加密码（免审批） ---------- */

type addPassword struct{ s *Service }

// Name 工具名
func (t *addPassword) Name() string { return "kb_add_password" }

// Description 说明
func (t *addPassword) Description() string {
	return "往白泽知识库里存一条密码记录（平台 + 账号 + 密码）。同一「平台 + 账号」再存一次时，" +
		"旧密码会自动进历史，不会覆盖丢失。这条不需要审批。"
}

// Schema 参数
func (t *addPassword) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":    map[string]any{"type": "string", "description": "平台/站点名称（必填）"},
			"account":  map[string]any{"type": "string", "description": "账号（必填）"},
			"password": map[string]any{"type": "string", "description": "密码"},
			"group":    map[string]any{"type": "string", "description": "归属：同一服务的多个站点填同一个"},
			"url":      map[string]any{"type": "string", "description": "登录地址"},
			"note":     map[string]any{"type": "string", "description": "备注"},
		},
		"required": []string{"title", "account"},
	}
}

// Run 执行
func (t *addPassword) Run(_ context.Context, args map[string]any) (any, error) {
	title := tools.ArgString(args, "title")
	account := tools.ArgString(args, "account")
	if title == "" || account == "" {
		return nil, errors.New("title 与 account 都不能为空")
	}
	res, err := t.s.runOp("vault.merge", map[string]any{
		"title": title, "account": account, "password": tools.ArgString(args, "password"),
		"group": tools.ArgString(args, "group"), "url": tools.ArgString(args, "url"),
		"note": tools.ArgString(args, "note"), "source": "agent",
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"saved": true}
	if m, ok := res.(map[string]any); ok {
		out["result"] = m["result"] // new=新建 / updated=换密码进历史 / same=本来就一样
	}
	return out, nil
}

/* ---------- 危险：删待办（需审批） ---------- */

type deleteTodo struct{ s *Service }

// Name 工具名
func (t *deleteTodo) Name() string { return "kb_delete_todo" }

// Description 说明
func (t *deleteTodo) Description() string {
	return "从知识库里删掉一条待办（不可恢复）。需要人工审批。"
}

// Schema 参数
func (t *deleteTodo) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":    map[string]any{"type": "string", "description": "待办 id（先用 kb_list_todos 查）"},
			"title": map[string]any{"type": "string", "description": "待办标题（仅用于审批时给人看，便于确认删对没删对）"},
		},
		"required": []string{"id"},
	}
}

// Dangerous 需要人工审批
func (t *deleteTodo) Dangerous() bool { return true }

// Run 执行
func (t *deleteTodo) Run(_ context.Context, args map[string]any) (any, error) {
	id := tools.ArgString(args, "id")
	if id == "" {
		return nil, errors.New("id 不能为空")
	}
	if _, err := t.s.runOp("todo.remove", map[string]any{"id": id}); err != nil {
		return nil, err
	}
	return map[string]any{"removed": id}, nil
}

/* ---------- 危险：删密码（需审批） ---------- */

type deletePassword struct{ s *Service }

// Name 工具名
func (t *deletePassword) Name() string { return "kb_delete_password" }

// Description 说明
func (t *deletePassword) Description() string {
	return "从知识库里删掉一条密码记录（不可恢复）。需要人工审批。"
}

// Schema 参数
func (t *deletePassword) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":    map[string]any{"type": "string", "description": "密码记录 id（先用 kb_search_passwords 查）"},
			"title": map[string]any{"type": "string", "description": "平台名（仅用于审批时给人看）"},
		},
		"required": []string{"id"},
	}
}

// Dangerous 需要人工审批
func (t *deletePassword) Dangerous() bool { return true }

// Run 执行
func (t *deletePassword) Run(_ context.Context, args map[string]any) (any, error) {
	id := tools.ArgString(args, "id")
	if id == "" {
		return nil, errors.New("id 不能为空")
	}
	if _, err := t.s.runOp("vault.remove", map[string]any{"id": id}); err != nil {
		return nil, err
	}
	return map[string]any{"removed": id}, nil
}

/* ---------- 危险：看密码明文（需审批） ---------- */

type revealPassword struct{ s *Service }

// Name 工具名
func (t *revealPassword) Name() string { return "kb_reveal_password" }

// Description 说明
func (t *revealPassword) Description() string {
	return "读出某条密码记录的明文密码。只有用户明确要「把密码念给我」这类请求才用，且需要人工审批。"
}

// Schema 参数
func (t *revealPassword) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":   map[string]any{"type": "string", "description": "平台名"},
			"account": map[string]any{"type": "string", "description": "账号（可选，用于区分同平台多账号）"},
		},
		"required": []string{"title"},
	}
}

// Dangerous 需要人工审批
func (t *revealPassword) Dangerous() bool { return true }

// Run 执行
func (t *revealPassword) Run(_ context.Context, args map[string]any) (any, error) {
	title := strings.TrimSpace(tools.ArgString(args, "title"))
	if title == "" {
		return nil, errors.New("title 不能为空")
	}
	res, err := t.s.runOp("vault.search", map[string]any{"query": title})
	if err != nil {
		return nil, err
	}
	account := strings.TrimSpace(tools.ArgString(args, "account"))
	items, _ := res.([]any)
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprint(m["title"]) != title {
			continue
		}
		if account != "" && fmt.Sprint(m["account"]) != account {
			continue
		}
		return map[string]any{
			"title": m["title"], "account": m["account"],
			"password": m["password"], "url": m["url"],
		}, nil
	}
	return nil, fmt.Errorf("没找到这条密码记录：%s（先用 kb_search_passwords 查一下）", title)
}

/* ---------- 注册 ---------- */

// RegisterTools 把知识库工具注册进注册中心
func RegisterTools(r *tools.Registry, s *Service) {
	if r == nil || s == nil {
		return
	}
	r.Register(&listTodos{s: s})
	r.Register(&addTodo{s: s})
	r.Register(&searchPasswords{s: s})
	r.Register(&addPassword{s: s})
	r.Register(&deleteTodo{s: s})
	r.Register(&deletePassword{s: s})
	r.Register(&revealPassword{s: s})
}

// runOp 走一遍统一的操作分发（参数转成 JSON 再交给核心处理）
func (s *Service) runOp(op string, args map[string]any) (any, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("参数组装失败：%w", err)
	}
	return s.Do(op, raw)
}
