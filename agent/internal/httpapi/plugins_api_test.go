package httpapi_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"baize/internal/config"
	"baize/internal/plugins"
)

// sha256Of 插件包摘要（源里登记的 sha256）
func sha256Of(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// zipBytes 在内存里打一个 zip（插件包）
func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatalf("建 zip 条目失败：%v", err)
		}
		if _, err := w.Write([]byte(files[n])); err != nil {
			t.Fatalf("写 zip 条目失败：%v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("收尾 zip 失败：%v", err)
	}
	return buf.Bytes()
}

// pluginPkg 造一个插件包（一个技能 + 一个本机 MCP 服务）
func pluginPkg(t *testing.T, id, slug string) []byte {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"schema": 1, "id": id, "name": "测试插件 " + id, "version": "1.0.0",
		"description": "接口测试用",
		"mcp":         []map[string]any{{"name": id + "-mcp", "transport": "http", "url": "http://127.0.0.1:19999/mcp"}},
	})
	return zipBytes(t, map[string]string{
		"plugin.json":                  string(raw),
		"skills/" + slug + "/SKILL.md": "---\nname: " + slug + "\ndescription: 测试技能。\n---\n\n正文",
	})
}

type marketResp struct {
	Dir       string               `json:"dir"`
	Sources   []plugins.SourceView `json:"sources"`
	Available []plugins.Available  `json:"available"`
	Installed []plugins.Installed  `json:"installed"`
}

type pluginActResp struct {
	OK             bool                  `json:"ok"`
	Plugin         plugins.Installed     `json:"plugin"`
	Installed      []plugins.Installed   `json:"installed"`
	InstalledError string                `json:"installedError"`
	Sources        []config.PluginSource `json:"sources"`
	Error          string                `json:"error"`
}

