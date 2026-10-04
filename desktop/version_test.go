package main

import "testing"

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		cand, cur string
		want      bool
	}{
		{"0.2.1", "0.2.0", true},
		{"0.3.0", "0.2.9", true},
		{"1.0.0", "0.99.99", true},
		{"0.2.0", "0.2.0", false},
		{"0.2.0", "0.2.1", false},
		{"0.2", "0.2.0", false},   // 缺位补 0，所以相等
		{"0.2.1", "0.2", true},    // 0.2.1 > 0.2.0
		{"v0.3.0", "0.2.0", true}, // 容忍前缀 v
		{"abc", "0.2.0", false},   // 解析不了 → 不升级
		{"0.3.0", "abc", false},   // 同上
		{"", "0.2.0", false},
		{"0.3.0", "", false},
		{"0.-1", "0.2.0", false}, // 负数不认
		{"2.0.0", "1.9.9", true},
	}
	for _, c := range cases {
		if got := newerVersion(c.cand, c.cur); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v，期望 %v", c.cand, c.cur, got, c.want)
		}
	}
}
