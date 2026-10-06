// Package plugins 是白泽的插件市场底座：插件源（一个静态 index.json）→ 插件包（.zip）→
// 装进白泽（技能目录 + MCP 服务配置）。
//
// 设计取向（对齐项目一贯做法）：
//   - **全是静态文件**：一个插件源就是一个 index.json + 若干 .zip，扔在任何静态托管上都行
//     （零成本、不需要任何后端服务、不需要实名）；同时也认**本地目录/本地 zip**，便于离线安装。
//   - **能验就验、没验就说**：源里给了 sha256 就逐个字节校验；没给就明确记"未校验"，
//     绝不假装验过。
//   - **绝不覆盖用户已有的东西**：装进来的技能/MCP 服务与现有重名时一律跳过，并把它记进
//     Installed.Note 如实回报——插件的优先级永远低于用户自己那摊。
//   - **卸载/停用不硬删**：用户可能改过插件带的技能，所以卸载是"归档"、停用是"移位"，
//     都留在数据目录里能捞回来（与 skill_delete 的归档语义一致）。
package plugins

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"baize/internal/mcp"
)

const (
	// SchemaVersion 清单/索引的格式版本（将来不兼容时靠它分流）
	SchemaVersion = 1
	// maxPackageBytes 单个插件包（zip 或解包前字节）上限
	maxPackageBytes = 64 << 20
	// maxUnpackBytes 解包后总字节上限（防 zip 炸弹）
	maxUnpackBytes = 256 << 20
	// maxFiles 解包文件数上限
	maxFiles = 5000
)

// Source 一个插件源（配在 config.pluginSources 里）。
// URL 指向该源的 index.json：http(s) 地址，或本机路径（离线/自建源/开发时用）。
type Source struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

