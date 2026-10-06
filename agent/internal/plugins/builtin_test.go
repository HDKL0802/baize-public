package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// seededSkillSlugs 随二进制分发的内置技能目录（official 插件里的技能不能与它们撞名，
// 否则插件装上去会因为"同名已存在"被整个跳过）
var seededSkillSlugs = []string{"make-skill", "file-reader", "office-files", "cron", "note-taking"}

func hasStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// frontmatter 取 SKILL.md 的 front-matter 里 name / description（只认顶层 key: value）
func frontmatter(text string) (name, desc string, ok bool) {
	lines := strings.Split(strings.TrimPrefix(text, "\ufeff"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			ok = true
			break
		}
		key, val, found := strings.Cut(lines[i], ":")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			name = strings.Trim(strings.TrimSpace(val), `"'`)
		case "description":
			desc = strings.Trim(strings.TrimSpace(val), `"'`)
		}
	}
	return name, desc, ok
}

// TestOfficialSourceContentIsValid 是官方内容的"体检"：清单字段、技能 front-matter、
// 技能目录名、与内置技能撞名、重复 slug —— 全都在这里挡住。
// 内置内容随二进制发布，出错就是发布事故，所以宁可测试失败也不能悄悄发出去。
func TestOfficialSourceContentIsValid(t *testing.T) {
	idx, err := builtinIndex()
	if err != nil {
		t.Fatalf("内置官方源加载失败：%v", err)
	}
	ids := builtinIDs()
	if len(ids) == 0 {
		t.Fatal("内置官方源里一个插件都没有")
	}
	if len(idx.Plugins) != len(ids) {
		t.Fatalf("索引 %d 条与插件目录 %d 个对不上", len(idx.Plugins), len(ids))
	}

	seenSkill := map[string]string{}
	for _, id := range ids {
		root := "official/" + id

		raw, err := fs.ReadFile(officialFS, root+"/plugin.json")
		if err != nil {
			t.Fatalf("读 %s 的清单失败：%v", id, err)
		}
		var m Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s 的 plugin.json 不是合法 JSON：%v", id, err)
		}
		if m.ID != id {
			t.Errorf("%s：清单里的 id 是 %q，与目录名不一致", id, m.ID)
		}
		if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Version) == "" || strings.TrimSpace(m.Description) == "" {
			t.Errorf("%s：清单的 name / version / description 必须写全", id)
		}
		if strings.TrimSpace(m.License) == "" {
			t.Errorf("%s：清单没写 license（官方插件要把许可写清）", id)
		}
		if m.Schema != SchemaVersion {
			t.Errorf("%s：清单 schema = %d，应为 %d", id, m.Schema, SchemaVersion)
		}

		skDir := root + "/skills"
		entries, err := fs.ReadDir(officialFS, skDir)
		if err != nil || len(entries) == 0 {
			t.Fatalf("%s 里一个技能都没有（插件总得带来点东西）：%v", id, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			slug := e.Name()
			if !ValidID(slug) {
				t.Errorf("%s：技能目录名 %q 不合法（%s）", id, slug, idRule)
				continue
			}
			if prev, dup := seenSkill[slug]; dup {
				t.Errorf("技能 %q 在 %s 与 %s 里重复了", slug, prev, id)
			}
			seenSkill[slug] = id
			if hasStr(seededSkillSlugs, slug) {
				t.Errorf("%s：技能 %q 与内置技能撞名，装上去会被整个跳过", id, slug)
			}

			body, err := fs.ReadFile(officialFS, skDir+"/"+slug+"/SKILL.md")
			if err != nil {
				t.Fatalf("%s/%s 没有 SKILL.md：%v", id, slug, err)
			}
			name, desc, ok := frontmatter(string(body))
			if !ok {
				t.Errorf("%s/%s 的 SKILL.md 没有以 --- 开头的 YAML front-matter", id, slug)
			}
			if strings.TrimSpace(name) == "" {
				t.Errorf("%s/%s 的 front-matter 缺 name", id, slug)
			}
			if strings.TrimSpace(desc) == "" {
				t.Errorf("%s/%s 的 front-matter 缺 description", id, slug)
			}
			if n := len([]rune(desc)); n > 60 {
				t.Errorf("%s/%s 的 description 有 %d 字，超过 60 字预算（技能清单只展示前 60 字，超出会被截断）", id, slug, n)
			}
			if n := len([]rune(string(body))); n < 300 {
				t.Errorf("%s/%s 的正文只有 %d 字，太薄（官方技能要真能照着做）", id, slug, n)
			}
		}
	}
}

// TestBuiltinPackagesAreDeterministic 内置包必须"同内容同字节"：
// 否则索引里的 sha256 每次都不一样，安装时的校验就会自己把自己拦下来。
func TestBuiltinPackagesAreDeterministic(t *testing.T) {
	idx, err := builtinIndex()
	if err != nil {
		t.Fatalf("内置官方源加载失败：%v", err)
	}
	for _, m := range idx.Plugins {
		a, at, err := readBuiltin(m.URL)
		if err != nil {
			t.Fatalf("读内置包 %s 失败：%v", m.URL, err)
		}
		if at != m.URL {
			t.Errorf("%s：解析到的地址是 %q，期望原样返回 %q", m.ID, at, m.URL)
		}
		b, _, err := readBuiltin(m.URL)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("%s：两次打出来的字节不一致（sha256 不稳定）", m.ID)
		}
		if got := sha256Hex(a); got != m.SHA256 {
			t.Fatalf("%s：索引里的 sha256 与实际包不符（安装会被校验拦下）", m.ID)
		}
		if int64(len(a)) != m.Size {
			t.Errorf("%s：索引里 size=%d，实际 %d", m.ID, m.Size, len(a))
		}
		dest := filepath.Join(t.TempDir(), m.ID)
		if err := extractZip(a, dest); err != nil {
			t.Fatalf("%s 解包失败：%v", m.ID, err)
		}
		if !fileExists(filepath.Join(dest, "plugin.json")) {
			t.Fatalf("%s 解出来的包根目录没有 plugin.json", m.ID)
		}
	}
}

