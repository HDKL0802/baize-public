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

	// 按清单必须有的 id 全部到位（国内 14 家 + 外版 + 双版本 + 计费档位分离）
	mustIDs := []string{
		"deepseek",
		"volcark-api", "volcark-coding",
		"zhipu", "zhipu-coding", "zhipu-intl",
		"moonshot", "moonshot-intl",
		"dashscope", "dashscope-intl", "dashscope-coding",
		"mimo",
		"minimax", "minimax-intl",
		"stepfun",
		"baidu-qianfan",
		"hunyuan", "hunyuan-intl",
		"siliconflow",
		"modelscope",
		"ucloud",
		"openrouter",
		"aws",
		"byteplus-modelark",
		"agnes", "agnes-intl",
	}
	byID := map[string]*Preset{}
	for i := range list {
		byID[list[i].ID] = &list[i]
	}
	for _, id := range mustIDs {
		if byID[id] == nil {
			t.Fatalf("预置模板缺 %s", id)
		}
	}

	// Agnes 双条（国内 + 国际），端点 + 默认主力模型名 agnes-3.0-flash
	wantAgnes := map[string]string{
		"agnes":      "https://api.agnes-ai.cn/v1",
		"agnes-intl": "https://apihub.agnes-ai.com/v1",
	}
	for id, base := range wantAgnes {
		p := byID[id]
		if p == nil {
			t.Fatalf("预置模板缺 %s", id)
		}
		if p.BaseURL != base {
			t.Fatalf("%s 端点应为 %s，实际 %s", id, base, p.BaseURL)
		}
		if p.Model != "agnes-3.0-flash" {
			t.Fatalf("%s 默认主力模型应为 agnes-3.0-flash，实际 %q", id, p.Model)
		}
	}

	// 火山方舟两档：API 按量 /api/v3 与 Coding Plan /api/coding/v3 必须分开
	if byID["volcark-api"].BaseURL != "https://ark.cn-beijing.volces.com/api/v3" {
		t.Fatalf("volcark-api 端点应为 /api/v3，实际 %s", byID["volcark-api"].BaseURL)
	}
	if byID["volcark-coding"].BaseURL != "https://ark.cn-beijing.volces.com/api/coding/v3" {
		t.Fatalf("volcark-coding 端点应为 /api/coding/v3，实际 %s", byID["volcark-coding"].BaseURL)
	}

	// 百炼三档：国内按量 / 国际按量 / Coding Plan（coding.dashscope.aliyuncs.com）
	if byID["dashscope-coding"].BaseURL != "https://coding.dashscope.aliyuncs.com/v1" {
		t.Fatalf("dashscope-coding 端点应为 coding.dashscope.aliyuncs.com/v1，实际 %s", byID["dashscope-coding"].BaseURL)
	}

	// 智谱两条按量/套餐端点不同
	if byID["zhipu-coding"].BaseURL != "https://open.bigmodel.cn/api/coding/paas/v4" {
		t.Fatalf("zhipu-coding 端点应为 /api/coding/paas/v4，实际 %s", byID["zhipu-coding"].BaseURL)
	}
}
