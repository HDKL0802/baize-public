package agentrt

import (
	"context"
	"fmt"
	"strings"

	"baize/internal/tools"
)

// ScrollChunk 一批「被滚出上下文」的原文。
//
// 关键点：这里**只记索引和摘要，不存原文** —— 原文一直在 messages 表里躺着。
// 老实现是把老对话压成摘要后原文就再也拿不回来了；现在只是把老轮次移出**上下文窗口**，
// 需要时可以用 context_recall 按区间把它们原样取回来（这就是「Scroll：不摘要、不丢」）。
type ScrollChunk struct {
	RunID    string `json:"runId"`
	StartIdx int    `json:"startIdx"` // 起始原始下标（含）
	EndIdx   int    `json:"endIdx"`   // 结束原始下标（不含）
	Summary  string `json:"summary"`  // 给模型看的摘要（原文仍可取）
	Tokens   int    `json:"tokens"`   // 被滚出原文的估算 token
	At       int64  `json:"at"`
}

// ScrollNote 拼给系统提示的那段「已滚出窗口」说明。
// 带上区间，模型才知道自己能用 context_recall 把原文要回来。
func (c ScrollChunk) Note() string {
	if c.EndIdx <= c.StartIdx {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n【较早的对话已滚出上下文窗口】\n")
	b.WriteString(fmt.Sprintf("原文共 %d 条（原始下标 %d–%d），摘要如下：\n%s\n",
		c.EndIdx-c.StartIdx, c.StartIdx, c.EndIdx-1, strings.TrimSpace(c.Summary)))
	b.WriteString(fmt.Sprintf("摘要不够用时，可调用 context_recall(from=%d, to=%d) 取回原文。\n",
		c.StartIdx, c.EndIdx))
	return b.String()
}

/* ---------------- 回放工具 ---------------- */

// ContextRecall 「上下文回放」工具：把被滚出窗口的原文按区间/关键词取回来。
type ContextRecall struct {
	store *Store
}

// NewContextRecall 创建回放工具
func NewContextRecall(store *Store) *ContextRecall {
	return &ContextRecall{store: store}
}

func (t *ContextRecall) Name() string { return "context_recall" }

func (t *ContextRecall) Description() string {
	return "取回较早对话的原文。当系统提示里的「较早对话摘要」不够用、" +
		"需要原始细节（确切命令、原始输出、具体数值）时调用。" +
		"按原始下标区间取，可用 from/to 限定，或用 keyword 过滤。"
}

func (t *ContextRecall) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"from":    map[string]any{"type": "integer", "description": "起始原始下标（含）；省略=0"},
			"to":      map[string]any{"type": "integer", "description": "结束原始下标（不含）；省略=到末尾"},
			"keyword": map[string]any{"type": "string", "description": "只返回含该关键词的消息（可选）"},
			"limit":   map[string]any{"type": "integer", "description": "最多返回多少条，默认 40"},
		},
	}
}

func (t *ContextRecall) Run(ctx context.Context, args map[string]any) (any, error) {
	runID := tools.RunIDFrom(ctx)
	if runID == "" {
		return nil, fmt.Errorf("当前不在某次运行里，无法回放上下文")
	}
	from := tools.ArgInt(args, "from", 0)
	to := tools.ArgInt(args, "to", -1)
	limit := tools.ArgInt(args, "limit", 40)
	keyword := strings.TrimSpace(tools.ArgString(args, "keyword"))

	msgs, err := t.store.MessagesRange(runID, from, to, limit)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		if keyword != "" && !strings.Contains(m.Content, keyword) {
			continue
		}
		item := map[string]any{"role": m.Role, "content": m.Content}
		if m.Name != "" {
			item["tool"] = m.Name
		}
		if len(m.ToolCalls) > 0 {
			names := make([]string, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				names = append(names, tc.Name)
			}
			item["toolCalls"] = names
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return map[string]any{
			"note": fmt.Sprintf("区间 %d–%d 内没有匹配的原文（可能没滚出过，或关键词不对）", from, to),
		}, nil
	}
	return map[string]any{"from": from, "to": to, "count": len(out), "messages": out}, nil
}

// 回放只读，不改工作目录、也不需要审批
var _ tools.Tool = (*ContextRecall)(nil)
