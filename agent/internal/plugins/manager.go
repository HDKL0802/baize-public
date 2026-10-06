package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"baize/internal/mcp"
)

// Hooks 插件管理要动白泽运行时的那两个口子，由 agentsvc 提供。
//
// 用函数字段而不是接口：这里只需要"读一下技能目录、把 MCP 列表存回去"这么几件事，
// 一个接口反而要调用方另立类型（CronHooks 也是这个路子）。
type Hooks struct {
	// SkillsDir 技能目录（插件带的技能落这里）
	SkillsDir func() string
	// ReloadSkills 技能目录变了之后重扫技能库（否则新技能要等重启才看得见）
	ReloadSkills func() error
	// MCP 读当前配置里的 MCP 服务列表
	MCP func() []mcp.ServerConfig
	// SaveMCP 写回 MCP 服务列表（热插拔 + 落盘）
	SaveMCP func([]mcp.ServerConfig) error
	// AllowRemote 当前是否允许远端 MCP 地址（校验插件带的 MCP 配置要用同一个口径）
	AllowRemote func() bool
	// Now 当前毫秒时间戳（测试可注入）
	Now func() int64
}

// Manager 插件管理：装 / 卸 / 停用启用 / 列市场。
//
// 目录布局（都在 <数据>/plugins 下）：
//
//	store/<id>/          插件的原始解包内容（停用时技能的副本挪进这里的 .off/）
//	store/<id>/.off/     停用期间技能的"活"副本（保留用户改动）
//	installed.json       已安装记录
//	_removed/<id>-<时间>/ 卸载时归档（技能 + 原始包），能捞回来
//	_stage/              解包暂存（用完即删）
type Manager struct {
	dir   string
	st    *store
	hooks Hooks
	hc    *http.Client
	mu    sync.Mutex // 整个安装/卸载流程串行：插件操作少，简单比精致更不容易出错
}

