// Package ecology 生态插件接入：把别的 Agent 生态的插件市场接进白泽。
//
// 背景（用户需求）：白泽要内置多种插件方式，把 DSH / Trae 等生态的插件市场都接进来，
// 让白泽拥有大生态库。参考实现是 DSH 的插件市场（开源，@deepseek-ai/dsh-plugin-market）。
//
// **诚实边界（本包最重要的约定）**：
//
//	别的生态的「插件本体」通常**不能直接装进白泽**。
//	例如 DSH 装的是 cordis 运行时插件（依赖 @deepseek-ai/cordis，靠 fiber 挂到宿主进程），
//	白泽没有 cordis 宿主，塞进去也不会跑起来。
//
//	所以本包做的是**内容转译**，不是「包搬运」：把生态插件里的**技能**（SKILL.md）、
//	**MCP 工具**（stdio/http server）取出来，按白泽的插件协议重新打包。
//	纯运行时插件（UI 面板 / hook）只进目录、不进安装列表 —— 界面上如实标「仅目录」。
//
// 一致性口径：
//   - 生态插件一律 channel=community（**不替第三方宣称官方**）；
//   - 转译出来的东西**只保留内容**，不执行第三方代码，不自动安装；
//   - 每条都记下来源（仓库地址 + 许可证），让用户自己判断。
package ecology

import (
	"encoding/json"
	"strings"

)

// ---------- DSH 市场索引格式 1 ----------

// dshIndex DSH 插件市场索引（格式 1）。字段名与 @deepseek-ai/dsh-plugin-market-index 一致。
// 只取我们用得到的字段；其余未定义成员直接忽略（该校验器的规则也是「丢弃未定义成员」）。
type dshIndex struct {
	Format      int              `json:"format"`
	Title       localizedText      `json:"title"`
	Description string           `json:"description"`
	Metadata    dshIndexMetadata `json:"metadata"`
	Plugins     []dshPlugin      `json:"plugins"`
}

type dshIndexMetadata struct {
	Categories map[string]dshDeclaration `json:"categories"`
	Labels     map[string]dshDeclaration `json:"labels"`
	SortKeys   map[string]dshSortKey     `json:"sortKeys"`
}

type dshDeclaration struct {
	Title localizedText `json:"title"`
}

type dshSortKey struct {
	Title json.RawMessage `json:"title"`
	Order string          `json:"order"`
}

// dshPlugin 一个可安装的插件（bundle）。
type dshPlugin struct {
	Name        string           `json:"name"`
	Title       localizedText `json:"title"`
	Description localizedText `json:"description"`
	Icon        string           `json:"icon"`
	Author      string           `json:"author"`
	Homepage    string           `json:"homepage"`
	Repository  *dshRepository   `json:"repository"`
	Metadata    dshPluginMeta    `json:"metadata"`
	Versions    []dshVersion     `json:"versions"`
}

type dshRepository struct {
	URL       string `json:"url"`
	Directory string `json:"directory"`
}

type dshPluginMeta struct {
	Category string            `json:"category"`
	Keywords []string          `json:"keywords"`
	Labels   []string          `json:"labels"`
	SortKeys map[string]float64 `json:"sortKeys"`
}

// dshVersion 一个确切版本及其安装来源。
type dshVersion struct {
	Version          string       `json:"version"`
	PublishTimestamp float64      `json:"publishTimestamp"`
	Source           dshSource    `json:"source"`
	Engines          dshEngines   `json:"engines"`
}

// dshSource 安装来源：npm 包或 git+commit。
type dshSource struct {
	Type     string `json:"type"`
	Registry string `json:"registry"`
	URL      string `json:"url"`
	Commit   string `json:"commit"`
	Path     string `json:"path"`
}

// dshEngines 运行时要求（DSH 版本范围）。白泽不解释它，只如实展示。
type dshEngines struct {
	DSH string `json:"dsh"`
}

// ---------- 本地化文本 ----------

// localizedText DSH 的 title/description 可能是字符串，也可能是 {zh:…, en:…}。
type localizedText struct {
	Raw json.RawMessage
}

// UnmarshalJSON 让本地化字段能直接按字符串或语言对象解出来。
//
// DSH 的 title/description 可能是字符串，也可能是 {"zh": "...", "en": "..."} 这种
// 语言对象 —— 不给这个方法，json 会把它原样塞进 Raw，String() 再解一次就变成带引号的串。
func (l *localizedText) UnmarshalJSON(data []byte) error {
	if l == nil {
		return nil
	}
	l.Raw = append(l.Raw[:0], data...)
	return nil
}
// String 取一份可读文本：字符串直接用；对象就按 zh → en → 第一个值的顺序取。
func (l localizedText) String() string {
	if len(l.Raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(l.Raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var m map[string]string
	if err := json.Unmarshal(l.Raw, &m); err == nil {
		for _, k := range []string{"zh", "zh-CN", "zh-Hans", "en", "en-US"} {
			if v, ok := m[k]; ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		for _, v := range m {
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// ---------- 生态条目 ----------

// Kind 生态插件转译后的落地类型。
type Kind string

const (
	// KindSkill 技能型：原插件带 SKILL.md / 提示词工作流 → 转成白泽技能。
	KindSkill Kind = "skill"
	// KindTool 工具型：原插件提供 MCP server → 转成白泽 MCP 服务。
	KindTool Kind = "tool"
	// KindRuntime 运行时型：纯 cordis 运行时（UI/hook）→ **白泽不接**，只进目录。
	KindRuntime Kind = "runtime"
	// KindUnknown 看不出内容形态 → 只进目录，并说明原因。
	KindUnknown Kind = "unknown"
)

// Entry 生态目录里的一条。
type Entry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
	Repository  string   `json:"repository,omitempty"`
	License     string   `json:"license,omitempty"`
	Category    string   `json:"category,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Kind        Kind     `json:"kind"`
	// Installable 白泽这边能不能真装（技能型/工具型 = 能）。
	Installable bool `json:"installable"`
	// Reason 不能装 / 装不了时，如实说明原因（界面上原样显示）。
	Reason string `json:"reason,omitempty"`
	// SkillPath 技能型插件在生态仓里的 SKILL.md 相对路径（若有）。
	SkillPath string `json:"skillPath,omitempty"`
	// MCP 工具型插件的 MCP 服务定义（可装进白泽的那些）。
	MCP []mcpServer `json:"mcp,omitempty"`
	// InstallSpec 生态侧的安装命令（仅展示，白泽不执行）。
	InstallSpec string `json:"installSpec,omitempty"`
}

// mcpServer 一个可注册进白泽的 MCP 服务。
type mcpServer struct {
	Name    string `json:"name"`
	Type    string `json:"type,omitempty"`
	Command string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Catalog 一次生态同步的结果。
type Catalog struct {
	Source    string  `json:"source"`
	Title     string  `json:"title,omitempty"`
	Updated   string  `json:"updated,omitempty"`
	Total     int     `json:"total"`
	Installable int   `json:"installable"`
	Directory int     `json:"directory"`
	Entries   []Entry `json:"entries"`
	Errors    []string `json:"errors,omitempty"`
}
