package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEstimateTokensAndChunking(t *testing.T) {
	if got := EstimateTokens("你好世界"); got < 4 {
		t.Fatalf("中文应至少 1 字 1 token，实际 %d", got)
	}
	short := "这是一段很短的记忆。"
	if chunks := ChunkText(short, 100); len(chunks) != 1 {
		t.Fatalf("短文本应只有 1 块，实际 %d", len(chunks))
	}

	// 造一段超长文本（远超 200 token 上限）
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("第")
		b.WriteString(strings.Repeat("字", 20))
		b.WriteString("，这是一段用来测试分块的中文内容。\n\n")
	}
	chunks := ChunkText(b.String(), 200)
	if len(chunks) < 2 {
		t.Fatalf("超长文本应被切成多块，实际 %d", len(chunks))
	}
	for i, c := range chunks {
		if EstimateTokens(c) > 260 { // 允许一点估算误差
			t.Fatalf("第 %d 块超过上限：%d token", i+1, EstimateTokens(c))
		}
	}
	if joined := strings.Join(chunks, ""); len(joined) < 100 {
		t.Fatal("分块后内容不应丢失")
	}
}

func TestNormalizeText(t *testing.T) {
	in := "第一行   \r\n\r\n\r\n\r\n第二行\t\r\n"
	out := NormalizeText(in)
	if strings.Contains(out, "\r") || strings.Contains(out, "   ") {
		t.Fatalf("应清掉回车与行尾空白：%q", out)
	}
	if strings.Count(out, "\n") > 2 {
		t.Fatalf("多余空行应被压缩：%q", out)
	}
}

