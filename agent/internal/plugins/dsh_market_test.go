package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestDshMarketEmbeddedIndexIsValid 内嵌的 11141 条索引是合法白泽格式，不伪造可装包。
func TestDshMarketEmbeddedIndexIsValid(t *testing.T) {
	data := DshMarketIndexBytes()
	if len(data) == 0 {
		t.Fatal("内嵌的 DSH 市场索引是空的")
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatalf("内嵌索引不是合法 JSON：%v", err)
	}
	if idx.Schema != SchemaVersion {
		t.Fatalf("索引 schema 应为 %d，实际 %d", SchemaVersion, idx.Schema)
	}
	if idx.Name != DshMarketSourceName {
		t.Errorf("索引名不对：%q", idx.Name)
	}
	if len(idx.Plugins) != 11141 {
		t.Fatalf("内嵌索引应有 11141 条，实际 %d", len(idx.Plugins))
	}
	if got := len(normalizeIndex(idx).Plugins); got != len(idx.Plugins) {
		t.Fatalf("归一后不该丢条目：%d -> %d", len(idx.Plugins), got)
	}
	seen := map[string]bool{}
	pet := 0
	for _, m := range idx.Plugins {
		if seen[m.ID] {
			t.Fatalf("重复 id：%s", m.ID)
		}
		seen[m.ID] = true
		if strings.Contains(m.ID, "pet") {
			pet++
		}
		if want := dshMarketPkgPrefix + m.ID + ".zip"; m.URL != want {
			t.Fatalf("%s 的 url 不对：%q，期望 %q", m.ID, m.URL, want)
		}
		if m.ID == "dsh-pet" {
			if m.Version != "0.4.8" || m.Homepage != "https://github.com/PC2005-cloud/dsh-pet" || m.Category != "interface" {
				t.Fatalf("dsh-pet 元数据不对：%+v", m)
			}
		}
	}
	if !seen["dsh-pet"] {
		t.Fatal("dsh-pet 桌宠样本没进内嵌索引")
	}
	if pet < 100 {
		t.Fatalf("PET 类样本太少：%d（真实公网索引里有 143 个）", pet)
	}
}

// TestReadDshMarketNeverFakesPackages 索引地址给字节，包地址明确报错，绝不返回假 zip。
func TestReadDshMarketNeverFakesPackages(t *testing.T) {
	b, at, err := readDshMarket(DshMarketSourceURL)
	if err != nil || len(b) == 0 || at != DshMarketSourceURL {
		t.Fatalf("读内置索引不对：bytes=%d at=%q err=%v", len(b), at, err)
	}
	_, _, err = readDshMarket(dshMarketPkgPrefix + "dsh-pet.zip")
	if err == nil || !strings.Contains(err.Error(), DshNotInstallableMsg) {
		t.Fatalf("包地址必须明确拒绝，实际：%v", err)
	}
	_, _, err = readDshMarket("builtin:dsh-market-whatever")
	if err == nil || !strings.Contains(err.Error(), "不认识") {
		t.Fatalf("陌生内置地址必须明确报错，实际：%v", err)
	}
	if !isDshMarketRef(DshMarketSourceURL) || !isDshMarketRef(dshMarketPkgPrefix+"x.zip") {
		t.Fatal("isDshMarketRef 没认出索引地址/包地址")
	}
	if isDshMarketRef(BuiltinSourceURL) {
		t.Fatal("官方内置源不该被当成 DSH 市场")
	}
}

// TestDshMarketCatalogAndInstallRefusal 市场能逛到 11141 条，点安装明确报错。
func TestDshMarketCatalogAndInstallRefusal(t *testing.T) {
	m := New(t.TempDir(), Hooks{SkillsDir: func() string { return t.TempDir() }})
	cat := m.Catalog(context.Background(), []Source{{Name: DshMarketSourceName, URL: DshMarketSourceURL, Enabled: true}})
	if len(cat.Sources) != 1 {
		t.Fatalf("应有 1 个源视图，实际 %d", len(cat.Sources))
	}
	sv := cat.Sources[0]
	if sv.Error != "" {
		t.Fatalf("内置源拉索引不该报错：%s", sv.Error)
	}
	if !sv.Builtin || sv.Count != 11141 {
		t.Fatalf("内置源状态不对：builtin=%v count=%d", sv.Builtin, sv.Count)
	}
	found := false
	for _, a := range cat.Available {
		if a.ID == "dsh-pet" {
			found = true
			if a.URL != dshMarketPkgPrefix+"dsh-pet.zip" || !a.Builtin {
				t.Fatalf("dsh-pet 货架项不对：%+v", a)
			}
		}
	}
	if !found {
		t.Fatal("货架上找不到 dsh-pet")
	}
	if _, err := m.Install(context.Background(), InstallRequest{URL: dshMarketPkgPrefix + "dsh-pet.zip"}); err == nil || !strings.Contains(err.Error(), DshNotInstallableMsg) {
		t.Fatalf("安装 DSH 生态包必须明确拒绝，实际：%v", err)
	}
}
