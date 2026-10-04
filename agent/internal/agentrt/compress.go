package agentrt

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"baize/internal/llm"
	"baize/internal/memory"
)

// Compressor 上下文压缩（Hermes context_compressor 思路的 Go 重写）：
// 超出预算时把"较早的对话"压成一段摘要，最近的若干轮保持原文，
// 保证关键上下文不丢，同时把 token 压回预算内。
type Compressor struct {
	Budget     int
	KeepRecent int
	Summarizer memory.Summarizer
	Logger     *slog.Logger
}

// NewCompressor 创建压缩器
func NewCompressor(budget int, sum memory.Summarizer, lg *slog.Logger) *Compressor {
	if budget <= 0 {
		budget = 6000
	}
	return &Compressor{Budget: budget, KeepRecent: 6, Summarizer: sum, Logger: lg}
}

// Compress 返回压缩后的消息列表；changed 表示是否做过压缩
func (c *Compressor) Compress(ctx context.Context, msgs []llm.Message, system string) ([]llm.Message, bool, error) {
	if c == nil || len(msgs) == 0 {
		return msgs, false, nil
	}
	if c.tokenCount(system, msgs) <= c.Budget {
		return msgs, false, nil
	}
	keep := c.KeepRecent
	if keep > len(msgs)/2 {
		keep = len(msgs) / 2 // 消息本身就不多时，也保证留出可压缩的部分
	}
	if keep < 1 {
		keep = 1
	}
	if keep > len(msgs) {
		keep = len(msgs)
	}
	cut := c.safeCut(msgs, len(msgs)-keep)
	if cut <= 0 {
		return msgs, false, nil
	}
	old := msgs[:cut]
	recent := msgs[cut:]

	texts := make([]string, 0, len(old))
	for _, m := range old {
		line := "[" + m.Role + "] " + strings.TrimSpace(m.Content)
		if len(m.ToolCalls) > 0 {
			names := make([]string, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				names = append(names, tc.Name)
			}
			line += "（调用工具：" + strings.Join(names, "、") + "）"
		}
		if strings.TrimSpace(line) != "" {
			texts = append(texts, line)
		}
	}
	summary := extractiveSummary(strings.Join(texts, "\n"), 400)
	if c.Summarizer != nil {
		if got, err := c.Summarizer.Summarize(ctx, "较早的对话摘要", texts); err == nil && strings.TrimSpace(got) != "" {
			summary = got
		} else if err != nil && c.Logger != nil {
			c.Logger.Warn("摘要失败，退回抽取式压缩", "err", err)
		}
	}
	if c.Logger != nil {
		c.Logger.Info("上下文已压缩",
			"beforeMessages", len(msgs), "afterMessages", len(recent)+1,
			"beforeTokens", c.tokenCount(system, msgs), "summaryTokens", memory.EstimateTokens(summary))
	}
	out := []llm.Message{{
		Role:    llm.RoleUser,
		Content: "【较早对话的压缩摘要】\n" + summary,
	}}
	return append(out, recent...), true, nil
}

// safeCut 找一个安全的切点：不能把 assistant 的工具调用和随后的 tool 结果切开
func (c *Compressor) safeCut(msgs []llm.Message, want int) int {
	cut := want
	if cut < 0 {
		cut = 0
	}
	if cut >= len(msgs) {
		return len(msgs)
	}
	// 往前找一条"不依赖前面工具结果"的消息（user / assistant 且有正文）
	for cut > 0 {
		m := msgs[cut]
		if m.Role == llm.RoleUser || (m.Role == llm.RoleAssistant && len(m.ToolCalls) == 0) {
			return cut
		}
		cut--
	}
	return 0
}

func (c *Compressor) tokenCount(system string, msgs []llm.Message) int {
	total := memory.EstimateTokens(system)
	for _, m := range msgs {
		total += memory.EstimateTokens(m.Content) + 8
		for _, tc := range m.ToolCalls {
			total += memory.EstimateTokens(tc.Name) + 16
		}
	}
	return total
}

// extractiveSummary 没有模型时的兜底：取前若干 token
func extractiveSummary(text string, tokens int) string {
	if memory.EstimateTokens(text) <= tokens {
		return text
	}
	runes := []rune(text)
	keep := tokens * 2
	if keep > len(runes) {
		keep = len(runes)
	}
	return fmt.Sprintf("%s…（原始内容约 %d token）", strings.TrimSpace(string(runes[:keep])), memory.EstimateTokens(text))
}
