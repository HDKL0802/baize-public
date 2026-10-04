package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"baize/core"
)

func statusMark(s string) string {
	switch s {
	case "done":
		return "x"
	case "doing":
		return "~"
	case "cancelled":
		return "-"
	}
	return " "
}

func todoLine(n int, t Todo) string {
	pieces := []string{}
	if t.Category != "" {
		pieces = append(pieces, t.Category)
	}
	if t.Priority != "" {
		pieces = append(pieces, priLabel(t.Priority))
	}
	if t.Due != "" {
		due := strings.Replace(t.Due, "T", " ", 1)
		if t.Weekly {
			due += " 每周"
		}
		pieces = append(pieces, due)
	}
	if t.Estimate > 0 {
		pieces = append(pieces, fmt.Sprintf("%d分钟", t.Estimate))
	}
	line := fmt.Sprintf("%d  [%s] %s", n, statusMark(t.Status), t.Title)
	if len(pieces) > 0 {
		line += "  (" + strings.Join(pieces, "｜") + ")"
	}
	return line
}

// cmdAdd bz add "标题" [--due ...] [--cat ...] [--pri ...] [--form ...] [--minutes N] [--weekly] [--note ...]
func cmdAdd(argv []string) error {
	p := parseArgs(argv)
	if len(p.pos) == 0 {
		return errors.New(`用法：bz add "标题" [--due 2026-09-26T10:00] [--cat 工作开发] [--pri high|mid|low] [--form schedule|leisure] [--minutes 45] [--weekly] [--note "备注"]`)
	}
	title := strings.TrimSpace(strings.Join(p.pos, " "))
	if title == "" {
		return errors.New("标题不能为空")
	}
	pri, err := normalizePriority(p.str("pri", "priority"))
	if err != nil {
		return err
	}
	due, err := normalizeDue(p.str("due"))
	if err != nil {
		return err
	}
	form := strings.ToLower(strings.TrimSpace(p.str("form")))
	switch form {
	case "":
		form = "schedule"
	case "schedule", "日程", "日程排期":
		form = "schedule"
	case "leisure", "闲暇", "闲暇待办":
		form = "leisure"
	default:
		return fmt.Errorf("形态只支持 schedule|leisure，收到：%s", form)
	}
	minutes, err := atoiDefault(p.str("minutes", "estimate"), 0)
	if err != nil {
		return err
	}
	if minutes < 0 {
		return errors.New("预计耗时不能为负数")
	}

	d, err := loadData()
	if err != nil {
		return err
	}
	t := Todo{
		ID:        newID(),
		Title:     title,
		Category:  strings.TrimSpace(p.str("cat", "category")),
		Priority:  pri,
		Form:      form,
		Due:       due,
		Weekly:    p.has("weekly"),
		Estimate:  minutes,
		Deps:      []string{},
		Atts:      []core.Attachment{},
		Note:      p.str("note"),
		Owner:     core.OwnerUser,
		Status:    core.StatusTodo,
		CreatedAt: time.Now().UnixMilli(),
	}
	d.Todos = append([]Todo{t}, d.Todos...) // 新待办排最前（与 App 的 quickAdd 一致）
	if err := saveData(d); err != nil {
		return err
	}
	outf("已添加：%s  (id: %s)\n", t.Title, t.ID)
	return nil
}

// cmdList bz list [--all] [--cat 领域] [--leisure] [--json]
func cmdList(argv []string) error {
	p := parseArgs(argv)
	d, err := loadData()
	if err != nil {
		return err
	}
	ord := visibleOrder(d.Todos, p.has("all"))
	cat := strings.TrimSpace(p.str("cat"))
	items := []Todo{}
	for _, i := range ord {
		t := d.Todos[i]
		if cat != "" && !strings.Contains(t.Category, cat) {
			continue
		}
		if p.has("leisure") && t.Form != "leisure" {
			continue
		}
		items = append(items, t)
	}

	if p.has("json") {
		b, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			return err
		}
		os.Stdout.WriteString(string(b) + "\n")
		return nil
	}
	if len(items) == 0 {
		outf("（没有匹配的待办）\n")
		return nil
	}
	for i, t := range items {
		outf("%s\n", todoLine(i+1, t))
	}
	return nil
}

// cmdMark bz done|undo <序号|id>
func cmdMark(argv []string, status string) error {
	p := parseArgs(argv)
	ref, err := p.firstPos()
	if err != nil {
		return errors.New("用法：bz done|undo <序号|id>")
	}
	d, err := loadData()
	if err != nil {
		return err
	}
	i, err := findTodo(d, ref)
	if err != nil {
		return err
	}
	d.Todos[i].Status = status
	if err := saveData(d); err != nil {
		return err
	}
	if status == "done" {
		outf("已完成：%s\n", d.Todos[i].Title)
	} else {
		outf("已恢复：%s\n", d.Todos[i].Title)
	}
	return nil
}

// cmdRemove bz rm <序号|id>
func cmdRemove(argv []string) error {
	p := parseArgs(argv)
	ref, err := p.firstPos()
	if err != nil {
		return errors.New("用法：bz rm <序号|id>")
	}
	d, err := loadData()
	if err != nil {
		return err
	}
	i, err := findTodo(d, ref)
	if err != nil {
		return err
	}
	title := d.Todos[i].Title
	d.Todos = append(d.Todos[:i], d.Todos[i+1:]...)
	if err := saveData(d); err != nil {
		return err
	}
	outf("已删除：%s\n", title)
	return nil
}

func cmdWhere() error {
	p := dataPath()
	state := "尚未创建"
	if _, err := os.Stat(p); err == nil {
		state = "已存在"
	}
	outf("数据文件：%s（%s）\n", p, state)
	return nil
}
