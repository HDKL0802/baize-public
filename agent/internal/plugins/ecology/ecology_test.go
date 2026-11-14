package ecology

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeIndex 写一个临时的 DSH 格式 1 索引文件。
func writeIndex(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "index.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("写临时索引失败：%v", err)
	}
	return p
}

// TestFetchClassify 四类判定：技能 / 工具 / 运行时 / 未知，外加探测失败。
func TestFetchClassify(t *testing.T) {
	idx := `{
	 "format": 1,
	 "title": "示例生态市场",
	 "metadata": {"categories": {"tool": {"title": "工具"}}, "labels": {}, "sortKeys": {}},
	 "plugins": [
	  {"name": "dsh-skill-one", "title": "技能一", "description": "带 SKILL.md",
	   "repository": {"url": "https://github.com/example/dsh-skill-one"},
	   "metadata": {"category": "tool", "keywords": ["a"], "labels": ["x"], "sortKeys": {}},
	   "versions": [{"version": "1.2.0", "publishTimestamp": 1, "source": {"type": "npm"}, "engines": {}}]},
	  {"name": "dsh-tool-one", "description": "带 MCP",
	   "repository": {"url": "https://github.com/example/dsh-tool-one"},
	   "metadata": {"keywords": [], "labels": [], "sortKeys": {}},
	   "versions": [{"version": "0.9.0", "publishTimestamp": 1, "source": {"type": "git", "url": "https://github.com/example/dsh-tool-one", "commit": "0123456789abcdef0123456789abcdef01234567"}, "engines": {}}]},
	  {"name": "dsh-runtime-one", "description": "cordis 运行时",
	   "metadata": {"keywords": [], "labels": [], "sortKeys": {}},
	   "versions": [{"version": "1.0.0", "publishTimestamp": 1, "source": {"type": "npm"}, "engines": {}}]},
	  {"name": "dsh-unknown-one", "description": "看不出形态",
	   "metadata": {"keywords": [], "labels": [], "sortKeys": {}},
	   "versions": [{"version": "1.0.0", "publishTimestamp": 1, "source": {"type": "npm"}, "engines": {}}]},
	  {"name": "dsh-offline-one", "description": "探测不到",
	   "metadata": {"keywords": [], "labels": [], "sortKeys": {}},
	   "versions": [{"version": "1.0.0", "publishTimestamp": 1, "source": {"type": "npm"}, "engines": {}}]}
	 ]
	}`
	path := writeIndex(t, idx)

	probe := func(name string, v dshVersion, repo string) (*ProbeInfo, error) {
		switch {
		case strings.Contains(name, "skill"):
			return &ProbeInfo{Kind: KindSkill, SkillPath: "SKILL.md"}, nil
		case strings.Contains(name, "tool"):
			return &ProbeInfo{Kind: KindTool, MCP: []mcpServer{{Name: "srv", Type: "stdio", Command: "node", Args: []string{"x.js"}}}}, nil
		case strings.Contains(name, "runtime"):
			return &ProbeInfo{Kind: KindRuntime}, nil
		case strings.Contains(name, "unknown"):
			return &ProbeInfo{Kind: KindUnknown}, nil
		}
		return nil, &offlineErr{}
	}

	cat, err := Fetch(path, probe)
	if err != nil {
		t.Fatalf("Fetch 失败：%v", err)
	}
	if cat.Total != 5 {
		t.Fatalf("应当 5 条，实际 %d", cat.Total)
	}
	if cat.Title != "示例生态市场" {
		t.Fatalf("标题没读对：%q", cat.Title)
	}
	byID := map[string]Entry{}
	for _, e := range cat.Entries {
		byID[e.ID] = e
	}

	e := byID["dsh-skill-one"]
	if !e.Installable || e.Kind != KindSkill {
		t.Errorf("技能型判定不对：installable=%v kind=%s", e.Installable, e.Kind)
	}
	if e.Name != "技能一" {
		t.Errorf("title 没读出来：%q", e.Name)
	}
	if e.Category != "tool" {
		t.Errorf("category 没读出来：%q", e.Category)
	}
	if len(e.Tags) != 2 {
		t.Errorf("keywords+labels 应合成 2 个标签，实际 %d", len(e.Tags))
	}

	e = byID["dsh-tool-one"]
	if !e.Installable || e.Kind != KindTool || len(e.MCP) != 1 {
		t.Errorf("工具型判定不对：installable=%v kind=%s mcp=%d", e.Installable, e.Kind, len(e.MCP))
	}
	if !strings.Contains(e.InstallSpec, "git+") {
		t.Errorf("git 源的安装说明不对：%q", e.InstallSpec)
	}

	e = byID["dsh-runtime-one"]
	if e.Installable {
		t.Error("cordis 运行时不该标成可安装")
	}
	if e.Reason == "" {
		t.Error("运行时条目必须写明为什么不能装")
	}

	e = byID["dsh-unknown-one"]
	if e.Installable {
		t.Error("看不出形态的条目不该标成可安装")
	}

	e = byID["dsh-offline-one"]
	if e.Installable {
		t.Error("探测失败的条目不该标成可安装")
	}
	if !strings.Contains(e.Reason, "没能探测到内容形态") {
		t.Errorf("探测失败应说明「没探测到」并保留原始原因，实际 %q", e.Reason)
	}

	if cat.Installable != 2 || cat.Directory != 3 {
		t.Errorf("统计不对：可装 %d / 仅目录 %d", cat.Installable, cat.Directory)
	}
}