func TestPluginsMarketFlow(t *testing.T) {
	e, svc := newAgentEnv(t)
	dataDir := svc.DataDir()

	// 造一个本地插件源：index.json + 插件包
	srcDir := filepath.Join(dataDir, "localsrc")
	pkg := pluginPkg(t, "demo", "demo-helper")
	if err := writeFile(filepath.Join(srcDir, "demo.zip"), string(pkg)); err != nil {
		t.Fatal(err)
	}
	idx, _ := json.Marshal(map[string]any{"schema": 1, "name": "本地测试源", "plugins": []map[string]any{{
		"id": "demo", "name": "演示插件", "version": "1.0.0",
		"description": "接口测试", "url": "demo.zip", "sha256": sha256Of(pkg),
	}}})
	if err := writeFile(filepath.Join(srcDir, "index.json"), string(idx)); err != nil {
		t.Fatal(err)
	}

	// 加源
	var add pluginActResp
	if code := e.do("POST", "/api/agent/plugins", map[string]any{
		"action": "addSource", "name": "本地测试源", "url": filepath.Join(srcDir, "index.json"),
	}, true, &add); code != 200 {
		t.Fatalf("加插件源失败：HTTP %d %s", code, add.Error)
	}
	if len(add.Sources) != 1 || !add.Sources[0].Enabled {
		t.Fatalf("插件源没加上：%+v", add.Sources)
	}

	// 逛市场：用户源在前、内置官方源在后
	var m marketResp
	if code := e.do("GET", "/api/agent/plugins", nil, true, &m); code != 200 {
		t.Fatalf("读市场失败：HTTP %d", code)
	}
	if len(m.Sources) != 2 {
		t.Fatalf("应当有 2 个源（用户源 + 内置官方源），实际 %d：%+v", len(m.Sources), m.Sources)
	}
	if m.Sources[0].Error != "" || m.Sources[0].Count != 1 || m.Sources[0].Builtin {
		t.Fatalf("用户源状态不对：%+v", m.Sources[0])
	}
	if !m.Sources[1].Builtin || m.Sources[1].Error != "" || m.Sources[1].Count == 0 {
		t.Fatalf("内置官方源状态不对：%+v", m.Sources[1])
	}
	if demo := findAvail(m, "demo"); demo == nil || demo.Installed || demo.Builtin {
		t.Fatalf("用户源里那格货架不对：%+v", demo)
	}
	if len(m.Installed) != 0 {
		t.Fatalf("还没装就有已装记录：%+v", m.Installed)
	}

	// 安装
	var inst pluginActResp
	if code := e.do("POST", "/api/agent/plugins", map[string]any{"action": "install", "id": "demo"}, true, &inst); code != 200 {
		t.Fatalf("安装失败：HTTP %d %s", code, inst.Error)
	}
	if inst.Plugin.ID != "demo" || len(inst.Plugin.Skills) != 1 || len(inst.Plugin.MCP) != 1 {
		t.Fatalf("安装结果不对：%+v", inst.Plugin)
	}
	// 技能真的落到了技能目录，且技能库能看见
	if _, err := os.Stat(filepath.Join(dataDir, "skills", "demo-helper", "SKILL.md")); err != nil {
		t.Fatalf("技能没落盘：%v", err)
	}
	found := false
	for _, sk := range svc.Skills() {
		if sk.Name == "demo-helper" {
			found = true
		}
	}
	if !found {
		t.Fatal("技能库没重扫到新技能")
	}
	// MCP 服务真的进了配置
	if len(svc.Config().MCPServers) != 1 || svc.Config().MCPServers[0].Name != "demo-mcp" {
		t.Fatalf("MCP 没进配置：%+v", svc.Config().MCPServers)
	}

	// 再逛一次：应显示已装
	if code := e.do("GET", "/api/agent/plugins", nil, true, &m); code != 200 {
		t.Fatal("二次读市场失败")
	}
	if len(m.Installed) != 1 {
		t.Fatalf("已装记录没反映出来：%+v", m.Installed)
	}
	if demo := findAvail(m, "demo"); demo == nil || !demo.Installed {
		t.Fatalf("货架上的已装状态没更新：%+v", demo)
	}

	// 停用 → 技能从技能目录挪走、MCP 关掉
	var off pluginActResp
	if code := e.do("POST", "/api/agent/plugins", map[string]any{"action": "disable", "id": "demo"}, true, &off); code != 200 {
		t.Fatalf("停用失败：HTTP %d %s", code, off.Error)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "skills", "demo-helper")); !os.IsNotExist(err) {
		t.Fatalf("停用后技能还在技能目录：%v", err)
	}
	if len(svc.Config().MCPServers) != 1 || svc.Config().MCPServers[0].Enabled {
		t.Fatalf("停用后 MCP 没关：%+v", svc.Config().MCPServers)
	}

	// 启用 → 回来
	var on pluginActResp
	if code := e.do("POST", "/api/agent/plugins", map[string]any{"action": "enable", "id": "demo"}, true, &on); code != 200 {
		t.Fatalf("启用失败：HTTP %d %s", code, on.Error)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "skills", "demo-helper", "SKILL.md")); err != nil {
		t.Fatalf("启用后技能没回来：%v", err)
	}

	// 卸载 → 技能与 MCP 一起撤掉
	var un pluginActResp
	if code := e.do("POST", "/api/agent/plugins", map[string]any{"action": "uninstall", "id": "demo"}, true, &un); code != 200 {
		t.Fatalf("卸载失败：HTTP %d %s", code, un.Error)
	}
	if len(un.Installed) != 0 {
		t.Fatalf("卸载后还有记录：%+v", un.Installed)
	}
	if len(svc.Config().MCPServers) != 0 {
		t.Fatalf("卸载后 MCP 没撤：%+v", svc.Config().MCPServers)
	}

	// 删源
	var rm pluginActResp
	if code := e.do("POST", "/api/agent/plugins", map[string]any{
		"action": "removeSource", "url": filepath.Join(srcDir, "index.json"),
	}, true, &rm); code != 200 {
		t.Fatalf("删源失败：HTTP %d %s", code, rm.Error)
	}
	if len(rm.Sources) != 0 {
		t.Fatalf("删源后还有源：%+v", rm.Sources)
	}
}

