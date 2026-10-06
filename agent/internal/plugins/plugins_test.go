package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"baize/internal/mcp"
)

/* ---------- 测试脚手架 ---------- */

// makeZip 在内存里造一个插件包
func makeZip(t *testing.T, files map[string]string) []byte {
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
			t.Fatalf("建 zip 条目 %s 失败：%v", n, err)
		}
		if _, err := w.Write([]byte(files[n])); err != nil {
			t.Fatalf("写 zip 条目 %s 失败：%v", n, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("收尾 zip 失败：%v", err)
	}
	return buf.Bytes()
}

// env 装了假 hooks 的插件管理（MCP 列表放内存，技能落临时目录）
type env struct {
	m        *Manager
	dir      string
	skills   string
	mcpCfgs  []mcp.ServerConfig
	reloaded int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{dir: dir, skills: filepath.Join(dir, "skills")}
	e.m = New(filepath.Join(dir, "plugins"), Hooks{
		SkillsDir:    func() string { return e.skills },
		ReloadSkills: func() error { e.reloaded++; return nil },
		MCP:          func() []mcp.ServerConfig { return e.mcpCfgs },
		SaveMCP:      func(c []mcp.ServerConfig) error { e.mcpCfgs = c; return nil },
		AllowRemote:  func() bool { return true },
	})
	return e
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pkgFiles 造一个标准插件包的内容（一个技能 + 一个 MCP 服务）
func pkgFiles(id, skill string) map[string]string {
	man := map[string]any{
		"schema": 1, "id": id, "name": "演示插件 " + id, "version": "1.0.0",
		"description": "测试用",
		"mcp":         []map[string]any{{"name": id + "-mcp", "transport": "http", "url": "http://127.0.0.1:19999/mcp"}},
	}
	raw, _ := json.Marshal(man)
	return map[string]string{
		"plugin.json":                   string(raw),
		"skills/" + skill + "/SKILL.md": "---\nname: " + skill + "\ndescription: 测试技能。\n---\n\n正文",
	}
}

func sortedList(t *testing.T, e *env) []Installed {
	t.Helper()
	list, err := e.m.List()
	if err != nil {
		t.Fatalf("读安装记录失败：%v", err)
	}
	return list
}

/* ---------- 基础工具 ---------- */

func TestNormalizeIndexDropsBadEntries(t *testing.T) {
	idx := normalizeIndex(Index{Plugins: []Meta{
		{ID: "ok", URL: "a.zip"},
		{ID: "no-url"},               // 没 url → 丢
		{ID: "Bad ID", URL: "x.zip"}, // id 非法 → 丢
		{ID: "ok", URL: "dup.zip"},   // 重复 id → 丢
		{ID: "second", URL: "b.zip"},
	}})
	if len(idx.Plugins) != 2 {
		t.Fatalf("期望留下 2 条，实际 %d：%+v", len(idx.Plugins), idx.Plugins)
	}
	if idx.Plugins[1].ID != "second" || idx.Plugins[1].Name != "second" {
		t.Fatalf("名字没兜底成 id：%+v", idx.Plugins[1])
	}
}

func TestResolveRef(t *testing.T) {
	cases := []struct{ base, ref, want string }{
		{"https://example.com/market/index.json", "pkgs/a.zip", "https://example.com/market/pkgs/a.zip"},
		{"https://example.com/market/index.json", "https://other/x.zip", "https://other/x.zip"},
		{filepath.Join("D:", "m", "index.json"), "pkgs/a.zip", filepath.Join("D:", "m", "pkgs", "a.zip")},
	}
	for _, c := range cases {
		if got := resolveRef(c.base, c.ref); got != c.want {
			t.Errorf("resolveRef(%q,%q)=%q，期望 %q", c.base, c.ref, got, c.want)
		}
	}
}

func TestExtractZipBlocksZipSlip(t *testing.T) {
	data := makeZip(t, map[string]string{"../evil.txt": "x"})
	dest := filepath.Join(t.TempDir(), "out")
	if err := extractZip(data, dest); err == nil {
		t.Fatal("带 .. 的条目竟然解包成功了（zip slip 没挡住）")
	}
}

/* ---------- 安装 ---------- */

func TestInstallFromLocalZip(t *testing.T) {
	e := newEnv(t)
	zipPath := filepath.Join(e.dir, "demo.zip")
	if err := os.WriteFile(zipPath, makeZip(t, pkgFiles("demo", "demo-helper")), 0o644); err != nil {
		t.Fatal(err)
	}

	it, err := e.m.Install(context.Background(), InstallRequest{URL: zipPath})
	if err != nil {
		t.Fatalf("安装失败：%v", err)
	}
	if it.ID != "demo" || it.Version != "1.0.0" || !it.Enabled {
		t.Fatalf("返回的记录不对：%+v", it)
	}
	if !fileExists(filepath.Join(e.skills, "demo-helper", "SKILL.md")) {
		t.Fatal("技能没落到技能目录")
	}
	if len(it.Skills) != 1 || it.Skills[0] != "demo-helper" {
		t.Fatalf("记录的技能清单不对：%+v", it.Skills)
	}
	if len(e.mcpCfgs) != 1 || e.mcpCfgs[0].Name != "demo-mcp" {
		t.Fatalf("MCP 服务没注册上：%+v", e.mcpCfgs)
	}
	// 本地路径装的：不该有"没校验"的唠叨
	if it.Note != "" {
		t.Fatalf("本地包装完不该有说明，实际：%q", it.Note)
	}
	if e.reloaded == 0 {
		t.Fatal("装完没重扫技能库")
	}
	if list := sortedList(t, e); len(list) != 1 || list[0].ID != "demo" {
		t.Fatalf("安装记录不对：%+v", list)
	}
}

func TestInstallSkipsExistingSkillAndMCP(t *testing.T) {
	e := newEnv(t)
	// 用户已经有一个同名技能 + 同名 MCP 服务
	writeFile(t, filepath.Join(e.skills, "demo-helper", "SKILL.md"), "用户自己写的")
	e.mcpCfgs = []mcp.ServerConfig{{Name: "demo-mcp", Transport: "http", URL: "http://127.0.0.1:1/mcp", Enabled: true}}

	zipPath := filepath.Join(e.dir, "demo.zip")
	if err := os.WriteFile(zipPath, makeZip(t, pkgFiles("demo", "demo-helper")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Install(context.Background(), InstallRequest{URL: zipPath}); err == nil {
		t.Fatal("技能与 MCP 都被同名跳过时应当报错（什么都没装上）")
	}
	// 用户那份必须原样保留
	raw, _ := os.ReadFile(filepath.Join(e.skills, "demo-helper", "SKILL.md"))
	if string(raw) != "用户自己写的" {
		t.Fatalf("用户的技能被覆盖了：%q", string(raw))
	}
	if len(e.mcpCfgs) != 1 || e.mcpCfgs[0].URL != "http://127.0.0.1:1/mcp" {
		t.Fatalf("用户的 MCP 配置被改了：%+v", e.mcpCfgs)
	}
}

func TestInstallRejectsSHA256Mismatch(t *testing.T) {
	e := newEnv(t)
	zipPath := filepath.Join(e.dir, "demo.zip")
	os.WriteFile(zipPath, makeZip(t, pkgFiles("demo", "demo-helper")), 0o644)

	srcDir := filepath.Join(e.dir, "src")
	idx := map[string]any{"schema": 1, "plugins": []map[string]any{{
		"id": "demo", "name": "演示", "version": "1.0.0",
		"url": "demo.zip", "sha256": "deadbeef",
	}}}
	raw, _ := json.Marshal(idx)
	writeFile(t, filepath.Join(srcDir, "index.json"), string(raw))
	// 把包也放到源目录下（相对 url 解析用）
	if err := os.Rename(zipPath, filepath.Join(srcDir, "demo.zip")); err != nil {
		t.Fatal(err)
	}

	_, err := e.m.Install(context.Background(), InstallRequest{
		ID: "demo", Sources: []Source{{Name: "本地源", URL: filepath.Join(srcDir, "index.json"), Enabled: true}},
	})
	if err == nil {
		t.Fatal("sha256 对不上竟然装成功了")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("sha256")) {
		t.Fatalf("报错里应当点明 sha256：%v", err)
	}
	if dirExists(filepath.Join(e.skills, "demo-helper")) {
		t.Fatal("校验失败却把技能落盘了")
	}
}

func TestInstallByIDFromLocalSource(t *testing.T) {
	e := newEnv(t)
	srcDir := filepath.Join(e.dir, "src")
	pkg := makeZip(t, pkgFiles("weather", "weather-helper"))
	writeFile(t, filepath.Join(srcDir, "weather.zip"), string(pkg))
	idx := map[string]any{"schema": 1, "name": "测试源", "plugins": []map[string]any{{
		"id": "weather", "name": "天气助手", "version": "1.0.0",
		"url": "weather.zip", "sha256": sha256Hex(pkg), "size": len(pkg),
	}}}
	raw, _ := json.Marshal(idx)
	writeFile(t, filepath.Join(srcDir, "index.json"), string(raw))

	srcs := []Source{{Name: "本地源", URL: filepath.Join(srcDir, "index.json"), Enabled: true}}
	it, err := e.m.Install(context.Background(), InstallRequest{ID: "weather", Sources: srcs})
	if err != nil {
		t.Fatalf("按 id 安装失败：%v", err)
	}
	// 包里的 plugin.json 是"装了什么"的权威，源里的只是货架信息 → 名字取包里的
	if it.Name != "演示插件 weather" || it.Source != "本地源" || it.Version != "1.0.0" {
		t.Fatalf("安装记录不对：%+v", it)
	}

	// 市场：能列出这一格，且标成已装、无更新
	cat := e.m.Catalog(context.Background(), srcs)
	if len(cat.Available) != 1 || !cat.Available[0].Installed || cat.Available[0].HasUpdate {
		t.Fatalf("市场货架不对：%+v", cat.Available)
	}
	if cat.Sources[0].Error != "" || cat.Sources[0].Count != 1 {
		t.Fatalf("源状态不对：%+v", cat.Sources[0])
	}
}

func TestManifestFallsBackToIndexMeta(t *testing.T) {
	e := newEnv(t)
	// 包里的清单只写了 id：名字/版本应当从源里兜底
	raw, _ := json.Marshal(map[string]any{"schema": 1, "id": "plain"})
	pkg := makeZip(t, map[string]string{
		"plugin.json":                  string(raw),
		"skills/plain-helper/SKILL.md": "---\nname: plain-helper\ndescription: 测试技能。\n---\n正文",
	})
	srcDir := filepath.Join(e.dir, "src")
	writeFile(t, filepath.Join(srcDir, "plain.zip"), string(pkg))
	idx := map[string]any{"schema": 1, "plugins": []map[string]any{{
		"id": "plain", "name": "朴素插件", "version": "3.1.0", "description": "来自源里的说明",
		"url": "plain.zip",
	}}}
	rawIdx, _ := json.Marshal(idx)
	writeFile(t, filepath.Join(srcDir, "index.json"), string(rawIdx))

	it, err := e.m.Install(context.Background(), InstallRequest{
		ID: "plain", Sources: []Source{{Name: "本地源", URL: filepath.Join(srcDir, "index.json"), Enabled: true}},
	})
	if err != nil {
		t.Fatalf("安装失败：%v", err)
	}
	if it.Name != "朴素插件" || it.Version != "3.1.0" {
		t.Fatalf("源里的元数据没兜底进来：%+v", it)
	}
}

// TestInstallOverHTTP 走真实 HTTP：源与包都从 httptest 服务上拉。
// 顺带验证"源没给 sha256 就如实说没校验"。
func TestInstallOverHTTP(t *testing.T) {
	e := newEnv(t)
	pkg := makeZip(t, pkgFiles("remote", "remote-helper"))

	mux := http.NewServeMux()
	mux.HandleFunc("/market/index.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema":1,"name":"远程源","plugins":[{"id":"remote","name":"远程插件","version":"1.0.0","url":"remote.zip"}]}`))
	})
	mux.HandleFunc("/market/remote.zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(pkg)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	srcs := []Source{{Name: "远程源", URL: srv.URL + "/market/index.json", Enabled: true}}
	it, err := e.m.Install(context.Background(), InstallRequest{ID: "remote", Sources: srcs})
	if err != nil {
		t.Fatalf("走 HTTP 安装失败：%v", err)
	}
	if !strings.Contains(it.Note, "sha256") {
		t.Fatalf("远端源没给 sha256 时应当如实说明，实际说明：%q", it.Note)
	}
	if !fileExists(filepath.Join(e.skills, "remote-helper", "SKILL.md")) {
		t.Fatal("技能没落地")
	}
	if len(e.mcpCfgs) != 1 {
		t.Fatalf("MCP 没注册：%+v", e.mcpCfgs)
	}
}

func TestCatalogReportsSourceError(t *testing.T) {
	e := newEnv(t)
	cat := e.m.Catalog(context.Background(), []Source{
		{Name: "坏的", URL: filepath.Join(e.dir, "nope", "index.json"), Enabled: true},
		{Name: "停的", URL: "http://x/index.json", Enabled: false},
	})
	if cat.Sources[0].Error == "" {
		t.Fatal("拉不到的源应当如实报错")
	}
	if cat.Sources[1].Error == "" {
		t.Fatal("停用的源应当说明不拉取")
	}
}

/* ---------- 卸载 / 启停 ---------- */

func TestUninstallArchivesAndRemovesMCP(t *testing.T) {
	e := newEnv(t)
	zipPath := filepath.Join(e.dir, "demo.zip")
	os.WriteFile(zipPath, makeZip(t, pkgFiles("demo", "demo-helper")), 0o644)
	if _, err := e.m.Install(context.Background(), InstallRequest{URL: zipPath}); err != nil {
		t.Fatal(err)
	}

	if _, err := e.m.Uninstall("demo"); err != nil {
		t.Fatalf("卸载失败：%v", err)
	}
	if dirExists(filepath.Join(e.skills, "demo-helper")) {
		t.Fatal("卸载后技能还在技能目录里")
	}
	if len(e.mcpCfgs) != 0 {
		t.Fatalf("卸载后 MCP 服务没撤掉：%+v", e.mcpCfgs)
	}
	if len(sortedList(t, e)) != 0 {
		t.Fatal("卸载后记录还在")
	}
	// 归档存在
	removed := filepath.Join(e.m.dir, "_removed")
	entries, err := os.ReadDir(removed)
	if err != nil || len(entries) == 0 {
		t.Fatalf("没有归档：err=%v entries=%d", err, len(entries))
	}
	box := filepath.Join(removed, entries[0].Name())
	if !fileExists(filepath.Join(box, "skills", "demo-helper", "SKILL.md")) {
		t.Fatal("归档里没有技能")
	}
	if !fileExists(filepath.Join(box, "pkg", "plugin.json")) {
		t.Fatal("归档里没有原始包")
	}
}

func TestDisableThenEnableMovesSkills(t *testing.T) {
	e := newEnv(t)
	zipPath := filepath.Join(e.dir, "demo.zip")
	os.WriteFile(zipPath, makeZip(t, pkgFiles("demo", "demo-helper")), 0o644)
	if _, err := e.m.Install(context.Background(), InstallRequest{URL: zipPath}); err != nil {
		t.Fatal(err)
	}

	if err := e.m.SetEnabled("demo", false); err != nil {
		t.Fatalf("停用失败：%v", err)
	}
	if dirExists(filepath.Join(e.skills, "demo-helper")) {
		t.Fatal("停用后技能还挂在技能目录里")
	}
	if !dirExists(filepath.Join(e.m.storeDir("demo"), ".off", "demo-helper")) {
		t.Fatal("停用的技能副本没挪进 .off")
	}
	if len(e.mcpCfgs) != 1 || e.mcpCfgs[0].Enabled {
		t.Fatalf("停用后 MCP 应当被置为禁用：%+v", e.mcpCfgs)
	}

	if err := e.m.SetEnabled("demo", true); err != nil {
		t.Fatalf("启用失败：%v", err)
	}
	if !fileExists(filepath.Join(e.skills, "demo-helper", "SKILL.md")) {
		t.Fatal("启用后技能没回来")
	}
	if len(e.mcpCfgs) != 1 || !e.mcpCfgs[0].Enabled {
		t.Fatalf("启用后 MCP 应当恢复：%+v", e.mcpCfgs)
	}
}

func TestUninstallUnknownPlugin(t *testing.T) {
	e := newEnv(t)
	if _, err := e.m.Uninstall("nope"); err != ErrNotInstalled {
		t.Fatalf("卸载没装过的插件应当报 ErrNotInstalled，实际：%v", err)
	}
	if err := e.m.SetEnabled("nope", false); err != ErrNotInstalled {
		t.Fatalf("启停没装过的插件应当报 ErrNotInstalled，实际：%v", err)
	}
}

func TestInstallRejectsManifestMismatch(t *testing.T) {
	e := newEnv(t)
	zipPath := filepath.Join(e.dir, "demo.zip")
	os.WriteFile(zipPath, makeZip(t, pkgFiles("demo", "demo-helper")), 0o644)

	srcDir := filepath.Join(e.dir, "src")
	idx := map[string]any{"schema": 1, "plugins": []map[string]any{{
		"id": "other", "name": "别的", "version": "1.0.0", "url": "demo.zip",
	}}}
	raw, _ := json.Marshal(idx)
	writeFile(t, filepath.Join(srcDir, "index.json"), string(raw))
	os.Rename(zipPath, filepath.Join(srcDir, "demo.zip"))

	_, err := e.m.Install(context.Background(), InstallRequest{
		ID: "other", Sources: []Source{{Name: "本地源", URL: filepath.Join(srcDir, "index.json"), Enabled: true}},
	})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("不一致")) {
		t.Fatalf("货不对板应当被拒绝，实际：%v", err)
	}
}

func TestInstallWithoutPluginJSON(t *testing.T) {
	e := newEnv(t)
	zipPath := filepath.Join(e.dir, "bad.zip")
	os.WriteFile(zipPath, makeZip(t, map[string]string{"skills/x/SKILL.md": "---\nname: x\ndescription: y.\n---\nz"}), 0o644)
	if _, err := e.m.Install(context.Background(), InstallRequest{URL: zipPath}); err == nil {
		t.Fatal("没有 plugin.json 的包应当被拒绝")
	}
}

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.2.0", "1.10.0", -1},
		{"2.0", "1.9.9", 1},
		{"1.0.1", "1.0", 1},
	}
	for _, c := range cases {
		if got := compareVersion(c.a, c.b); got != c.want {
			t.Errorf("compareVersion(%q,%q)=%d 期望 %d", c.a, c.b, got, c.want)
		}
	}
}
