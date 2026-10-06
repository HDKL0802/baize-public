package llm

import (
	"strings"
	"testing"
)

// 预置模板体检：字段齐、协议/地域取值合法、id 不重复、本地项地址确实是本机
func TestPresetsValid(t *testing.T) {
	list := Presets()
	if len(list) == 0 {
		t.Fatal("预置模板不该为空")
	}
	seen := map[string]bool{}
	regions := map[string]bool{"local": true, "cn": true, "intl": true}
	for _, p := range list {
		if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Name) == "" {
			t.Fatalf("模板缺 id/name：%+v", p)
		}
		if seen[p.ID] {
			t.Fatalf("模板 id 重复：%s", p.ID)
		}
		seen[p.ID] = true
		if p.Protocol != "openai" && p.Protocol != "anthropic" {
			t.Fatalf("模板 %s 协议不合法：%s", p.ID, p.Protocol)
		}
		if !regions[p.Region] {
			t.Fatalf("模板 %s 地域不合法：%s", p.ID, p.Region)
		}
		if p.BaseURL != "" && !strings.HasPrefix(p.BaseURL, "http://") && !strings.HasPrefix(p.BaseURL, "https://") {
			t.Fatalf("模板 %s 地址不像 URL：%s", p.ID, p.BaseURL)
		}
		if p.Region == "local" && !isLocalURL(p.BaseURL) {
			t.Fatalf("本地模板 %s 的地址看着不是本机：%s", p.ID, p.BaseURL)
		}
		if p.Region != "local" && p.NeedsKey != true {
			t.Fatalf("云端模板 %s 应标 needsKey：%+v", p.ID, p)
		}
	}
	// Agnes 那条必须"端点留空 + 有说明"（不替它编地址）
	for _, p := range list {
		if p.ID == "agnes" {
			if p.BaseURL != "" {
				t.Fatal("Agnes 端点未经核实，不应预填 baseUrl")
			}
			if !strings.Contains(p.Note, "自行") && !strings.Contains(p.Note, "以 agnes-ai.com") {
				t.Fatalf("Agnes 应给出「端点自行确认」的说明：%s", p.Note)
			}
		}
	}
}