// findAvail 在货架里按 id 找一格
func findAvail(m marketResp, id string) *plugins.Available {
	for i := range m.Available {
		if m.Available[i].ID == id {
			return &m.Available[i]
		}
	}
	return nil
}

func TestPluginsBuiltinSourceAndDirectInstall(t *testing.T) {
	e, svc := newAgentEnv(t)

	// 没配任何用户源时，市场里也一定有内置官方源（随后端分发，开箱有内容、离线可用）
	var m marketResp
	if code := e.do("GET", "/api/agent/plugins", nil, true, &m); code != 200 {
		t.Fatalf("读市场失败：HTTP %d", code)
	}
	if len(m.Sources) != 1 || !m.Sources[0].Builtin || m.Sources[0].Error != "" {
		t.Fatalf("应当只有内置官方源且不报错：%+v", m.Sources)
	}
	if len(m.Available) == 0 {
		t.Fatal("内置官方源的货架是空的")
	}
	if len(m.Installed) != 0 {
		t.Fatalf("还没装就有已装记录：%+v", m.Installed)
	}
	if m.Dir == "" {
		t.Fatal("市场接口应当回报插件目录位置")
	}
	first := m.Available[0]

	// 装一个内置插件：技能要真的落进技能目录
	var inst pluginActResp
	if code := e.do("POST", "/api/agent/plugins", map[string]any{"action": "install", "id": first.ID}, true, &inst); code != 200 {
		t.Fatalf("装内置插件失败：HTTP %d %s", code, inst.Error)
	}
	if inst.Plugin.ID != first.ID || len(inst.Plugin.Skills) == 0 {
		t.Fatalf("内置插件装出来不对：%+v", inst.Plugin)
	}
	if _, err := os.Stat(filepath.Join(svc.DataDir(), "skills", inst.Plugin.Skills[0], "SKILL.md")); err != nil {
		t.Fatalf("内置插件的技能没落盘：%v", err)
	}
	// 内置源随后端分发，不许手动往配置里加（加了会出现重复的源）
	if code := e.do("POST", "/api/agent/plugins", map[string]any{
		"action": "addSource", "name": "重加内置", "url": "builtin:official",
	}, true, nil); code != 400 {
		t.Fatalf("手动添加内置源应当 400，实际 %d", code)
	}

	// 直接用本地 zip 路径装
	zipPath := filepath.Join(svc.DataDir(), "direct.zip")
	if err := writeFile(zipPath, string(pluginPkg(t, "direct", "direct-helper"))); err != nil {
		t.Fatal(err)
	}
	if code := e.do("POST", "/api/agent/plugins", map[string]any{"action": "install", "url": zipPath}, true, &inst); code != 200 {
		t.Fatalf("本地包装失败：HTTP %d %s", code, inst.Error)
	}
	if inst.Plugin.ID != "direct" {
		t.Fatalf("装错插件：%+v", inst.Plugin)
	}
}

func TestPluginsBadRequests(t *testing.T) {
	e, _ := newAgentEnv(t)

	// 什么都不给
	if code := e.do("POST", "/api/agent/plugins", map[string]any{"action": "install"}, true, nil); code != 400 {
		t.Fatalf("空 install 应当 400，实际 %d", code)
	}
	// 未知 action
	if code := e.do("POST", "/api/agent/plugins", map[string]any{"action": "wat"}, true, nil); code != 400 {
		t.Fatalf("未知 action 应当 400，实际 %d", code)
	}
	// 卸没装的
	if code := e.do("POST", "/api/agent/plugins", map[string]any{"action": "uninstall", "id": "nope"}, true, nil); code != 400 {
		t.Fatalf("卸不存在的插件应当 400，实际 %d", code)
	}
	// 加空源
	if code := e.do("POST", "/api/agent/plugins", map[string]any{"action": "addSource", "url": ""}, true, nil); code != 400 {
		t.Fatalf("空源地址应当 400，实际 %d", code)
	}
}
