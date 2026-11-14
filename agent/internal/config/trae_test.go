package config

import (
	"strings"
	"testing"
)

// TestTraeAdapterDefaultsClosed Trae 可选适配器默认关闭、地址为空。
func TestTraeAdapterDefaultsClosed(t *testing.T) {
	cfg := Default()
	if cfg.TraeAdapterEnabled {
		t.Fatal("Trae 适配器默认必须关闭")
	}
	if cfg.TraeAdapterIndexURL != "" {
		t.Fatalf("Trae 索引地址默认必须为空，实际 %q", cfg.TraeAdapterIndexURL)
	}
}

// TestTraeAdapterSurvivesNormalize 归一不会把用户填的开关/地址清掉。
func TestTraeAdapterSurvivesNormalize(t *testing.T) {
	cfg := Default()
	cfg.TraeAdapterEnabled = true
	cfg.TraeAdapterIndexURL = "  https://example.com/trae/index.json  "
	cfg.normalize()
	if !cfg.TraeAdapterEnabled {
		t.Fatal("normalize 不该把开关关掉")
	}
	if strings.TrimSpace(cfg.TraeAdapterIndexURL) == "" {
		t.Fatalf("normalize 不该把地址清空：%q", cfg.TraeAdapterIndexURL)
	}
}

// TestTraeAdapterSaveLoadRoundTrip 写盘再读回，两个字段原样保留（桌面端开关能真落后端）。
func TestTraeAdapterSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.TraeAdapterEnabled = true
	cfg.TraeAdapterIndexURL = "https://example.com/trae/index.json"
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("保存配置失败：%v", err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("读回配置失败：%v", err)
	}
	if !got.TraeAdapterEnabled || got.TraeAdapterIndexURL != cfg.TraeAdapterIndexURL {
		t.Fatalf("往返后字段丢了：enabled=%v url=%q", got.TraeAdapterEnabled, got.TraeAdapterIndexURL)
	}
}
