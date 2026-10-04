package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store 是核心数据的落地层：一份 {todos, vault} JSON 文档 + 一份设置 JSON。
// 文件格式与 CLI 的 ~/.baize-todo/data.json、App 导出的 JSON 完全同构，
// 手机端内核只是把这两个文件放到 App 私有目录里。
type Store struct {
	dir  string
	mu   sync.Mutex
	path string
}

// Open 打开（或准备创建）某个目录下的数据文件
func Open(dir string) *Store {
	return &Store{dir: dir, path: filepath.Join(dir, "data.json")}
}

// OpenFile 指定确切的 data.json 路径
func OpenFile(path string) *Store {
	return &Store{dir: filepath.Dir(path), path: path}
}

// Path 数据文件路径
func (s *Store) Path() string { return s.path }

// SettingsPath 设置文件路径
func (s *Store) SettingsPath() string { return filepath.Join(s.dir, "settings.json") }

// Load 读取数据。文件不存在时返回空文档（不报错）；文件损坏时报错并说明路径。
func (s *Store) Load() (*Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (*Doc, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewDoc(), nil
		}
		return nil, fmt.Errorf("读取数据文件失败（%s）：%w", s.path, err)
	}
	doc := NewDoc()
	if len(raw) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(raw, doc); err != nil {
		return nil, fmt.Errorf("数据文件解析失败（%s）：%w", s.path, err)
	}
	if doc.Todos == nil {
		doc.Todos = []Task{}
	}
	if doc.Vault == nil {
		doc.Vault = []Pass{}
	}
	return doc, nil
}

// Save 原子写回（先写临时文件再改名），并做一次规范化
func (s *Store) Save(doc *Doc) error {
	if doc == nil {
		return errors.New("待保存的数据为空")
	}
	doc.Normalize()
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化失败：%w", err)
	}
	raw = append(raw, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("创建数据目录失败：%w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("写入数据文件失败：%w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换数据文件失败：%w", err)
	}
	return nil
}

// LoadSettings 读取设置（不存在时返回默认值）
func (s *Store) LoadSettings() (Settings, error) {
	raw, err := os.ReadFile(s.SettingsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultSettings(), nil
		}
		return Settings{}, fmt.Errorf("读取设置失败（%s）：%w", s.SettingsPath(), err)
	}
	st := DefaultSettings()
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &st); err != nil {
			return Settings{}, fmt.Errorf("设置文件解析失败（%s）：%w", s.SettingsPath(), err)
		}
	}
	st.ApplyDefaults()
	return st, nil
}

// SaveSettings 写回设置
func (s *Store) SaveSettings(st Settings) error {
	st.ApplyDefaults()
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化设置失败：%w", err)
	}
	raw = append(raw, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("创建设置目录失败：%w", err)
	}
	tmp := s.SettingsPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("写入设置失败：%w", err)
	}
	if err := os.Rename(tmp, s.SettingsPath()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换设置文件失败：%w", err)
	}
	return nil
}

// ImportJSON 从 App/localStorage 风格或导出风格的 JSON 导入（用于手机端首次迁移）。
// 支持三种输入：{"todos":[…],"vault":[…]}、仅 todos 数组、仅 vault 数组。
func ImportJSON(raw []byte) (*Doc, error) {
	trimmed := []byte(trimSpace(string(raw)))
	if len(trimmed) == 0 {
		return nil, errors.New("内容为空")
	}
	if trimmed[0] == '[' {
		// 数组：按内容猜是待办还是密码
		var todos []Task
		if err := json.Unmarshal(trimmed, &todos); err == nil && looksLikeTodos(trimmed) {
			return &Doc{Todos: todos, Vault: []Pass{}}, nil
		}
		var vault []Pass
		if err := json.Unmarshal(trimmed, &vault); err == nil {
			return &Doc{Todos: []Task{}, Vault: vault}, nil
		}
		return nil, errors.New("无法识别的 JSON 数组（既不是待办也不是密码本）")
	}
	doc := NewDoc()
	if err := json.Unmarshal(trimmed, doc); err != nil {
		return nil, fmt.Errorf("JSON 解析失败：%w", err)
	}
	if doc.Todos == nil {
		doc.Todos = []Task{}
	}
	if doc.Vault == nil {
		doc.Vault = []Pass{}
	}
	return doc, nil
}

func looksLikeTodos(raw []byte) bool {
	var probe []map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil || len(probe) == 0 {
		return false
	}
	_, hasTitle := probe[0]["title"]
	_, hasAccount := probe[0]["account"]
	return hasTitle && !hasAccount
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\r' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\r' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
