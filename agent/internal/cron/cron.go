// Package cron 是定时任务引擎（昆帕定时任务机制的 Go 重写）。
//
// 支持标准五段 cron（分 时 日 月 周，支持 * / 步长 / 区间 / 列表）
// 以及 @every 30m、@hourly、@daily、@weekly 这类简写。
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule 一个已解析的调度表达式
type Schedule struct {
	raw    string
	every  time.Duration // @every 用
	minute field
	hour   field
	dom    field
	month  field
	dow    field
}

type field struct {
	any   bool
	units map[int]bool
}

func (f field) match(v int) bool {
	if f.any {
		return true
	}
	return f.units[v]
}

// Parse 解析表达式
func Parse(expr string) (*Schedule, error) {
	raw := strings.TrimSpace(expr)
	if raw == "" {
		return nil, fmt.Errorf("定时表达式不能为空")
	}
	low := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(low, "@every"):
		spec := strings.TrimSpace(strings.TrimPrefix(low, "@every"))
		d, err := time.ParseDuration(spec)
		if err != nil || d < time.Second {
			return nil, fmt.Errorf("@every 的间隔不合法：%s（例如 @every 30m）", spec)
		}
		return &Schedule{raw: raw, every: d}, nil
	case low == "@hourly":
		raw = "0 * * * *"
	case low == "@daily" || low == "@midnight":
		raw = "0 0 * * *"
	case low == "@weekly":
		raw = "0 0 * * 0"
	case low == "@monthly":
		raw = "0 0 1 * *"
	}

	parts := strings.Fields(raw)
	if len(parts) != 5 {
		return nil, fmt.Errorf("五段 cron 需要 5 个字段（分 时 日 月 周），收到 %d 个：%s", len(parts), raw)
	}
	s := &Schedule{raw: expr}
	var err error
	if s.minute, err = parseField(parts[0], 0, 59, false); err != nil {
		return nil, fmt.Errorf("分钟字段错误：%w", err)
	}
	if s.hour, err = parseField(parts[1], 0, 23, false); err != nil {
		return nil, fmt.Errorf("小时字段错误：%w", err)
	}
	if s.dom, err = parseField(parts[2], 1, 31, false); err != nil {
		return nil, fmt.Errorf("日字段错误：%w", err)
	}
	if s.month, err = parseField(parts[3], 1, 12, false); err != nil {
		return nil, fmt.Errorf("月字段错误：%w", err)
	}
	if s.dow, err = parseField(parts[4], 0, 6, true); err != nil {
		return nil, fmt.Errorf("周字段错误：%w", err)
	}
	return s, nil
}

// parseField 解析单个字段
func parseField(spec string, min, max int, sunday7 bool) (field, error) {
	f := field{units: map[int]bool{}}
	if spec == "*" || spec == "?" {
		f.any = true
		return f, nil
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			v, err := strconv.Atoi(part[i+1:])
			if err != nil || v <= 0 {
				return f, fmt.Errorf("步长不合法：%s", part)
			}
			step = v
			part = part[:i]
		}
		lo, hi := min, max
		if part != "*" {
			if i := strings.Index(part, "-"); i >= 0 {
				var err error
				if lo, err = strconv.Atoi(part[:i]); err != nil {
					return f, fmt.Errorf("区间起点不合法：%s", part)
				}
				if hi, err = strconv.Atoi(part[i+1:]); err != nil {
					return f, fmt.Errorf("区间终点不合法：%s", part)
				}
			} else {
				v, err := strconv.Atoi(part)
				if err != nil {
					return f, fmt.Errorf("数值不合法：%s", part)
				}
				lo, hi = v, v
			}
		}
		if sunday7 {
			// 周日既可以写 0 也可以写 7
			if lo == 7 {
				lo = 0
			}
			if hi == 7 {
				f.units[0] = true
				hi = 6
			}
		}
		if lo < min || hi > max || lo > hi {
			return f, fmt.Errorf("超出取值范围 %d-%d：%s", min, max, part)
		}
		for v := lo; v <= hi; v += step {
			f.units[v] = true
		}
	}
	if len(f.units) == 0 && !f.any {
		return f, fmt.Errorf("字段没有可用取值：%s", spec)
	}
	return f, nil
}

// Next 返回 after 之后的第一个触发时刻（本地时区，秒为 0）
func (s *Schedule) Next(after time.Time) (time.Time, error) {
	if s.every > 0 {
		return after.Add(s.every).Truncate(time.Second), nil
	}
	base := after.Truncate(time.Minute).Add(time.Minute)
	// 最多往前找 2 年（cron 里有 2 月 30 日这种永远不触发的写法，避免死循环）
	limit := base.AddDate(2, 0, 0)
	for day := base; day.Before(limit); day = day.AddDate(0, 0, 1) {
		if !s.month.match(int(day.Month())) {
			continue
		}
		if !s.dom.match(day.Day()) {
			continue
		}
		if !s.dow.match(int(day.Weekday())) {
			continue
		}
		for h := 0; h < 24; h++ {
			if !s.hour.match(h) {
				continue
			}
			for m := 0; m < 60; m++ {
				if !s.minute.match(m) {
					continue
				}
				cand := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
				if cand.After(after) {
					return cand, nil
				}
			}
		}
		// 这一天的时刻都已经过去，跳到第二天 00:00
		day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location()).AddDate(0, 0, 1).Add(-time.Second)
	}
	return time.Time{}, fmt.Errorf("表达式 %q 在两年内没有触发时刻", s.raw)
}

// String 原始表达式
func (s *Schedule) String() string { return s.raw }
