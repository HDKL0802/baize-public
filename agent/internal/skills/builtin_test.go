package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedBuiltinWritesSkills(t *testing.T) {
	dir := t.TempDir()

	n, err := SeedBuiltin(dir)
	if err != nil {
		t.Fatalf("落盘内置技能失败：%v", err)
	}
	names := BuiltinNames()
	if n != len(names) || n == 0 {
		t.Fatalf("应写入 %d 个内置技能，实际 %d", len(names), n)
	}
	// 每个技能都要真的有 SKILL.md
	for _, slug := range names {
		p := filepath.Join(dir, slug, "SKILL.md")
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("内置技能 %s 没落盘：%v", slug, err)
		}
	}
	// 落盘后应该能被技能库加载出来（说明格式合法）
	lib, err := Load(dir)
	if err != nil {
		t.Fatalf("加载技能库失败：%v", err)
	}
	if got := len(lib.All()); got < len(names) {
		t.Fatalf("技能库只认到 %d 个技能，少于内置的 %d 个", got, len(names))
	}
	// 每个技能都得有非空 description（系统提示里靠它决定要不要用）
	for _, sk := range lib.All() {
		if strings.TrimSpace(sk.Description) == "" {
			t.Fatalf("技能 %s 没有 description（这样以后不会被想起来）", sk.Name)
		}
	}
}

func TestSeedBuiltinIsIdempotentAndPreservesUserEdits(t *testing.T) {
	dir := t.TempDir()
	if _, err := SeedBuiltin(dir); err != nil {
		t.Fatalf("首次落盘失败：%v", err)
	}

	// 版本没变：再跑一次什么都不做
	n, err := SeedBuiltin(dir)
	if err != nil {
		t.Fatalf("二次落盘失败：%v", err)
	}
	if n != 0 {
		t.Fatalf("版本未变时不该重复落盘，实际写了 %d 个", n)
	}

	// 用户改过内置技能：删掉标记模拟"版本升级"，再落盘必须**不覆盖**用户的版本
	slug := BuiltinNames()[0]
	edited := "---\nname: 用户改过的\n---\n\n我的版本。\n"
	target := filepath.Join(dir, slug, "SKILL.md")
	if err := os.WriteFile(target, []byte(edited), 0o644); err != nil {
		t.Fatalf("改写技能失败：%v", err)
	}
	if err := os.Remove(filepath.Join(dir, builtinMarker)); err != nil {
		t.Fatalf("删标记失败：%v", err)
	}
	if _, err := SeedBuiltin(dir); err != nil {
		t.Fatalf("重播种失败：%v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if string(got) != edited {
		t.Fatalf("用户改过的技能被覆盖了：\n%s", string(got))
	}
}

func TestSeedBuiltinEmptyDir(t *testing.T) {
	if n, err := SeedBuiltin(""); err != nil || n != 0 {
		t.Fatalf("目录为空时应直接返回 0：n=%d err=%v", n, err)
	}
}
