// Package backup 负责白泽数据目录的备份与恢复。
//
// 形态：一个 zip 归档，内含 manifest.json（格式版本、时间、内容选择、每个文件的 sha256）。
// 恢复前默认先打一份"安全点"（当前数据的整份备份），并且归档里的路径一律做越界检查，
// 杜绝 ../ 之类的相对路径逃逸。
package backup

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// Format 归档格式版本
	Format = 1
	// ManifestName 归档内清单文件名
	ManifestName = "manifest.json"
	// AppName 归档归属
	AppName = "baize-agent"
	// DirName 默认备份目录（在数据目录下）
	DirName = "backups"
)

// Selection 备份内容选择
type Selection struct {
	Config      bool `json:"config"`      // config.json + token
	Hub         bool `json:"hub"`         // agent.db（设备 / 任务 / 记录 / 记忆）
	Memory      bool `json:"memory"`      // memory.db（Memory Tree）
	Runs        bool `json:"runs"`        // runs.db（Agent 运行记录）
	Skills      bool `json:"skills"`      // skills/（技能库）
	Checkpoints bool `json:"checkpoints"` // checkpoints/（变更前快照）
	Workspace   bool `json:"workspace"`   // workspace/（Agent 工作目录）
	Vault       bool `json:"vault"`       // vault.*（若存在）
}

// Full 全选
func Full() Selection {
	return Selection{Config: true, Hub: true, Memory: true, Runs: true, Skills: true, Checkpoints: true, Workspace: true, Vault: true}
}

// Empty 什么都没选
func (s Selection) Empty() bool {
	return !(s.Config || s.Hub || s.Memory || s.Runs || s.Skills || s.Checkpoints || s.Workspace || s.Vault)
}

// Label 中文标签（控制台显示用）
func (s Selection) Label() string {
	var parts []string
	add := func(on bool, name string) {
		if on {
			parts = append(parts, name)
		}
	}
	add(s.Config, "配置与令牌")
	add(s.Hub, "设备任务库")
	add(s.Memory, "记忆库")
	add(s.Runs, "运行记录")
	add(s.Skills, "技能库")
	add(s.Checkpoints, "快照")
	add(s.Workspace, "工作目录")
	add(s.Vault, "密码本")
	if len(parts) == 0 {
		return "（空）"
	}
	return strings.Join(parts, "、")
}

// Entry 归档内一个文件
type Entry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest 归档清单
type Manifest struct {
	Format    int       `json:"format"`
	App       string    `json:"app"`
	Version   string    `json:"version"`
	CreatedAt int64     `json:"createdAt"`
	DataDir   string    `json:"dataDir"`
	Selection Selection `json:"selection"`
	Entries   []Entry   `json:"entries"`
	TotalSize int64     `json:"totalSize"`
	Note      string    `json:"note,omitempty"`
}

// Archive 备份文件（列表用）
type Archive struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Manifest *Manifest `json:"manifest,omitempty"`
	Err      string    `json:"error,omitempty"`
}

// RestoreOptions 恢复选项
type RestoreOptions struct {
	SkipVerify  bool     // 不做 sha256 校验（不建议）
	NoSafePoint bool     // 不先打安全点（不建议）
	DryRun      bool     // 只校验并报告会恢复什么，不动数据
	Only        []string // 只恢复这些相对路径（空 = 全部）
}

// RestoreResult 恢复结果
type RestoreResult struct {
	Manifest  Manifest `json:"manifest"`
	SafePoint string   `json:"safePoint,omitempty"`
	Restored  []string `json:"restored"`
	Skipped   []string `json:"skipped,omitempty"`
	Bytes     int64    `json:"bytes"`
	DryRun    bool     `json:"dryRun"`
	Note      string   `json:"note,omitempty"`
}

// Dir 默认备份目录
func Dir(dataDir string) string { return filepath.Join(dataDir, DirName) }

/* ---------- 导出 ---------- */

