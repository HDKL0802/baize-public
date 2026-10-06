// Package agentrt 是 Agent 运行时（Hermes run_agent 主循环的 Go 重写）：
// provider → 提示词 → 工具调用 → 重试/回退 → 事件钩子 → 上下文压缩 → 持久化，
// 外加变更前快照与回滚。
package agentrt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxSnapshotFile 单个文件超过这个大小就不进快照（避免把磁盘打满）
const maxSnapshotFile = 32 << 20

// blobsDirName 内容寻址的 blob 仓库（放在快照根下，List 会跳过它）
const blobsDirName = "_blobs"

// CheckpointInfo 一个快照。
//
// 快照本身不存文件副本，只存一份「路径 → blob 哈希」的清单（manifest.json），
// 真正的文件内容按哈希存在共享的 _blobs/ 里 —— 所以多个快照之间天然去重：
// 只改了一个文件，第二次快照就只多存那一个 blob（这就是「增量」，不依赖 git）。
type CheckpointInfo struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Key       string `json:"key,omitempty"` // 去重键（例如同一次运行）：配合 CreateOnce 用
	Dir       string `json:"dir"`
	Files     int    `json:"files"`    // 快照引用的文件数
	Bytes     int64  `json:"bytes"`    // 引用的原始总字节（去重前）
	NewBytes  int64  `json:"newBytes"` // 本次真正新写入的字节（去重后）—— 看"增量"有多大就看它
	CreatedAt int64  `json:"createdAt"`
}

// manifestEntry 清单里的一条
type manifestEntry struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
	Mode uint32 `json:"mode"`
}

// manifest 快照清单：相对路径 → blob
type manifest struct {
	Files map[string]manifestEntry `json:"files"`
}

// RestoreOptions 回滚选项
type RestoreOptions struct {
	// DryRun 只算计划不落盘（先看清楚要动哪些文件，再决定要不要真回滚）
	DryRun bool
	// Files 白名单：只回滚这些相对路径；空 = 全部
	Files []string
	// PruneExtra 是否把"快照里有、当前工作目录没有"的文件删掉。
	// 默认 false —— 只覆盖不删除，比"清空再铺回去"安全得多。
	PruneExtra bool
}

// RestorePlan 回滚计划 / 结果（DryRun 时只有计划，Applied=false）
type RestorePlan struct {
	ID      string   `json:"id"`
	DryRun  bool     `json:"dryRun"`
	Applied bool     `json:"applied"`
	Restore []string `json:"restore"` // 将被写入或覆盖
	Delete  []string `json:"delete"`  // 将被删除（仅 PruneExtra）
	Skip    []string `json:"skip"`    // 跳过（白名单外 / 超大文件 / 目录）
}

// CheckpointManager 快照管理：内容寻址 + 清单，支持增量、文件级恢复与 dry-run
type CheckpointManager struct {
	dataDir   string
	workdir   string
	maxKeep   int
	skipDirs  map[string]bool
	mu        sync.Mutex
	lastByKey map[string]int64 // key -> 最近一次快照的时间（毫秒）
}

// NewCheckpointManager 创建快照管理器
func NewCheckpointManager(dataDir, workdir string, maxKeep int) (*CheckpointManager, error) {
	if strings.TrimSpace(dataDir) == "" || strings.TrimSpace(workdir) == "" {
		return nil, errors.New("快照需要同时指定数据目录与工作目录")
	}
	if maxKeep <= 0 {
		maxKeep = 20
	}
	m := &CheckpointManager{
		dataDir: dataDir,
		workdir: filepath.Clean(workdir),
		maxKeep: maxKeep,
		// 快照目录本身如果在工作目录里，必须跳过，否则会自我递归
		skipDirs:  map[string]bool{filepath.Clean(filepath.Join(dataDir, "checkpoints")): true},
		lastByKey: map[string]int64{},
	}
	m.loadKeyIndex()
	return m, nil
}

/* ---------------- 路径 ---------------- */

func (m *CheckpointManager) root() string    { return filepath.Join(m.dataDir, "checkpoints") }
func (m *CheckpointManager) blobDir() string { return filepath.Join(m.root(), blobsDirName) }
func (m *CheckpointManager) snapDir(id string) string {
	return filepath.Join(m.root(), id)
}

