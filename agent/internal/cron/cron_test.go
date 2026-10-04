package cron

import (
	"testing"
	"time"
)

func TestParseAndNext(t *testing.T) {
	base := time.Date(2026, 9, 26, 10, 30, 0, 0, time.Local) // 周六

	cases := []struct {
		expr string
		want string
	}{
		{"*/15 * * * *", "2026-09-26T10:45"},
		{"0 9 * * 1", "2026-09-28T09:00"},  // 下周一
		{"30 8 1 * *", "2026-10-01T08:30"}, // 下月 1 号
		{"@hourly", "2026-09-26T11:00"},
		{"@daily", "2026-09-27T00:00"},
		{"0 0 * * 0", "2026-09-27T00:00"}, // 周日（0 与 7 等价）
		{"0 0 * * 7", "2026-09-27T00:00"},
		{"5,10 11 * * *", "2026-09-26T11:05"},
		{"0 0 29 2 *", "2028-02-29T00:00"}, // 闰年 2 月 29
	}
	for _, c := range cases {
		s, err := Parse(c.expr)
		if err != nil {
			t.Fatalf("解析 %q 失败：%v", c.expr, err)
		}
		next, err := s.Next(base)
		if err != nil {
			t.Fatalf("计算 %q 的下次时间失败：%v", c.expr, err)
		}
		if got := next.Format("2006-01-02T15:04"); got != c.want {
			t.Fatalf("%q 的下次触发应为 %s，实际 %s", c.expr, c.want, got)
		}
	}
}

func TestEvery(t *testing.T) {
	s, err := Parse("@every 30m")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	base := time.Date(2026, 9, 26, 10, 0, 0, 0, time.Local)
	next, err := s.Next(base)
	if err != nil {
		t.Fatalf("计算失败：%v", err)
	}
	if want := base.Add(30 * time.Minute); !next.Equal(want) {
		t.Fatalf("@every 30m 应为 %s，实际 %s", want, next)
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "*/0 * * * *", "a * * * *", "@every", "@every 1ms"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("非法表达式 %q 应报错", bad)
		}
	}
}

// 永远不触发的表达式要给明确错误，不能死循环
func TestImpossibleSchedule(t *testing.T) {
	s, err := Parse("0 0 30 2 *") // 2 月 30 日
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if _, err := s.Next(time.Now()); err == nil {
		t.Fatal("两年内无触发时刻应报错")
	}
}

func TestString(t *testing.T) {
	s, _ := Parse("0 8 * * 1-5")
	if s.String() != "0 8 * * 1-5" {
		t.Fatalf("原始表达式应保留：%s", s.String())
	}
}
