package core

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

/* ---------- 取值归一化 ---------- */

// NormalizePriority 把各种写法收敛成 high|mid|low
func NormalizePriority(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "mid", "medium", "中", "中优", "中优先级":
		return PriorityMid, nil
	case "high", "urgent", "高", "高优", "高优先级", "紧急":
		return PriorityHigh, nil
	case "low", "低", "低优", "低优先级":
		return PriorityLow, nil
	}
	return "", fmt.Errorf("优先级只支持 high|mid|low，收到：%s", v)
}

// PriorityLabel 优先级的中文短标签（MD 导出用，与 App 一致）
func PriorityLabel(p string) string {
	switch p {
	case PriorityHigh:
		return "高优"
	case PriorityMid:
		return "中"
	case PriorityLow:
		return "低"
	}
	return p
}

// NormalizeForm 归一化任务形式
func NormalizeForm(v string) string {
	if strings.TrimSpace(v) == FormLeisure {
		return FormLeisure
	}
	return FormSchedule
}

// NormalizeStatus 归一化状态
func NormalizeStatus(v string) (string, error) {
	switch strings.TrimSpace(v) {
	case "", StatusTodo, "open", "pending":
		return StatusTodo, nil
	case StatusDoing, "in_progress":
		return StatusDoing, nil
	case StatusDone, "finished", "completed":
		return StatusDone, nil
	case StatusCancelled, "canceled":
		return StatusCancelled, nil
	}
	return "", fmt.Errorf("状态只支持 todo|doing|done|cancelled，收到：%s", v)
}

const dueLayout = "2006-01-02T15:04"

// NormalizeDue 统一为 "YYYY-MM-DDTHH:mm"（与 App 一致，本地时间）
func NormalizeDue(v string) (string, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return "", nil
	}
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.Replace(s, "T", " ", 1)
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return "", fmt.Errorf("时间格式无法识别：%s", v)
	}
	datePart := fields[0]
	timePart := "09:00"
	if len(fields) > 1 {
		timePart = fields[1]
	}
	if d, err := time.ParseInLocation("2006-01-02 15:04", datePart+" "+timePart, time.Local); err == nil {
		return d.Format(dueLayout), nil
	}
	if d, err := time.ParseInLocation("2006-1-2 15:04", datePart+" "+timePart, time.Local); err == nil {
		return d.Format(dueLayout), nil
	}
	if d, err := time.ParseInLocation("2006-01-02", datePart, time.Local); err == nil {
		return d.Format(dueLayout), nil
	}
	return "", fmt.Errorf("时间格式无法识别：%s（示例 2026-09-26 14:00）", v)
}

// DueForDisplay 展示/导出用：把 T 换成空格
func DueForDisplay(due string) string {
	return strings.Replace(due, "T", " ", 1)
}

// ParseDue 解析 due 字符串
func ParseDue(due string) (time.Time, bool) {
	if strings.TrimSpace(due) == "" {
		return time.Time{}, false
	}
	if t, err := time.ParseInLocation(dueLayout, due, time.Local); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", due, time.Local); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// DefaultDue 默认截止时间：一小时后（与 App 一致）
func DefaultDue() string {
	return time.Now().Add(time.Hour).Format(dueLayout)
}

/* ---------- 构造与增删改 ---------- */

// NewTask 新建待办（默认值与 App 的 quickAdd 一致）
func NewTask(title string, form, owner string) Task {
	t := Task{
		ID:        NewID(),
		Title:     strings.TrimSpace(title),
		Category:  "日常生活",
		Priority:  PriorityMid,
		Form:      NormalizeForm(form),
		Weekly:    false,
		Estimate:  0,
		Deps:      []string{},
		Atts:      []Attachment{},
		Owner:     owner,
		Status:    StatusTodo,
		Remind:    false,
		CreatedAt: Now(),
	}
	if t.Owner != OwnerAgent {
		t.Owner = OwnerUser
	}
	if t.Form == FormSchedule {
		t.Due = DefaultDue()
	}
	return t
}

// AddTask 插入待办（与 App 一致：新任务排在最前，缺省字段自动补齐）
func (d *Doc) AddTask(t Task) Task {
	t.Normalize()
	if t.CreatedAt == 0 {
		t.CreatedAt = Now()
	}
	d.Todos = append([]Task{t}, d.Todos...)
	return t
}

// FindTask 按「序号」或「id / id 前缀」定位待办，返回在 d.Todos 中的下标。
// 序号基于默认列表排序（未收尾项），与 CLI 行为一致。
func (d *Doc) FindTask(ref string) (int, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return -1, errors.New("缺少待办标识（序号或 id）")
	}
	if n, err := strconv.Atoi(ref); err == nil {
		for _, includeClosed := range []bool{false, true} {
			ord := VisibleOrder(d.Todos, includeClosed)
			if n >= 1 && n <= len(ord) {
				return ord[n-1], nil
			}
		}
		return -1, fmt.Errorf("序号 %d 超出范围（当前共 %d 条待办）", n, len(d.Todos))
	}
	return MatchIDPrefix(idsOf(d.Todos), ref)
}

