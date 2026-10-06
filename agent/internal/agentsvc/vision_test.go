package agentsvc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"baize/internal/config"
)

func visJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// 截图提问：提取文字 / 翻译 两条 prompt 要能区分；图片要真带上；kinds=vision 的通道优先。
func TestVisionExtractAndTranslate(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("识别到的文字") })
	s := newService(t, nil, nil)

	if _, err := s.ProviderSave(config.Provider{
		Name: "vis", Protocol: "openai", BaseURL: f.srv.URL + "/v1", Model: "qwen-vl-max",
		Kinds: []string{"vision"},
	}, false); err != nil {
		t.Fatalf("配多模态通道失败：%v", err)
	}

	img := base64.StdEncoding.EncodeToString([]byte("PNGBYTES"))
	res, err := s.Vision(context.Background(), "extract", img, "")
	if err != nil {
		t.Fatalf("提取文字失败：%v", err)
	}
	if res.Text != "识别到的文字" {
		t.Fatalf("结果不对：%+v", res)
	}
	f.mu.Lock()
	body := visJSON(f.bodies[len(f.bodies)-1])
	f.mu.Unlock()
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Fatalf("请求里没带图片：%s", body)
	}
	if !strings.Contains(body, "原样提取") {
		t.Fatalf("提取文字的 prompt 不对：%s", body)
	}

	if _, err := s.Vision(context.Background(), "translate", img, "zh"); err != nil {
		t.Fatalf("翻译(zh)失败：%v", err)
	}
	f.mu.Lock()
	bodyZh := visJSON(f.bodies[len(f.bodies)-1])
	f.mu.Unlock()
	if !strings.Contains(bodyZh, "翻译成中文") {
		t.Fatalf("翻译(zh) prompt 不对：%s", bodyZh)
	}

	if _, err := s.Vision(context.Background(), "translate", img, "en"); err != nil {
		t.Fatalf("翻译(en)失败：%v", err)
	}
	f.mu.Lock()
	bodyEn := visJSON(f.bodies[len(f.bodies)-1])
	f.mu.Unlock()
	if !strings.Contains(bodyEn, "翻译成英文") {
		t.Fatalf("翻译(en) prompt 不对：%s", bodyEn)
	}
}

// 坏输入 / 没配通道：一律明确报错；带 data URI 前缀要能吃
func TestVisionErrors(t *testing.T) {
	s := newService(t, nil, nil)
	img := base64.StdEncoding.EncodeToString([]byte("ok"))
	if _, err := s.Vision(context.Background(), "extract", img, ""); err == nil {
		t.Fatal("没配通道必须报错")
	}

	f := newFakeLLM(t)
	s2 := newService(t, nil, nil)
	if _, err := s2.ProviderSave(config.Provider{
		Name: "p", Protocol: "openai", BaseURL: f.srv.URL + "/v1", Model: "m",
	}, false); err != nil {
		t.Fatalf("配通道失败：%v", err)
	}
	if _, err := s2.Vision(context.Background(), "wat", img, ""); err == nil {
		t.Fatal("坏 action 必须报错")
	}
	if _, err := s2.Vision(context.Background(), "extract", "   ", ""); err == nil {
		t.Fatal("空图片必须报错")
	}
	if _, err := s2.Vision(context.Background(), "extract", "!!!not-base64!!!", ""); err == nil {
		t.Fatal("非 base64 必须报错")
	}
	if _, err := s2.Vision(context.Background(), "extract", "data:image/png;base64,"+img, ""); err != nil {
		t.Fatalf("带 data URI 前缀应能吃：%v", err)
	}
}

// kinds 里显式标了 vision 的通道要优先（哪怕它不是第一条）
func TestVisionPrefersExplicitChannel(t *testing.T) {
	other := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("别的通道") })
	vis := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("多模态通道") })
	s := newService(t, nil, nil)
	// 先加一条普通通道（排在前面），再加标了 vision 的
	if _, err := s.ProviderSave(config.Provider{Name: "plain", Protocol: "openai", BaseURL: other.srv.URL + "/v1", Model: "m"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProviderSave(config.Provider{Name: "vis", Protocol: "openai", BaseURL: vis.srv.URL + "/v1", Model: "vl", Kinds: []string{"vision"}}, false); err != nil {
		t.Fatal(err)
	}
	res, err := s.Vision(context.Background(), "extract", base64.StdEncoding.EncodeToString([]byte("x")), "")
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}
	if res.Text != "多模态通道" {
		t.Fatalf("应优先用标了 vision 的通道，实际用了：%q", res.Text)
	}
}