func (m *CheckpointManager) blobPath(hash string) string {
	if len(hash) < 3 {
		return filepath.Join(m.blobDir(), hash)
	}
	return filepath.Join(m.blobDir(), hash[:2], hash)
}

/* ---------------- 创建 ---------------- */

// Create 打一个快照，返回快照信息
func (m *CheckpointManager) Create(label string) (CheckpointInfo, error) {
	return m.create("", label)
}

// CreateOnce 打一个"带去重键"的快照：
// 同一个 key 在 within 时间窗内已经打过，就直接复用那一份，不再重复打。
//
// 用途：一次 Agent 运行里可能连续改好几个文件，变更前的状态其实是同一个。
// 老实现每次写文件都全量拷一遍工作目录，跑 10 步就是 10 份完整拷贝；
// 用 key=runID + within 就能压成"每次运行只在第一次动手前打一次"。
// within <= 0 表示不按时间窗去重（同 key 只打一次，直到被 prune 掉）。
func (m *CheckpointManager) CreateOnce(key, label string, within time.Duration) (CheckpointInfo, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		info, err := m.create("", label)
		return info, true, err
	}
	m.mu.Lock()
	last, seen := m.lastByKey[key]
	m.mu.Unlock()
	if seen && within <= 0 {
		// 同 key 已有快照且不按时间窗去重 → 直接复用
		if info, ok := m.findByKey(key); ok {
			return info, false, nil
		}
	}
	if seen && within > 0 && time.Now().UnixMilli()-last < within.Milliseconds() {
		if info, ok := m.findByKey(key); ok {
			return info, false, nil
		}
	}
	info, err := m.create(key, label)
	return info, true, err
}

func (m *CheckpointManager) create(key, label string) (CheckpointInfo, error) {
	info := CheckpointInfo{
		ID:        "ck" + fmt.Sprint(time.Now().UnixMilli()),
		Label:     label,
		Key:       key,
		CreatedAt: time.Now().UnixMilli(),
	}
	info.Dir = m.snapDir(info.ID)
	if err := os.MkdirAll(info.Dir, 0o755); err != nil {
		return info, fmt.Errorf("创建快照目录失败：%w", err)
	}
	man, err := m.scanWorkdir()
	if err != nil {
		return info, err
	}
	// 逐条写 blob（已存在则跳过）——这就是增量：没变的文件一个字节都不重写
	for rel, e := range man.Files {
		src := filepath.Join(m.workdir, filepath.FromSlash(rel))
		n, err := m.putBlob(e.Hash, src)
		if err != nil {
			return info, fmt.Errorf("写入快照内容失败（%s）：%w", rel, err)
		}
		info.NewBytes += n
		info.Files++
		info.Bytes += e.Size
	}
	if err := m.saveManifest(info.Dir, man); err != nil {
		return info, err
	}
	if err := writeMeta(filepath.Join(info.Dir, "meta.json"), info); err != nil {
		return info, err
	}
	if key != "" {
		m.mu.Lock()
		m.lastByKey[key] = info.CreatedAt
		m.mu.Unlock()
	}
	_ = m.prune()
	return info, nil
}

