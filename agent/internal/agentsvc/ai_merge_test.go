package agentsvc

import (
	"testing"
)

// 模型回话的契约解析：只认 JSON 主体，容忍围栏与前后杂文；解析不出就报错。
func TestParseAIJudgeReply(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want AIJudgeDecision
		fail bool
	}{
		{
			name: "干净的 keep",
			raw:  `{"decision":"keep","docId":"abc","reason":"B 的改动更全面"}`,
			want: AIJudgeDecision{Decision: "keep", DocID: "abc", Reason: "B 的改动更全面"},
		},
		{
			name: "带 markdown 围栏与前后杂文",
			raw:  "好的，我的判定如下：\n```json\n{\"decision\":\"merge\",\"mergedContent\":\"# 合并稿\\n正文\",\"reason\":\"双方共识\"}\n```\n完毕。",
			want: AIJudgeDecision{Decision: "merge", MergedContent: "# 合并稿\n正文", Reason: "双方共识"},
		},
		{
			name: "JSON 前后有废话（取第一个 { 到最后一个 }）",
			raw:  `前面是解释 {"decision":"keep","docId":"x","reason":"r"} 后面是总结`,
			want: AIJudgeDecision{Decision: "keep", DocID: "x", Reason: "r"},
		},
		{
			name: "完全没有 JSON 就报错",
			raw:  "我觉得保留 B 版比较好。",
			fail: true,
		},
		{
			name: "坏 JSON 也报错",
			raw:  `{"decision":`,
			fail: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseAIJudgeReply(c.raw)
			if c.fail {
				if err == nil {
					t.Fatalf("应当报错，实得 %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if got.Decision != c.want.Decision || got.DocID != c.want.DocID ||
				got.MergedContent != c.want.MergedContent || got.Reason != c.want.Reason {
				t.Fatalf("解析结果不对：%+v（期望 %+v）", got, c.want)
			}
		})
	}
}