// Meta 索引里的一条插件（插件市场"货架"上的一格）。
type Meta struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
	License     string   `json:"license,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	MinBaize    string   `json:"minBaize,omitempty"`
	// Channel 板块：official（官方）/ community（社区）。
	// **缺省按 community 处理** —— 没标注来源的东西，不替它宣称"官方"。
	Channel string `json:"channel,omitempty"`
	// Stage 阶段：stable（正式版）/ beta（测试版）。官方板块必须分这两种，社区也可以标。
	Stage string `json:"stage,omitempty"`
	// Category 分类：板块内再分（如 调研写作 / 跨端设备 / 自动化）。空 = 未分类。
	Category string `json:"category,omitempty"`
	// URL 插件包（.zip）地址。可以是绝对地址，也可以相对 index.json ——
	// 相对路径让整套东西能整体搬运（换域名/换目录都不用改）。
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// Index 一个插件源的索引（index.json）。
type Index struct {
	Schema  int    `json:"schema"`
	Name    string `json:"name,omitempty"`
	Updated string `json:"updated,omitempty"`
	Plugins []Meta `json:"plugins"`
}

// Manifest 插件包根目录里的 plugin.json。
//
// 技能不用在清单里列：包里的 skills/<slug>/SKILL.md 会被自动发现；
// 这里只需要声明包"额外"要动白泽配置的部分（目前是 MCP 服务）。
type Manifest struct {
	Schema      int                `json:"schema"`
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Version     string             `json:"version"`
	Description string             `json:"description,omitempty"`
	Author      string             `json:"author,omitempty"`
	Homepage    string             `json:"homepage,omitempty"`
	License     string             `json:"license,omitempty"`
	MinBaize    string             `json:"minBaize,omitempty"`
	Tags        []string           `json:"tags,omitempty"`
	MCP         []mcp.ServerConfig `json:"mcp,omitempty"`
}

// Installed 已安装的一条记录（落 <数据>/plugins/installed.json）。
type Installed struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Source      string   `json:"source,omitempty"` // 来源描述（源名，或直接给的 url/路径）
	Enabled     bool     `json:"enabled"`
	Skills      []string `json:"skills"` // 由本插件落进技能目录的技能目录名（我们负责删）
	MCP         []string `json:"mcp"`    // 由本插件注册进配置的 MCP 服务名（我们负责删）
	InstalledAt int64    `json:"installedAt"`
	UpdatedAt   int64    `json:"updatedAt"`
	Note        string   `json:"note,omitempty"` // 安装时跳过了什么、有没有校验，如实写清
}

// Catalog 一次"逛市场"的结果：源 + 各源货架 + 已安装。
type Catalog struct {
	Sources   []SourceView `json:"sources"`
	Available []Available  `json:"available"`
	Installed []Installed  `json:"installed"`
}

// SourceView 一个源 + 它的货架（或错误）。
type SourceView struct {
	Source
	IndexName string `json:"indexName,omitempty"`
	Updated   string `json:"updated,omitempty"`
	Count     int    `json:"count"`
	Builtin   bool   `json:"builtin,omitempty"` // 内置官方源：随后端分发，删不掉也不用管
	Error     string `json:"error,omitempty"`   // 拉不到就如实报（网络/路径/格式），不装作"这个源是空的"
}

// Available 货架上的一格（源里的元数据 + 是否已装/能否更新）。
type Available struct {
	Meta
	Source      string `json:"source"` // 来自哪个源
	Installed   bool   `json:"installed"`
	InstVersion string `json:"instVersion,omitempty"` // 已装版本（有更新时用来对比）
	HasUpdate   bool   `json:"hasUpdate"`
	Builtin     bool   `json:"builtin,omitempty"` // 来自内置官方源
}

// InstallRequest 安装请求：三种来源任选其一。
type InstallRequest struct {
	// ID 从某个已配置的源里按 id 装（最常用）
	ID string `json:"id"`
	// URL 直接给插件包地址或本地路径（离线安装 / 自己造的包 / 源里没有的）
	URL string `json:"url"`
	// Sources 按 ID 检索时用哪些源（一般传当前配置里启用的源）
	Sources []Source `json:"-"`
}

/* ---------- 校验与归一 ---------- */

var (
	idRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	idRule = "只能用小写字母、数字、连字符、点和下划线，且必须以字母或数字开头"
)

// ValidID 插件 id / 技能目录名的合法字符（决定落盘位置，必须卡死）
func ValidID(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && len(s) <= 64 && idRe.MatchString(s)
}

// 板块与阶段。板块决定"谁做的、背书到哪一步"，阶段决定"能不能上生产"。
const (
	// ChannelOfficial 官方板块（白泽自己出的插件）
	ChannelOfficial = "official"
	// ChannelCommunity 社区板块（第三方投稿）
	ChannelCommunity = "community"
	// StageStable 正式版
	StageStable = "stable"
	// StageBeta 测试版
	StageBeta = "beta"
)

// NormalizeChannel 收敛板块名。**认不出来的一律当社区** ——
// 索引是别人写的文件，缺字段/写错时不能替它宣称"官方"。
func NormalizeChannel(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case ChannelOfficial, "官方":
		return ChannelOfficial
	default:
		return ChannelCommunity
	}
}

// NormalizeStage 收敛阶段名：只有明确写成 beta/测试版 才算测试版，其余按正式版。
func NormalizeStage(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case StageBeta, "测试版", "测试":
		return StageBeta
	default:
		return StageStable
	}
}

// NormalizeSource 归一一个源：去空白、补名字、按 url 判空。
func NormalizeSource(s Source) Source {
	s.Name = strings.TrimSpace(s.Name)
	s.URL = strings.TrimSpace(s.URL)
	if s.Name == "" {
		s.Name = s.URL
	}
	return s
}

// normalizeIndex 归一索引：丢掉没有 url 的条目、id 非法的条目。
// 坏条目**整条丢弃并计数**，而不是让它带着空 url 进市场（点下去才发现装不了）。
func normalizeIndex(idx Index) Index {
	out := Index{Schema: idx.Schema, Name: strings.TrimSpace(idx.Name), Updated: strings.TrimSpace(idx.Updated)}
	seen := map[string]bool{}
	for _, m := range idx.Plugins {
		m.ID = strings.TrimSpace(m.ID)
		m.Name = strings.TrimSpace(m.Name)
		m.Version = strings.TrimSpace(m.Version)
		m.URL = strings.TrimSpace(m.URL)
		if !ValidID(m.ID) || m.URL == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if m.Name == "" {
			m.Name = m.ID
		}
		if m.Version == "" {
			m.Version = "0"
		}
		m.Channel = NormalizeChannel(m.Channel)
		m.Stage = NormalizeStage(m.Stage)
		m.Category = strings.TrimSpace(m.Category)
		out.Plugins = append(out.Plugins, m)
	}
	return out
}

// normalizeManifest 校验并归一插件清单。
func normalizeManifest(m Manifest) (Manifest, error) {
	m.ID = strings.TrimSpace(m.ID)
	m.Name = strings.TrimSpace(m.Name)
	m.Version = strings.TrimSpace(m.Version)
	if !ValidID(m.ID) {
		return m, fmt.Errorf("插件 id %q 不合法：%s", m.ID, idRule)
	}
	if m.Name == "" {
		m.Name = m.ID
	}
	if m.Version == "" {
		m.Version = "0"
	}
	// MCP 服务只做基本清理；真正确认（command/url 齐不齐、远端要不要 allowRemote）
	// 交给 MCPUpsert 那一层——那里是白泽唯一的规整口径。
	cleaned := make([]mcp.ServerConfig, 0, len(m.MCP))
	for _, c := range m.MCP {
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" {
			continue
		}
		cleaned = append(cleaned, c)
	}
	m.MCP = cleaned
	return m, nil
}

// ErrNotInstalled 卸载/启停一个没装过的插件
var ErrNotInstalled = errors.New("没有安装这个插件")