// Create 打一个备份包：outPath 为空时写到 <dataDir>/backups/backup-<时间>.zip
func Create(dataDir, version, outPath string, sel Selection, note string) (Manifest, error) {
	man := Manifest{Format: Format, App: AppName, Version: version, Selection: sel, Note: note}
	if sel.Empty() {
		return man, errors.New("备份内容为空：至少要选一项")
	}
	if strings.TrimSpace(dataDir) == "" {
		return man, errors.New("数据目录不能为空")
	}
	if st, err := os.Stat(dataDir); err != nil || !st.IsDir() {
		return man, fmt.Errorf("数据目录不可用：%s", dataDir)
	}
	files, err := collectFiles(dataDir, sel)
	if err != nil {
		return man, err
	}
	if len(files) == 0 {
		return man, fmt.Errorf("所选内容里一个文件都没有（%s），没有可备份的东西", sel.Label())
	}

	if strings.TrimSpace(outPath) == "" {
		outPath = filepath.Join(Dir(dataDir), "backup-"+time.Now().Format("20060102-150405")+".zip")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return man, err
	}

	man.CreatedAt = time.Now().UnixMilli()
	man.DataDir = dataDir
	tmp := outPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return man, fmt.Errorf("创建备份文件失败：%w", err)
	}
	zw := zip.NewWriter(f)

	for _, rel := range files {
		abs := filepath.Join(dataDir, filepath.FromSlash(rel))
		entry, err := addFile(zw, abs, rel)
		if err != nil {
			zw.Close()
			f.Close()
			os.Remove(tmp)
			return man, err
		}
		man.Entries = append(man.Entries, entry)
		man.TotalSize += entry.Size
	}
	// 清单最后写：这样只要文件存在，内容一定是完整的
	mw, err := zw.Create(ManifestName)
	if err == nil {
		err = json.NewEncoder(mw).Encode(&man)
	}
	if err != nil {
		zw.Close()
		f.Close()
		os.Remove(tmp)
		return man, fmt.Errorf("写入清单失败：%w", err)
	}
	if err := zw.Close(); err != nil {
		f.Close()
		os.Remove(tmp)
		return man, fmt.Errorf("收尾备份文件失败：%w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return man, fmt.Errorf("关闭备份文件失败：%w", err)
	}
	if err := os.Rename(tmp, outPath); err != nil {
		os.Remove(tmp)
		return man, fmt.Errorf("落盘备份文件失败：%w", err)
	}
	return man, nil
}

// addFile 把一个文件写进 zip 并返回它的校验信息
func addFile(zw *zip.Writer, abs, rel string) (Entry, error) {
	entry := Entry{Path: rel}
	fi, err := os.Stat(abs)
	if err != nil {
		return entry, fmt.Errorf("读取 %s 失败：%w", rel, err)
	}
	entry.Size = fi.Size()

	src, err := os.Open(abs)
	if err != nil {
		return entry, fmt.Errorf("打开 %s 失败：%w", rel, err)
	}
	defer src.Close()

	w, err := zw.Create(rel)
	if err != nil {
		return entry, fmt.Errorf("写入归档 %s 失败：%w", rel, err)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), src); err != nil {
		return entry, fmt.Errorf("复制 %s 失败：%w", rel, err)
	}
	entry.SHA256 = hex.EncodeToString(h.Sum(nil))
	return entry, nil
}

// collectFiles 按选择列出要备份的相对路径（正斜杠形式），已排序
func collectFiles(dataDir string, sel Selection) ([]string, error) {
	var candidates []string
	if sel.Config {
		candidates = append(candidates, "config.json", "token")
	}
	if sel.Hub {
		candidates = append(candidates, "agent.db", "agent.db-wal", "agent.db-shm")
	}
	if sel.Memory {
		candidates = append(candidates, "memory.db", "memory.db-wal", "memory.db-shm")
	}
	if sel.Runs {
		candidates = append(candidates, "runs.db", "runs.db-wal", "runs.db-shm")
	}
	if sel.Skills {
		candidates = append(candidates, "skills")
	}
	if sel.Checkpoints {
		candidates = append(candidates, "checkpoints")
	}
	if sel.Workspace {
		candidates = append(candidates, "workspace")
	}
	if sel.Vault {
		candidates = append(candidates, "vault.json", "vault.enc", "vault.md", "vault.db")
		if m, _ := filepath.Glob(filepath.Join(dataDir, "vault.*")); len(m) > 0 {
			for _, p := range m {
				candidates = append(candidates, filepath.Base(p))
			}
		}
	}

	seen := map[string]bool{}
	var out []string
	for _, c := range candidates {
		abs := filepath.Join(dataDir, c)
		info, err := os.Lstat(abs)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("读取 %s 失败：%w", c, err)
		}
		if info.IsDir() {
			err := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if p != abs && d.IsDir() {
					if shouldSkipDir(d.Name()) {
						return fs.SkipDir
					}
					return nil
				}
				if d.IsDir() {
					return nil
				}
				if d.Type()&fs.ModeSymlink != 0 || shouldSkipFile(d.Name()) {
					return nil
				}
				rel, err := filepath.Rel(dataDir, p)
				if err != nil {
					return err
				}
				rel = filepath.ToSlash(rel)
				if !seen[rel] {
					seen[rel] = true
					out = append(out, rel)
				}
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("遍历 %s 失败：%w", c, err)
			}
			continue
		}
		rel := filepath.ToSlash(c)
		if !seen[rel] {
			seen[rel] = true
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out, nil
}

