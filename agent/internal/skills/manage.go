package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"baize/internal/tools"
)

// 本文件是 Hermes「技能管理（skill_manage）」的 Go 重写：
// 让 Agent 自己把"学到的做法"沉淀成技能，走与 skills.go 相同的目录结构
// （<skills>/[分类/]<名字>/SKILL.md + references|templates|scripts|assets）。
//
// 两条口径与 Hermes 保持一致：
//  1. 新建技能时 description 必须能塞进系统提示的一行预算（超了会被截断，触发信息就没了）；
//  2. 删除不硬删，一律归档到 <skills>/.archive/，人还能捞回来。

const (
	// MaxNameLength 技能名长度上限
	MaxNameLength = 64
	// MaxDescriptionLength description 长度上限
	MaxDescriptionLength = 1024
	// MaxContentChars SKILL.md 正文长度上限
	MaxContentChars = 100_000
	// MaxFileBytes 单个支持文件大小上限（1 MiB）
	MaxFileBytes = 1 << 20
	// promptDescLimit 技能清单里 description 的展示预算（与 Hermes 的 SKILL_PROMPT_DESC_LIMIT 对齐）
	promptDescLimit = 60
	// archiveDirName 技能归档目录
	archiveDirName = ".archive"
)

// AllowedSubdirs 允许写支持文件的子目录
var AllowedSubdirs = []string{"references", "templates", "scripts", "assets"}

var (
	validNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	nameRule    = "只能用小写字母、数字、连字符、点和下划线"
)

// Manager 技能管理（新建 / 改写 / 写支持文件 / 归档删除）
type Manager struct {
	lib *Library
	mu  sync.Mutex // 整个管理流程串行：技能数量少，简单比精致更不容易出错
}

// NewManager 基于一个技能库建管理器（写完原地刷新该库，无需再通知别处）
func NewManager(lib *Library) *Manager { return &Manager{lib: lib} }

// Dir 技能根目录
func (m *Manager) Dir() string {
	if m == nil || m.lib == nil {
		return ""
	}
	return m.lib.Dir()
}

/* ---------- 校验 ---------- */

func (m *Manager) ready() error {
	if m == nil || m.lib == nil {
		return errors.New("技能管理未启用")
	}
	if strings.TrimSpace(m.Dir()) == "" {
		return errors.New("技能目录未配置（请在后端配置里设置 skillsDir，或使用默认的数据目录）")
	}
	return nil
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("技能名不能为空")
	}
	if len([]rune(name)) > MaxNameLength {
		return fmt.Errorf("技能名超过 %d 字", MaxNameLength)
	}
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("技能名 %q 不合法：%s，且必须以字母或数字开头", name, nameRule)
	}
	return nil
}

