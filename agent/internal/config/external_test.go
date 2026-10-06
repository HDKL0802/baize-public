package config

import "testing"

// 外部 Agent 的归一：去空白、按 id 去重、类型/超时兜底、名字缺省用 id
func TestExternalAgentNormalize(t *testing.T) {
	cfg := Config{ExternalAgents: []ExternalAgent{
		{ID: " a ", Type: " BAIZE ", URL: "http://x/"},
		{ID: "a", Type: "http", URL: "http://y"}, // 重复 id：丢
		{ID: "", Type: "http", URL: "http://z"},  // 没 id：丢
		{ID: "b", Type: "grpc", URL: "http://w/", TimeoutSec: 0},
	}}
	cfg.normalize()

	if len(cfg.ExternalAgents) != 2 {
		t.Fatalf("应剩 2 个外部 Agent，实际 %d：%+v", len(cfg.ExternalAgents), cfg.ExternalAgents)
	}
	a := cfg.ExternalAgents[0]
	if a.ID != "a" || a.Type != "baize" || a.URL != "http://x" || a.Name != "a" {
		t.Fatalf("第一条归一不对：%+v", a)
	}
	if a.TimeoutSec != 120 {
		t.Fatalf("超时应兜底 120：%+v", a)
	}
	b := cfg.ExternalAgents[1]
	if b.Type != "http" || b.URL != "http://w" {
		t.Fatalf("非法类型应回落 http、URL 去尾斜杠：%+v", b)
	}

	if !ValidExternalAgentType("HTTP") || !ValidExternalAgentType("baize") {
		t.Fatal("合法类型应被认（大小写容忍）")
	}
	if ValidExternalAgentType("nope") {
		t.Fatal("未知类型不该被认")
	}
	if len(ExternalAgentTypes()) != 2 {
		t.Fatalf("类型清单应为 http/baize：%+v", ExternalAgentTypes())
	}
}
