// Package ecology 第三部分：探测生态插件的内容形态（Probe）与打包。
//
// 这是「能不能装进白泽」的关键一步：
//   - 有 SKILL.md / 提示词工作流 → 技能型，白泽能直接用；
//   - 有可注册的 MCP server（stdio/http） → 工具型，白泽能接；
//   - 是 cordis 运行时（界面/钩子） → 白泽没有 cordis 宿主，只进目录。
package ecology

import (
	"encoding/json"
	"fmt"
	"strings"

)

// ProbeInfo 探测结果。
type ProbeInfo struct {
	// Kind 内容形态。
	Kind Kind
	// SkillPath 生态仓里 SKILL.md 的相对路径（技能型）。
	SkillPath string
	// MCP 可注册进白泽的 MCP 服务（工具型）。
	MCP []mcpServer
	// License 仓库里的许可证（若能读到）。
	License string
}

// Probe 探测一个生态插件的内容形态。
//
// 传入：包名、版本、仓库地址。返回 nil 表示**探测不了**（如离线、仓库访问不到），
// 调用方会如实写进 Reason，不猜。
type Probe func(name string, v dshVersion, repo string) (*ProbeInfo, error)

// ---------- 通用仓库探测（GitHub / GitLab，公开仓库）----------

// ProbeRepo 用 GitHub/GitLab 的公开 API 探测一个生态插件的仓库内容。
//
// 只读公开信息，不 clone、不执行第三方代码；探测不到的如实返回错误。
func ProbeRepo(name string, v dshVersion, repo string) (*ProbeInfo, error) {
	if strings.TrimSpace(repo) == "" {
		return nil, fmt.Errorf("这个插件没有给出仓库地址，探测不了内容形态")
	}
	info, ok := githubRepo(repo)
	if !ok {
		return nil, fmt.Errorf("仓库地址不是 GitHub/GitLab 形态，暂不探测：%s", repo)
	}
	c := newGH(info)
	dir := strings.Trim(strings.TrimSpace(v.Source.Path), "/")
	res := &ProbeInfo{Kind: KindUnknown}

	// 1) 找 SKILL.md（技能型）
	if p, err := c.findFile(info.owner, info.repo, info.ref, dir, "SKILL.md"); err == nil && p != "" {
		res.Kind = KindSkill
		res.SkillPath = p
		return res, nil
	}

	// 2) 找 mcp 配置（工具型）
	for _, n := range []string{"mcp.json", ".mcp.json"} {
		p, err := c.findFile(info.owner, info.repo, info.ref, dir, n)
		if err != nil || p == "" {
			continue
		}
		raw, err := c.readFile(info.owner, info.repo, info.ref, p)
		if err != nil {
			continue
		}
		servers := parseMCPServers(raw)
		if len(servers) > 0 {
			res.Kind = KindTool
			res.MCP = servers
			return res, nil
		}
	}

	// 3) 有 markdown（提示词/工作流）→ 技能型
	if files, err := c.listFiles(info.owner, info.repo, info.ref, dir); err == nil {
		for _, f := range files {
			if f.Type == "file" && strings.HasSuffix(strings.ToLower(f.Name), ".md") {
				res.Kind = KindSkill
				res.SkillPath = f.Path
				return res, nil
			}
		}
	}

	// 4) package.json 依赖 cordis → 运行时型（白泽不接）
	if p, err := c.findFile(info.owner, info.repo, info.ref, dir, "package.json"); err == nil && p != "" {
		if raw, rerr := c.readFile(info.owner, info.repo, info.ref, p); rerr == nil && looksCordisBundle(raw) {
			res.Kind = KindRuntime
			return res, nil
		}
	}

	return res, nil
}

// parseMCPServers 从一份 MCP 配置 JSON 里解析出服务列表。
// 支持两种常见形状：{mcpServers:{...}} 与 {servers:[...]}。
func parseMCPServers(raw []byte) []mcpServer {
	var doc struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Type    string            `json:"type"`
		} `json:"mcpServers"`
		Servers []struct {
			Name    string            `json:"name"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Type    string            `json:"type"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	var out []mcpServer
	for n, s := range doc.MCPServers {
		if strings.TrimSpace(s.Command) == "" && strings.TrimSpace(s.URL) == "" {
			continue
		}
		out = append(out, mcpServer{Name: n, Type: s.Type, Command: s.Command, Args: s.Args, Env: s.Env, URL: s.URL})
	}
	for _, s := range doc.Servers {
		if strings.TrimSpace(s.Command) == "" && strings.TrimSpace(s.URL) == "" {
			continue
		}
		out = append(out, mcpServer{Name: s.Name, Type: s.Type, Command: s.Command, Args: s.Args, Env: s.Env, URL: s.URL})
	}
	return out
}

// looksCordisBundle 判断一个 package.json 是不是 cordis 运行时插件。
func looksCordisBundle(raw []byte) bool {
	t := strings.ToLower(string(raw))
	return strings.Contains(t, "@deepseek-ai/cordis") || strings.Contains(t, "dsh-client-runtime")
}
