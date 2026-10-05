// Package persona 是「人设」文件（QwenPaw「智能体的人设」机制的 Go 重写）：
// 一组 Markdown 文件按顺序拼进系统提示，决定智能体的行为风格与工作方式。
//
// 与技能（skills）的分工：技能是"怎么做某件事"的操作手册，按需取全文（渐进式披露）；
// 人设是"你是谁、按什么规矩办事"，每次运行都整篇进系统提示。所以模板刻意写短。
//
// 热重载：Build 每次都现读文件、不缓存，改完立即生效，无需重启。
package persona

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultFiles 默认加载的人设文件（与内置模板同名；全部可选，缺了就跳过）
var DefaultFiles = []string{"AGENTS.md", "SOUL.md", "PROFILE.md"}

// File 一个人设文件的当前状态（控制台列表用）
type File struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"` // 是否在加载清单里
	Exists  bool   `json:"exists"`  // 文件在不在（启用但不存在 = 显示出来让用户补）
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime,omitempty"`
	Tokens  int    `json:"tokens,omitempty"`
	Preview string `json:"preview,omitempty"` // 开头一段，列表里给个印象
	Builtin bool   `json:"builtin"`           // 有内置模板，可「恢复默认」
}

// Library 人设库：只管一个目录里的 .md 文件
type Library struct {
	dir string
}

// New 指向某个目录（不读盘；目录在首次写入时建）
func New(dir string) *Library { return &Library{dir: dir} }

// Dir 人设目录（控制台显示用）
func (l *Library) Dir() string {
	if l == nil {
		return ""
	}
	return l.dir
}

// Valid 判断文件名能不能当人设文件用：只允许目录内的 <名字>.md，
// 不接受路径分隔符、点开头的隐藏文件。
func Valid(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return false
	}
	if strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") {
		return false
	}
	return strings.EqualFold(filepath.Ext(name), ".md")
}

// List 列人设文件：先按启用顺序列出清单里的（含"启用了但文件不在"的），
// 再补目录里其余还没启用的 .md（用户能在界面上把它们打开）。
func (l *Library) List(enabled []string) []File {
	if l == nil {
		return []File{}
	}
	out := make([]File, 0, len(enabled))
	seen := map[string]bool{}
	for _, name := range enabled {
		name = strings.TrimSpace(name)
		if !Valid(name) || seen[name] {
			continue
		}
		seen[name] = true
		f := l.stat(name)
		f.Enabled = true
		out = append(out, f)
	}
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return out
	}
	rest := []string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || seen[name] || !Valid(name) {
			continue
		}
		rest = append(rest, name)
	}
	sort.Strings(rest)
	for _, name := range rest {
		out = append(out, l.stat(name))
	}
	return out
}

// Read 读一个人设文件的原文（文件不存在时明确报错，不回空串）
func (l *Library) Read(name string) (string, error) {
	path, err := l.path(name)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("没有这个人设文件：%s", strings.TrimSpace(name))
		}
		return "", fmt.Errorf("读取人设文件失败（%s）：%w", name, err)
	}
	return strings.TrimPrefix(string(raw), "\ufeff"), nil
}

// Write 写人设文件（不存在则新建；内容原样落盘）
func (l *Library) Write(name, content string) error {
	path, err := l.path(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return fmt.Errorf("创建人设目录失败（%s）：%w", l.dir, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写入人设文件失败（%s）：%w", name, err)
	}
	return nil
}

// Archive 删除 = 归档：移到 <dir>/.archive/，不硬删（与技能删除同口径）
func (l *Library) Archive(name string) error {
	path, err := l.path(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("没有这个人设文件：%s", strings.TrimSpace(name))
		}
		return err
	}
	dir := filepath.Join(l.dir, ".archive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建归档目录失败：%w", err)
	}
	dst := filepath.Join(dir, strings.TrimSuffix(name, ".md")+"-"+time.Now().Format("20060102-150405")+".md")
	if err := os.Rename(path, dst); err != nil {
		return fmt.Errorf("归档人设文件失败（%s）：%w", name, err)
	}
	return nil
}

// Reset 把人设文件恢复成内置模板（只对有模板的三个文件有效）
func (l *Library) Reset(name string) error {
	name = strings.TrimSpace(name)
	tpl, ok := Templates[name]
	if !ok {
		return fmt.Errorf("「%s」没有内置模板（只有 %s 能恢复默认）", name, strings.Join(BuiltinNames(), "、"))
	}
	return l.Write(name, tpl)
}

// Build 拼出要注入系统提示的人设文本；没有任何可用内容时返回空串。
// heartbeatEnabled 控制 AGENTS.md 里 <!-- heartbeat:start/end --> 段落的去留
// （心跳没启用时把那段删掉，避免提示里写着一套不存在的机制）。
func (l *Library) Build(enabled []string, heartbeatEnabled bool) string {
	if l == nil {
		return ""
	}
	parts := make([]string, 0, len(enabled))
	for _, name := range enabled {
		content, ok := l.load(name, heartbeatEnabled)
		if !ok {
			continue
		}
		parts = append(parts, "# "+name+"\n\n"+content)
	}
	return strings.Join(parts, "\n\n")
}

