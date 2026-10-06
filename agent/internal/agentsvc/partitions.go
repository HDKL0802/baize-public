package agentsvc

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"baize/internal/accounts"
	"baize/internal/conflicts"
	"baize/internal/kb"
	"baize/internal/llm"
	"baize/internal/memory"
)

// partition 一个数据分区：某个用户（或某个用户组）独立的记忆库 + 知识库。
//
// 文件布局见 accounts.PartitionDir：
//
//	users/<uid>/memory.db + kb/     某用户的私人数据
//	groups/<gid>/memory.db + kb/    某用户组的共享数据
type partition struct {
	key    string
	mem    *memory.Store
	kb     *kb.Service
	shared *kb.FileStore    // 用户组的共享文档仓库（用户分区为 nil）
	conf   *conflicts.Store // 用户组的冲突协商记录（用户分区为 nil）
}

// Accounts 账号注册表（多用户底座；没接时为 nil）
func (s *Service) Accounts() *accounts.Registry { return s.accounts }

// Partition 取某个主体的数据分区（记忆库 + 知识库）。
//
// 默认主体直接复用服务自身那份（单人用法与历史行为完全一致）；
// 用户/组分区按需打开并缓存 —— 同一个主体只开一次，之后都是内存里取指针。
func (s *Service) Partition(p accounts.Principal) (*memory.Store, *kb.Service, error) {
	if s.accounts == nil || p.IsDefault() {
		return s.mem, s.kbs, nil
	}
	key := p.Key()

	s.partMu.Lock()
	defer s.partMu.Unlock()
	if pt, ok := s.parts[key]; ok {
		return pt.mem, pt.kb, nil
	}

	dir := s.accounts.PartitionDir(p)
	mem, err := memory.Open(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("打开分区「%s」的记忆库失败：%w", key, err)
	}
	kbSvc, err := kb.Open(dir, s.lg)
	if err != nil {
		mem.Close()
		return nil, nil, fmt.Errorf("打开分区「%s」的知识库失败：%w", key, err)
	}

	// 向量化通道跟着主库走：同一个后端只配一套 embedding
	s.mu.RLock()
	emb := s.embedder
	s.mu.RUnlock()
	mem.SetEmbedder(emb)

	// 用户组多一个「共享文档」仓库（各成员都能上传/下载；内容寻址天然支持"同名两版"）
	var shared *kb.FileStore
	var conf *conflicts.Store
	if p.GroupID() != "" {
		shared, err = kb.OpenFileStore(filepath.Join(dir, "shared"))
		if err != nil {
			mem.Close()
			return nil, nil, fmt.Errorf("打开分区「%s」的共享文档库失败：%w", key, err)
		}
		conf, err = conflicts.Open(dir)
		if err != nil {
			mem.Close()
			return nil, nil, fmt.Errorf("打开分区「%s」的协商记录库失败：%w", key, err)
		}
	}

	if s.parts == nil {
		s.parts = map[string]*partition{}
	}
	s.parts[key] = &partition{key: key, mem: mem, kb: kbSvc, shared: shared, conf: conf}
	s.lg.Info("已打开数据分区", "principal", key, "dir", dir)
	return mem, kbSvc, nil
}

// PartitionMemory 只取分区记忆库（多数接口只关心它）
func (s *Service) PartitionMemory(p accounts.Principal) (*memory.Store, error) {
	mem, _, err := s.Partition(p)
	return mem, err
}

// PartitionKB 只取分区知识库
func (s *Service) PartitionKB(p accounts.Principal) (*kb.Service, error) {
	_, k, err := s.Partition(p)
	return k, err
}

// GroupDocs 取某个用户组的共享文档仓库（非用户组主体返回错误）
func (s *Service) GroupDocs(p accounts.Principal) (*kb.FileStore, error) {
	if p.GroupID() == "" {
		return nil, errors.New("只有用户组有共享文档库")
	}
	if _, _, err := s.Partition(p); err != nil {
		return nil, err
	}
	s.partMu.Lock()
	pt := s.parts[p.Key()]
	s.partMu.Unlock()
	if pt == nil || pt.shared == nil {
		return nil, errors.New("共享文档库未就绪")
	}
	return pt.shared, nil
}

// ConflictsFor 取某个用户组的冲突协商记录库（非用户组主体返回错误）
func (s *Service) ConflictsFor(p accounts.Principal) (*conflicts.Store, error) {
	if p.GroupID() == "" {
		return nil, errors.New("只有用户组有冲突协商记录")
	}
	if _, _, err := s.Partition(p); err != nil {
		return nil, err
	}
	s.partMu.Lock()
	pt := s.parts[p.Key()]
	s.partMu.Unlock()
	if pt == nil || pt.conf == nil {
		return nil, errors.New("协商记录库未就绪")
	}
	return pt.conf, nil
}

// closePartitions 关闭全部非默认分区（知识库是纯文件，无需关闭）
func (s *Service) closePartitions() {
	s.partMu.Lock()
	defer s.partMu.Unlock()
	for _, pt := range s.parts {
		if pt.mem != nil {
			pt.mem.Close()
		}
		if pt.conf != nil {
			pt.conf.Close()
		}
	}
	s.parts = nil
}

// applyEmbedderToPartitions 向量通道变化时，把它同步到已打开的分区
// （否则登录其他用户后，那套记忆库会一直以为"没配 embedding"）。
// 调用方**不要持有 s.mu**（内部只拿 partMu）。
func (s *Service) applyEmbedderToPartitions(e llm.Embedder) {
	s.partMu.Lock()
	defer s.partMu.Unlock()
	for _, pt := range s.parts {
		if pt.mem != nil {
			pt.mem.SetEmbedder(e)
		}
	}
}

// RebuildMemoryTreeFor 与 RebuildMemoryTree 同款，但针对指定主体的记忆库
func (s *Service) RebuildMemoryTreeFor(ctx context.Context, p accounts.Principal) (int, error) {
	mem, err := s.PartitionMemory(p)
	if err != nil {
		return 0, err
	}
	if mem == nil {
		return 0, errors.New("记忆库未启用")
	}
	s.mu.RLock()
	router := s.router
	s.mu.RUnlock()
	if router == nil {
		return 0, errors.New("模型通道还没建起来")
	}
	cp, err := router.Pick("chat")
	if err != nil {
		return 0, errors.New("没有可用的模型通道，生成摘要要先配一条：" + err.Error())
	}
	return mem.RebuildTreeIfStale(ctx, memory.NewLLMSummarizer(cp), 8)
}