func validateCategory(category string) error {
	category = strings.TrimSpace(category)
	if category == "" {
		return nil
	}
	if strings.ContainsAny(category, `/\`) {
		return fmt.Errorf("分类 %q 不合法：只能是单层目录名，不能带斜杠", category)
	}
	if len([]rune(category)) > MaxNameLength {
		return fmt.Errorf("分类名超过 %d 字", MaxNameLength)
	}
	if !validNameRe.MatchString(category) {
		return fmt.Errorf("分类 %q 不合法：%s，且必须以字母或数字开头", category, nameRule)
	}
	return nil
}

// splitFrontmatter 解析 YAML front-matter（逐行 key: value，与 skills.go 的口径一致）
func splitFrontmatter(content string) (map[string]string, string, error) {
	text := strings.ReplaceAll(strings.TrimPrefix(content, "\ufeff"), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, "", errors.New("SKILL.md 必须以 YAML front-matter（---）开头，可参考已有技能的写法")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, "", errors.New("SKILL.md 的 front-matter 没有闭合（缺少收尾那一行 ---）")
	}
	fields := map[string]string{}
	for _, line := range lines[1:end] {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue // 嵌套结构/续行：本实现只看顶层字段
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if val != "" {
			fields[key] = val
		}
	}
	return fields, strings.TrimSpace(strings.Join(lines[end+1:], "\n")), nil
}

func validateFrontmatter(content string, newSkill bool) error {
	if strings.TrimSpace(content) == "" {
		return errors.New("内容不能为空")
	}
	fields, body, err := splitFrontmatter(content)
	if err != nil {
		return err
	}
	if fields["name"] == "" {
		return errors.New("front-matter 里必须有 name 字段")
	}
	desc := fields["description"]
	if desc == "" {
		return errors.New("front-matter 里必须有 description 字段")
	}
	if n := len([]rune(desc)); n > MaxDescriptionLength {
		return fmt.Errorf("description 有 %d 字，超过 %d 字上限", n, MaxDescriptionLength)
	}
	if newSkill {
		if n := len([]rune(desc)); n > promptDescLimit {
			return fmt.Errorf("description 有 %d 字，新建技能必须控制在 %d 字以内（一句话、先说触发场景、以句号结尾）：技能清单只展示前 %d 字，再长会把触发信息截掉，请把细节移到正文里",
				n, promptDescLimit, promptDescLimit-3)
		}
	}
	if body == "" {
		return errors.New("front-matter 之后必须有正文（步骤、说明等），不能只有抬头")
	}
	return nil
}

func validateContentSize(content, label string) error {
	if n := len([]rune(content)); n > MaxContentChars {
		return fmt.Errorf("%s 有 %d 字，超过 %d 字上限：请拆成更小的 SKILL.md，把细节放进 references/ 或 templates/ 里",
			label, n, MaxContentChars)
	}
	return nil
}

// validateFilePath 支持文件路径：必须在允许的子目录下，且不能跳出技能目录
func validateFilePath(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return errors.New("file_path 不能为空")
	}
	if filepath.IsAbs(p) || strings.Contains(p, ":") {
		return errors.New("file_path 必须是技能目录内的相对路径")
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	parts := strings.Split(clean, "/")
	for _, seg := range parts {
		if seg == ".." {
			return errors.New("file_path 里不允许出现 ..")
		}
	}
	if len(parts) == 1 && parts[0] == "SKILL.md" {
		return nil
	}
	if len(parts) == 2 && parts[1] == "SKILL.md" {
		return nil
	}
	if !containsStr(AllowedSubdirs, parts[0]) {
		return fmt.Errorf("文件必须放在这些目录之一：%s（收到的是 %q）", strings.Join(AllowedSubdirs, "、"), p)
	}
	if len(parts) < 2 {
		return fmt.Errorf("请给出文件路径而不是目录，例如 %s/example.md", parts[0])
	}
	return nil
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

/* ---------- 定位与写盘 ---------- */

// locateAll 找出所有同名技能目录（bare 名字，或分类相对路径）
func (m *Manager) locateAll(name string) []string {
	root := m.Dir()
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if strings.ContainsAny(name, `/\`) {
		cand := filepath.Join(root, filepath.FromSlash(filepath.ToSlash(name)))
		if isSkillDir(cand) {
			return []string{cand}
		}
		return nil
	}
	hits := []string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == root {
			return nil
		}
		base := d.Name()
		if base == archiveDirName || strings.HasPrefix(base, ".") {
			return fs.SkipDir
		}
		if base == name && isSkillDir(p) {
			hits = append(hits, p)
		}
		return nil
	})
	return hits
}

func isSkillDir(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	return err == nil && !st.IsDir()
}

// locate 必须唯一命中
func (m *Manager) locate(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("name 不能为空")
	}
	hits := m.locateAll(name)
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("没有找到技能 %q（现有：%s）", name, strings.Join(m.lib.Names(), "、"))
	case 1:
		return hits[0], nil
	default:
		return "", fmt.Errorf("技能名 %q 有多个同名目录，无法确定改哪个：%s", name, strings.Join(hits, "、"))
	}
}

func relTo(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil {
		return filepath.ToSlash(rel)
	}
	return p
}

// writeFileAtomic 先写临时文件再改名：写一半断电也不会留下半截 SKILL.md
func writeFileAtomic(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败（%s）：%w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败：%w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("写入失败：%w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("写入失败：%w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("保存失败（%s）：%w", path, err)
	}
	return nil
}

// afterWrite 写完刷新技能库；刷新失败不吞掉，作为 warning 带回给模型
func (m *Manager) afterWrite(res map[string]any) map[string]any {
	if err := m.lib.Reload(); err != nil {
		res["warning"] = "技能已写入，但技能库重新加载失败：" + err.Error()
	}
	return res
}

/* ---------- 动作 ---------- */

// Create 新建技能：<skills>/[分类/]<名字>/SKILL.md
func (m *Manager) Create(name, category, content string) (map[string]any, error) {
	if err := m.ready(); err != nil {
		return nil, err
	}
	name, category = strings.TrimSpace(name), strings.TrimSpace(category)
	if err := validateName(name); err != nil {
		return nil, err
	}
	if err := validateCategory(category); err != nil {
		return nil, err
	}
	if err := validateFrontmatter(content, true); err != nil {
		return nil, err
	}
	if err := validateContentSize(content, "SKILL.md"); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if hits := m.locateAll(name); len(hits) > 0 {
		return nil, fmt.Errorf("已经有一个叫 %q 的技能了：%s", name, relTo(m.Dir(), hits[0]))
	}
	dir := filepath.Join(m.Dir(), category, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建技能目录失败：%w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, "SKILL.md"), content); err != nil {
		return nil, err
	}
	res := map[string]any{
		"success": true,
		"message": fmt.Sprintf("技能 %q 已创建", name),
		"path":    relTo(m.Dir(), dir),
		"hint":    fmt.Sprintf("要再加参考文件，用 skill_manage(action='write_file', name='%s', file_path='references/example.md', file_content='...')", name),
	}
	return m.afterWrite(res), nil
}

// Edit 整篇重写已有技能的 SKILL.md
func (m *Manager) Edit(name, content string) (map[string]any, error) {
	if err := m.ready(); err != nil {
		return nil, err
	}
	if err := validateFrontmatter(content, false); err != nil {
		return nil, err
	}
	if err := validateContentSize(content, "SKILL.md"); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, err := m.locate(name)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, "SKILL.md"), content); err != nil {
		return nil, err
	}
	return m.afterWrite(map[string]any{
		"success": true,
		"message": fmt.Sprintf("技能 %q 已整篇重写", name),
		"path":    relTo(m.Dir(), dir),
	}), nil
}

// Patch 局部替换：默认改 SKILL.md，也可指定支持文件
func (m *Manager) Patch(name, oldString, newString, filePath string, replaceAll bool) (map[string]any, error) {
	if err := m.ready(); err != nil {
		return nil, err
	}
	if oldString == "" {
		return nil, errors.New("old_string 不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, err := m.locate(name)
	if err != nil {
		return nil, err
	}
	label := "SKILL.md"
	target := filepath.Join(dir, "SKILL.md")
	if strings.TrimSpace(filePath) != "" {
		if err := validateFilePath(filePath); err != nil {
			return nil, err
		}
		label = filepath.ToSlash(filepath.Clean(filePath))
		if label == "SKILL.md" {
			target = filepath.Join(dir, "SKILL.md")
		} else {
			// 允许 "<skill>/SKILL.md" 这种写法
			parts := strings.Split(label, "/")
			if parts[len(parts)-1] == "SKILL.md" {
				target = filepath.Join(dir, "SKILL.md")
			} else {
				target = filepath.Join(dir, filepath.FromSlash(label))
			}
		}
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("技能 %q 里没有这个文件：%s", name, label)
		}
		return nil, fmt.Errorf("读取失败（%s）：%w", label, err)
	}
	content := strings.ReplaceAll(strings.TrimPrefix(string(raw), "\ufeff"), "\r\n", "\n")
	count := strings.Count(content, oldString)
	if count == 0 {
		return nil, fmt.Errorf("%s 里找不到要替换的内容（old_string 必须与原文逐字一致，含缩进）", label)
	}
	if count > 1 && !replaceAll {
		return nil, fmt.Errorf("%s 里匹配到 %d 处，不唯一：请把 old_string 写得更长以定位一处，或显式传 replace_all=true 全部替换", label, count)
	}
	next := strings.ReplaceAll(content, oldString, newString)
	if label == "SKILL.md" {
		if err := validateFrontmatter(next, false); err != nil {
			return nil, fmt.Errorf("这次替换会破坏 SKILL.md 的结构：%v", err)
		}
	}
	if err := validateContentSize(next, label); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(target, next); err != nil {
		return nil, err
	}
	return m.afterWrite(map[string]any{
		"success":    true,
		"message":    fmt.Sprintf("技能 %q 的 %s 已替换 %d 处", name, label, count),
		"matchCount": count,
	}), nil
}

// WriteFile 写支持文件（references/ templates/ scripts/ assets/）
func (m *Manager) WriteFile(name, filePath, content string) (map[string]any, error) {
	if err := m.ready(); err != nil {
		return nil, err
	}
	if err := validateFilePath(filePath); err != nil {
		return nil, err
	}
	if len(content) > MaxFileBytes {
		return nil, fmt.Errorf("文件内容 %d 字节，超过 %d 字节上限（1 MiB）：请拆成更小的文件", len(content), MaxFileBytes)
	}
	if err := validateContentSize(content, filepath.ToSlash(filePath)); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, err := m.locate(name)
	if err != nil {
		return nil, err
	}
	label := filepath.ToSlash(filepath.Clean(filePath))
	target := filepath.Join(dir, filepath.FromSlash(label))
	if err := writeFileAtomic(target, content); err != nil {
		return nil, err
	}
	return m.afterWrite(map[string]any{
		"success": true,
		"message": fmt.Sprintf("技能 %q 的 %s 已写入", name, label),
		"path":    relTo(m.Dir(), target),
	}), nil
}

// RemoveFile 删支持文件（只允许在允许的子目录里）
func (m *Manager) RemoveFile(name, filePath string) (map[string]any, error) {
	if err := m.ready(); err != nil {
		return nil, err
	}
	if err := validateFilePath(filePath); err != nil {
		return nil, err
	}
	label := filepath.ToSlash(filepath.Clean(filePath))
	if label == "SKILL.md" || strings.HasSuffix(label, "/SKILL.md") {
		return nil, errors.New("SKILL.md 不能用 remove_file 删；要删整个技能请用 skill_delete")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, err := m.locate(name)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(dir, filepath.FromSlash(label))
	if _, err := os.Stat(target); err != nil {
		available := []string{}
		for _, sub := range AllowedSubdirs {
			base := filepath.Join(dir, sub)
			_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					available = append(available, relTo(dir, p))
				}
				return nil
			})
		}
		return nil, fmt.Errorf("技能 %q 里没有 %s（现有文件：%s）", name, label, strings.Join(available, "、"))
	}
	if err := os.Remove(target); err != nil {
		return nil, fmt.Errorf("删除失败（%s）：%w", label, err)
	}
	_ = os.Remove(filepath.Dir(target)) // 目录空了就顺手收掉，非空会失败，忽略
	return map[string]any{
		"success": true,
		"message": fmt.Sprintf("技能 %q 的 %s 已删除", name, label),
	}, nil
}

// Delete 删技能：归档到 <skills>/.archive/<名字>-<时间>/，不是硬删。
// 归档目录是点目录，不会被当成技能加载，但人能进去捞回来。
func (m *Manager) Delete(name string) (map[string]any, error) {
	if err := m.ready(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, err := m.locate(name)
	if err != nil {
		return nil, err
	}
	root := m.Dir()
	// 安全护栏：只允许归档技能根目录之下的目录（<root>/<name> 或 <root>/<分类>/<name>），
	// 其余一律拒绝——宁可报错，也绝不误删技能目录之外的东西。
	if !strings.HasPrefix(filepath.Clean(dir), filepath.Clean(root)+string(os.PathSeparator)) {
		return nil, fmt.Errorf("拒绝删除：技能目录 %s 不在技能根目录 %s 之下", dir, root)
	}
	archiveRoot := filepath.Join(root, archiveDirName)
	if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
		return nil, fmt.Errorf("创建归档目录失败：%w", err)
	}
	stamp := time.Now().Format("20060102-150405")
	dest := filepath.Join(archiveRoot, filepath.Base(dir)+"-"+stamp)
	for i := 1; ; i++ {
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			break
		}
		dest = filepath.Join(archiveRoot, fmt.Sprintf("%s-%s-%d", filepath.Base(dir), stamp, i))
	}
	if err := os.Rename(dir, dest); err != nil {
		return nil, fmt.Errorf("归档失败（%s → %s）：%w", dir, dest, err)
	}
	// 分类目录空了就收掉（绝不动技能根目录）
	if parent := filepath.Dir(dir); filepath.Clean(parent) != filepath.Clean(root) {
		_ = os.Remove(parent)
	}
	return m.afterWrite(map[string]any{
		"success":    true,
		"message":    fmt.Sprintf("技能 %q 已归档（没硬删，需要时可从归档目录恢复）", name),
		"archivedTo": relTo(root, dest),
	}), nil
}

/* ---------- 工具注册 ---------- */

// ManageTool 技能管理工具：新建 / 整篇重写 / 局部替换 / 写支持文件 / 删支持文件。
// 这些操作都不会毁掉已有技能（删支持文件也只删自己写进去的），所以免审批，
// 但改动前要打快照（Mutating），出问题能回滚。
type ManageTool struct{ M *Manager }

// Name 工具名（只允许字母数字下划线，见 llm.CheckToolNames）
func (t *ManageTool) Name() string { return "skill_manage" }

// Description 说明
func (t *ManageTool) Description() string {
	return "把你的做法沉淀成技能（SKILL.md），或修改已有技能。技能清单会进系统提示，之后照做时用 skill_load 取全文。"
}

// Mutating 会写文件：执行前打快照
func (t *ManageTool) Mutating() bool { return true }

// Schema 参数说明
func (t *ManageTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"create", "edit", "patch", "write_file", "remove_file"},
				"description": "create=新建技能（要 content）；edit=整篇重写 SKILL.md（要 content）；" +
					"patch=在 SKILL.md 或支持文件里做局部替换（要 old_string/new_string）；" +
					"write_file=写参考/模板/脚本等支持文件（要 file_path/file_content）；remove_file=删支持文件（要 file_path）",
			},
			"name":         map[string]any{"type": "string", "description": "技能名：小写字母、数字、连字符、点、下划线，字母或数字开头"},
			"category":     map[string]any{"type": "string", "description": "可选：单层分类目录名（例如 ops、writing）"},
			"content":      map[string]any{"type": "string", "description": "SKILL.md 全文：必须带 YAML front-matter（name + description），后面跟正文"},
			"file_path":    map[string]any{"type": "string", "description": "支持文件路径，必须在 references/ templates/ scripts/ assets/ 之一，例如 references/checklist.md"},
			"file_content": map[string]any{"type": "string", "description": "支持文件内容"},
			"old_string":   map[string]any{"type": "string", "description": "patch：要被替换的原文（必须逐字一致，默认要求唯一匹配）"},
			"new_string":   map[string]any{"type": "string", "description": "patch：替换成的内容（传空字符串表示删掉匹配内容）"},
			"replace_all":  map[string]any{"type": "boolean", "description": "patch：是否替换所有匹配（默认 false，多匹配会报错）"},
		},
		"required": []string{"action", "name"},
	}
}

// Run 执行
func (t *ManageTool) Run(_ context.Context, args map[string]any) (any, error) {
	if t.M == nil {
		return nil, errors.New("技能管理未启用")
	}
	action := strings.ToLower(tools.ArgString(args, "action"))
	name := tools.ArgString(args, "name")
	switch action {
	case "create", "edit", "write_file":
		if _, ok := args["content"]; !ok && action != "write_file" {
			return nil, errors.New(action + " 需要 content（SKILL.md 全文）")
		}
		if _, ok := args["file_content"]; !ok && action == "write_file" {
			return nil, errors.New("write_file 需要 file_content")
		}
	case "patch":
		if _, ok := args["new_string"]; !ok {
			return nil, errors.New("patch 需要 new_string（想删掉匹配内容就显式传空字符串）")
		}
	}
	var (
		res map[string]any
		err error
	)
	switch action {
	case "create":
		res, err = t.M.Create(name, tools.ArgString(args, "category"), tools.ArgString(args, "content"))
	case "edit":
		res, err = t.M.Edit(name, tools.ArgString(args, "content"))
	case "patch":
		res, err = t.M.Patch(name, tools.ArgString(args, "old_string"), tools.ArgString(args, "new_string"),
			tools.ArgString(args, "file_path"), tools.ArgBool(args, "replace_all"))
	case "write_file":
		res, err = t.M.WriteFile(name, tools.ArgString(args, "file_path"), tools.ArgString(args, "file_content"))
	case "remove_file":
		res, err = t.M.RemoveFile(name, tools.ArgString(args, "file_path"))
	default:
		return nil, fmt.Errorf("不支持的 action：%q（可用：create、edit、patch、write_file、remove_file）", action)
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

// DeleteTool 删除技能（归档，可恢复）。删东西必须人工审批。
type DeleteTool struct{ M *Manager }

// Name 工具名
func (t *DeleteTool) Name() string { return "skill_delete" }

// Description 说明
func (t *DeleteTool) Description() string {
	return "删除一个技能（会归档到技能目录下的 .archive/，不硬删，需要人工审批）。"
}

// Dangerous 需要人工审批
func (t *DeleteTool) Dangerous() bool { return true }

// Mutating 会动技能目录：执行前打快照
func (t *DeleteTool) Mutating() bool { return true }

// Schema 参数说明
func (t *DeleteTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "要删除的技能名"},
		},
		"required": []string{"name"},
	}
}

// Run 执行
func (t *DeleteTool) Run(_ context.Context, args map[string]any) (any, error) {
	if t.M == nil {
		return nil, errors.New("技能管理未启用")
	}
	return t.M.Delete(tools.ArgString(args, "name"))
}

// RegisterTools 注册技能管理工具（技能目录没配好就不注册，免得模型调到一半才发现不能用）
func RegisterTools(r *tools.Registry, m *Manager) {
	if r == nil || m == nil || strings.TrimSpace(m.Dir()) == "" {
		return
	}
	r.Register(&ManageTool{M: m})
	r.Register(&DeleteTool{M: m})
}
