package backup

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeDataDir 造一个像样的数据目录
func makeDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("config.json", `{"allowShell":false}`)
	write("token", "deadbeef\n")
	write("agent.db", "hub-db-bytes")
	write("memory.db", "memory-db-bytes")
	write("runs.db", "runs-db-bytes")
	write("skills/triage/SKILL.md", "# 分诊\n")
	write("workspace/note.md", "工作笔记\n")
	write("checkpoints/ck-1/meta.json", `{"id":"ck-1"}`)
	write("backend.log", "日志不该被备份\n")
	return dir
}

func TestCreateVerifyList(t *testing.T) {
	dir := makeDataDir(t)
	man, err := Create(dir, "0.2.0", "", Full(), "单测")
	if err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	if man.Format != Format || len(man.Entries) == 0 {
		t.Fatalf("清单不对：%+v", man)
	}
	for _, e := range man.Entries {
		if strings.HasSuffix(e.Path, ".log") {
			t.Fatalf("日志文件不该进备份：%s", e.Path)
		}
		if strings.HasPrefix(e.Path, DirName+"/") {
			t.Fatalf("备份目录不该自包含：%s", e.Path)
		}
	}

	list, err := List(dir)
	if err != nil || len(list) != 1 {
		t.Fatalf("列表不对：%v %+v", err, list)
	}
	if list[0].Err != "" {
		t.Fatalf("读清单不该出错：%s", list[0].Err)
	}
	if list[0].Manifest == nil || list[0].Manifest.TotalSize != man.TotalSize {
		t.Fatalf("列表里的清单不对：%+v", list[0])
	}

	if _, err := Verify(list[0].Path); err != nil {
		t.Fatalf("校验应该通过：%v", err)
	}
}

func TestCreateRejectsEmptySelection(t *testing.T) {
	dir := makeDataDir(t)
	if _, err := Create(dir, "0.2.0", "", Selection{}, ""); err == nil {
		t.Fatal("空选择应该报错")
	}
}

func TestCreateReportsNothingToBackup(t *testing.T) {
	dir := t.TempDir() // 空目录
	_, err := Create(dir, "0.2.0", "", Full(), "")
	if err == nil {
		t.Fatal("没有文件可备份时应明确报错，不能产出一个空包")
	}
	if !strings.Contains(err.Error(), "一个文件都没有") {
		t.Fatalf("错误信息应说明原因：%v", err)
	}
}

