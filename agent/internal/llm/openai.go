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

// OpenAI 走 OpenAI 兼容协议（Ollama / vLLM / llama.cpp / DeepSeek / 各种中转都支持）
type OpenAI struct {
	cfg    Config
	client *http.Client
}

// NewOpenAI 创建 OpenAI 兼容通道
func NewOpenAI(cfg Config) *OpenAI {
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &OpenAI{cfg: cfg, client: &http.Client{Timeout: timeout}}
}

// Name 通道名
func (o *OpenAI) Name() string {
	if o.cfg.Name != "" {
		return o.cfg.Name
	}
	return "openai:" + firstNonEmpty(o.cfg.Model, "default")
}

type oaRequest struct {
	Model       string      `json:"model"`
	Messages    []oaMessage `json:"messages"`
	Tools       []oaTool    `json:"tools,omitempty"`
	ToolChoice  string      `json:"tool_choice,omitempty"`
	Temperature float64     `json:"temperature,omitempty"`
	MaxTokens   int         `json:"max_tokens,omitempty"`
	Stream      bool        `json:"stream"`
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	Name       string       `json:"name,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type oaResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string    `json:"finish_reason"`
		Message      oaMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Chat 发起一次对话
func (o *OpenAI) Chat(ctx context.Context, req Request) (Response, error) {
	if err := CheckToolNames(req.Tools); err != nil {
		return Response{}, err
	}
	body := oaRequest{
		Model:       firstNonEmpty(req.Model, o.cfg.Model),
		Temperature: pickFloat(req.Temperature, o.cfg.Temperature),
		MaxTokens:   pickInt(req.MaxTokens, o.cfg.MaxTokens),
		Stream:      false,
	}
	if body.Model == "" {
		return Response{}, fmt.Errorf("通道 %s 没配置模型名（model）", o.Name())
	}
	if strings.TrimSpace(req.System) != "" {
		body.Messages = append(body.Messages, oaMessage{Role: RoleSystem, Content: req.System})
	}
	for _, m := range req.Messages {
		om := oaMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
		if m.Role == RoleTool && om.Content == "" {
			om.Content = "(空结果)"
		}
		for _, tc := range m.ToolCalls {
			args, err := json.Marshal(nonNilMap(tc.Args))
			if err != nil {
				return Response{}, fmt.Errorf("工具参数编码失败：%w", err)
			}
			oc := oaToolCall{ID: tc.ID, Type: "function"}
			oc.Function.Name = tc.Name
			oc.Function.Arguments = string(args)
			om.ToolCalls = append(om.ToolCalls, oc)
		}
		body.Messages = append(body.Messages, om)
	}
	for _, t := range req.Tools {
		ot := oaTool{Type: "function"}
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Schema
		body.Tools = append(body.Tools, ot)
	}
	if len(body.Tools) > 0 {
		body.ToolChoice = "auto"
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	url := joinURL(o.cfg.BaseURL, "/chat/completions")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	}

	resp, err := o.client.Do(httpReq)
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

	var parsed oaResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Response{}, fmt.Errorf("响应解析失败：%w（原始内容：%s）", err, snippet(data, 200))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Response{}, fmt.Errorf("模型接口报错：%s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("模型接口没有返回任何选择项（原始内容：%s）", snippet(data, 200))
	}

	choice := parsed.Choices[0]
	out := Response{
		Text:       strings.TrimSpace(choice.Message.Content),
		Model:      firstNonEmpty(parsed.Model, body.Model),
		StopReason: choice.FinishReason,
	}
	for _, tc := range choice.Message.ToolCalls {
		args := map[string]any{}
		if strings.TrimSpace(tc.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				// 参数不是合法 JSON：照样把调用抛给上层，让它把错误喂回模型
				args = map[string]any{"_raw": tc.Function.Arguments, "_parseError": err.Error()}
			}
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args})
	}
	out.Usage = Usage{
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
	}
	return out, nil
}

func pickFloat(v, def float64) float64 {
	if v != 0 {
		return v
	}
	return def
}

func pickInt(v, def int) int {
	if v != 0 {
		return v
	}
	return def
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func snippet(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
