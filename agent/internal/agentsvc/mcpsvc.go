package agentsvc

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"baize/internal/backup"
	"baize/internal/mcp"
)

/* ---------- MCP 热插拔 ---------- */

// MCP 当前 MCP 服务状态
func (s *Service) MCP() []mcp.ServerState {
	if s.mcp == nil {
		return nil
	}
	return s.mcp.Status()
}

// MCPSave 覆盖式保存 MCP 服务列表（改完立刻热插拔生效，并写回 config.json）
func (s *Service) MCPSave(cfgs []mcp.ServerConfig) ([]mcp.ServerState, error) {
	s.mu.Lock()
	cfg := s.cfg
	cfg.MCPServers = append([]mcp.ServerConfig{}, cfgs...)
	s.mu.Unlock()
	if err := s.SaveConfig(cfg); err != nil {
		return nil, err
	}
	return s.MCP(), nil
}

// MCPUpsert 新增或更新一个 MCP 服务（按 name 匹配）
func (s *Service) MCPUpsert(one mcp.ServerConfig) ([]mcp.ServerState, error) {
	s.mu.RLock()
	cfgs := append([]mcp.ServerConfig{}, s.cfg.MCPServers...)
	s.mu.RUnlock()

	if strings.TrimSpace(one.Name) == "" {
		return nil, errors.New("MCP 服务必须有 name")
	}
	found := false
	for i := range cfgs {
		if cfgs[i].Name == one.Name {
			cfgs[i] = one
			found = true
			break
		}
	}
	if !found {
		cfgs = append(cfgs, one)
	}
	return s.MCPSave(cfgs)
}

// MCPRemove 删掉一个 MCP 服务（配置一并移除，连接随即断开）
func (s *Service) MCPRemove(name string) ([]mcp.ServerState, error) {
	s.mu.RLock()
	cfgs := append([]mcp.ServerConfig{}, s.cfg.MCPServers...)
	s.mu.RUnlock()

	name = strings.TrimSpace(name)
	kept := make([]mcp.ServerConfig, 0, len(cfgs))
	hit := false
	for _, c := range cfgs {
		if c.Name == name {
			hit = true
			continue
		}
		kept = append(kept, c)
	}
	if !hit {
		return nil, errors.New("没有这个 MCP 服务：" + name)
	}
	return s.MCPSave(kept)
}

// MCPReload 强制重连某个（或全部）MCP 服务
func (s *Service) MCPReload(name string) ([]mcp.ServerState, error) {
	if s.mcp == nil {
		return nil, errors.New("MCP 管理器不可用")
	}
	if strings.TrimSpace(name) == "" {
		s.mu.RLock()
		cfgs := append([]mcp.ServerConfig{}, s.cfg.MCPServers...)
		s.mu.RUnlock()
		return s.mcp.Apply(cfgs), nil
	}
	if _, ok := s.mcp.ServerConfig(name); !ok {
		return nil, errors.New("没有这个 MCP 服务：" + name)
	}
	return s.mcp.Reload(name), nil
}

// MCPCall 直接调一个远端 MCP 工具（控制台/CLI 手动调用，走同一套连接）
func (s *Service) MCPCall(ctx context.Context, server, tool string, args map[string]any) (mcp.CallResult, error) {
	if s.mcp == nil {
		return mcp.CallResult{}, errors.New("MCP 管理器不可用")
	}
	if strings.TrimSpace(server) == "" || strings.TrimSpace(tool) == "" {
		return mcp.CallResult{}, errors.New("server 与 tool 都不能为空")
	}
	return s.mcp.CallTool(ctx, server, tool, args)
}

// MCPTools 当前已连接服务提供的工具清单（热插拔后的实时结果）
func (s *Service) MCPTools() []mcp.ToolRef {
	if s.mcp == nil {
		return nil
	}
	return s.mcp.ToolRefs()
}

// MCPHints 给控制台显示的精简状态
func (s *Service) MCPHints() string {
	if s.mcp == nil {
		return ""
	}
	return s.mcp.StatusHint()
}

/* ---------- 备份 / 恢复 ---------- */

// Backups 备份列表
func (s *Service) Backups() ([]backup.Archive, error) { return backup.List(s.dataDir) }

// BackupDir 备份目录
func (s *Service) BackupDir() string { return backup.Dir(s.dataDir) }

// CreateBackup 打一份备份；sel 为零值时按"全选"处理
func (s *Service) CreateBackup(sel backup.Selection, note string) (backup.Manifest, error) {
	if sel.Empty() {
		sel = backup.Full()
	}
	man, err := backup.Create(s.dataDir, Version, "", sel, note)
	if err != nil {
		return man, err
	}
	// 自动备份只留最近 N 份，避免把磁盘塞满
	s.mu.RLock()
	keep := s.cfg.BackupKeep
	s.mu.RUnlock()
	if removed, err := backup.Prune(s.dataDir, keep); err != nil {
		s.lg.Warn("清理旧备份失败", "err", err)
	} else if len(removed) > 0 {
		s.lg.Info("已清理旧备份", "removed", removed)
	}
	return man, nil
}

// VerifyBackup 校验一份备份包
func (s *Service) VerifyBackup(path string) (backup.Manifest, error) {
	p, err := s.resolveBackupPath(path)
	if err != nil {
		return backup.Manifest{}, err
	}
	return backup.Verify(p)
}

// RestoreBackup 从备份恢复（默认先打安全点；dryRun 只校验不动数据）
func (s *Service) RestoreBackup(path string, opts backup.RestoreOptions) (backup.RestoreResult, error) {
	p, err := s.resolveBackupPath(path)
	if err != nil {
		return backup.RestoreResult{}, err
	}
	res, err := backup.Restore(s.dataDir, p, Version, opts)
	if err != nil {
		return res, err
	}
	if !res.DryRun {
		s.lg.Warn("已从备份恢复数据", "archive", p, "files", len(res.Restored), "safePoint", res.SafePoint)
	}
	return res, nil
}

// DeleteBackup 删掉一份备份
func (s *Service) DeleteBackup(path string) error {
	p, err := s.resolveBackupPath(path)
	if err != nil {
		return err
	}
	if err := backup.Delete(s.dataDir, p); err != nil {
		return err
	}
	s.lg.Info("已删除备份", "archive", p)
	return nil
}

// resolveBackupPath 只接受备份目录里的文件名或完整路径，别的路径一律拒绝
func (s *Service) resolveBackupPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("备份文件路径不能为空")
	}
	dir := backup.Dir(s.dataDir)
	if !strings.ContainsAny(p, `/\`) {
		p = filepath.Join(dir, p)
	} else if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if filepath.Dir(abs) != absDir {
		return "", errors.New("只允许操作备份目录 " + absDir + " 下的文件")
	}
	return abs, nil
}

// BackupHint 给日志/控制台用的一句话
func (s *Service) BackupHint() string {
	list, err := backup.List(s.dataDir)
	if err != nil {
		return ""
	}
	if len(list) == 0 {
		return ""
	}
	latest := list[0]
	when := time.UnixMilli(0)
	if latest.Manifest != nil {
		when = time.UnixMilli(latest.Manifest.CreatedAt)
	}
	return "共 " + strconv.Itoa(len(list)) + " 份备份，最近一份 " + when.Format("2006-01-02 15:04")
}
