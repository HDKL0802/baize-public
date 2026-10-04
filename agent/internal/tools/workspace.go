package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Workspace 工作目录沙箱：所有文件类工具只能在工作目录内活动。
// 这是"最小权限"的第一道闸门——越界一律明确拒绝，不做静默兜底。
type Workspace struct {
	root         string
	allowOutside bool
}

// NewWorkspace 创建工作目录
func NewWorkspace(root string, allowOutside bool) (*Workspace, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("工作目录不能为空")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("工作目录非法：%w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("创建工作目录失败：%w", err)
	}
	return &Workspace{root: filepath.Clean(abs), allowOutside: allowOutside}, nil
}

// Root 工作目录
func (w *Workspace) Root() string { return w.root }

// AllowOutside 是否允许越界访问
func (w *Workspace) AllowOutside() bool { return w.allowOutside }

// Resolve 把工具传来的路径解析成绝对路径，并做越界检查。
// 相对路径一律相对「工作目录」解析（不是相对进程的当前目录），
// 否则从别处启动 Agent 时同样的相对路径会指到完全不同的位置。
func (w *Workspace) Resolve(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("路径不能为空")
	}
	raw := p
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(w.root, raw)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("路径非法：%w", err)
	}
	abs = filepath.Clean(abs)
	if w.allowOutside {
		return abs, nil
	}
	rel, err := filepath.Rel(w.root, abs)
	if err != nil {
		return "", fmt.Errorf("路径不在工作目录内：%s", abs)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("拒绝越界访问：%s 不在工作目录 %s 内", abs, w.root)
	}
	return abs, nil
}
