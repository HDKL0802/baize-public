package agentsvc

import (
	"context"
	"strings"
	"testing"
)

// 正常翻译：zh / en 两条提示词要能区分，译文与模型名要带回来
func TestTranslateZhAndEn(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("译文") })
	s := newService(t, f, nil)

	res, err := s.TranslateText(context.Background(), "hello world", "zh")
	if err != nil {
		t.Fatalf("翻译(zh)失败：%v", err)
	}
	if res.Text != "译文" || res.Model != "fake-model" {
		t.Fatalf("翻译结果不对：%+v", res)
	}
	f.mu.Lock()
	bodyZh := visJSON(f.bodies[len(f.bodies)-1])
	f.mu.Unlock()
	if !strings.Contains(bodyZh, "翻译成中文") {
		t.Fatalf("翻译(zh) 的 prompt 不对：%s", bodyZh)
	}

	if _, err := s.TranslateText(context.Background(), "你好", "en"); err != nil {
		t.Fatalf("翻译(en)失败：%v", err)
	}
	f.mu.Lock()
	bodyEn := visJSON(f.bodies[len(f.bodies)-1])
	f.mu.Unlock()
	if !strings.Contains(bodyEn, "翻译成英文") {
		t.Fatalf("翻译(en) 的 prompt 不对：%s", bodyEn)
	}
}

// 非法 / 缺省 to 一律按 zh 处理（不报错）
func TestTranslateInvalidToFallsBackToZh(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("译文") })
	s := newService(t, f, nil)

	for _, to := range []string{"fr", ""} {
		if _, err := s.TranslateText(context.Background(), "bonjour", to); err != nil {
			t.Fatalf("to=%q 不该报错：%v", to, err)
		}
		f.mu.Lock()
		body := visJSON(f.bodies[len(f.bodies)-1])
		f.mu.Unlock()
		if !strings.Contains(body, "翻译成中文") {
			t.Fatalf("to=%q 应按 zh 处理：%s", to, body)
		}
	}
}

// 空 text / 超长 text：明确报错，且一个模型请求都不该发
func TestTranslateBadInput(t *testing.T) {
	f := newFakeLLM(t)
	s := newService(t, f, nil)

	if _, err := s.TranslateText(context.Background(), "   ", "zh"); err == nil {
		t.Fatal("空 text 必须报错")
	}
	long := strings.Repeat("字", TranslateMaxChars+1)
	if _, err := s.TranslateText(context.Background(), long, "zh"); err == nil {
		t.Fatal("超长 text 必须报错")
	}
	f.mu.Lock()
	calls := len(f.bodies)
	f.mu.Unlock()
	if calls != 0 {
		t.Fatalf("坏输入不该调用模型，实际调了 %d 次", calls)
	}
}

// 没有可用通道：给明确错误，而不是空结果
func TestTranslateNoProvider(t *testing.T) {
	s := newService(t, nil, nil)
	_, err := s.TranslateText(context.Background(), "hello", "zh")
	if err == nil {
		t.Fatal("没有可用通道必须报错")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Fatal("错误信息不能为空")
	}
}
