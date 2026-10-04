package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

/* ---------- fs_read ---------- */

// FSRead 读文件
type FSRead struct{ ws *Workspace }

// NewFSRead 创建 fs_read
func NewFSRead(ws *Workspace) *FSRead { return &FSRead{ws: ws} }

// Name 工具名
func (t *FSRead) Name() string { return "fs_read" }

// Description 说明
func (t *FSRead) Description() string {
	return "读取工作目录内的文本文件，返回内容（超出上限会截断并标注）；路径必须是工作目录内的相对路径或绝对路径"
}

// Schema 参数说明
func (t *FSRead) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":     map[string]any{"type": "string", "description": "文件路径"},
			"maxBytes": map[string]any{"type": "integer", "description": "最多读取多少字节，默认 65536"},
		},
		"required": []string{"path"},
	}
}

// Run 执行
func (t *FSRead) Run(_ context.Context, args map[string]any) (any, error) {
	abs, err := t.ws.Resolve(ArgString(args, "path"))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("文件不存在：%s", abs)
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s 是目录，请用 fs_list", abs)
	}
	max := ArgInt(args, "maxBytes", 64*1024)
	if max <= 0 {
		max = 64 * 1024
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, max)
	n, err := f.Read(buf)
	if err != nil && n == 0 && !errors.Is(err, os.ErrClosed) && err.Error() != "EOF" {
		return nil, err
	}
	content := string(buf[:n])
	truncated := info.Size() > int64(n)
	return map[string]any{
		"path":      abs,
		"size":      info.Size(),
		"truncated": truncated,
		"content":   content,
	}, nil
}

/* ---------- fs_write ---------- */

// FSWrite 写文件（危险：会改磁盘，主循环会在执行前打快照）
type FSWrite struct{ ws *Workspace }

// NewFSWrite 创建 fs_write
func NewFSWrite(ws *Workspace) *FSWrite { return &FSWrite{ws: ws} }

// Name 工具名
func (t *FSWrite) Name() string { return "fs_write" }

// Description 说明
func (t *FSWrite) Description() string {
	return "在工作目录内写入/追加文本文件（父目录会自动创建）。append=true 时追加到文件末尾"
}

// Schema 参数说明
func (t *FSWrite) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "文件路径"},
			"content": map[string]any{"type": "string", "description": "要写入的内容"},
			"append":  map[string]any{"type": "boolean", "description": "是否追加，默认 false（覆盖）"},
		},
		"required": []string{"path", "content"},
	}
}

// Dangerous 写文件在沙箱内，不需要人工审批（审批口径见文档：删除/支付/发布/发消息/改权限）
func (t *FSWrite) Dangerous() bool { return false }

// Mutating 会改工作目录 → 执行前自动打快照
func (t *FSWrite) Mutating() bool { return true }

// Run 执行
func (t *FSWrite) Run(_ context.Context, args map[string]any) (any, error) {
	abs, err := t.ws.Resolve(ArgString(args, "path"))
	if err != nil {
		return nil, err
	}
	content := ""
	if v, ok := args["content"]; ok && v != nil {
		content = fmt.Sprint(v)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, fmt.Errorf("创建父目录失败：%w", err)
	}
	appendMode := ArgBool(args, "append")
	flag := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(abs, flag, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n, err := f.WriteString(content)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path": abs, "bytes": n, "append": appendMode, "at": time.Now().UnixMilli(),
	}, nil
}

/* ---------- fs_list ---------- */

// FSList 列目录
type FSList struct{ ws *Workspace }

// NewFSList 创建 fs_list
func NewFSList(ws *Workspace) *FSList { return &FSList{ws: ws} }

// Name 工具名
func (t *FSList) Name() string { return "fs_list" }

// Description 说明
func (t *FSList) Description() string {
	return "列出工作目录内某个目录下的文件与子目录（默认工作目录根）"
}

// Schema 参数说明
func (t *FSList) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "目录路径，默认 ."},
		},
	}
}

// Run 执行
func (t *FSList) Run(_ context.Context, args map[string]any) (any, error) {
	p := ArgString(args, "path")
	if p == "" {
		p = "."
	}
	abs, err := t.ws.Resolve(p)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("读取目录失败：%w", err)
	}
	type item struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size"`
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		size := int64(0)
		if err == nil {
			size = info.Size()
		}
		items = append(items, item{Name: e.Name(), IsDir: e.IsDir(), Size: size})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir != items[j].IsDir {
			return items[i].IsDir
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return map[string]any{"path": abs, "count": len(items), "items": items}, nil
}

/* ---------- fs_delete ---------- */

// FSDelete 删除文件/目录（危险，必须过审批闸门）
type FSDelete struct{ ws *Workspace }

// NewFSDelete 创建 fs_delete
func NewFSDelete(ws *Workspace) *FSDelete { return &FSDelete{ws: ws} }

// Name 工具名
func (t *FSDelete) Name() string { return "fs_delete" }

// Description 说明
func (t *FSDelete) Description() string {
	return "删除工作目录内的文件或目录（危险操作：需要人工审批；工作目录本身、系统目录一律拒绝）"
}

// Schema 参数说明
func (t *FSDelete) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "要删除的文件或目录路径"},
		},
		"required": []string{"path"},
	}
}

// Dangerous 需要人工审批（删除类动作，文档明确要求）
func (t *FSDelete) Dangerous() bool { return true }

// Mutating 会改工作目录 → 执行前自动打快照
func (t *FSDelete) Mutating() bool { return true }

// Run 执行
func (t *FSDelete) Run(_ context.Context, args map[string]any) (any, error) {
	abs, err := t.ws.Resolve(ArgString(args, "path"))
	if err != nil {
		return nil, err
	}
	if abs == t.ws.Root() {
		return nil, fmt.Errorf("拒绝删除工作目录本身：%s", abs)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("目标不存在：%s", abs)
		}
		return nil, err
	}
	if info.IsDir() {
		if err := os.RemoveAll(abs); err != nil {
			return nil, err
		}
	} else if err := os.Remove(abs); err != nil {
		return nil, err
	}
	return map[string]any{"path": abs, "removed": true, "at": time.Now().UnixMilli()}, nil
}

// RegisterFS 注册全部文件类工具
func RegisterFS(r *Registry, ws *Workspace) {
	r.Register(NewFSRead(ws))
	r.Register(NewFSWrite(ws))
	r.Register(NewFSList(ws))
	r.Register(NewFSDelete(ws))
}
