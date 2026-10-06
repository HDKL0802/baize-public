package httpapi_test

import (
	"strings"
	"testing"

	"baize/internal/agentsvc"
	"baize/internal/config"
)

// 实时双语字幕：POST /api/agent/voice/translate 的令牌闸门、成功口径与校验。
func TestVoiceTranslateAPI(t *testing.T) {
	e, svc := newAgentEnv(t)
	openai := fakeOpenAI(t)
	if _, err := svc.ProviderSave(config.Provider{
		Name: "local", Protocol: "openai", BaseURL: openai.URL + "/v1", Model: "fake-openai",
	}, false); err != nil {
		t.Fatalf("配通道失败：%v", err)
	}

	// 必须过令牌闸门
	if code := e.do("POST", "/api/agent/voice/translate",
		map[string]any{"text": "hi", "to": "zh"}, false, nil); code != 401 {
		t.Fatalf("没有令牌应 401，实际 %d", code)
	}

	// 正常翻译：200 {ok:true,text,model,millis}
	var ok struct {
		OK     bool   `json:"ok"`
		Text   string `json:"text"`
		Model  string `json:"model"`
		Millis int64  `json:"millis"`
	}
	if code := e.do("POST", "/api/agent/voice/translate",
		map[string]any{"text": "hello", "to": "zh"}, true, &ok); code != 200 {
		t.Fatalf("翻译失败，HTTP %d", code)
	}
	if !ok.OK || ok.Text != "能通" || ok.Model == "" {
		t.Fatalf("翻译结果不对：%+v", ok)
	}

	// 空 text → 400
	var bad struct {
		Error string `json:"error"`
	}
	if code := e.do("POST", "/api/agent/voice/translate",
		map[string]any{"text": "  ", "to": "zh"}, true, &bad); code != 400 {
		t.Fatalf("空 text 应 400，实际 %d", code)
	}
	if bad.Error != "text 不能为空" {
		t.Fatalf("空 text 的错误信息不对：%q", bad.Error)
	}

	// 超长 text → 400，且错误里写清上限
	long := strings.Repeat("字", agentsvc.TranslateMaxChars+1)
	if code := e.do("POST", "/api/agent/voice/translate",
		map[string]any{"text": long, "to": "zh"}, true, &bad); code != 400 {
		t.Fatalf("超长 text 应 400，实际 %d", code)
	}
	if !strings.Contains(bad.Error, "2000") {
		t.Fatalf("超长的错误信息应写清上限：%q", bad.Error)
	}
}

// 没有可用通道：按 stt 的口径回 200 {ok:false,error}
func TestVoiceTranslateNoProviderReturnsOKFalse(t *testing.T) {
	e, _ := newAgentEnv(t)
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if code := e.do("POST", "/api/agent/voice/translate",
		map[string]any{"text": "hello", "to": "zh"}, true, &out); code != 200 {
		t.Fatalf("没有通道时也应按 stt 口径回 200，实际 %d", code)
	}
	if out.OK || strings.TrimSpace(out.Error) == "" {
		t.Fatalf("应回 ok:false 且带原因：%+v", out)
	}
}