func idsOf(tasks []Task) []string {
	out := make([]string, len(tasks))
	for i := range tasks {
		out[i] = tasks[i].ID
	}
	return out
}

// SetTaskStatus 改状态
func (d *Doc) SetTaskStatus(id, status string) error {
	s, err := NormalizeStatus(status)
	if err != nil {
		return err
	}
	i := d.taskIndex(id)
	if i < 0 {
		return fmt.Errorf("找不到待办：%s", id)
	}
	d.Todos[i].Status = s
	return nil
}

// ToggleTaskDone 在完成/待办之间切换
func (d *Doc) ToggleTaskDone(id string) (string, error) {
	i := d.taskIndex(id)
	if i < 0 {
		return "", fmt.Errorf("找不到待办：%s", id)
	}
	if d.Todos[i].Status == StatusDone {
		d.Todos[i].Status = StatusTodo
	} else {
		d.Todos[i].Status = StatusDone
	}
	return d.Todos[i].Status, nil
}

// RemoveTask 删除待办
func (d *Doc) RemoveTask(id string) error {
	i := d.taskIndex(id)
	if i < 0 {
		return fmt.Errorf("找不到待办：%s", id)
	}
	d.Todos = append(d.Todos[:i], d.Todos[i+1:]...)
	return nil
}

// UpdateTask 整体覆盖指定待办的字段（保留 id/createdAt）
func (d *Doc) UpdateTask(id string, patch Task) error {
	i := d.taskIndex(id)
	if i < 0 {
		return fmt.Errorf("找不到待办：%s", id)
	}
	patch.ID = d.Todos[i].ID
	patch.CreatedAt = d.Todos[i].CreatedAt
	patch.Normalize()
	d.Todos[i] = patch
	return nil
}

// ConvertForm 日程⇄闲暇互转（转到闲暇清空截止时间，与 App 一致）
func (d *Doc) ConvertForm(id, form string) error {
	i := d.taskIndex(id)
	if i < 0 {
		return fmt.Errorf("找不到待办：%s", id)
	}
	f := NormalizeForm(form)
	d.Todos[i].Form = f
	if f == FormLeisure {
		d.Todos[i].Due = ""
	} else if d.Todos[i].Due == "" {
		d.Todos[i].Due = DefaultDue()
	}
	return nil
}

// AuthorizeAgent 标记 Agent 待办「已申请授权执行」
func (d *Doc) AuthorizeAgent(id string) error {
	i := d.taskIndex(id)
	if i < 0 {
		return fmt.Errorf("找不到待办：%s", id)
	}
	d.Todos[i].Auth = AuthPending
	return nil
}

func (d *Doc) taskIndex(id string) int {
	for i := range d.Todos {
		if d.Todos[i].ID == id {
			return i
		}
	}
	return -1
}

/* ---------- 排序、分组、统计 ---------- */