func shouldSkipDir(name string) bool {
	l := strings.ToLower(name)
	switch l {
	case DirName, "logs", "cache", "__pycache__", "node_modules":
		return true
	}
	// 恢复过程中的临时目录
	return strings.HasPrefix(l, ".restore-")
}

func shouldSkipFile(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".log") || strings.HasSuffix(l, ".tmp") || strings.HasSuffix(l, ".bak")
}

/* ---------- 列表 / 校验 ---------- */

// List 列出备份目录里的所有归档（按时间倒序）
func List(dataDir string) ([]Archive, error) {
	dir := Dir(dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []Archive{}, nil
		}
		return nil, fmt.Errorf("读取备份目录失败：%w", err)
	}
	out := make([]Archive, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".zip") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		a := Archive{Path: p, Name: e.Name()}
		if info, err := e.Info(); err == nil {
			a.Size = info.Size()
		}
		man, err := ReadManifest(p)
		if err != nil {
			a.Err = err.Error()
		} else {
			a.Manifest = &man
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := int64(0), int64(0)
		if out[i].Manifest != nil {
			ti = out[i].Manifest.CreatedAt
		}
		if out[j].Manifest != nil {
			tj = out[j].Manifest.CreatedAt
		}
		if ti != tj {
			return ti > tj
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

// ReadManifest 只读归档里的清单（不校验内容）
func ReadManifest(archivePath string) (Manifest, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("打开归档失败：%w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != ManifestName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return Manifest{}, fmt.Errorf("读取清单失败：%w", err)
		}
		defer rc.Close()
		var man Manifest
		if err := json.NewDecoder(io.LimitReader(rc, 8<<20)).Decode(&man); err != nil {
			return Manifest{}, fmt.Errorf("清单解析失败：%w", err)
		}
		return man, nil
	}
	return Manifest{}, errors.New("归档里没有 manifest.json，不是白泽的备份包")
}

// Verify 全量校验：清单、越界路径、文件是否齐全、sha256 是否对得上
func Verify(archivePath string) (Manifest, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("打开归档失败：%w", err)
	}
	defer zr.Close()
	man, err := manifestFrom(zr)
	if err != nil {
		return man, err
	}
	if man.Format != Format {
		return man, fmt.Errorf("归档格式版本不支持：%d（本程序支持 %d）", man.Format, Format)
	}
	// 逐条做越界检查
	present := map[string]bool{}
	for _, f := range zr.File {
		if f.Name == ManifestName {
			continue
		}
		rel, err := safeRel(f.Name)
		if err != nil {
			return man, err
		}
		present[filepath.ToSlash(rel)] = true
	}
	// 清单里登记的文件必须都在
	for _, e := range man.Entries {
		if !present[path.Clean(e.Path)] {
			return man, fmt.Errorf("归档缺少文件：%s", e.Path)
		}
	}
	// 内容逐文件比对
	if err := verifyAgainstFiles(zr, entriesByPath(man)); err != nil {
		return man, err
	}
	return man, nil
}

func manifestFrom(zr *zip.ReadCloser) (Manifest, error) {
	for _, f := range zr.File {
		if f.Name != ManifestName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return Manifest{}, fmt.Errorf("读取清单失败：%w", err)
		}
		defer rc.Close()
		var man Manifest
		if err := json.NewDecoder(io.LimitReader(rc, 8<<20)).Decode(&man); err != nil {
			return Manifest{}, fmt.Errorf("清单解析失败：%w", err)
		}
		return man, nil
	}
	return Manifest{}, errors.New("归档里没有 manifest.json，不是白泽的备份包")
}

