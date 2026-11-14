// Package ecology 第五部分：把生态条目打成白泽插件包，并产出白泽格式索引。
//
// 产物结构与白泽内置源完全一致：
//
//	<out>/index.json                                   白泽索引（plugins.Index）
//	<out>/packages/<id>-<version>.zip                  插件包
//	<out>/packages/<id>-<version>.zip  内含
//	    plugin.json                                    白泽清单（plugins.Manifest）
//	    skills/<slug>/SKILL.md                          技能（技能型插件）
//
// 这个目录挂到 config.pluginSources 里，就是一个普通的白泽插件源。
package ecology

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"baize/internal/mcp"
	"baize/internal/plugins"
)

// BuildOptions 打包参数。
type BuildOptions struct {
	// OutDir 输出目录（会创建 packages/ 与 index.json）。
	OutDir string
	// SourceName 写进 index.json 的源名。
	SourceName string
	// SkillReader 读生态插件的 SKILL.md 原文（技能型插件要用）。
	SkillReader func(entry Entry) (string, error)
	// MCPReader 读生态插件的 MCP 配置原文（工具型插件要用）。
	MCPReader func(entry Entry) ([]byte, error)
}

// BuildResult 打包结果。
type BuildResult struct {
	IndexPath string   `json:"indexPath"`
	Packages  []string `json:"packages"`
	Built     int      `json:"built"`
	Skipped   int      `json:"skipped"`
	Errors    []string `json:"errors,omitempty"`
}

// Build 把可安装的生态条目打成白泽插件包与索引。
func Build(cat *Catalog, opt BuildOptions) (*BuildResult, error) {
	if cat == nil {
		return nil, fmt.Errorf("没有目录可打包")
	}
	if strings.TrimSpace(opt.OutDir) == "" {
		return nil, fmt.Errorf("缺少输出目录")
	}
	pkgDir := filepath.Join(opt.OutDir, "packages")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return nil, err
	}
	res := &BuildResult{IndexPath: filepath.Join(opt.OutDir, "index.json")}
	idx := plugins.Index{Schema: plugins.SchemaVersion, Name: firstNonEmpty(opt.SourceName, cat.Title, "生态源")}
	for _, e := range cat.Entries {
		if !e.Installable {
			res.Skipped++
			continue
		}
		meta, err := buildOne(e, opt, pkgDir)
		if err != nil {
			if len(res.Errors) < 50 {
				res.Errors = append(res.Errors, e.ID+": "+err.Error())
			}
			res.Skipped++
			continue
		}
		idx.Plugins = append(idx.Plugins, meta)
		res.Packages = append(res.Packages, filepath.Join(pkgDir, fmt.Sprintf("%s-%s.zip", e.ID, firstNonEmpty(e.Version, "0"))))
		res.Built++
	}
	// 索引里的 url 用相对路径 —— 整套源可以整体搬家。
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(res.IndexPath, append(data, '\n'), 0o644); err != nil {
		return nil, err
	}
	return res, nil
}

// buildOne 打一个插件包，返回它的索引条目。
func buildOne(e Entry, opt BuildOptions, pkgDir string) (plugins.Meta, error) {
	slug := e.ID
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// 1) plugin.json
	man := plugins.Manifest{
		Schema:      plugins.SchemaVersion,
		ID:          e.ID,
		Name:        firstNonEmpty(e.Name, e.ID),
		Version:     firstNonEmpty(e.Version, "0"),
		Description: firstNonEmpty(e.Description, "来自生态市场的插件（"+e.ID+"）"),
		Author:      e.Author,
		Homepage:    e.Homepage,
		License:     e.License,
		Tags:        e.Tags,
	}
	// 技能型：把 SKILL.md 塞进包里的 skills/<slug>/SKILL.md。
	if e.Kind == KindSkill {
		if opt.SkillReader == nil {
			return plugins.Meta{}, fmt.Errorf("没有技能读取器")
		}
		text, err := opt.SkillReader(e)
		if err != nil {
			return plugins.Meta{}, fmt.Errorf("读技能原文失败：%w", err)
		}
		if strings.TrimSpace(text) == "" {
			return plugins.Meta{}, fmt.Errorf("技能原文是空的")
		}
		if err := writeZip(zw, "skills/"+slug+"/SKILL.md", []byte(text)); err != nil {
			return plugins.Meta{}, err
		}
	}
	// 工具型：写 mcp[] 段。
	if e.Kind == KindTool {
		if len(e.MCP) == 0 {
			return plugins.Meta{}, fmt.Errorf("工具型插件没有可注册的 MCP 服务")
		}
		for _, s := range e.MCP {
			man.MCP = append(man.MCP, mcpServerConfig(s))
		}
	}
	raw, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return plugins.Meta{}, err
	}
	if err := writeZip(zw, "plugin.json", append(raw, '\n')); err != nil {
		return plugins.Meta{}, err
	}
	if err := zw.Close(); err != nil {
		return plugins.Meta{}, err
	}
	pkg := buf.Bytes()
	name := fmt.Sprintf("%s-%s.zip", e.ID, firstNonEmpty(e.Version, "0"))
	dest := filepath.Join(pkgDir, name)
	if err := os.WriteFile(dest, pkg, 0o644); err != nil {
		return plugins.Meta{}, err
	}
	sum := sha256.Sum256(pkg)
	return plugins.Meta{
		ID:          e.ID,
		Name:        firstNonEmpty(e.Name, e.ID),
		Version:     firstNonEmpty(e.Version, "0"),
		Description: man.Description,
		Author:      e.Author,
		Homepage:    e.Homepage,
		License:     e.License,
		Tags:        e.Tags,
		Channel:     plugins.ChannelCommunity,
		Stage:       plugins.NormalizeStage(""),
		Category:    e.Category,
		URL:         "packages/" + name,
		SHA256:      hex.EncodeToString(sum[:]),
		Size:        int64(len(pkg)),
	}, nil
}

// writeZip 往 zip 里写一个文件。
func writeZip(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// mcpServerConfig 把生态侧的 MCP 定义转成白泽的 mcp.ServerConfig。
//
// 生态侧的 type（stdio/http）对应白泽的 Transport；白泽的 Env 是 K=V 列表，
// 生态侧的 map 按 key 排序转成列表 —— 顺序固定，便于产物稳定可比对。
func mcpServerConfig(s mcpServer) mcp.ServerConfig {
	c := mcp.ServerConfig{Name: s.Name, Command: s.Command, Args: s.Args, URL: s.URL, Enabled: true}
	switch strings.ToLower(strings.TrimSpace(s.Type)) {
	case "http", "streamable-http", "sse":
		c.Transport = "http"
	default:
		c.Transport = "stdio"
	}
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c.Env = append(c.Env, k+"="+s.Env[k])
	}
	return c
}

// SortEntries 按 id 排序（输出稳定，便于 diff）。
func SortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
}
