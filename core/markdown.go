package core

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

/* ================= 导出（与 App 的 exportMarkdown 逐行一致） =================

# 待办中心导出（2026-09-26）

## 待办
- [ ] 整理本周工作计划 ｜ 类别: 工作开发 ｜ 优先级: 高优 ｜ 截止: 2026-09-26 14:00 ｜ 备注: 示例 ｜ 依赖: 2项

## 密码
### GitHub
- 账号: user@example.com
- 归属: Trae
- 密码: abc123
- 历史密码: old123 ｜ 2026-09-20 10:00
- URL: https://github.com
- 备注: 初始示例
*/

const mdDateLayout = "2006-01-02"
const mdTimeLayout = "2006-01-02 15:04"

// ExportMarkdown 完整导出（待办 + 密码）
func ExportMarkdown(doc *Doc, now time.Time) string {
	return "# 待办中心导出（" + now.Format(mdDateLayout) + "）\n\n" +
		BuildTodosMD(doc) + "\n" + BuildVaultMD(doc)
}

// ExportVaultMarkdown 只导出密码本（可被同一个导入器读回）
func ExportVaultMarkdown(doc *Doc, now time.Time) string {
	return "# 密码本导出（" + now.Format(mdDateLayout) + "）\n\n" + BuildVaultMD(doc)
}

// BuildTodosMD 待办段落
func BuildTodosMD(doc *Doc) string {
	var b strings.Builder
	b.WriteString("## 待办\n")
	for _, t := range doc.Todos {
		mark := " "
		if t.Status == StatusDone {
			mark = "x"
		}
		title := t.Title
		if t.Owner == OwnerAgent {
			title += "（Agent）"
		}
		parts := []string{"- [" + mark + "] " + title}
		if t.Category != "" {
			parts = append(parts, "类别: "+t.Category)
		}
		if t.Priority != "" {
			parts = append(parts, "优先级: "+PriorityLabel(t.Priority))
		}
		if t.Due != "" {
			parts = append(parts, "截止: "+DueForDisplay(t.Due))
		}
		if t.Note != "" {
			parts = append(parts, "备注: "+t.Note)
		}
		if len(t.Deps) > 0 {
			parts = append(parts, "依赖: "+itoa(len(t.Deps))+"项")
		}
		b.WriteString(strings.Join(parts, " ｜ ") + "\n")
	}
	return b.String()
}

