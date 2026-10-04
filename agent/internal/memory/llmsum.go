package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"baize/internal/llm"
)

// LLMSummarizer 用模型通道做摘要（记忆树 L1/L2 节点、上下文压缩都用它）。
// 没有配置模型时，调用方传 nil 即可，记忆库会自动退回抽取式摘要。
type LLMSummarizer struct {
	Provider  llm.Provider
	MaxTokens int
}

// NewLLMSummarizer 创建摘要器
func NewLLMSummarizer(p llm.Provider) *LLMSummarizer {
	return &LLMSummarizer{Provider: p, MaxTokens: 400}
}

// Summarize 把若干段文本压成要点
func (s *LLMSummarizer) Summarize(ctx context.Context, title string, texts []string) (string, error) {
	if s == nil || s.Provider == nil {
		return "", errors.New("没有可用的模型通道")
	}
	var b strings.Builder
	for i, t := range texts {
		if i >= 40 {
			b.WriteString("…（还有更多条目已省略）\n")
			break
		}
		b.WriteString("- " + strings.TrimSpace(t) + "\n")
	}
	max := s.MaxTokens
	if max <= 0 {
		max = 400
	}
	resp, err := s.Provider.Chat(ctx, llm.Request{
		System: "你是记忆压缩器。只保留事实、决定、数字、路径、人名、时间与未完成事项，" +
			"去掉寒暄、重复与过程性描述；用中文短句要点列表输出，不要客套话，不要标题。",
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: "标题：" + title + "\n待压缩内容：\n" + b.String(),
		}},
		Temperature: 0.2,
		MaxTokens:   max,
	})
	if err != nil {
		return "", fmt.Errorf("摘要失败：%w", err)
	}
	return strings.TrimSpace(resp.Text), nil
}