func hashZipFile(f *zip.File) (string, int64, error) {
	rc, err := f.Open()
	if err != nil {
		return "", 0, fmt.Errorf("读取 %s 失败：%w", f.Name, err)
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(h, rc)
	if err != nil {
		return "", 0, fmt.Errorf("读取 %s 失败：%w", f.Name, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func verifyAgainstFiles(zr *zip.ReadCloser, want map[string]Entry) error {
	for _, f := range zr.File {
		if f.Name == ManifestName {
			continue
		}
		rel, err := safeRel(f.Name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		e, ok := want[rel]
		if !ok {
			continue // 清单里没登记的多余文件，忽略
		}
		sum, size, err := hashZipFile(f)
		if err != nil {
			return err
		}
		if e.SHA256 != "" && !strings.EqualFold(e.SHA256, sum) {
			return fmt.Errorf("文件校验不通过：%s（清单 %s，实际 %s）", rel, brief(e.SHA256, 12), brief(sum, 12))
		}
		if e.Size > 0 && e.Size != size {
			return fmt.Errorf("文件大小对不上：%s（清单 %d，实际 %d）", rel, e.Size, size)
		}
	}
	return nil
}

/* ---------- 恢复 ---------- */

// Restore 从归档恢复数据
func Restore(dataDir, archivePath, version string, opts RestoreOptions) (RestoreResult, error) {
	res := RestoreResult{DryRun: opts.DryRun}
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return res, fmt.Errorf("打开归档失败：%w", err)
	}
	defer zr.Close()

	man, err := manifestFrom(zr)
	if err != nil {
		return res, err
	}
	res.Manifest = man
	if man.Format != Format {
		return res, fmt.Errorf("归档格式版本不支持：%d（本程序支持 %d）", man.Format, Format)
	}

	// 1) 预检：路径越界、筛选要恢复哪些
	type item struct {
		f   *zip.File
		rel string
	}
	entries := entriesByPath(man)
	var items []item
	for _, f := range zr.File {
		if f.Name == ManifestName {
			continue
		}
		rel, err := safeRel(f.Name)
		if err != nil {
			return res, err
		}
		relSlash := filepath.ToSlash(rel)
		if len(opts.Only) > 0 && !matchAny(relSlash, opts.Only) {
			res.Skipped = append(res.Skipped, relSlash)
			continue
		}
		items = append(items, item{f: f, rel: rel})
	}
	if len(items) == 0 {
		return res, errors.New("归档里没有可恢复的文件（或都被 Only 过滤掉了）")
	}

	// 2) 校验 + 解到临时目录
	tmp, err := os.MkdirTemp(dataDir, ".restore-")
	if err != nil {
		return res, fmt.Errorf("创建临时目录失败：%w", err)
	}
	defer os.RemoveAll(tmp)

	for _, it := range items {
		dst := filepath.Join(tmp, it.rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return res, err
		}
		sum, size, err := extractFile(it.f, dst)
		if err != nil {
			return res, err
		}
		if !opts.SkipVerify {
			if e, ok := entries[filepath.ToSlash(it.rel)]; ok {
				if e.SHA256 != "" && !strings.EqualFold(e.SHA256, sum) {
					return res, fmt.Errorf("校验不通过，已中止恢复：%s", it.rel)
				}
			}
		}
		res.Restored = append(res.Restored, filepath.ToSlash(it.rel))
		res.Bytes += size
	}
	sort.Strings(res.Restored)

	if opts.DryRun {
		res.Note = "只做了校验，没有改动任何数据"
		return res, nil
	}

	// 3) 安全点：恢复前把当前数据整份备一份，随时能退回
	if !opts.NoSafePoint {
		spPath := filepath.Join(Dir(dataDir), "auto-before-restore-"+time.Now().Format("20060102-150405")+".zip")
		if _, err := Create(dataDir, version, spPath, Full(), "恢复前自动安全点"); err != nil {
			return res, fmt.Errorf("打安全点失败，已中止恢复（不给您留没退路的状态）：%w", err)
		}
		res.SafePoint = spPath
	}

	// 4) 落位：先把旧文件挪到一边（占用/权限问题在这一步就会暴露，不会破坏数据），
	//    再把新文件放上去；中途出事就把旧文件挪回来。
	oldRoot, err := os.MkdirTemp(dataDir, ".restore-old-")
	if err != nil {
		return res, fmt.Errorf("创建临时目录失败：%w", err)
	}
	defer os.RemoveAll(oldRoot)

	var moved []string
	rollback := func() {
		for i := len(moved) - 1; i >= 0; i-- {
			rel := filepath.FromSlash(moved[i])
			_ = os.Remove(filepath.Join(dataDir, rel))
			_ = os.Rename(filepath.Join(oldRoot, rel), filepath.Join(dataDir, rel))
		}
	}

	for _, rel := range res.Restored {
		src := filepath.Join(tmp, filepath.FromSlash(rel))
		dst := filepath.Join(dataDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			rollback()
			return res, fmt.Errorf("创建目录失败（%s）：%w", filepath.Dir(dst), err)
		}
		if _, statErr := os.Stat(dst); statErr == nil {
			old := filepath.Join(oldRoot, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
				rollback()
				return res, err
			}
			if err := os.Rename(dst, old); err != nil {
				rollback()
				return res, fmt.Errorf("恢复 %s 失败：该文件正被占用（后端运行时数据库文件是锁着的）。"+
					"请先停止后端，再用命令行 `agent backup restore <备份文件>` 恢复；"+
					"当前数据没有被改动%s", rel, safePointHint(res.SafePoint))
			}
			moved = append(moved, rel)
		}
		if err := os.Rename(src, dst); err != nil {
			rollback()
			return res, fmt.Errorf("恢复 %s 失败：%w（已回退到原状态）", rel, err)
		}
	}
	res.Note = "恢复完成；数据库连接仍是旧的，重启后端后才完全生效"
	return res, nil
}

func safePointHint(p string) string {
	if p == "" {
		return ""
	}
	return "（安全点：" + p + "）"
}

// extractFile 把归档里一个文件解到 dst，并返回它的 sha256 与字节数
func extractFile(f *zip.File, dst string) (string, int64, error) {
	rc, err := f.Open()
	if err != nil {
		return "", 0, fmt.Errorf("读取归档内 %s 失败：%w", f.Name, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", 0, fmt.Errorf("写入 %s 失败：%w", dst, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), rc)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, fmt.Errorf("解包 %s 失败：%w", f.Name, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Delete 删除一个备份文件（只允许删备份目录里的东西）
func Delete(dataDir, archivePath string) error {
	dir, err := filepath.Abs(Dir(dataDir))
	if err != nil {
		return err
	}
	target, err := filepath.Abs(archivePath)
	if err != nil {
		return err
	}
	if filepath.Dir(target) != dir {
		return fmt.Errorf("只允许删除备份目录 %s 下的文件", dir)
	}
	if !strings.HasSuffix(strings.ToLower(target), ".zip") {
		return errors.New("只允许删除 .zip 备份文件")
	}
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("删除失败：%w", err)
	}
	return nil
}

// Prune 只保留最近 keep 份自动备份（backup-* / auto-*），返回被删掉的文件名
func Prune(dataDir string, keep int) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}
	list, err := List(dataDir)
	if err != nil {
		return nil, err
	}
	var auto []Archive
	for _, a := range list {
		n := strings.ToLower(a.Name)
		if strings.HasPrefix(n, "backup-") || strings.HasPrefix(n, "auto-") {
			auto = append(auto, a)
		}
	}
	var removed []string
	for i := keep; i < len(auto); i++ {
		if err := os.Remove(auto[i].Path); err != nil {
			return removed, err
		}
		removed = append(removed, auto[i].Name)
	}
	return removed, nil
}

/* ---------- 路径安全 ---------- */

// safeRel 把归档里的路径统一成安全的相对路径，越界一律拒绝
func safeRel(p string) (string, error) {
	s := strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if s == "" {
		return "", errors.New("归档里出现空路径")
	}
	if strings.HasPrefix(s, "/") {
		return "", fmt.Errorf("归档里出现绝对路径，已拒绝：%s", p)
	}
	if len(s) >= 2 && s[1] == ':' {
		return "", fmt.Errorf("归档里出现盘符路径，已拒绝：%s", p)
	}
	clean := path.Clean(s)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("归档里出现越界路径，已拒绝：%s", p)
	}
	return filepath.FromSlash(clean), nil
}

func matchAny(rel string, only []string) bool {
	for _, o := range only {
		o = strings.Trim(strings.ReplaceAll(strings.TrimSpace(o), "\\", "/"), "/")
		if o == "" {
			continue
		}
		if rel == o || strings.HasPrefix(rel, o+"/") {
			return true
		}
	}
	return false
}

func entriesByPath(man Manifest) map[string]Entry {
	out := make(map[string]Entry, len(man.Entries))
	for _, e := range man.Entries {
		out[path.Clean(e.Path)] = e
	}
	return out
}

func brief(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