// BuildVaultMD 密码段落
func BuildVaultMD(doc *Doc) string {
	var b strings.Builder
	b.WriteString("## 密码\n")
	for _, v := range doc.Vault {
		b.WriteString("### " + v.Title + "\n")
		if v.Account != "" {
			b.WriteString("- 账号: " + v.Account + "\n")
		}
		if v.Group != "" {
			b.WriteString("- 归属: " + v.Group + "\n")
		}
		if v.Password != "" {
			b.WriteString("- 密码: " + v.Password + "\n")
		}
		for _, h := range v.History {
			if h.Pwd == "" {
				continue
			}
			if h.At > 0 {
				b.WriteString("- 历史密码: " + h.Pwd + " ｜ " + time.UnixMilli(h.At).Format(mdTimeLayout) + "\n")
			} else {
				b.WriteString("- 历史密码: " + h.Pwd + "\n")
			}
		}
		if v.URL != "" {
			b.WriteString("- URL: " + v.URL + "\n")
		}
		if v.Note != "" {
			b.WriteString("- 备注: " + v.Note + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

/* ================= 导入 ================= */

var (
	mdTodoLineRe = regexp.MustCompile(`^[-*]\s*\[([ xX])\]\s*(.+)$`)
	mdKVRe       = regexp.MustCompile(`^[-*]\s*([^:：]+)[:：]\s*(.*)$`)
	mdFieldSplit = regexp.MustCompile(`[｜|]`)
)

// mdPriorityIn 导入时的优先级写法（与 App 的 PRI_IN 一致）
var mdPriorityIn = map[string]string{
	"高优": PriorityHigh, "高": PriorityHigh, "high": PriorityHigh,
	"中": PriorityMid, "mid": PriorityMid, "中优": PriorityMid,
	"低": PriorityLow, "low": PriorityLow,
}

// ParseMarkdown 解析导出格式的 Markdown，返回待办与密码条目。
// 兼容 App 导出的完整/密码本导出两种文件，也兼容电脑侧 CLI 的导出。
func ParseMarkdown(text string) ([]Task, []Pass) {
	todos := []Task{}
	passes := []Pass{}

	var cur *Pass
	flush := func() {
		if cur == nil {
			return
		}
		if strings.TrimSpace(cur.Title) != "" && (cur.Account != "" || cur.Password != "" || len(cur.History) > 0) {
			passes = append(passes, *cur)
		}
		cur = nil
	}

	text = strings.ReplaceAll(text, "\r\n", "\n")
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		// 待办行（无论在哪一节都认，与 App/CLI 的宽松解析一致）
		if m := mdTodoLineRe.FindStringSubmatch(line); m != nil {
			if t, ok := parseTodoLine(m[1], m[2]); ok {
				todos = append(todos, t)
			}
			continue
		}

		if strings.HasPrefix(line, "#") {
			flush()
			title := strings.TrimSpace(strings.TrimLeft(line, "#"))
			// 章节标题（## 待办 / ## 密码）不是密码条目：它们没有账号也没有密码，flush 时会被丢掉
			switch title {
			case "待办", "密码":
				continue
			}
			cur = &Pass{Title: title, History: []PassHistory{}, Source: SourceMD}
			continue
		}

		if cur == nil {
			continue
		}
		m := mdKVRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key, val := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
		switch strings.ToLower(key) {
		case "账号", "账户", "用户名":
			cur.Account = val
		case "密码":
			cur.Password = val
		case "归属", "分组", "group":
			cur.Group = val
		case "历史密码", "旧密码":
			if h := parseHistoryVal(val); h.Pwd != "" {
				cur.History = append(cur.History, h)
			}
		case "url", "网址", "链接":
			cur.URL = val
		case "备注":
			cur.Note = val
		}
	}
	flush()
	return todos, passes
}

func parseTodoLine(mark, rest string) (Task, bool) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return Task{}, false
	}
	segs := mdFieldSplit.Split(rest, -1)
	title := strings.TrimSpace(segs[0])
	owner := OwnerUser
	if strings.HasSuffix(title, "（Agent）") {
		title = strings.TrimSpace(strings.TrimSuffix(title, "（Agent）"))
		owner = OwnerAgent
	}
	if title == "" {
		return Task{}, false
	}
	t := Task{
		ID:        NewID(),
		Title:     title,
		Category:  "日常生活",
		Priority:  PriorityMid,
		Form:      FormSchedule,
		Deps:      []string{},
		Atts:      []Attachment{},
		Owner:     owner,
		Status:    StatusTodo,
		Remind:    false,
		CreatedAt: Now(),
	}
	if strings.ToLower(strings.TrimSpace(mark)) == "x" {
		t.Status = StatusDone
	}
	for _, seg := range segs[1:] {
		kv := strings.SplitN(strings.TrimSpace(seg), ":", 2)
		if len(kv) < 2 {
			kv = strings.SplitN(strings.TrimSpace(seg), "：", 2)
		}
		if len(kv) < 2 {
			continue
		}
		key, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		switch key {
		case "类别", "分类":
			t.Category = val
		case "优先级":
			if p, ok := mdPriorityIn[strings.ToLower(val)]; ok {
				t.Priority = p
			}
		case "截止", "截止时间":
			if due, err := NormalizeDue(val); err == nil {
				t.Due = due
			}
		case "备注":
			t.Note = val
		case "耗时", "预计耗时":
			if n, err := parseLeadingInt(strings.TrimSuffix(val, "分钟")); err == nil {
				t.Estimate = n
			}
		}
	}
	// 导出格式不含「形态」字段：无截止即闲暇待办（与 App 一致）
	if t.Due == "" {
		t.Form = FormLeisure
	}
	return t, true
}

// parseHistoryVal 解析「历史密码」行的值：`xxx ｜ 2026-09-20 10:00`（时间可省）
func parseHistoryVal(val string) PassHistory {
	parts := mdFieldSplit.Split(val, 2)
	h := PassHistory{Pwd: strings.TrimSpace(parts[0])}
	if len(parts) > 1 {
		ts := strings.TrimSpace(parts[1])
		if ts != "" {
			if t, err := time.ParseInLocation(mdTimeLayout, ts, time.Local); err == nil {
				h.At = t.UnixMilli()
			} else if t, err := time.ParseInLocation("2006-01-02T15:04", ts, time.Local); err == nil {
				h.At = t.UnixMilli()
			} else if t, err := time.ParseInLocation(mdDateLayout, ts, time.Local); err == nil {
				h.At = t.UnixMilli()
			}
		}
	}
	return h
}

// ImportResult 导入统计
type ImportResult struct {
	TodoAdded   int
	TodoSkipped int
	PassAdded   int
	PassMerged  int
}

// ImportMarkdown 把 Markdown 合并进文档：
// 待办按「标题 + 截止」去重，密码按「平台 + 账号」合并进历史。
func (d *Doc) ImportMarkdown(text string) ImportResult {
	todos, passes := ParseMarkdown(text)
	return d.ImportDoc(&Doc{Todos: todos, Vault: passes})
}

// ImportDoc 把另一份文档并进来（手机端首次迁移、电脑端导入都用它）
func (d *Doc) ImportDoc(other *Doc) ImportResult {
	res := ImportResult{}
	if other == nil {
		return res
	}
	now := Now()
	// 待办：新条目插到最前（与 App 一致），保持源文件里的相对顺序
	for i := len(other.Todos) - 1; i >= 0; i-- {
		t := other.Todos[i]
		dup := false
		for _, e := range d.Todos {
			if e.Title == t.Title && e.Due == t.Due {
				dup = true
				break
			}
		}
		if dup {
			res.TodoSkipped++
			continue
		}
		if t.CreatedAt == 0 {
			t.CreatedAt = now
		}
		t.Normalize()
		d.Todos = append([]Task{t}, d.Todos...)
		res.TodoAdded++
	}
	for _, p := range other.Vault {
		r, _ := d.MergePass(p)
		if r == "new" {
			res.PassAdded++
		} else {
			res.PassMerged++
		}
	}
	return res
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func parseLeadingInt(s string) (int, error) {
	digits := ""
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			break
		}
		digits += string(r)
	}
	if digits == "" {
		return 0, errors.New("不是数字")
	}
	return strconv.Atoi(digits)
}
