package plugins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// installedFile 已安装插件的记录文件（在 <数据>/plugins/installed.json）。
//
// 为什么单独一份文件而不是塞进 config.json：它是**派生状态**（我们装了哪些技能、往配置里
// 加了哪些 MCP 服务），由插件管理自己维护，混进用户手改的 config 里容易两边打架。
const installedFile = "installed.json"

// store 已安装记录的读写（带锁；插件操作本来就很轻，简单比精细更不容易出错）
type store struct {
	mu  sync.Mutex
	dir string
}

func newStore(dir string) *store { return &store{dir: dir} }

func (s *store) path() string { return filepath.Join(s.dir, installedFile) }

// list 读取全部记录（按安装时间倒序）。文件不存在 = 一个都没装，不算错误。
func (s *store) list() ([]Installed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *store) listLocked() ([]Installed, error) {
	raw, err := os.ReadFile(s.path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Installed{}, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return []Installed{}, nil
	}
	var list []Installed
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("插件安装记录解析失败（" + s.path() + "）：" + err.Error())
	}
	sort.Slice(list, func(i, j int) bool { return list[i].InstalledAt > list[j].InstalledAt })
	return list, nil
}

func (s *store) get(id string) (Installed, bool, error) {
	list, err := s.list()
	if err != nil {
		return Installed{}, false, err
	}
	for _, it := range list {
		if it.ID == id {
			return it, true, nil
		}
	}
	return Installed{}, false, nil
}

// put 写入/覆盖一条记录
func (s *store) put(item Installed) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.listLocked()
	if err != nil {
		return err
	}
	replaced := false
	for i := range list {
		if list[i].ID == item.ID {
			list[i] = item
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, item)
	}
	return s.writeLocked(list)
}

// remove 删掉一条记录
func (s *store) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.listLocked()
	if err != nil {
		return err
	}
	kept := make([]Installed, 0, len(list))
	for _, it := range list {
		if it.ID != id {
			kept = append(kept, it)
		}
	}
	return s.writeLocked(kept)
}

func (s *store) writeLocked(list []Installed) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	if list == nil {
		list = []Installed{}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].InstalledAt > list[j].InstalledAt })
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	path := s.path()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
