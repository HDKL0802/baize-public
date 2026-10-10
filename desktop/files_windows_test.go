//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"baize/shared/proto"
)

// setPermForTest 改全局权限配置（测试用；用完记得恢复）
func setPermForTest(t *testing.T, perm int, scopes []string) {
	t.Helper()
	cfgMu.Lock()
	oldP, oldS := cfg.GuiPerm, cfg.GuiScopes
	cfg.GuiPerm, cfg.GuiScopes = perm, cleanScopes(scopes)
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.GuiPerm, cfg.GuiScopes = oldP, oldS
		cfgMu.Unlock()
	})
}

// listPaths：目录在前、按名字排、带大小；不存在的目录如实报错。
func TestListPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "zsub"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := listPaths([]string{dir})
	dirs, ok := res["dirs"].([]map[string]any)
	if !ok || len(dirs) != 1 {
		t.Fatalf("dirs 结构不对：%+v", res)
	}
	d := dirs[0]
	if d["ok"] != true {
		t.Fatalf("应成功：%+v", d)
	}
	entries, _ := d["entries"].([]map[string]any)
	if len(entries) != 3 {
		t.Fatalf("应有 3 个条目：%+v", entries)
	}
	// 目录必须排在文件前面
	if entries[0]["name"] != "zsub" || entries[0]["isDir"] != true {
		t.Fatalf("目录应排最前：%+v", entries[0])
	}
	// 文件按名字：a.txt 在 b.txt 前
	if entries[1]["name"] != "a.txt" || entries[2]["name"] != "b.txt" {
		t.Fatalf("文件应按名字排序：%+v / %+v", entries[1], entries[2])
	}
	if entries[1]["size"] != int64(2) {
		t.Fatalf("a.txt 应为 2 字节：%+v", entries[1])
	}

	// 不存在的目录：ok=false + error，不 panic
	res2 := listPaths([]string{filepath.Join(dir, "nope")})
	dirs2, _ := res2["dirs"].([]map[string]any)
	if len(dirs2) != 1 || dirs2[0]["ok"] != false || dirs2[0]["error"] == nil {
		t.Fatalf("不存在的目录应如实报错：%+v", dirs2)
	}
}

// permGate：fs.list 与 fs.stat 同口径 —— 档位/范围都要过。
func TestPermGateFsList(t *testing.T) {
	inside := t.TempDir()
	outside := t.TempDir()

	// 关闭档：一律拒绝
	setPermForTest(t, PermOff, nil)
	if err := permGate(proto.ActionFsList, []string{inside}); err == nil {
		t.Fatal("关闭档不该允许 fs.list")
	}
	// 只读档：仍拒绝（只允许 sys.info / window.now）
	setPermForTest(t, PermReadOnly, nil)
	if err := permGate(proto.ActionFsList, []string{inside}); err == nil {
		t.Fatal("只读档不该允许 fs.list")
	}
	// 只读指定目录：圈内放行、圈外拒绝
	setPermForTest(t, PermDirScoped, []string{inside})
	if err := permGate(proto.ActionFsList, []string{inside}); err != nil {
		t.Fatalf("圈内应放行：%v", err)
	}
	if err := permGate(proto.ActionFsList, []string{outside}); err == nil {
		t.Fatal("圈外应拒绝")
	}
	// 完全访问：放行
	setPermForTest(t, PermFullAccess, nil)
	if err := permGate(proto.ActionFsList, []string{outside}); err != nil {
		t.Fatalf("完全访问应放行：%v", err)
	}
	// 空路径：报缺参数（不 panic）
	if err := permGate(proto.ActionFsList, nil); err == nil {
		t.Fatal("空 paths 应报错")
	}
}

// MIME / Kind 推断：够用即可，但图片必须归到 image。
func TestFileMimeAndKind(t *testing.T) {
	cases := []struct {
		name, mime, kind string
	}{
		{"a.png", "image/png", "image"},
		{"b.JPG", "image/jpeg", "image"},
		{"c.pdf", "application/pdf", "file"},
		{"d.md", "text/plain", "file"},
		{"e.unknown", "", "file"},
	}
	for _, c := range cases {
		m := fileMimeByExt(c.name)
		if m != c.mime {
			t.Errorf("fileMimeByExt(%q)=%q，期望 %q", c.name, m, c.mime)
		}
		if k := fileKindOf(c.name, m); k != c.kind {
			t.Errorf("fileKindOf(%q)=%q，期望 %q", c.name, k, c.kind)
		}
	}
}

// parentDir：盘根返回空（界面据此回「此电脑」）
func TestParentDir(t *testing.T) {
	if got := parentDir(`D:\`); got != "" {
		t.Fatalf(`parentDir(D:\) 应为空，实得 %q`, got)
	}
	if got := parentDir(`D:\a`); got != `D:\` {
		t.Fatalf("parentDir 计算不对：%q", got)
	}
	if got := parentDir(`D:\a\b`); got != `D:\a` {
		t.Fatalf("parentDir 计算不对：%q", got)
	}
}
