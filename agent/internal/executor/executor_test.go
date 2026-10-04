package executor

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"baize/shared/proto"
)

func TestFsDeleteReal(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(proto.ActionFsDelete, map[string]any{"paths": []any{file, sub}}, Options{AllowRoots: []string{dir}})
	if err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if res["ok"] != 2 || res["failed"] != 0 {
		t.Fatalf("结果计数不对：%+v", res)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("文件应已删除，stat 结果：%v", err)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Fatalf("目录应已删除，stat 结果：%v", err)
	}
	if res["dryRun"] != false {
		t.Fatalf("dryRun 应为 false：%+v", res)
	}
}

func TestFsDeleteDryRun(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(file, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(proto.ActionFsDelete, map[string]any{"paths": file}, Options{DryRun: true})
	if err != nil {
		t.Fatalf("干跑不该报错：%v", err)
	}
	if res["dryRun"] != true {
		t.Fatalf("dryRun 应为 true：%+v", res)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("dry-run 不得真的删除文件：%v", err)
	}
}

// 护栏用例一律只调 guardPath 做判定，绝不对真实路径发起删除。
// 起因：早期版本护栏在 Windows 上因分隔符不一致漏判，测试真的递归删过用户主目录。
func assertBlocked(t *testing.T, p string, opt Options, why string) {
	t.Helper()
	abs, err := guardPath(p, opt)
	if err == nil {
		t.Fatalf("护栏应拦下 %s（%s），但放行了：%s", p, why, abs)
	}
	if abs != "" {
		t.Fatalf("被拦下时不应返回路径，实际：%s", abs)
	}
}

func TestGuardBlocksSystemPaths(t *testing.T) {
	sys := "/etc"
	if runtime.GOOS == "windows" {
		sys = `C:\Windows`
	}
	assertBlocked(t, sys, Options{}, "系统目录")
	assertBlocked(t, filepath.Join(sys, "System32"), Options{}, "系统目录内部")
}

func TestGuardBlocksRootAndHome(t *testing.T) {
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	assertBlocked(t, root, Options{}, "磁盘根目录")

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("取不到用户主目录，跳过")
	}
	assertBlocked(t, home, Options{}, "用户主目录")
	assertBlocked(t, filepath.Dir(home), Options{}, "用户主目录的上级")
	assertBlocked(t, home+string(filepath.Separator)+"sub"+string(filepath.Separator)+"..", Options{}, "主目录的变体写法")
	if runtime.GOOS == "windows" {
		assertBlocked(t, strings.ToUpper(home), Options{}, "Windows 大小写变体")
		assertBlocked(t, strings.ReplaceAll(home, `\`, `/`), Options{}, "Windows 正斜杠变体")
	}
}

func TestGuardBlocksWorkingDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Skip("取不到工作目录，跳过")
	}
	assertBlocked(t, wd, Options{}, "当前工作目录")
	assertBlocked(t, filepath.Dir(wd), Options{}, "工作目录的上级")
}

func TestGuardAllowRootsWhitelist(t *testing.T) {
	allowed := t.TempDir()
	inner := filepath.Join(allowed, "a", "b.txt")
	if err := os.MkdirAll(filepath.Dir(inner), 0o755); err != nil {
		t.Fatal(err)
	}
	opt := Options{AllowRoots: []string{allowed}}
	if _, err := guardPath(inner, opt); err != nil {
		t.Fatalf("白名单内的路径应放行：%v", err)
	}
	outside := filepath.Join(t.TempDir(), "other.txt")
	assertBlocked(t, outside, opt, "白名单之外")
}

// fs.delete 未开启危险能力时必须明确拒绝，而不是静默假成功
func TestFsDeleteRefusedWhenDangerDisabled(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(proto.ActionFsDelete, map[string]any{"paths": file}, Options{})
	if !errors.Is(err, ErrDangerDisabled) {
		t.Fatalf("应返回 ErrDangerDisabled，实际：%v", err)
	}
	if _, statErr := os.Stat(file); statErr != nil {
		t.Fatal("被拒绝时不得动文件")
	}
}

// Run 层：护栏拦下的路径要计入 failed，且不产生副作用
func TestFsDeleteBlockedPathCountsAsFailure(t *testing.T) {
	blocked := "/etc"
	if runtime.GOOS == "windows" {
		blocked = `C:\Windows`
	}
	res, err := Run(proto.ActionFsDelete, map[string]any{"paths": blocked}, Options{DryRun: true})
	if err == nil {
		t.Fatalf("系统目录必须被拒绝：%+v", res)
	}
	if res["failed"] != 1 || res["ok"] != 0 {
		t.Fatalf("应记 1 个失败：%+v", res)
	}
	if !strings.Contains(err.Error(), "拒绝删除") {
		t.Fatalf("错误信息应说明拒绝原因，实际：%v", err)
	}
}

func TestFsDeleteMissingPathIsNotError(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "not-here.txt")
	res, err := Run(proto.ActionFsDelete, map[string]any{"paths": missing}, Options{AllowRoots: []string{dir}})
	if err != nil {
		t.Fatalf("目标不存在不该算失败：%v", err)
	}
	if res["missing"] != 1 {
		t.Fatalf("应记 1 个 missing：%+v", res)
	}
}

func TestPathsArgumentValidation(t *testing.T) {
	if _, err := Run(proto.ActionFsDelete, map[string]any{}, Options{DryRun: true}); err == nil {
		t.Fatal("缺少 paths 必须报错")
	}
	if _, err := Run(proto.ActionFsDelete, map[string]any{"paths": []any{}}, Options{DryRun: true}); err == nil {
		t.Fatal("paths 为空必须报错")
	}
	if _, err := Run(proto.ActionFsDelete, map[string]any{"paths": []any{"a", "b"}}, Options{DryRun: true, MaxPaths: 1}); err == nil {
		t.Fatal("超过 MaxPaths 必须报错")
	}
	res, err := Run(proto.ActionFsDelete, map[string]any{"paths": "a.txt,b.txt"}, Options{DryRun: true, MaxPaths: 5})
	if err != nil {
		t.Fatalf("字符串形式的 paths 应被接受：%v", err)
	}
	if res["count"] != 2 {
		t.Fatalf("应解析出 2 个路径：%+v", res)
	}
}

func TestPingAndUnknownAction(t *testing.T) {
	res, err := Run(proto.ActionPing, nil, Options{})
	if err != nil || res["pong"] != true {
		t.Fatalf("ping 应返回 pong：%+v %v", res, err)
	}
	if _, err := Run("sys.exec", map[string]any{"cmd": "whoami"}, Options{}); err == nil {
		t.Fatal("未实现的动作必须明确报错，不能静默假成功")
	}
}

func TestSysInfoAndStat(t *testing.T) {
	res, err := Run(proto.ActionSysInfo, nil, Options{})
	if err != nil {
		t.Fatalf("sys.info 失败：%v", err)
	}
	if res["os"] == "" || res["arch"] == "" || res["hostname"] == "" {
		t.Fatalf("sys.info 缺少关键字段：%+v", res)
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "s.txt")
	if err := os.WriteFile(file, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = Run(proto.ActionFsStat, map[string]any{"paths": []any{file, filepath.Join(dir, "none")}}, Options{})
	if err != nil {
		t.Fatalf("fs.stat 失败：%v", err)
	}
	items, ok := res["items"].([]map[string]any)
	if !ok || len(items) != 2 {
		t.Fatalf("fs.stat 应返回 2 条：%+v", res)
	}
	if items[0]["exists"] != true || items[0]["size"].(int64) != 5 {
		t.Fatalf("第一个文件信息不对：%+v", items[0])
	}
	if items[1]["exists"] != false {
		t.Fatalf("第二个文件应不存在：%+v", items[1])
	}
}
