package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// OpenAI 兼容视觉：请求里要带 image_url 的 data URI（PNG）
func TestVisionOpenAI(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"vis-1","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"图里的字"}}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`))
	}))
	defer srv.Close()

	resp, err := VisionChat(context.Background(),
		Config{Name: "v", Protocol: "openai", BaseURL: srv.URL + "/v1", Model: "vis-1"},
		VisionRequest{Prompt: "看图说话", ImagePNG: []byte("PNGDATA")})
	if err != nil {
		t.Fatalf("视觉调用失败：%v", err)
	}
	if resp.Text != "图里的字" || resp.Usage.TotalTokens != 13 {
		t.Fatalf("结果不对：%+v", resp)
	}
	raw, _ := json.Marshal(got)
	s := string(raw)
	if !strings.Contains(s, "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte("PNGDATA"))) {
		t.Fatalf("请求里没带图片 data URI：%s", s)
	}
	if !strings.Contains(s, "看图说话") {
		t.Fatalf("请求里没带 prompt：%s", s)
	}
}

// Anthropic 视觉：走 /v1/messages，图片是 base64 image 块
func TestVisionAnthropic(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"claude-x","content":[{"type":"text","text":"译文"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`))
	}))
	defer srv.Close()

	resp, err := VisionChat(context.Background(),
		Config{Name: "a", Protocol: "anthropic", BaseURL: srv.URL, APIKey: "sk-x", Model: "claude-x"},
		VisionRequest{Prompt: "翻译", ImagePNG: []byte("IMG")})
	if err != nil {
		t.Fatalf("视觉调用失败：%v", err)
	}
	if path != "/v1/messages" {
		t.Fatalf("Anthropic 路径不对：%s", path)
	}
	if resp.Text != "译文" || resp.Usage.TotalTokens != 7 {
		t.Fatalf("结果不对：%+v", resp)
	}
	raw, _ := json.Marshal(got)
	s := string(raw)
	if !strings.Contains(s, `"media_type":"image/png"`) || !strings.Contains(s, base64.StdEncoding.EncodeToString([]byte("IMG"))) {
		t.Fatalf("Anthropic 请求里没带图片：%s", s)
	}
}

// 缺省 / 坏输入 / 模型不支持图片：都要明确报错，别假装成功
func TestVisionErrors(t *testing.T) {
	if _, err := VisionChat(context.Background(),
		Config{Name: "x", Protocol: "openai", BaseURL: "http://127.0.0.1:1/v1"},
		VisionRequest{Prompt: "p", ImagePNG: []byte("x")}); err == nil {
		t.Fatal("没配 model 必须报错")
	}
	if _, err := VisionChat(context.Background(),
		Config{Name: "x", Protocol: "openai", BaseURL: "http://127.0.0.1:1/v1", Model: "m"},
		VisionRequest{Prompt: "p"}); err == nil {
		t.Fatal("没图片必须报错")
	}
	if _, err := VisionChat(context.Background(),
		Config{Protocol: "grpc", Model: "m"}, VisionRequest{Prompt: "p", ImagePNG: []byte("x")}); err == nil {
		t.Fatal("坏协议必须报错")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"model does not support image input"}}`))
	}))
	defer srv.Close()
	_, err := VisionChat(context.Background(),
		Config{Name: "x", Protocol: "openai", BaseURL: srv.URL + "/v1", Model: "text-only"},
		VisionRequest{Prompt: "p", ImagePNG: []byte("x")})
	if err == nil || !strings.Contains(err.Error(), "model does not support image input") {
		t.Fatalf("400 要把接口原话带出来：%v", err)
	}
}
