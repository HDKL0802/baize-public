// Package agentrt 是 Agent 运行时（Hermes run_agent 主循环的 Go 重写）：
// provider → 提示词 → 工具调用 → 重试/回退 → 事件钩子 → 上下文压缩 → 持久化，
// 外加变更前快照与回滚。
package agentrt

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CheckpointInfo 一个快照
type CheckpointInfo struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Dir       string `json:"dir"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"createdAt"`
}

// CheckpointManager 快照管理：变更前把工作目录整份拷进 <dataDir>/checkpoints/<id>/
type CheckpointManager struct {
	dataDir  string
	workdir  string
	maxKeep  int
	skipDirs map[string]bool
}

// NewCheckpointManager 创建快照管理器
func NewCheckpointManager(dataDir, workdir string, maxKeep int) (*CheckpointManager, error) {
	if strings.TrimSpace(dataDir) == "" || strings.TrimSpace(workdir) == "" {
		return nil, errors.New("快照需要同时指定数据目录与工作目录")
	}
	if maxKeep <= 0 {
		maxKeep = 20
	}
	return &CheckpointManager{
		dataDir: dataDir,
		workdir: filepath.Clean(workdir),
		maxKeep: maxKeep,
		// 快照目录本身如果在工作目录里，必须跳过，否则会自我递归
		skipDirs: map[string]bool{filepath.Clean(filepath.Join(dataDir, "checkpoints")): true},
	}, nil
}

// Create 打一个快照，返回快照 id
func (m *CheckpointManager) Create(label string) (CheckpointInfo, error) {
	info := CheckpointInfo{
		ID:        "ck" + fmt.Sprint(time.Now().UnixMilli()),
		Label:     label,
		CreatedAt: time.Now().UnixMilli(),
	}
	info.Dir = filepath.Join(m.dataDir, "checkpoints", info.ID)
	if err := os.MkdirAll(info.Dir, 0o755); err != nil {
		return info, fmt.Errorf("创建快照目录失败：%w", err)
	}
	if err := copyTree(m.workdir, info.Dir, m.skipDirs, &info); err != nil {
		return info, err
	}
	if err := writeMeta(filepath.Join(info.Dir, "meta.json"), info); err != nil {
		return info, err
	}
	_ = m.prune()
	return info, nil
}

// List 列出快照（新的在前）
func (m *CheckpointManager) List() ([]CheckpointInfo, error) {
	root := filepath.Join(m.dataDir, "checkpoints")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []CheckpointInfo{}, nil
		}
		return nil, err
	}
	out := []CheckpointInfo{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		info := CheckpointInfo{ID: e.Name(), Dir: dir}
		if raw, err := os.ReadFile(filepath.Join(dir, "meta.json")); err == nil {
			_ = json.Unmarshal(raw, &info)
			info.ID = e.Name()
			info.Dir = dir
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// Rollback 回滚到某个快照（先清空工作目录再恢复）
func (m *CheckpointManager) Rollback(id string) error {
	dir := filepath.Join(m.dataDir, "checkpoints", id)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("找不到快照：%s", id)
	}
	entries, err := os.ReadDir(m.workdir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(m.workdir, e.Name())
		if m.skipDirs[filepath.Clean(p)] {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("清理工作目录失败：%w", err)
		}
	}
	var info CheckpointInfo
	return copyTree(dir, m.workdir, map[string]bool{"meta.json": false}, &info)
}

func (m *CheckpointManager) prune() error {
	list, err := m.List()
	if err != nil || len(list) <= m.maxKeep {
		return err
	}
	for _, info := range list[m.maxKeep:] {
		_ = os.RemoveAll(info.Dir)
	}
	return nil
}

const maxSnapshotFile = 32 << 20 // 单个文件超过 32MB 不进快照（避免把磁盘打满）

func copyTree(from, to string, skip map[string]bool, info *CheckpointInfo) error {
	return filepath.Walk(from, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if skip[filepath.Clean(path)] || skip[rel] {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(to, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		if fi.Size() > maxSnapshotFile {
			return nil
		}
		if err := copyFile(path, target); err != nil {
			return err
		}
		info.Files++
		info.Bytes += fi.Size()
		return nil
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func writeMeta(path string, info CheckpointInfo) error {
	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
