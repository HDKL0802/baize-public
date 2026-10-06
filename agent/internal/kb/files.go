package kb

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxFileBytes 单个附件上限（手机上大多是照片/文档，16MB 足够；再大就该走网盘）。
// 注意：手机壳那条链路是 JSON+base64，16MB 的文件在传输时约 21MB，别把上限调得太高。
const MaxFileBytes = 16 << 20

// FileInfo 一个附件
type FileInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // image | file
	Mime string `json:"mime,omitempty"`
	Size int64  `json:"size"`
	At   int64  `json:"at"`
	Refs int    `json:"refs"` // 被引用次数（待办里挂了几处），只是参考值
	// By / ByID 上传者（用户组共享文档用；手机端附件留空）。
	// 同名但不同内容 = 两份文档并存，也就是"分叉"，靠它们标出各是谁的一版；
	// ByID 是稳定标识（判"谁放弃了自己那版"要用），By 只是给人看的名字。
	By   string `json:"by,omitempty"`
	ByID string `json:"byId,omitempty"`
}

// FileStore 附件仓库：内容寻址（id = 内容 sha256 前 16 位），同一份内容只存一份
type FileStore struct {
	dir string
	mu  sync.Mutex
}

func openFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建附件目录失败：%w", err)
	}
	return &FileStore{dir: dir}, nil
}

// OpenFileStore 在任意目录上开一个内容寻址的文件仓库（用户组的「共享文档」就用它）。
// 复用同一套实现的好处：上传去重、id 校验（挡路径穿越）、元信息格式全一致，
// 以后要给共享文档加"版本/分叉"，改一处两边都受益。
func OpenFileStore(dir string) (*FileStore, error) { return openFileStore(dir) }

// Dir 附件目录
func (f *FileStore) Dir() string { return f.dir }

// validID 只认 16 位小写十六进制，杜绝 ../ 之类的路径穿越
func validID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (f *FileStore) blobPath(id string) string { return filepath.Join(f.dir, id+".bin") }
func (f *FileStore) metaPath(id string) string { return filepath.Join(f.dir, id+".json") }

// Put 存一份附件，返回它的信息（同样的内容第二次上传直接复用，不重复占地方）
func (f *FileStore) Put(name, kind, mime string, data []byte) (FileInfo, error) {
	return f.PutBy(name, kind, mime, "", "", data)
}

// PutBy 与 Put 相同，额外记录上传者（用户组共享文档用来标"这是谁的一版"）
func (f *FileStore) PutBy(name, kind, mime, byID, byName string, data []byte) (FileInfo, error) {
	if len(data) == 0 {
		return FileInfo{}, errors.New("附件内容为空")
	}
	if len(data) > MaxFileBytes {
		return FileInfo{}, fmt.Errorf("附件超过 %d MB，先压缩或换个小一点的", MaxFileBytes>>20)
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:8])

	f.mu.Lock()
	defer f.mu.Unlock()

	if info, err := f.readMeta(id); err == nil && info.Size == int64(len(data)) {
		return info, nil // 同一份内容已在库里
	}
	if err := os.WriteFile(f.blobPath(id), data, 0o600); err != nil {
		return FileInfo{}, fmt.Errorf("写入附件失败：%w", err)
	}
	info := FileInfo{
		ID: id, Name: strings.TrimSpace(name), Kind: kind, Mime: mime,
		Size: int64(len(data)), At: time.Now().UnixMilli(),
		ByID: strings.TrimSpace(byID), By: strings.TrimSpace(byName),
	}
	if info.Name == "" {
		info.Name = id
	}
	if info.Kind == "" {
		info.Kind = "file"
	}
	if err := f.writeMeta(info); err != nil {
		return FileInfo{}, err
	}
	return info, nil
}

// Get 取一份附件的元信息与内容
func (f *FileStore) Get(id string) (FileInfo, []byte, error) {
	if !validID(id) {
		return FileInfo{}, nil, errors.New("附件 id 不合法")
	}
	f.mu.Lock()
	info, err := f.readMeta(id)
	f.mu.Unlock()
	if err != nil {
		return FileInfo{}, nil, fmt.Errorf("找不到附件 %s", id)
	}
	data, err := os.ReadFile(f.blobPath(id))
	if err != nil {
		return FileInfo{}, nil, fmt.Errorf("读取附件失败：%w", err)
	}
	return info, data, nil
}

// List 列出附件（limit<=0 表示全列）
func (f *FileStore) List(limit int) ([]FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []FileInfo{}, nil
		}
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if !validID(id) {
			continue
		}
		info, err := f.readMeta(id)
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Remove 删掉一份附件
func (f *FileStore) Remove(id string) error {
	if !validID(id) {
		return errors.New("附件 id 不合法")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := os.Stat(f.metaPath(id)); err != nil {
		return fmt.Errorf("找不到附件 %s", id)
	}
	_ = os.Remove(f.blobPath(id))
	return os.Remove(f.metaPath(id))
}

/* ---------- 内部：元信息读写（调用方需自行加锁） ---------- */

func (f *FileStore) readMeta(id string) (FileInfo, error) {
	raw, err := os.ReadFile(f.metaPath(id))
	if err != nil {
		return FileInfo{}, err
	}
	info := FileInfo{}
	if err := json.Unmarshal(raw, &info); err != nil {
		return FileInfo{}, err
	}
	return info, nil
}

func (f *FileStore) writeMeta(info FileInfo) error {
	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.metaPath(info.ID) + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("写入附件信息失败：%w", err)
	}
	return os.Rename(tmp, f.metaPath(info.ID))
}