// VisibleOrder 展示顺序（下标）：按截止时间升序、无截止的排最后，同截止按创建时间升序。
// includeClosed=false 时排除已完成/已取消。
func VisibleOrder(tasks []Task, includeClosed bool) []int {
	idx := make([]int, 0, len(tasks))
	for i := range tasks {
		if !includeClosed && tasks[i].IsClosed() {
			continue
		}
		idx = append(idx, i)
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ta, tb := tasks[idx[a]], tasks[idx[b]]
		if ta.Due == "" && tb.Due != "" {
			return false
		}
		if ta.Due != "" && tb.Due == "" {
			return true
		}
		if ta.Due != tb.Due {
			return ta.Due < tb.Due
		}
		return ta.CreatedAt < tb.CreatedAt
	})
	return idx
}

// SortMode 排序模式
const (
	SortByTime     = "time"
	SortByPriority = "priority"
)

// SortTasks 与 App 列表排序一致：
// time 模式按 due 字符串升序（无 due 视为 "9999" 排最后）；
// priority 模式先按 high→mid→low，再按时间。
func SortTasks(tasks []Task, mode string) {
	byTime := func(a, b Task) int {
		x, y := a.Due, b.Due
		if x == "" {
			x = "9999"
		}
		if y == "" {
			y = "9999"
		}
		return strings.Compare(x, y)
	}
	rank := func(p string) int {
		switch p {
		case PriorityHigh:
			return 0
		case PriorityMid:
			return 1
		case PriorityLow:
			return 2
		}
		return 9
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if mode == SortByPriority {
			ra, rb := rank(tasks[i].Priority), rank(tasks[j].Priority)
			if ra != rb {
				return ra < rb
			}
		}
		return byTime(tasks[i], tasks[j]) < 0
	})
}

// FilterTasks 按属主/形态筛选（与 App 列表一致：form 缺省视为 schedule）
func FilterTasks(tasks []Task, owner, form string) []Task {
	out := []Task{}
	for _, t := range tasks {
		if owner != "" && t.Owner != owner {
			continue
		}
		f := t.Form
		if f != FormLeisure {
			f = FormSchedule
		}
		if form != "" && f != form {
			continue
		}
		out = append(out, t)
	}
	return out
}

// TaskStats 统计未收尾待办的日程/闲暇条数
func TaskStats(tasks []Task, owner string) (sched, leisure int) {
	for _, t := range tasks {
		if owner != "" && t.Owner != owner {
			continue
		}
		if t.IsClosed() {
			continue
		}
		if t.Form == FormLeisure {
			leisure++
		} else {
			sched++
		}
	}
	return sched, leisure
}

// DayGroup 日程视图里的一天
type DayGroup struct {
	Key   string // "YYYY-MM-DD" 或 "" 表示未排期
	Tasks []Task
}

// GroupByDay 按日期分组（未排期单独一组且排最后），组内按时间排序
func GroupByDay(tasks []Task, mode string) []DayGroup {
	byDay := map[string][]Task{}
	for _, t := range tasks {
		key := ""
		if d, ok := ParseDue(t.Due); ok {
			key = d.Format("2006-01-02")
		} else if t.Due != "" {
			key = strings.Replace(t.Due, "T", " ", 1)[:min(10, len(strings.Replace(t.Due, "T", " ", 1)))]
		}
		byDay[key] = append(byDay[key], t)
	}
	keys := make([]string, 0, len(byDay))
	for k := range byDay {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i] == "" {
			return false
		}
		if keys[j] == "" {
			return true
		}
		return keys[i] < keys[j]
	})
	out := make([]DayGroup, 0, len(keys))
	for _, k := range keys {
		arr := byDay[k]
		SortTasks(arr, mode)
		out = append(out, DayGroup{Key: k, Tasks: arr})
	}
	return out
}

/* ---------- id 前缀匹配 ---------- */

// MatchIDPrefix 先精确匹配 id，再按前缀匹配；多条命中时报错
func MatchIDPrefix(ids []string, ref string) (int, error) {
	for i, id := range ids {
		if id == ref {
			return i, nil
		}
	}
	hit := -1
	for i, id := range ids {
		if strings.HasPrefix(id, ref) {
			if hit >= 0 {
				return -1, fmt.Errorf("id 前缀 %q 匹配到多条记录，请输入更完整的 id", ref)
			}
			hit = i
		}
	}
	if hit >= 0 {
		return hit, nil
	}
	return -1, fmt.Errorf("找不到匹配 id 的记录：%s", ref)
}