// scanWorkdir 扫工作目录，生成"相对路径 → blob 哈希"清单
func (m *CheckpointManager) scanWorkdir() (manifest, error) {
	man := manifest{Files: map[string]manifestEntry{}}
	err := filepath.Walk(m.workdir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(m.workdir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if m.skipDirs[filepath.Clean(path)] || m.skipDirs[rel] {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if fi.IsDir() {
			return nil
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		if fi.Size() > maxSnapshotFile {
			return nil
		}
		hash, err := hashFile(path)
		if err != nil {
			return err
		}
		man.Files[filepath.ToSlash(rel)] = manifestEntry{
			Hash: hash, Size: fi.Size(), Mode: uint32(fi.Mode().Perm()),
		}
		return nil
	})
	if err != nil {
		return man, err
	}
	return man, nil
}

// putBlob 把文件内容按哈希存进 blob 仓库；已存在则直接复用（返回本次新增字节数）
func (m *CheckpointManager) putBlob(hash, src string) (int64, error) {
	dst := m.blobPath(hash)
	if st, err := os.Stat(dst); err == nil && st.Mode().IsRegular() {
		return 0, nil // 内容已经在库里了，不用再写
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	cerr := out.Close()
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if cerr != nil {
		os.Remove(tmp)
		return 0, cerr
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return n, nil
}

/* ---------------- 查询 ---------------- */

// List 列出快照（新的在前）。同时兼容老格式（老快照目录里直接放的是文件副本）。
func (m *CheckpointManager) List() ([]CheckpointInfo, error) {
	entries, err := os.ReadDir(m.root())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []CheckpointInfo{}, nil
		}
		return nil, err
	}
	out := []CheckpointInfo{}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == blobsDirName {
			continue
		}
		dir := m.snapDir(e.Name())
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

func (m *CheckpointManager) findByKey(key string) (CheckpointInfo, bool) {
	list, err := m.List()
	if err != nil {
		return CheckpointInfo{}, false
	}
	for _, info := range list {
		if info.Key == key {
			return info, true
		}
	}
	return CheckpointInfo{}, false
}

// loadKeyIndex 启动时把已有快照的去重键读进内存，避免重启后重复打快照
func (m *CheckpointManager) loadKeyIndex() {
	list, err := m.List()
	if err != nil {
		return
	}
	for _, info := range list {
		if info.Key == "" {
			continue
		}
		if cur, ok := m.lastByKey[info.Key]; !ok || info.CreatedAt > cur {
			m.lastByKey[info.Key] = info.CreatedAt
		}
	}
}

/* ---------------- 回滚 ---------------- */

// Rollback 完整回滚到某个快照（老语义：先清空工作目录再铺回去）。
// 需要更细的控制（dry-run / 只回滚部分文件）用 RollbackWith。
func (m *CheckpointManager) Rollback(id string) error {
	_, err := m.RollbackWith(id, RestoreOptions{PruneExtra: true})
	return err
}

// RollbackWith 按选项回滚；返回回滚计划（DryRun 时只算不做）
func (m *CheckpointManager) RollbackWith(id string, opts RestoreOptions) (RestorePlan, error) {
	plan := RestorePlan{ID: id, DryRun: opts.DryRun}
	dir := m.snapDir(id)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return plan, fmt.Errorf("找不到快照：%s", id)
	}
	man, err := m.loadManifestFor(id)
	if err != nil {
		return plan, err
	}
	allow := map[string]bool{}
	for _, f := range opts.Files {
		f = filepath.ToSlash(strings.TrimSpace(f))
		if f != "" {
			allow[f] = true
		}
	}
	onlySome := len(allow) > 0

	// 当前工作目录里的文件（用来判断哪些要覆盖、哪些是多余的）
	current := map[string]bool{}
	_ = filepath.Walk(m.workdir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(m.workdir, path)
		if rerr != nil || rel == "." {
			return nil
		}
		if m.skipDirs[filepath.Clean(path)] || m.skipDirs[rel] {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if fi.Mode().IsRegular() {
			current[filepath.ToSlash(rel)] = true
		}
		return nil
	})

	rels := make([]string, 0, len(man.Files))
	for rel := range man.Files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if onlySome && !allow[rel] {
			plan.Skip = append(plan.Skip, rel)
			continue
		}
		if e := man.Files[rel]; e.Size > maxSnapshotFile {
			plan.Skip = append(plan.Skip, rel)
			continue
		}
		plan.Restore = append(plan.Restore, rel)
		delete(current, rel)
	}
	if opts.PruneExtra {
		for rel := range current {
			if onlySome && !allow[rel] {
				plan.Skip = append(plan.Skip, rel)
				continue
			}
			plan.Delete = append(plan.Delete, rel)
		}
		sort.Strings(plan.Delete)
	}

	if opts.DryRun {
		return plan, nil
	}

	for _, rel := range plan.Delete {
		if err := os.RemoveAll(filepath.Join(m.workdir, filepath.FromSlash(rel))); err != nil {
			return plan, fmt.Errorf("删除多余文件失败（%s）：%w", rel, err)
		}
	}
	for _, rel := range plan.Restore {
		e := man.Files[rel]
		if err := m.restoreOne(e, filepath.Join(m.workdir, filepath.FromSlash(rel))); err != nil {
			return plan, fmt.Errorf("恢复失败（%s）：%w", rel, err)
		}
	}
	plan.Applied = true
	return plan, nil
}

// restoreOne 从 blob 还原一个文件；
// 老格式快照（Hash 字段里存的是快照内的真实文件路径）则直接按那个路径拷。
func (m *CheckpointManager) restoreOne(e manifestEntry, dst string) error {
	var src string
	if isBlobHash(e.Hash) {
		src = m.blobPath(e.Hash)
		if st, err := os.Stat(src); err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("快照内容缺失（blob %s）", e.Hash)
		}
	} else {
		src = filepath.FromSlash(e.Hash)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	mode := os.FileMode(e.Mode)
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

/* ---------------- 清单读写 ---------------- */

func (m *CheckpointManager) manifestPath(id string) string {
	return filepath.Join(m.snapDir(id), "manifest.json")
}

func (m *CheckpointManager) saveManifest(dir string, man manifest) error {
	raw, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), append(raw, '\n'), 0o644)
}