func TestIngestSearchAndIdempotency(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("打开记忆库失败：%v", err)
	}
	defer st.Close()

	res, err := st.Ingest("决定：密码本用 PBKDF2 150000 次迭代 + AES-GCM，密文库存在 bz_vault_enc。",
		IngestOptions{Source: "agent", SourceRef: "run:1", Kind: "decision", Title: "加密方案"})
	if err != nil {
		t.Fatalf("收数失败：%v", err)
	}
	if res.Chunks != 1 || res.Tokens <= 0 {
		t.Fatalf("收数结果不对：%+v", res)
	}

	// 同一来源 + 同一内容重复收数 → 幂等跳过
	again, err := st.Ingest("决定：密码本用 PBKDF2 150000 次迭代 + AES-GCM，密文库存在 bz_vault_enc。",
		IngestOptions{Source: "agent", SourceRef: "run:1", Kind: "decision", Title: "加密方案"})
	if err != nil {
		t.Fatalf("重复收数失败：%v", err)
	}
	if again.Chunks != 0 || again.Skipped != 1 {
		t.Fatalf("重复内容应跳过：%+v", again)
	}

	hits, err := st.Search("加密 密文库", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if len(hits) == 0 {
		t.Fatal("应能检索到刚写入的记忆")
	}
	if hits[0].Chunk.Title != "加密方案" || hits[0].Score <= 0 {
		t.Fatalf("检索结果不对：%+v", hits[0])
	}
	// 命中会累加使用频次
	after, _ := st.Search("加密 密文库", 5)
	if after[0].Chunk.Hits <= hits[0].Chunk.Hits {
		t.Fatal("命中后使用频次应增加")
	}

	// 检索不到的词不应硬凑结果
	none, err := st.Search("完全不相关的火星探测器", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if len(none) != 0 {
		t.Fatalf("不该有命中：%+v", none)
	}

	if _, err := st.Ingest("   ", IngestOptions{}); err == nil {
		t.Fatal("空内容应报错")
	}
}

func TestScoreAndRecency(t *testing.T) {
	now := int64(1_700_000_000_000)
	if RecencyFactor(now, now) < 0.9 {
		t.Fatal("刚写入的记忆新鲜度应接近 1")
	}
	old := now - int64(30*24*3600*1000)
	if RecencyFactor(now, old) > 0.3 {
		t.Fatal("30 天前的记忆新鲜度应明显衰减")
	}
	high := Score(1.0, 1.0, 0.8, 0)
	low := Score(0.2, 0.1, 0.1, 0)
	if !(high > low) {
		t.Fatalf("重要度高的记忆评分应更高：high=%f low=%f", high, low)
	}
	if Score(1, 1, 1, 100) > 1.0 {
		t.Fatal("评分应归一到 0-1")
	}
	if Richness("2026-09-26 决定用 \"PBKDF2\" 150000 次迭代") <= 0.05 {
		t.Fatal("包含数字与专有名词的内容信息量应偏高")
	}
}

// 假摘要器：验证"层级摘要树"确实调用了摘要通道
type fakeSummarizer struct{ calls int }

func (f *fakeSummarizer) Summarize(_ context.Context, title string, texts []string) (string, error) {
	f.calls++
	return "【摘要】" + title + " 共 " + itoaTest(len(texts)) + " 条", nil
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}

func TestTreeAndObsidian(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("打开记忆库失败：%v", err)
	}
	defer st.Close()

	for i, text := range []string{
		"今天把待办中心的密码本合并规则改成了同账号进历史。",
		"决定：密码本用 PBKDF2 加密，旧记录靠解密验证兼容。",
		"发现一个 bug：锁定时不能往明文槽写。",
	} {
		if _, err := st.Ingest(text, IngestOptions{Source: "agent", SourceRef: filepath.Join("run", itoaTest(i)), Kind: "note"}); err != nil {
			t.Fatalf("收数失败：%v", err)
		}
	}

	sum := &fakeSummarizer{}
	n, err := st.RebuildTree(context.Background(), sum)
	if err != nil {
		t.Fatalf("重建记忆树失败：%v", err)
	}
	if n == 0 || sum.calls == 0 {
		t.Fatalf("应生成 L1 节点并调用摘要器：nodes=%d calls=%d", n, sum.calls)
	}

	nodes, err := st.Tree(20)
	if err != nil {
		t.Fatalf("读记忆树失败：%v", err)
	}
	var day, week *Node
	for i := range nodes {
		if nodes[i].Level == 1 && day == nil {
			day = &nodes[i]
		}
		if nodes[i].Level == 2 && week == nil {
			week = &nodes[i]
		}
	}
	if day == nil || day.Chunks != 3 || !strings.Contains(day.Summary, "【摘要】") {
		t.Fatalf("L1 节点不对：%+v", day)
	}
	if week == nil || !strings.Contains(week.Summary, "【摘要】") {
		t.Fatalf("应生成 L2 周节点：%+v", week)
	}

	out := filepath.Join(t.TempDir(), "vault-export")
	written, err := st.WriteObsidian(out)
	if err != nil {
		t.Fatalf("导出 Obsidian 失败：%v", err)
	}
	if written == 0 {
		t.Fatal("应写出日记忆文件")
	}
	md := filepath.Join(out, "memory")
	entries, err := os.ReadDir(md)
	if err != nil || len(entries) < 2 {
		t.Fatalf("导出目录内容不足：%v %d", err, len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(md, day.Day+".md"))
	if err != nil {
		t.Fatalf("读日记忆文件失败：%v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "tags: [baize/memory]") || !strings.Contains(body, "## 明细") {
		t.Fatalf("Obsidian 文件格式不对：\n%s", body)
	}
	if _, err := os.Stat(filepath.Join(md, "index.md")); err != nil {
		t.Fatalf("应生成索引文件：%v", err)
	}

	stats, err := st.Stats()
	if err != nil || stats.Chunks != 3 || stats.Nodes == 0 {
		t.Fatalf("统计不对：%+v %v", stats, err)
	}
}

func TestTokenize(t *testing.T) {
	terms := Tokenize("密码本 AES-GCM encryption 方案")
	want := map[string]bool{"密码": true, "码本": true, "gcm": true, "encryption": true, "aes": true}
	for w := range want {
		found := false
		for _, t := range terms {
			if t == w {
				found = true
			}
		}
		if !found {
			t.Fatalf("切词缺少 %q：%+v", w, terms)
		}
	}
	if len(Tokenize("，。！")) != 0 {
		t.Fatalf("纯标点不应切出词：%+v", Tokenize("，。！"))
	}
}