// New 创建插件管理
func New(dir string, hooks Hooks) *Manager {
	if hooks.Now == nil {
		hooks.Now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Manager{dir: dir, st: newStore(dir), hooks: hooks, hc: newHTTPClient()}
}

// Dir 插件数据目录
func (m *Manager) Dir() string { return m.dir }

func (m *Manager) storeDir(id string) string { return filepath.Join(m.dir, "store", id) }

func (m *Manager) skillsDir() string {
	if m.hooks.SkillsDir == nil {
		return ""
	}
	return strings.TrimSpace(m.hooks.SkillsDir())
}

// List 已安装的插件
func (m *Manager) List() ([]Installed, error) { return m.st.list() }

/* ---------- 逛市场 ---------- */

// Catalog 拉取所有源的货架，并标出哪些已装 / 有更新。
//
// 单个源拉不到**不影响**别的源：它会在自己的 SourceView.Error 里如实写下原因，
// 而不是把整个市场变成一片空白让人以为"没有插件"。
func (m *Manager) Catalog(ctx context.Context, sources []Source) Catalog {
	installed, err := m.st.list()
	if err != nil {
		installed = []Installed{}
	}
	byID := map[string]Installed{}
	for _, it := range installed {
		byID[it.ID] = it
	}
	out := Catalog{Sources: []SourceView{}, Available: []Available{}, Installed: installed}
	seenID := map[string]bool{}
	for _, raw := range sources {
		src := NormalizeSource(raw)
		sv := SourceView{Source: src}
		if strings.TrimSpace(src.URL) == "" {
			sv.Error = "这个源没有填地址"
			out.Sources = append(out.Sources, sv)
			continue
		}
		if !src.Enabled {
			sv.Error = "已停用（不拉取）"
			out.Sources = append(out.Sources, sv)
			continue
		}
		idx, _, err := fetchIndex(ctx, m.hc, src)
		if err != nil {
			sv.Error = err.Error()
			out.Sources = append(out.Sources, sv)
			continue
		}
		sv.IndexName = idx.Name
		sv.Updated = idx.Updated
		sv.Count = len(idx.Plugins)
		out.Sources = append(out.Sources, sv)
		for _, meta := range idx.Plugins {
			if seenID[meta.ID] {
				continue // 多个源有同名插件：以先出现的源为准，不重复列
			}
			seenID[meta.ID] = true
			av := Available{Meta: meta, Source: src.Name}
			if it, ok := byID[meta.ID]; ok {
				av.Installed = true
				av.InstVersion = it.Version
				av.HasUpdate = compareVersion(meta.Version, it.Version) > 0
			}
			out.Available = append(out.Available, av)
		}
	}
	return out
}

// findMeta 在所有启用的源里按 id 找插件，返回它的元数据 + 索引真实地址（用于解析相对 url）+ 源名。
func (m *Manager) findMeta(ctx context.Context, sources []Source, id string) (Meta, string, string, error) {
	var tried, failed []string
	var firstErr error
	for _, raw := range sources {
		src := NormalizeSource(raw)
		if !src.Enabled || strings.TrimSpace(src.URL) == "" {
			continue
		}
		tried = append(tried, src.Name)
		idx, at, err := fetchIndex(ctx, m.hc, src)
		if err != nil {
			failed = append(failed, src.Name)
			if firstErr == nil {
				firstErr = fmt.Errorf("源「%s」：%w", src.Name, err)
			}
			continue
		}
		for _, meta := range idx.Plugins {
			if meta.ID == id {
				return meta, at, src.Name, nil
			}
		}
	}
	if len(tried) == 0 {
		return Meta{}, "", "", errors.New("还没有配置任何可用的插件源：请先添加一个源的 index.json 地址，或直接用「本地 zip / URL」安装")
	}
	if len(failed) == len(tried) && firstErr != nil {
		return Meta{}, "", "", fmt.Errorf("插件源都拉不到（%v），没法在源里找 %q", firstErr, id)
	}
	return Meta{}, "", "", fmt.Errorf("这些源里没有 id 为 %q 的插件：%s", id, strings.Join(tried, "、"))
}

/* ---------- 安装 ---------- */

// Install 安装（或升级）一个插件。三种来源任选其一：req.ID（源里按 id）或 req.URL（包里直接给）。
func (m *Manager) Install(ctx context.Context, req InstallRequest) (Installed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	skDir := m.skillsDir()
	if skDir == "" {
		return Installed{}, errors.New("技能目录未配置，插件没地方落盘（请先检查后端配置里的 skillsDir）")
	}

	reqURL, reqID := strings.TrimSpace(req.URL), strings.TrimSpace(req.ID)
	if reqURL == "" && reqID == "" {
		return Installed{}, errors.New("要装什么？给 id（从插件源里装）或 url / 本地 zip 路径（直接装）")
	}

	var (
		data       []byte
		meta       Meta
		sourceDesc string
		pkgRef     string
	)
	if reqURL != "" {
		b, at, err := readRef(ctx, m.hc, reqURL, "", maxPackageBytes)
		if err != nil {
			return Installed{}, err
		}
		data, sourceDesc, pkgRef = b, at, at
	} else {
		found, idxAt, srcName, err := m.findMeta(ctx, req.Sources, reqID)
		if err != nil {
			return Installed{}, err
		}
		meta = found
		b, pkgAt, err := readRef(ctx, m.hc, found.URL, idxAt, maxPackageBytes)
		if err != nil {
			return Installed{}, err
		}
		data, sourceDesc, pkgRef = b, srcName, pkgAt
	}

	verified, err := verifySHA256(data, meta.SHA256)
	if err != nil {
		return Installed{}, err
	}

	// 先解到暂存目录：包有问题就整整齐齐地失败，不把半个包装进 store
	staging := filepath.Join(m.dir, "_stage")
	_ = os.RemoveAll(staging)
	defer os.RemoveAll(staging)
	if err := extractZip(data, staging); err != nil {
		return Installed{}, err
	}
	manifest, err := loadManifest(staging)
	if err != nil {
		return Installed{}, err
	}
	// 源里登记的信息给包兜底（包里没写的字段用源里的）
	if manifest.Name == "" {
		manifest.Name = meta.Name
	}
	if manifest.Version == "" {
		manifest.Version = meta.Version
	}
	if manifest.Description == "" {
		manifest.Description = meta.Description
	}
	manifest, err = normalizeManifest(manifest)
	if err != nil {
		return Installed{}, err
	}
	// 防"货不对板"：源里说装 A，包里声明的却是 B
	if meta.ID != "" && !strings.EqualFold(meta.ID, manifest.ID) {
		return Installed{}, fmt.Errorf("插件包与源里登记的不一致（源：%q，包：%q），已拒绝安装", meta.ID, manifest.ID)
	}

	notes := []string{}
	now := m.hooks.Now()
	if old, ok, err := m.st.get(manifest.ID); err != nil {
		return Installed{}, err
	} else if ok {
		m.teardown(old, skDir, false, &notes)
		notes = append(notes, fmt.Sprintf("已覆盖旧版本 %s", old.Version))
	}

	dest := m.storeDir(manifest.ID)
	_ = os.RemoveAll(dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return Installed{}, err
	}
	if err := moveDir(staging, dest); err != nil {
		return Installed{}, fmt.Errorf("安装到 %s 失败：%w", dest, err)
	}

	inst := Installed{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version,
		Source: sourceDesc, Enabled: true, InstalledAt: now, UpdatedAt: now,
	}
	// 技能：逐个落进技能目录；同名（很可能是用户自己写的）一律跳过
	for _, s := range discoverSkills(dest) {
		live := filepath.Join(skDir, s.Slug)
		if dirExists(live) {
			notes = append(notes, fmt.Sprintf("技能「%s」已存在，跳过（不覆盖你现有的）", s.Slug))
			continue
		}
		if err := copyDir(s.Dir, live); err != nil {
			return Installed{}, fmt.Errorf("写入技能「%s」失败：%w", s.Slug, err)
		}
		inst.Skills = append(inst.Skills, s.Slug)
	}
	// MCP 服务：同名同样跳过；配置不合法（缺 command/url、远端未放行）如实记进说明
	if len(manifest.MCP) > 0 && m.hooks.MCP != nil && m.hooks.SaveMCP != nil {
		cfgs := m.hooks.MCP()
		existing := map[string]bool{}
		for _, c := range cfgs {
			existing[strings.TrimSpace(c.Name)] = true
		}
		allowRemote := m.hooks.AllowRemote != nil && m.hooks.AllowRemote()
		var added []mcp.ServerConfig
		for _, raw := range manifest.MCP {
			raw.Name = strings.TrimSpace(raw.Name)
			if existing[raw.Name] {
				notes = append(notes, fmt.Sprintf("MCP 服务「%s」已存在，跳过（不覆盖你现有的）", raw.Name))
				continue
			}
			norm, nerr := raw.Normalized(allowRemote)
			if nerr != nil {
				notes = append(notes, fmt.Sprintf("MCP 服务「%s」没注册上：%v", raw.Name, nerr))
				continue
			}
			added = append(added, norm)
			existing[norm.Name] = true
			inst.MCP = append(inst.MCP, norm.Name)
		}
		if len(added) > 0 {
			if err := m.hooks.SaveMCP(append(cfgs, added...)); err != nil {
				return Installed{}, fmt.Errorf("注册 MCP 服务失败：%w", err)
			}
		}
	}

	if len(inst.Skills) == 0 && len(inst.MCP) == 0 {
		_ = os.RemoveAll(dest) // 啥也没装上：别占着记录，如实报错
		msg := fmt.Sprintf("插件「%s」没带来任何可用的东西", manifest.ID)
		if len(notes) > 0 {
			msg += "（" + strings.Join(notes, "；") + "）"
		}
		return Installed{}, errors.New(msg)
	}
	if !verified && isRemote(pkgRef) {
		notes = append(notes, "插件源没有提供 sha256，本次未做完整性校验")
	}
	inst.Note = strings.Join(notes, "；")
	if err := m.st.put(inst); err != nil {
		return Installed{}, err
	}
	m.reloadSkills(&inst)
	return inst, nil
}

/* ---------- 卸载 / 停用 ---------- */

// Uninstall 卸载：技能与原始包一起归档到 _removed/（不硬删，能捞回来），并撤掉它注册的 MCP 服务。
func (m *Manager) Uninstall(id string) (Installed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok, err := m.st.get(strings.TrimSpace(id))
	if err != nil {
		return Installed{}, err
	}
	if !ok {
		return Installed{}, ErrNotInstalled
	}
	notes := []string{}
	m.teardown(it, m.skillsDir(), true, &notes)
	if err := m.st.remove(it.ID); err != nil {
		return Installed{}, err
	}
	it.Note = joinNote(it.Note, strings.Join(notes, "；"))
	m.reloadSkills(&it)
	return it, nil
}

// SetEnabled 停用 / 启用：技能的"活"副本在技能目录与 store/<id>/.off 之间搬，保留用户改动；
// 顺带把它注册的 MCP 服务置为禁用 / 启用。
func (m *Manager) SetEnabled(id string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok, err := m.st.get(strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotInstalled
	}
	if it.Enabled == on {
		return nil // 幂等：已经是这个状态
	}
	skDir := m.skillsDir()
	if skDir == "" {
		return errors.New("技能目录未配置")
	}
	if on {
		for _, slug := range it.Skills {
			live := filepath.Join(skDir, slug)
			if dirExists(live) {
				continue // 已经在技能目录里了（可能用户自己又放了一份）
			}
			off := filepath.Join(m.storeDir(it.ID), ".off", slug)
			src := off
			if !dirExists(src) {
				src = filepath.Join(m.storeDir(it.ID), "skills", slug)
			}
			if !dirExists(src) {
				return fmt.Errorf("插件「%s」的技能「%s」原始副本已丢失，恢复不了：%s", it.ID, slug, src)
			}
			if err := moveDir(src, live); err != nil {
				return fmt.Errorf("恢复技能「%s」失败：%w", slug, err)
			}
		}
	} else {
		for _, slug := range it.Skills {
			live := filepath.Join(skDir, slug)
			if !dirExists(live) {
				continue
			}
			if err := moveDir(live, filepath.Join(m.storeDir(it.ID), ".off", slug)); err != nil {
				return fmt.Errorf("停用技能「%s」失败：%w", slug, err)
			}
		}
	}
	if len(it.MCP) > 0 && m.hooks.MCP != nil && m.hooks.SaveMCP != nil {
		owned := map[string]bool{}
		for _, n := range it.MCP {
			owned[n] = true
		}
		cfgs := m.hooks.MCP()
		changed := false
		for i := range cfgs {
			if owned[strings.TrimSpace(cfgs[i].Name)] && cfgs[i].Enabled != on {
				cfgs[i].Enabled = on
				changed = true
			}
		}
		if changed {
			if err := m.hooks.SaveMCP(cfgs); err != nil {
				return fmt.Errorf("切换 MCP 服务开关失败：%w", err)
			}
		}
	}
	it.Enabled = on
	it.UpdatedAt = m.hooks.Now()
	if err := m.st.put(it); err != nil {
		return err
	}
	m.reloadSkills(&it)
	return nil
}

/* ---------- 内部 ---------- */

// teardown 拆掉一个已装插件的"活"部分。
// archive=true（卸载）：技能与原始包一起归档到 _removed/<id>-<时间>/，能捞回来；
// archive=false（升级/覆盖）：直接删，反正马上就要装新的。
func (m *Manager) teardown(it Installed, skDir string, archive bool, notes *[]string) {
	if archive {
		stamp := time.Now().Format("20060102-150405")
		box := uniqueDir(filepath.Join(m.dir, "_removed", it.ID+"-"+stamp))
		for _, slug := range it.Skills {
			live := filepath.Join(skDir, slug)
			if skDir != "" && dirExists(live) {
				if err := moveDir(live, filepath.Join(box, "skills", slug)); err != nil {
					*notes = append(*notes, fmt.Sprintf("归档技能「%s」失败：%v", slug, err))
				}
			}
		}
		if dirExists(m.storeDir(it.ID)) {
			if err := moveDir(m.storeDir(it.ID), filepath.Join(box, "pkg")); err != nil {
				*notes = append(*notes, "归档插件包失败："+err.Error())
				_ = os.RemoveAll(m.storeDir(it.ID))
			}
		}
	} else {
		for _, slug := range it.Skills {
			if skDir != "" {
				_ = os.RemoveAll(filepath.Join(skDir, slug))
			}
		}
		_ = os.RemoveAll(m.storeDir(it.ID))
	}
	// 撤掉我们加进配置的 MCP 服务（用户自己另配的同名服务也会被删——但那种情况
	// 安装时就被跳过了，根本没进 it.MCP，所以这里删的一定是我们自己加的）
	if len(it.MCP) > 0 && m.hooks.MCP != nil && m.hooks.SaveMCP != nil {
		owned := map[string]bool{}
		for _, n := range it.MCP {
			owned[strings.TrimSpace(n)] = true
		}
		kept := make([]mcp.ServerConfig, 0)
		for _, c := range m.hooks.MCP() {
			if owned[strings.TrimSpace(c.Name)] {
				continue
			}
			kept = append(kept, c)
		}
		if err := m.hooks.SaveMCP(kept); err != nil {
			*notes = append(*notes, "移除 MCP 服务失败："+err.Error())
		}
	}
}

// reloadSkills 重扫技能库，失败只作为说明带回（技能其实已经写好，重启也能生效）
func (m *Manager) reloadSkills(inst *Installed) {
	if m.hooks.ReloadSkills == nil {
		return
	}
	if err := m.hooks.ReloadSkills(); err != nil {
		inst.Note = joinNote(inst.Note, "技能库重扫失败（重启后生效）："+err.Error())
	}
}

func joinNote(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "；" + b
	}
}