// loadManifestFor 读快照清单；老格式（没有 manifest.json）就现场按目录里现成的文件副本补一份
func (m *CheckpointManager) loadManifestFor(id string) (manifest, error) {
	path := m.manifestPath(id)
	if raw, err := os.ReadFile(path); err == nil {
		var man manifest
		if err := json.Unmarshal(raw, &man); err != nil {
			return man, fmt.Errorf("快照清单损坏：%w", err)
		}
		if man.Files == nil {
			man.Files = map[string]manifestEntry{}
		}
		return man, nil
	}
	// 老格式：快照目录里放的是真实文件副本，直接按目录内容当清单
	dir := m.snapDir(id)
	man := manifest{Files: map[string]manifestEntry{}}
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || rel == "." {
			return nil
		}
		if fi.IsDir() {
			return nil
		}
		if !fi.Mode().IsRegular() || fi.Name() == "meta.json" {
			return nil
		}
		// 老格式没有 blob，用"快照内绝对路径"当 hash 占位，restoreOne 会直接按路径拷
		man.Files[filepath.ToSlash(rel)] = manifestEntry{
			Hash: filepath.ToSlash(p), Size: fi.Size(), Mode: uint32(fi.Mode().Perm()),
		}
		return nil
	})
	if err != nil {
		return man, err
	}
	return man, nil
}

/* ---------------- 清理 ---------------- */

// prune 只保留最近 maxKeep 个快照，并回收没人引用的 blob
func (m *CheckpointManager) prune() error {
	list, err := m.List()
	if err != nil {
		return err
	}
	if len(list) > m.maxKeep {
		for _, info := range list[m.maxKeep:] {
			_ = os.RemoveAll(info.Dir)
		}
	}
	return m.gcBlobs()
}

// gcBlobs 删掉没有被任何快照引用的 blob（去重存储不做回收会一直涨）
func (m *CheckpointManager) gcBlobs() error {
	list, err := m.List()
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for _, info := range list {
		man, err := m.loadManifestFor(info.ID)
		if err != nil {
			continue
		}
		for _, e := range man.Files {
			if isBlobHash(e.Hash) {
				used[e.Hash] = true
			}
		}
	}
	root := m.blobDir()
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	return filepath.Walk(root, func(p string, fi os.FileInfo, werr error) error {
		if werr != nil {
			return nil
		}
		if fi.IsDir() {
			return nil
		}
		if !isBlobHash(fi.Name()) || used[fi.Name()] {
			return nil
		}
		_ = os.Remove(p)
		return nil
	})
}

// isBlobHash 判断是不是 sha256 十六进制串（用来把 blob 和老格式的路径占位区分开）
func isBlobHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

/* ---------------- 小工具 ---------------- */

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeMeta(path string, info CheckpointInfo) error {
	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
