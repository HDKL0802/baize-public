// 版本号比较。桌面端升级只认 "1.2.3" 这种点分数字（本项目就是这么发的），
// 解析不出来的（比如带 -rc 后缀）一律当"不比对方新" —— 宁可漏升，不可误升。
package main

import (
	"strconv"
	"strings"
)

func parseVersion(v string) ([]int, bool) {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// newerVersion 报告 cand 是否比 cur 新（严格大于）。短的那边缺位按 0 算：0.2 == 0.2.0。
func newerVersion(cand, cur string) bool {
	a, okA := parseVersion(cand)
	b, okB := parseVersion(cur)
	if !okA || !okB {
		return false
	}
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}