// TestBuiltinSourceCatalog 内置源在"逛市场"里的样子
func TestBuiltinSourceCatalog(t *testing.T) {
	e := newEnv(t)
	srcs := []Source{{Name: BuiltinSourceName, URL: BuiltinSourceURL, Enabled: true}}
	cat := e.m.Catalog(context.Background(), srcs)

	if len(cat.Sources) != 1 {
		t.Fatalf("应当只有 1 个源，实际 %d", len(cat.Sources))
	}
	sv := cat.Sources[0]
	if sv.Error != "" {
		t.Fatalf("内置源不该报错：%s", sv.Error)
	}
	if !sv.Builtin {
		t.Fatal("内置源没被标成 builtin")
	}
	if sv.Count != len(builtinIDs()) {
		t.Fatalf("内置源说 %d 个插件，实际 %d 个目录", sv.Count, len(builtinIDs()))
	}
	if len(cat.Available) != len(builtinIDs()) {
		t.Fatalf("货架 %d 格，期望 %d 格", len(cat.Available), len(builtinIDs()))
	}
	for _, a := range cat.Available {
		if !a.Builtin {
			t.Errorf("%s 没标成内置", a.ID)
		}
		if a.Installed {
			t.Errorf("%s 还没装却标成已装", a.ID)
		}
		if a.SHA256 == "" {
			t.Errorf("%s：内置源也应当给 sha256（这样才能走真校验）", a.ID)
		}
		if a.Name == "" || a.Description == "" {
			t.Errorf("%s：货架上信息不全（name/description 都要有）", a.ID)
		}
	}
}

// TestInstallUpgradeFromBuiltin 内置源安装 + 覆盖升级：支持文件要跟着走，校验要走真的
func TestInstallUpgradeFromBuiltin(t *testing.T) {
	e := newEnv(t)
	srcs := []Source{{Name: BuiltinSourceName, URL: BuiltinSourceURL, Enabled: true}}

	it, err := e.m.Install(context.Background(), InstallRequest{ID: "web-research", Sources: srcs})
	if err != nil {
		t.Fatalf("装内置插件失败：%v", err)
	}
	if it.ID != "web-research" || it.Source != BuiltinSourceName {
		t.Fatalf("安装记录不对：%+v", it)
	}
	if len(it.Skills) != 1 || it.Skills[0] != "web-research" {
		t.Fatalf("技能清单不对：%+v", it.Skills)
	}
	if !fileExists(filepath.Join(e.skills, "web-research", "SKILL.md")) {
		t.Fatal("技能的 SKILL.md 没落地")
	}
	if !fileExists(filepath.Join(e.skills, "web-research", "references", "source-note-template.md")) {
		t.Fatal("技能的 references/ 支持文件没跟着落地")
	}
	// 内置源给了 sha256 → 不该出现"没校验"那句说明
	if it.Note != "" {
		t.Fatalf("内置源安装不该有说明（sha256 已校验），实际：%q", it.Note)
	}

	// 再装一遍 = 覆盖升级：旧技能先被清掉再重落，所以会成功并注明"已覆盖"
	up, err := e.m.Install(context.Background(), InstallRequest{ID: "web-research", Sources: srcs})
	if err != nil {
		t.Fatalf("覆盖安装失败：%v", err)
	}
	if !strings.Contains(up.Note, "已覆盖") {
		t.Fatalf("覆盖安装没在说明里写明，实际：%q", up.Note)
	}
	if !fileExists(filepath.Join(e.skills, "web-research", "SKILL.md")) {
		t.Fatal("覆盖后技能不在")
	}
	if list := sortedList(t, e); len(list) != 1 {
		t.Fatalf("覆盖后应当只有一条记录，实际 %d 条", len(list))
	}
}

// TestBuiltinIsNotRemote 内置地址不能被当成本地路径或被当作远端（两处都会出错）
func TestBuiltinIsNotRemote(t *testing.T) {
	if !IsBuiltinSource(BuiltinSourceURL) {
		t.Fatal("BuiltinSourceURL 没被识别成内置源")
	}
	if !IsBuiltinSource(builtinPkgPrefix + "x.zip") {
		t.Fatal("内置包地址没被识别成内置源")
	}
	if IsBuiltinSource("https://example.com/index.json") || IsBuiltinSource("D:\\x\\index.json") {
		t.Fatal("普通地址被误判成内置源")
	}
	if isRemote(BuiltinSourceURL) {
		t.Fatal("内置地址被当成了远端（会去走 HTTP）")
	}
	if _, _, err := readBuiltin(builtinPkgPrefix + "nope.zip"); err == nil {
		t.Fatal("不存在的内置包应当报错")
	}
	if _, _, err := readBuiltin("builtin:whatever"); err == nil {
		t.Fatal("不认识的内置地址应当报错")
	}
}
