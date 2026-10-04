// Package kb 是白泽知识库：待办与密码本的正本，落在后端数据目录里。
//
// 为什么放后端：用户要求「手机上不留正本，数据全落 NAS，App 通过后端读写」。
// 领域能力直接复用 baize/core（数据模型、密码合并与历史、加解密、统计口径全一致），
// 所以手机内核把它当远端用时，两边行为不会漂移。
package kb

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"baize/core"
)

// Version 知识库模块版本
const Version = "0.1.0"

// Service 知识库
type Service struct {
	dir   string
	lg    *slog.Logger
	core  *core.Service
	files *FileStore
}

// Open 打开（或首次创建）后端数据目录下的知识库
func Open(dataDir string, lg *slog.Logger) (*Service, error) {
	if lg == nil {
		lg = slog.Default()
	}
	dir := filepath.Join(dataDir, "kb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建知识库目录失败：%w", err)
	}
	svc, err := core.NewService(dir, "")
	if err != nil {
		return nil, fmt.Errorf("打开知识库失败：%w", err)
	}
	files, err := openFileStore(filepath.Join(dir, "files"))
	if err != nil {
		return nil, err
	}
	return &Service{dir: dir, lg: lg, core: svc, files: files}, nil
}

// Dir 知识库目录
func (s *Service) Dir() string { return s.dir }

// Files 附件仓库
func (s *Service) Files() *FileStore { return s.files }

// State 知识库快照（待办 + 密码本 + 统计）
func (s *Service) State() (core.Snapshot, error) { return s.core.Snapshot() }

// Do 执行一条操作：op 口径与手机内核的 /api/op 完全一致
func (s *Service) Do(op string, args json.RawMessage) (any, error) {
	op = strings.TrimSpace(op)
	if op == "" {
		return nil, errors.New("op 不能为空")
	}
	return s.core.Do(core.OpRequest{Op: op, Args: args})
}

// Stats 控制台要用的概览
type Stats struct {
	Dir       string `json:"dir"`
	Todos     int    `json:"todos"`
	User      int    `json:"user"`
	Agent     int    `json:"agent"`
	Done      int    `json:"done"`
	Overdue   int    `json:"overdue"`
	Passwords int    `json:"passwords"`
	Locked    bool   `json:"vaultLocked"`
	HasPwd    bool   `json:"hasMasterPwd"`
	Files     int    `json:"files"`
	FileBytes int64  `json:"fileBytes"`
}

// Stats 汇总知识库现状（待办用核心口径统计，附件走文件仓库）
func (s *Service) Stats() Stats {
	raw, why := s.core.RunDeviceAction("todo.stats", nil)
	out := Stats{Dir: s.dir}
	if snap, err := s.core.Snapshot(); err == nil {
		out.Passwords = len(snap.Vault)
		out.Locked = snap.VaultLocked
		out.HasPwd = snap.HasMasterPwd
	}
	if why == "" && raw != nil {
		out.Todos = toInt(raw["total"])
		out.User = toInt(raw["user"])
		out.Agent = toInt(raw["agent"])
		out.Done = toInt(raw["done"])
		out.Overdue = toInt(raw["overdue"])
	}
	if s.files != nil {
		if list, err := s.files.List(0); err == nil {
			out.Files = len(list)
			for _, f := range list {
				out.FileBytes += f.Size
			}
		}
	}
	return out
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}