// offlineErr 模拟探测失败。
type offlineErr struct{}

func (e *offlineErr) Error() string { return "探测不到（离线）" }

// TestEcosystemID 包名归一成白泽合法 id。
func TestEcosystemID(t *testing.T) {
	cases := map[string]string{
		"dsh-meme":                  "dsh-meme",
		"@weibaohui/dsh-fireworks":  "weibaohui.dsh-fireworks",
		"@scope/pkg":                "scope.pkg",
		"UPPER/Case":                "upper.case",
		"  ":                        "",
	}
	for in, want := range cases {
		if got := EcosystemID(in); got != want {
			t.Errorf("EcosystemID(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestBuildPackages 把目录条目打成白泽插件包 + 索引。
func TestBuildPackages(t *testing.T) {
	cat := &Catalog{
		Title: "示例生态市场",
		Entries: []Entry{
			{ID: "dsh-skill-one", Name: "技能一", Version: "1.2.0", Description: "带 SKILL.md", Kind: KindSkill, Installable: true, SkillPath: "SKILL.md", Repository: "https://github.com/example/dsh-skill-one", License: "MIT", Category: "tool"},
			{ID: "dsh-tool-one", Name: "工具一", Version: "0.9.0", Description: "带 MCP", Kind: KindTool, Installable: true, MCP: []mcpServer{{Name: "srv", Type: "stdio", Command: "node", Args: []string{"x.js"}, Env: map[string]string{"K": "V"}}}, Repository: "https://github.com/example/dsh-tool-one", License: "MIT"},
		},
	}
	outDir := filepath.Join(t.TempDir(), "market")
	res, err := Build(cat, BuildOptions{
		OutDir:     outDir,
		SourceName: "DSH 生态",
		SkillReader: func(e Entry) (string, error) {
			return "# 技能\n\n正文\n", nil
		},
		MCPReader: func(e Entry) ([]byte, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("Build 失败：%v", err)
	}
	if len(res.Packages) != 2 {
		t.Fatalf("应当产出 2 个包，实际 %d", len(res.Packages))
	}

	idxBytes, err := os.ReadFile(filepath.Join(outDir, "index.json"))
	if err != nil {
		t.Fatalf("读索引失败：%v", err)
	}
	var idx map[string]any
	if err := json.Unmarshal(idxBytes, &idx); err != nil {
		t.Fatalf("索引不是合法 JSON：%v", err)
	}
	plugs, _ := idx["plugins"].([]any)
	if len(plugs) != 2 {
		t.Fatalf("索引里应当 2 条，实际 %d", len(plugs))
	}

	for _, id := range []string{"dsh-skill-one", "dsh-tool-one"} {
		found := ""
		for _, p := range res.Packages {
			if strings.Contains(p, id) {
				found = p
			}
		}
		if found == "" {
			t.Fatalf("没找到 %s 的包", id)
		}
		r, err := zip.OpenReader(found)
		if err != nil {
			t.Fatalf("打开包失败：%v", err)
		}
		hasPlugin, hasSkill := false, false
		for _, f := range r.File {
			if f.Name == "plugin.json" {
				hasPlugin = true
			}
			if strings.HasSuffix(f.Name, "/SKILL.md") {
				hasSkill = true
			}
		}
		r.Close()
		if !hasPlugin {
			t.Errorf("%s 的包里没有 plugin.json", id)
		}
		if id == "dsh-skill-one" && !hasSkill {
			t.Errorf("%s 的包里没有 SKILL.md", id)
		}
	}
}
