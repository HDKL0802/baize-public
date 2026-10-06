package agentrt

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"baize/internal/llm"
	"baize/internal/tools"
)

// Scroll：上下文超预算时把较早的对话滚出窗口，但**原文可回放**。
//
// 这是和老实现的根本区别：老实现把老对话压成摘要就丢了原文；
// 现在只是移出窗口，原文留在 messages 表里，模型能用 context_recall 取回来。
func TestScrollRecordsAndRecallsOriginals(t *testing.T) {
	h := newHarness(t, false)
	marker := "MARKER-原始细节-12345"
	big := marker + strings.Repeat("这是一段很早以前的对话内容。", 200)
	if err := os.WriteFile(filepath.Join(h.ws.Root(), "big.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}

	p := llm.NewFake("fake", llm.Script(
		llm.CallTool("c1", "fs_read", map[string]any{"path": "big.md"}),
		llm.CallTool("c2", "fs_list", map[string]any{"path": "."}),
		llm.CallTool("c3", "fs_list", map[string]any{"path": "."}),
		llm.CallTool("c4", "fs_list", map[string]any{"path": "."}),
		llm.Say("看完了"),
	))
	res, err := h.run(p, func(c *Config) {
		c.Approve = func(string, map[string]any) bool { return true }
		c.TokenBudget = 300 // 故意开得很小，逼出滚动
	}, "看看 big.md 里写了什么")
	if err != nil {
		t.Fatalf("运行失败：%v", err)
	}

	// ① 应当留下了滚出记录（说明压缩真的触发了 —— 老实现因为 safeCut 太严从没触发过）
	chunks, err := h.store.ScrollChunks(res.RunID)
	if err != nil {
		t.Fatalf("读滚出记录失败：%v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("超预算时应发生滚动并留下记录")
	}
	first := chunks[0]
	if first.EndIdx <= first.StartIdx {
		t.Fatalf("滚出区间不合法：%+v", first)
	}
	if strings.TrimSpace(first.Summary) == "" {
		t.Fatalf("滚出记录应带摘要：%+v", first)
	}

	// ② 原文仍在（messages 表），可以按区间回放。
	// 一次运行可能滚出多段，这里把已滚出的区间全取回来一起看。
	from := chunks[0].StartIdx
	to := chunks[len(chunks)-1].EndIdx
	tool := NewContextRecall(h.store)
	ctx := tools.WithRunID(context.Background(), res.RunID)
	out, err := tool.Run(ctx, map[string]any{"from": from, "to": to, "limit": 200})
	if err != nil {
		t.Fatalf("回放失败：%v", err)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), marker) {
		t.Fatalf("回放应能取回被滚出窗口的原文（含 %s）：%s", marker, string(raw)[:min(400, len(raw))])
	}

	// ③ 没有 runID 的上下文里调用应明确报错，而不是静默返回空
	if _, err := tool.Run(context.Background(), map[string]any{}); err == nil {
		t.Fatal("不在运行里调用应报错")
	}
}

// 摘要是给模型看的"引子"，必须带上可回放的区间提示
func TestScrollNoteCarriesRecallHint(t *testing.T) {
	c := ScrollChunk{RunID: "r1", StartIdx: 2, EndIdx: 9, Summary: "之前聊了 A 和 B", Tokens: 120}
	note := c.Note()
	if note == "" {
		t.Fatal("应生成提示")
	}
	for _, want := range []string{"2", "8", "context_recall", "之前聊了 A 和 B"} {
		if !strings.Contains(note, want) {
			t.Fatalf("提示里应包含 %q：%s", want, note)
		}
	}
	// 空区间不产生噪音
	if (ScrollChunk{StartIdx: 5, EndIdx: 5}).Note() != "" {
		t.Fatal("空区间不该生成提示")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
