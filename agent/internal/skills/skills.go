// Package skills 是技能库（Hermes「技能系统 + 渐进式披露」的 Go 重写，
// 目录结构兼容 agentskills.io：每个技能一个目录，内含 SKILL.md）。
//
// 渐进式披露：系统提示里只放"名字 + 一句话说明"，模型需要时再用 skill_load 取全文。
package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Skill 一个技能
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Path        string   `json:"path"`
	Body        string   `json:"body"`               // 去掉 front-matter 的正文
	Triggers    []string `json:"triggers,omitempty"` // 可选：触发词（先做展示，后续接意图路由）
	Tokens      int      `json:"tokens"`
}

// Library 技能库。技能在运行中可能被 Agent 自己新建/改写（skill_manage），
// 所以读方法都加锁：一边跑任务一边刷新清单不会读到半截数据。
type Library struct {
	mu     sync.RWMutex
	dir    string
	skills []Skill
}

// Empty 返回一个指向 dir、内容为空的技能库（Load 失败时的兜底，
// 保证技能管理仍然可用，而不是直接罢工）
func Empty(dir string) *Library {
	return &Library{dir: dir, skills: []Skill{}}
}

// Load 扫描目录（递归）：支持 <dir>/[<分类>/]<名字>/SKILL.md 与顶层的 <dir>/<名字>.md；
// 以点开头的目录（.archive 等内部目录）不当作技能。
func Load(dir string) (*Library, error) {
	lib := &Library{dir: dir, skills: []Skill{}}
	if strings.TrimSpace(dir) == "" {
		return lib, nil
	}
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return lib, nil // 技能目录不存在不算错误
		}
		return lib, fmt.Errorf("读取技能目录失败（%s）：%w", dir, err)
	}
	seen := map[string]bool{}
	add := func(path string) error {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sk := parse(string(raw), path)
		if sk.Name == "" || seen[sk.Name] {
			return nil
		}
		seen[sk.Name] = true
		lib.skills = append(lib.skills, sk)
		return nil
	}
	// 递归扫描：技能可能在分类目录下（<dir>/<分类>/<名字>/SKILL.md，与 Hermes 一致），
	// 所以"扫一层"会把带分类的技能藏起来——那种"看着有其实用不上"的情况必须避免。
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 单个条目读不了就跳过，别拖垮其余技能
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() && p != dir {
				return fs.SkipDir // .archive（技能归档）/ .locks 等内部目录不是技能
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(name, "SKILL.md") {
			return add(p)
		}
		// 兼容老写法：顶层散落的 <名字>.md 也当技能
		if filepath.Dir(p) == dir && strings.EqualFold(filepath.Ext(name), ".md") {
			return add(p)
		}
		return nil
	})
	if err != nil {
		return lib, fmt.Errorf("读取技能目录失败（%s）：%w", dir, err)
	}
	sort.Slice(lib.skills, func(i, j int) bool { return lib.skills[i].Name < lib.skills[j].Name })
	return lib, nil
}

// parse 解析 SKILL.md：支持 YAML front-matter（name/description/triggers），
// 没有 front-matter 时退化为"一级标题当名字，首段当说明"。
func parse(text, path string) Skill {
	sk := Skill{Path: path}
	body := text
	if strings.HasPrefix(strings.TrimSpace(text), "---") {
		lines := strings.Split(text, "\n")
		end := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				end = i
				break
			}
		}
		if end > 0 {
			for _, line := range lines[1:end] {
				key, val, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				key = strings.ToLower(strings.TrimSpace(key))
				val = strings.Trim(strings.TrimSpace(val), `"'`)
				switch key {
				case "name":
					sk.Name = val
				case "description":
					sk.Description = val
				case "triggers", "trigger":
					sk.Triggers = splitList(val)
				}
			}
			body = strings.Join(lines[end+1:], "\n")
		}
	}
	body = strings.TrimSpace(body)
	if sk.Name == "" {
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "# ") {
				sk.Name = strings.TrimSpace(strings.TrimPrefix(line, "# "))
				break
			}
		}
	}
	if sk.Name == "" {
		sk.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if sk.Description == "" {
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			sk.Description = truncate(line, 120)
			break
		}
	}
	sk.Body = body
	sk.Tokens = estimateTokens(body)
	return sk
}

// Names 技能名（排序）
func (l *Library) Names() []string {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, 0, len(l.skills))
	for _, s := range l.skills {
		out = append(out, s.Name)
	}
	return out
}

// All 全部技能
func (l *Library) All() []Skill {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]Skill{}, l.skills...)
}

// Get 按名字取技能（大小写不敏感）
func (l *Library) Get(name string) (Skill, bool) {
	if l == nil {
		return Skill{}, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	name = strings.TrimSpace(name)
	for _, s := range l.skills {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	// 允许用目录名（<name>/SKILL.md）或文件名（<name>.md）来指定技能
	for _, s := range l.skills {
		dirBase := filepath.Base(filepath.Dir(s.Path))
		fileBase := strings.TrimSuffix(filepath.Base(s.Path), filepath.Ext(s.Path))
		if strings.EqualFold(dirBase, name) || strings.EqualFold(fileBase, name) {
			return s, true
		}
	}
	return Skill{}, false
}

// Index 给系统提示用的技能清单（渐进式披露：只列名字与说明）
func (l *Library) Index() string {
	if l == nil {
		return ""
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if len(l.skills) == 0 {
		return ""
	}
	var b strings.Builder
	for _, s := range l.skills {
		b.WriteString("- " + s.Name + "：" + s.Description + "\n")
	}
	return b.String()
}

// Dir 技能目录
func (l *Library) Dir() string {
	if l == nil {
		return ""
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.dir
}

// SetDir 换技能目录（配置里改了 skillsDir 时用；下次 Reload 生效）
func (l *Library) SetDir(dir string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dir = dir
}

// Reload 原地重扫技能目录。刻意不换 Library 指针：
// 已注册的 skill_load / 技能清单都持着同一个指针，原地刷新才能立刻看到新技能。
func (l *Library) Reload() error {
	if l == nil {
		return errors.New("技能库未初始化")
	}
	l.mu.RLock()
	dir := l.dir
	l.mu.RUnlock()
	fresh, err := Load(dir)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.dir = fresh.dir
	l.skills = fresh.skills
	l.mu.Unlock()
	return nil
}

func splitList(v string) []string {
	v = strings.Trim(v, "[]")
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '，' || r == '|' })
	out := []string{}
	for _, p := range parts {
		if p = strings.Trim(strings.TrimSpace(p), `"'`); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func estimateTokens(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if r > 0x2E80 {
			cjk++
		} else {
			other++
		}
	}
	return cjk + other/4 + 1
}
