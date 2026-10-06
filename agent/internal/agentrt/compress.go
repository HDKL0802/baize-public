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

// CompressResult 一次压缩的结果
//
// 和旧实现的关键区别：摘要**不再替换掉**那些老消息（那是「有损、不可逆」），
// 而是把老消息移出**上下文窗口**、把摘要单独交回去。调用方把摘要拼进系统提示，
// 原文则一直留在 messages 表里，需要时用 context_recall 回放。
type CompressResult struct {
	Messages []llm.Message // 压缩后留在上下文里的消息（近期原文）
	Summary  string        // 被滚出那段的摘要（由调用方拼进系统提示）
	Scrolled int           // 本次滚出的条数
	Tokens   int           // 被滚出原文的估算 token
	Changed  bool          // 是否真的滚出过
}

// Compress 超出预算时，把较早的对话移出上下文，只保留最近的若干轮。
// 返回的 Summary/Scrolled 用于记录「滚出了哪一段」，原文不销毁。
func (c *Compressor) Compress(ctx context.Context, msgs []llm.Message, system string) (CompressResult, error) {
	if c == nil || len(msgs) == 0 {
		return CompressResult{Messages: msgs}, nil
	}
	if c.tokenCount(system, msgs) <= c.Budget {
		return CompressResult{Messages: msgs}, nil
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
		return CompressResult{Messages: msgs}, nil
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
	outTokens := 0
	for _, m := range old {
		outTokens += memory.EstimateTokens(m.Content)
	}
	if c.Logger != nil {
		c.Logger.Info("上下文滚出较早对话",
			"beforeMessages", len(msgs), "afterMessages", len(recent), "scrolled", cut,
			"beforeTokens", c.tokenCount(system, msgs), "summaryTokens", memory.EstimateTokens(summary))
	}
	return CompressResult{
		Messages: recent,
		Summary:  summary,
		Scrolled: cut,
		Tokens:   outTokens,
		Changed:  true,
	}, nil
}

// safeCut 找一个安全的切点。
//
// 关键约束只有一个：**不能切在 tool 消息上** —— tool 结果必须紧跟在发起调用的
// assistant 之后，从 tool 消息开始的消息序列是非法的。
//
// 老实现要求切点必须是 user 或「无工具调用的 assistant」，这在单次运行的序列
// （user → asst(工具调用) → tool → asst(工具调用) → tool …）里永远找不到，
// 于是 safeCut 一路退到 0、压缩**从来没真正触发过**，长任务只能眼睁睁撑爆上下文。
// 现在只排除 tool 边界，压缩能在任意一轮工具调用之间切开。
func (c *Compressor) safeCut(msgs []llm.Message, want int) int {
	cut := want
	if cut < 0 {
		cut = 0
	}
	if cut >= len(msgs) {
		return len(msgs)
	}
	for cut > 0 {
		if msgs[cut].Role != llm.RoleTool {
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