// loadManifest 读插件包根目录的 plugin.json。没有就直接报清楚（不猜一个默认清单）。
func loadManifest(root string) (Manifest, error) {
	path := filepath.Join(root, "plugin.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Manifest{}, errors.New("插件包根目录里没有 plugin.json（这是插件的清单文件，必须有）")
		}
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("plugin.json 不是合法 JSON：%w", err)
	}
	if m.Schema != 0 && m.Schema != SchemaVersion {
		return Manifest{}, fmt.Errorf("插件清单格式版本 %d 不认识（本机支持 %d）", m.Schema, SchemaVersion)
	}
	return m, nil
}

// skillSrc 一个待落盘的技能：slug（目录名）+ 来源目录
type skillSrc struct{ Slug, Dir string }

// discoverSkills 在插件包里找技能：优先 skills/<slug>/SKILL.md，也认根目录下的 <slug>/SKILL.md。
// 只认"目录名合法且含 SKILL.md"的，其它一律不当技能（免得把 references/ 之类的目录也搬过去）。
func discoverSkills(root string) []skillSrc {
	out := []skillSrc{}
	seen := map[string]bool{}
	scan := func(base string) {
		entries, err := os.ReadDir(base)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || !ValidID(name) || seen[name] {
				continue
			}
			dir := filepath.Join(base, name)
			if !fileExists(filepath.Join(dir, "SKILL.md")) {
				continue
			}
			seen[name] = true
			out = append(out, skillSrc{Slug: name, Dir: dir})
		}
	}
	scan(filepath.Join(root, "skills"))
	scan(root)
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// compareVersion 宽松的版本比较：按点分段，段内取前导数字比大小；两边都不是数字时按字符串比。
// 只服务于"有没有更新"这个判断，不追求语义化版本的完整语义。
func compareVersion(a, b string) int {
	as, bs := strings.Split(strings.TrimSpace(a), "."), strings.Split(strings.TrimSpace(b), ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		xn, xok := numPrefix(x)
		yn, yok := numPrefix(y)
		if xok && yok {
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
			continue
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// numPrefix 取字符串开头那串数字；没有数字就返回 ok=false（交给字符串比较）
func numPrefix(s string) (int, bool) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:i])
	return n, err == nil
}
