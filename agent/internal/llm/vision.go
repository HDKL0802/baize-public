package llm

// 多模态（看图）能力：截图提问的「提取文字 / 翻译」走这里。
//
// 两条协议各写各的请求体（OpenAI 兼容用 image_url 的 data URI；Anthropic 用 base64 image 块），
// 响应复用各自的解析结构。注意：**必须是多模态模型**（qwen-vl / gpt-4o / claude / gemini 等），
// 纯文本模型会返回 400 —— 那种情况我们如实把原话抛出去，不假装识别成功。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// VisionRequest 一次「看图 + 说话」的请求
type VisionRequest struct {
	Prompt    string
	ImagePNG  []byte // PNG 字节
	MaxTokens int    // 0 = 用通道默认
	Model     string // 覆盖通道默认模型
}

// VisionChat 走通道的多模态（视觉）接口
func VisionChat(ctx context.Context, cfg Config, req VisionRequest) (Response, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Protocol)) {
	case "", "openai":
		return visionOpenAI(ctx, cfg, req)
	case "anthropic", "claude":
		return visionAnthropic(ctx, cfg, req)
	}
	return Response{}, fmt.Errorf("不支持的协议：%s（可选 openai | anthropic）", cfg.Protocol)
}

func visionHTTPClient(cfg Config) *http.Client {
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

func visionOpenAI(ctx context.Context, cfg Config, req VisionRequest) (Response, error) {
	model := firstNonEmpty(req.Model, cfg.Model)
	if strings.TrimSpace(model) == "" {
		return Response{}, fmt.Errorf("通道 %s 没配置模型名（model）", cfg.Name)
	}
	if len(req.ImagePNG) == 0 {
		return Response{}, fmt.Errorf("没有可识别的图片")
	}
	b64 := base64.StdEncoding.EncodeToString(req.ImagePNG)
	body := map[string]any{
		"model":  model,
		"stream": false,
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": req.Prompt},
					map[string]any{"type": "image_url", "image_url": map[string]any{
						"url": "data:image/png;base64," + b64,
					}},
				},
			},
		},
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	url := joinURL(cfg.BaseURL, "/chat/completions")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := visionHTTPClient(cfg).Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("请求 %s 失败：%w", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Response{}, fmt.Errorf("读取响应失败：%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("视觉接口返回 HTTP %d：%s（模型可能不支持图片，请换多模态模型，如 qwen-vl-max / gpt-4o / claude）", resp.StatusCode, snippet(data, 400))
	}
	var parsed oaResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Response{}, fmt.Errorf("响应解析失败：%w（原始内容：%s）", err, snippet(data, 200))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Response{}, fmt.Errorf("视觉接口报错：%s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("视觉接口没有返回任何选择项（原始内容：%s）", snippet(data, 200))
	}
	out := Response{
		Text:       strings.TrimSpace(parsed.Choices[0].Message.Content),
		Model:      firstNonEmpty(parsed.Model, model),
		StopReason: parsed.Choices[0].FinishReason,
	}
	out.Usage = Usage{
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
	}
	return out, nil
}

func visionAnthropic(ctx context.Context, cfg Config, req VisionRequest) (Response, error) {
	model := firstNonEmpty(req.Model, cfg.Model)
	if strings.TrimSpace(model) == "" {
		return Response{}, fmt.Errorf("通道 %s 没配置模型名（model）", cfg.Name)
	}
	if len(req.ImagePNG) == 0 {
		return Response{}, fmt.Errorf("没有可识别的图片")
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = pickInt(cfg.MaxTokens, 2048)
	}
	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": req.Prompt},
					map[string]any{"type": "image", "source": map[string]any{
						"type":       "base64",
						"media_type": "image/png",
						"data":       base64.StdEncoding.EncodeToString(req.ImagePNG),
					}},
				},
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	url := joinURL(cfg.BaseURL, "/v1/messages")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if cfg.APIKey != "" {
		httpReq.Header.Set("x-api-key", cfg.APIKey)
	}
	resp, err := visionHTTPClient(cfg).Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("请求 %s 失败：%w", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Response{}, fmt.Errorf("读取响应失败：%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("视觉接口返回 HTTP %d：%s（模型可能不支持图片，请换多模态模型，如 claude-3-5-sonnet）", resp.StatusCode, snippet(data, 400))
	}
	var parsed anResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Response{}, fmt.Errorf("响应解析失败：%w（原始内容：%s）", err, snippet(data, 200))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Response{}, fmt.Errorf("视觉接口报错：%s", parsed.Error.Message)
	}
	var texts []string
	for _, block := range parsed.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			texts = append(texts, block.Text)
		}
	}
	out := Response{
		Text:       strings.TrimSpace(strings.Join(texts, "\n")),
		Model:      firstNonEmpty(parsed.Model, model),
		StopReason: parsed.StopReason,
	}
	out.Usage = Usage{
		PromptTokens:     parsed.Usage.InputTokens,
		CompletionTokens: parsed.Usage.OutputTokens,
		TotalTokens:      parsed.Usage.InputTokens + parsed.Usage.OutputTokens,
	}
	return out, nil
}