func TestVerifyDetectsTamperedContent(t *testing.T) {
	dir := makeDataDir(t)
	out := filepath.Join(dir, DirName, "tampered.zip")
	if _, err := Create(dir, "0.2.0", out, Selection{Config: true}, ""); err != nil {
		t.Fatalf("备份失败：%v", err)
	}

	// 重写归档：config.json 的内容换掉，但清单里的 sha256 保持原样
	rewriteZipEntry(t, out, "config.json", []byte("被篡改的内容"))

	if _, err := Verify(out); err == nil {
		t.Fatal("内容被篡改，校验必须失败")
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	dir := makeDataDir(t)
	man, err := Create(dir, "0.2.0", "", Full(), "")
	if err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	archive := filepath.Join(Dir(dir), "backup-fixed.zip")
	if err := os.Rename(filepath.Join(Dir(dir), firstZip(t, dir)), archive); err != nil {
		t.Fatalf("改名失败：%v", err)
	}
	_ = man

	// 改动现场：改一个文件、删一个文件
	if err := os.WriteFile(filepath.Join(dir, "workspace/note.md"), []byte("被改坏了\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "skills/triage/SKILL.md")); err != nil {
		t.Fatal(err)
	}

	res, err := Restore(dir, archive, "0.2.0", RestoreOptions{})
	if err != nil {
		t.Fatalf("恢复失败：%v", err)
	}
	if res.SafePoint == "" {
		t.Fatal("默认应该先打安全点")
	}
	if _, err := os.Stat(res.SafePoint); err != nil {
		t.Fatalf("安全点文件不存在：%v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "workspace/note.md"))
	if err != nil || string(b) != "工作笔记\n" {
		t.Fatalf("文件没恢复：%q %v", string(b), err)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills/triage/SKILL.md")); err != nil {
		t.Fatalf("被删的文件没恢复：%v", err)
	}
	if !strings.Contains(res.Note, "重启") {
		t.Fatalf("应提示需要重启才完全生效：%q", res.Note)
	}
}

func TestRestoreDryRunDoesNotTouchData(t *testing.T) {
	dir := makeDataDir(t)
	if _, err := Create(dir, "0.2.0", "", Selection{Config: true}, ""); err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	archive := filepath.Join(Dir(dir), firstZip(t, dir))

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"changed":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Restore(dir, archive, "0.2.0", RestoreOptions{DryRun: true})
	if err != nil {
		t.Fatalf("试恢复失败：%v", err)
	}
	if !res.DryRun || len(res.Restored) == 0 {
		t.Fatalf("试恢复结果不对：%+v", res)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if string(b) != `{"changed":true}` {
		t.Fatalf("试恢复不该改动数据：%q", string(b))
	}
}

func TestRestoreOnly(t *testing.T) {
	dir := makeDataDir(t)
	if _, err := Create(dir, "0.2.0", "", Full(), ""); err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	archive := filepath.Join(Dir(dir), firstZip(t, dir))

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("改过"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.db"), []byte("改过"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Restore(dir, archive, "0.2.0", RestoreOptions{Only: []string{"config.json"}})
	if err != nil {
		t.Fatalf("恢复失败：%v", err)
	}
	if len(res.Restored) != 1 || res.Restored[0] != "config.json" {
		t.Fatalf("只应恢复 config.json：%+v", res.Restored)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "config.json")); string(b) != `{"allowShell":false}` {
		t.Fatalf("config.json 没恢复：%q", string(b))
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "memory.db")); string(b) != "改过" {
		t.Fatalf("没选中的 memory.db 不该被覆盖：%q", string(b))
	}
	if len(res.Skipped) == 0 {
		t.Fatal("应报告被跳过的文件")
	}
}

func TestSafeRelRejectsEscape(t *testing.T) {
	bad := []string{"../evil.txt", "/etc/passwd", `C:\Windows\x.txt`, "a/../../b", "..", ""}
	for _, p := range bad {
		if _, err := safeRel(p); err == nil {
			t.Fatalf("%q 应被拒绝", p)
		}
	}
	good := map[string]string{
		"config.json":            "config.json",
		"skills/triage/SKILL.md": filepath.FromSlash("skills/triage/SKILL.md"),
		"workspace/a/./b.md":     filepath.FromSlash("workspace/a/b.md"),
	}
	for in, want := range good {
		got, err := safeRel(in)
		if err != nil {
			t.Fatalf("%q 应被接受：%v", in, err)
		}
		if got != want {
			t.Fatalf("safeRel(%q)=%q，期望 %q", in, got, want)
		}
	}
}

func TestRestoreRejectsZipSlip(t *testing.T) {
	dir := t.TempDir()
	evil := filepath.Join(dir, "evil.zip")
	writeEvilZip(t, evil)
	if _, err := Restore(dir, evil, "0.2.0", RestoreOptions{NoSafePoint: true}); err == nil {
		t.Fatal("路径越界的归档必须拒绝")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); err == nil {
		t.Fatal("越界文件绝不能被写出去")
	}
}

func TestDeleteGuardsDirectory(t *testing.T) {
	dir := makeDataDir(t)
	if _, err := Create(dir, "0.2.0", "", Selection{Config: true}, ""); err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	archive := filepath.Join(Dir(dir), firstZip(t, dir))

	if err := Delete(dir, filepath.Join(dir, "config.json")); err == nil {
		t.Fatal("只允许删备份目录里的 zip")
	}
	if err := Delete(dir, archive); err != nil {
		t.Fatalf("删备份应该成功：%v", err)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("备份文件应该已被删除")
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	dir := makeDataDir(t)
	for _, name := range []string{"backup-1.zip", "backup-2.zip", "backup-3.zip"} {
		if _, err := Create(dir, "0.2.0", filepath.Join(Dir(dir), name), Selection{Config: true}, ""); err != nil {
			t.Fatalf("备份 %s 失败：%v", name, err)
		}
	}
	// 手写的导出包不该被清理
	if _, err := Create(dir, "0.2.0", filepath.Join(Dir(dir), "myname.zip"), Selection{Config: true}, ""); err != nil {
		t.Fatal(err)
	}
	removed, err := Prune(dir, 2)
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if len(removed) != 1 || removed[0] != "backup-1.zip" {
		t.Fatalf("应只删最旧的一份：%+v", removed)
	}
	if _, err := os.Stat(filepath.Join(Dir(dir), "myname.zip")); err != nil {
		t.Fatal("非自动命名的导出包不该被清理")
	}
}

/* ---------- 测试辅助 ---------- */

func firstZip(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(Dir(dir))
	if err != nil {
		t.Fatalf("读备份目录失败：%v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".zip") {
			return e.Name()
		}
	}
	t.Fatal("备份目录里没有 zip")
	return ""
}

// rewriteZipEntry 重写归档里的一个文件内容（保留其余文件与清单）
func rewriteZipEntry(t *testing.T, archivePath, name string, content []byte) {
	t.Helper()
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	type item struct {
		name string
		body []byte
	}
	var items []item
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buf []byte
		buf = make([]byte, f.UncompressedSize64)
		if _, err := readFull(rc, buf); err != nil {
			t.Fatal(err)
		}
		rc.Close()
		if f.Name == name {
			buf = content
		}
		items = append(items, item{name: f.Name, body: buf})
	}
	zr.Close()

	out, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for _, it := range items {
		w, err := zw.Create(it.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(it.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeEvilZip 造一个含 ../ 逃逸路径的归档
func writeEvilZip(t *testing.T, p string) {
	t.Helper()
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	sum := sha256.Sum256([]byte("evil"))
	man := Manifest{
		Format: Format, App: AppName, Version: "0.2.0", CreatedAt: 1,
		Selection: Selection{Config: true},
		Entries:   []Entry{{Path: "../evil.txt", Size: 4, SHA256: hex.EncodeToString(sum[:])}},
	}
	w, _ := zw.Create("../evil.txt")
	_, _ = w.Write([]byte("evil"))
	mw, _ := zw.Create(ManifestName)
	_ = json.NewEncoder(mw).Encode(man)
	_ = zw.Close()
	_ = f.Close()
}

func readFull(rc interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := rc.Read(buf[total:])
		total += n
		if err != nil {
			if total == len(buf) {
				return total, nil
			}
			return total, err
		}
	}
	return total, nil
}
