package agentsvc

import (
	"strings"
	"testing"

	"baize/internal/config"
	"baize/internal/plugins"
)

// traeSourceOf 在源列表里找「Trae 生态适配器」源。
func traeSourceOf(src []plugins.Source) (plugins.Source, bool) {
	for _, s := range src {
		if s.Name == "Trae 生态适配器" {
			return s, true
		}
	}
	return plugins.Source{}, false
}

// TestPluginSourcesBuiltinDefaults 默认一定挂两个内置源（官方 + DSH 生态市场），且无 Trae。
func TestPluginSourcesBuiltinDefaults(t *testing.T) {
	s := &Service{cfg: config.Default()}
	got := s.pluginSources()
	if len(got) != 2 {
		t.Fatalf("默认应只有 2 个内置源，实际 %d：%+v", len(got), got)
	}
	if got[0].URL != plugins.BuiltinSourceURL || got[1].URL != plugins.DshMarketSourceURL {
		t.Fatalf("内置源顺序/地址不对：%+v", got)
	}
	for _, src := range got {
		if !src.Enabled {
			t.Errorf("内置源应默认启用：%+v", src)
		}
	}
	if _, ok := traeSourceOf(got); ok {
		t.Fatal("默认不该挂 Trae 适配器")
	}
}

// TestTraeAdapterEnabledButNoURL 开关打开但地址为空：不追加空地址源（不假装能用）。
func TestTraeAdapterEnabledButNoURL(t *testing.T) {
	cfg := config.Default()
	cfg.TraeAdapterEnabled = true
	cfg.TraeAdapterIndexURL = "   "
	s := &Service{cfg: cfg}
	got := s.pluginSources()
	if len(got) != 2 {
		t.Fatalf("空地址时仍应只有 2 个内置源，实际 %d：%+v", len(got), got)
	}
	if _, ok := traeSourceOf(got); ok {
		t.Fatal("地址为空不该挂 Trae 适配器")
	}
}

// TestTraeAdapterEnabledWithURL 开关打开且地址非空：追加源，地址 TrimSpace 后生效。
func TestTraeAdapterEnabledWithURL(t *testing.T) {
	cfg := config.Default()
	cfg.TraeAdapterEnabled = true
	cfg.TraeAdapterIndexURL = "  https://example.com/trae/index.json  "
	s := &Service{cfg: cfg}
	got := s.pluginSources()
	if len(got) != 3 {
		t.Fatalf("应追加第 3 个源，实际 %d：%+v", len(got), got)
	}
	trae, ok := traeSourceOf(got)
	if !ok {
		t.Fatalf("没找到 Trae 适配器源：%+v", got)
	}
	if trae.URL != "https://example.com/trae/index.json" || !trae.Enabled {
		t.Fatalf("Trae 源地址/启用状态不对：%+v", trae)
	}
	if !strings.HasSuffix(got[2].Name, "Trae 生态适配器") {
		t.Fatalf("Trae 源应挂末尾：%+v", got[2])
	}
}

// TestTraeAdapterDisabledWithURL 开关关闭时即使填了地址也不挂。
func TestTraeAdapterDisabledWithURL(t *testing.T) {
	cfg := config.Default()
	cfg.TraeAdapterEnabled = false
	cfg.TraeAdapterIndexURL = "https://example.com/trae/index.json"
	s := &Service{cfg: cfg}
	got := s.pluginSources()
	if _, ok := traeSourceOf(got); ok {
		t.Fatal("开关关闭时不该挂 Trae 适配器")
	}
}
