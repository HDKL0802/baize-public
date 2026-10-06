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
	// Agnes：国内 + 国际两条，端点用用户给的（.cn 国内；apihub.agnes-ai.com 国际）
	wantAgnes := map[string]string{
		"agnes":      "https://api.agnes-ai.cn/v1",
		"agnes-intl": "https://apihub.agnes-ai.com/v1",
	}
	for id, base := range wantAgnes {
		var hit *Preset
		for i := range list {
			if list[i].ID == id {
				hit = &list[i]
			}
		}
		if hit == nil {
			t.Fatalf("预置模板缺 %s", id)
		}
		if hit.BaseURL != base {
			t.Fatalf("%s 端点应为 %s，实际 %s", id, base, hit.BaseURL)
		}
		if !strings.Contains(hit.Note, "文档") && !strings.Contains(hit.Note, "模型名") {
			t.Fatalf("%s 应提示模型名 / 文档：%s", id, hit.Note)
		}
	}
}