// EnsureTemplates 首次初始化：目录里还没有初始化过时，落一份默认人设模板。
// 用 .initialized 标记，保证「用户把人设全删了」之后重启不会又把模板塞回来。
// 返回是否真的创建了。
func (l *Library) EnsureTemplates() (bool, error) {
	if l == nil {
		return false, nil
	}
	marker := filepath.Join(l.dir, ".initialized")
	if _, err := os.Stat(marker); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return false, fmt.Errorf("创建人设目录失败（%s）：%w", l.dir, err)
	}
	for _, name := range DefaultFiles {
		tpl, ok := Templates[name]
		if !ok {
			continue
		}
		if err := os.WriteFile(filepath.Join(l.dir, name), []byte(tpl), 0o644); err != nil {
			return false, fmt.Errorf("写入人设模板失败（%s）：%w", name, err)
		}
	}
	if err := os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return false, fmt.Errorf("写入初始化标记失败：%w", err)
	}
	return true, nil
}

/* ---------- 内部 ---------- */

func (l *Library) path(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !Valid(name) {
		return "", fmt.Errorf("人设文件名不合法：%q（只允许目录内的 .md 文件名）", name)
	}
	return filepath.Join(l.dir, name), nil
}

func (l *Library) stat(name string) File {
	f := File{Name: name}
	if _, ok := Templates[name]; ok {
		f.Builtin = true
	}
	info, err := os.Stat(filepath.Join(l.dir, name))
	if err != nil {
		return f
	}
	f.Exists = true
	f.Size = info.Size()
	f.ModTime = info.ModTime().UnixMilli()
	raw, err := os.ReadFile(filepath.Join(l.dir, name))
	if err != nil {
		return f
	}
	content := strings.TrimSpace(strings.TrimPrefix(string(raw), "\ufeff"))
	f.Tokens = estimateTokens(content)
	f.Preview = preview(content, 160)
	return f
}

// load 读 + 清洗一个人设文件：去 BOM、去 front-matter、按开关处理心跳段
func (l *Library) load(name string, heartbeatEnabled bool) (string, bool) {
	path, err := l.path(name)
	if err != nil {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false // 启用了但文件不在：跳过，不报错、不阻塞运行
	}
	content := strings.TrimSpace(strings.TrimPrefix(string(raw), "\ufeff"))
	content = stripFrontmatter(content)
	content = processHeartbeat(content, heartbeatEnabled)
	return content, content != ""
}

// stripFrontmatter 去掉开头的 YAML front-matter（--- 包起来的一段），
// 方便在文件头上记 tags/version 之类的元信息而不污染系统提示。
func stripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---") {
		return content
	}
	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return content // 只有开头没有结尾：当普通正文，不动
	}
	return strings.TrimSpace(rest[idx+len("\n---"):])
}

// heartbeat 段的标记：段外内容永远生效，段内内容只在心跳启用时生效
const (
	heartbeatStart = "<!-- heartbeat:start -->"
	heartbeatEnd   = "<!-- heartbeat:end -->"
)

func processHeartbeat(content string, enabled bool) string {
	start := strings.Index(content, heartbeatStart)
	if start < 0 {
		return content
	}
	rel := strings.Index(content[start:], heartbeatEnd)
	if rel < 0 {
		return content // 标记不成对：当普通正文，不动
	}
	stop := start + rel + len(heartbeatEnd)
	before := strings.TrimSpace(content[:start])
	after := strings.TrimSpace(content[stop:])
	if !enabled {
		return strings.TrimSpace(joinParts(before, after))
	}
	section := strings.Replace(content[start:stop], heartbeatStart, "", 1)
	section = strings.TrimSpace(strings.Replace(section, heartbeatEnd, "", 1))
	return strings.TrimSpace(joinParts(before, section, after))
}

func joinParts(parts ...string) string {
	keep := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			keep = append(keep, strings.TrimSpace(p))
		}
	}
	return strings.Join(keep, "\n\n")
}

// preview 取开头一段做列表预览（按行截，避免截出半个字符）
func preview(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 && !utf8ValidBoundary(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}

// utf8ValidBoundary 判断截断处是不是合法的 UTF-8 边界（字节高位不是续接字节）
func utf8ValidBoundary(s string) bool {
	if s == "" {
		return true
	}
	b := s[len(s)-1]
	return b&0xC0 != 0x80
}

// estimateTokens 粗估 token（中文按 1 字 1 token、其余按 4 字节 1 token），
// 与 skills 的口径一致，够用来提示"人设占多少上下文"
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