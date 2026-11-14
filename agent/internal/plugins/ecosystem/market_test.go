package ecosystem

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestEcosystemIDMatchesExpectedShapes 包名归一成白泽合法 id。
func TestEcosystemIDMatchesExpectedShapes(t *testing.T) {
	cases := map[string]string{
		"dsh-pet":                 "dsh-pet",
		"@A8Chann/dsh-pet-live2d": "a8chann.dsh-pet-live2d",
		"@keleus/deepseek-pet":    "keleus.deepseek-pet",
		"UPPER/Case":              "upper.case",
		"  ":                      "",
	}
	for in, want := range cases {
		if got := EcosystemID(in); got != want {
			t.Errorf("EcosystemID(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestGeneratedIndexMatchesPublicSnapshot 真实公网快照 -> 生成索引逐条可追溯。
func TestGeneratedIndexMatchesPublicSnapshot(t *testing.T) {
	sb, err := os.ReadFile("dsh-index.snapshot.json")
	if err != nil {
		t.Fatalf("读不到公网快照凭证：%v", err)
	}
	gb, err := os.ReadFile("dsh-market.index.json")
	if err != nil {
		t.Fatalf("读不到生成索引：%v", err)
	}
	var snap, gen map[string]any
	if err := json.Unmarshal(sb, &snap); err != nil {
		t.Fatalf("快照不是合法 JSON：%v", err)
	}
	if err := json.Unmarshal(gb, &gen); err != nil {
		t.Fatalf("生成索引不是合法 JSON：%v", err)
	}
	if snap["format"] != float64(1) {
		t.Fatalf("快照 format 应为 1，实际 %v", snap["format"])
	}
	if gen["schema"] != float64(1) {
		t.Fatalf("生成索引 schema 应为 1，实际 %v", gen["schema"])
	}
	snapPlugins, _ := snap["plugins"].([]any)
	genPlugins, _ := gen["plugins"].([]any)
	if len(snapPlugins) != 11141 || len(genPlugins) != 11141 {
		t.Fatalf("条数不对：快照 %d / 生成 %d", len(snapPlugins), len(genPlugins))
	}

	idRe := regexp.MustCompile("^[a-z0-9][a-z0-9._-]*$")
	byID := map[string]map[string]any{}
	for _, p := range genPlugins {
		m, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("生成索引里有不是对象的条目：%T", p)
		}
		id, _ := m["id"].(string)
		if !idRe.MatchString(id) || len(id) > 64 {
			t.Fatalf("非法 id：%q", id)
		}
		if _, dup := byID[id]; dup {
			t.Fatalf("重复 id：%q", id)
		}
		byID[id] = m
		if want := DshMarketPkgPrefix + id + ".zip"; m["url"] != want {
			t.Fatalf("%s 的 url 不对：%v", id, m["url"])
		}
		if m["channel"] != "community" || m["stage"] != "stable" {
			t.Fatalf("%s 的 channel/stage 不对：%v/%v", id, m["channel"], m["stage"])
		}
	}

	pet := 0
	for _, p := range snapPlugins {
		m, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("快照里有不是对象的条目：%T", p)
		}
		name, _ := m["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		if strings.Contains(strings.ToLower(name), "pet") {
			pet++
		}
		if _, ok := byID[EcosystemID(name)]; !ok {
			t.Fatalf("快照里的 %q 没进生成索引（id=%q）", name, EcosystemID(name))
		}
	}
	if pet < 100 {
		t.Fatalf("PET 桌宠样本太少：%d", pet)
	}
	m := byID["dsh-pet"]
	if m == nil {
		t.Fatal("dsh-pet 不在生成索引里")
	}
	if m["version"] != "0.4.8" || m["homepage"] != "https://github.com/PC2005-cloud/dsh-pet" || m["category"] != "interface" {
		t.Fatalf("dsh-pet 元数据不对：%+v", m)
	}
}
