package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Anthropic 走 Anthropic Messages 协议
type Anthropic struct {
	cfg    Config
	client *http.Client
}

// NewAnthropic 创建 Anthropic 通道
func NewAnthropic(cfg Config) *Anthropic {
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &Anthropic{cfg: cfg, client: &http.Client{Timeout: timeout}}
}

// Name 通道名
func (a *Anthropic) Name() string {
	if a.cfg.Name != "" {
		return a.cfg.Name
	}
	return "anthropic:" + firstNonEmpty(a.cfg.Model, "default")
}

type anRequest struct {
	Model       string      `json:"model"`
	System      string      `json:"system,omitempty"`
	Messages    []anMessage `json:"messages"`
	Tools       []anTool    `json:"tools,omitempty"`
	MaxTokens   int         `json:"max_tokens"`
	Temperature float64     `json:"temperature,omitempty"`
}

type anMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type anTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anResponse struct {
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Chat 发起一次对话
func (a *Anthropic) Chat(ctx context.Context, req Request) (Response, error) {
	if err := CheckToolNames(req.Tools); err != nil {
		return Response{}, err
	}
	body := anRequest{
		Model:       firstNonEmpty(req.Model, a.cfg.Model),
		System:      req.System,
		MaxTokens:   pickInt(req.MaxTokens, pickInt(a.cfg.MaxTokens, 2048)),
		Temperature: pickFloat(req.Temperature, a.cfg.Temperature),
	}
	if body.Model == "" {
		return Response{}, fmt.Errorf("通道 %s 没配置模型名（model）", a.Name())
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleTool:
			body.Messages = append(body.Messages, anMessage{Role: "user", Content: []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": m.ToolCallID,
					"content":     firstNonEmpty(m.Content, "(空结果)"),
				},
			}})
		case RoleAssistant:
			blocks := []any{}
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, map[string]any{
					"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": nonNilMap(tc.Args),
				})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, map[string]any{"type": "text", "text": "(空)"})
			}
			body.Messages = append(body.Messages, anMessage{Role: "assistant", Content: blocks})
		default:
			body.Messages = append(body.Messages, anMessage{Role: "user", Content: []any{
				map[string]any{"type": "text", "text": m.Content},
			}})
		}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	url := joinURL(a.cfg.BaseURL, "/v1/messages")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if a.cfg.APIKey != "" {
		httpReq.Header.Set("x-api-key", a.cfg.APIKey)
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("请求 %s 失败：%w", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Response{}, fmt.Errorf("读取响应失败：%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("模型接口返回 HTTP %d：%s", resp.StatusCode, snippet(data, 400))
	}

	var parsed anResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Response{}, fmt.Errorf("响应解析失败：%w（原始内容：%s）", err, snippet(data, 200))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Response{}, fmt.Errorf("模型接口报错：%s", parsed.Error.Message)
	}

	out := Response{Model: firstNonEmpty(parsed.Model, body.Model), StopReason: parsed.StopReason}
	var texts []string
	for _, block := range parsed.Content {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				texts = append(texts, block.Text)
			}
		case "tool_use":
			args := map[string]any{}
			if len(block.Input) > 0 {
				if err := json.Unmarshal(block.Input, &args); err != nil {
					args = map[string]any{"_raw": string(block.Input), "_parseError": err.Error()}
				}
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: block.ID, Name: block.Name, Args: args})
		}
	}
	out.Text = strings.TrimSpace(strings.Join(texts, "\n"))
	out.Usage = Usage{
		PromptTokens:     parsed.Usage.InputTokens,
		CompletionTokens: parsed.Usage.OutputTokens,
		TotalTokens:      parsed.Usage.InputTokens + parsed.Usage.OutputTokens,
	}
	return out, nil
}
