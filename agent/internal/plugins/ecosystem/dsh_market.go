// Package ecosystem 放 DSH 生态市场的来源凭证与内嵌索引数据。
//
// 本包只依赖标准库：plugins 包会 //go:embed 本目录下的 dsh-market.index.json，
// 而 ecology 包又依赖 plugins —— 让 ecosystem 去 import ecology 会成环，所以
// EcosystemID 在这里独立实现一份等价函数。
package ecosystem

import "strings"

// DSH 生态市场的地址常量（与 plugins 包里的同名常量保持一致；本包只放数据侧，
// 不 import plugins，避免与 ecology 形成循环依赖）。
const (
	DshMarketSourceURL  = "builtin:dsh-market"
	DshMarketSourceName = "DSH 生态市场（内置）"
	DshMarketPkgPrefix  = "builtin:dsh-market/"
)

// DshNotInstallableMsg 与 plugins 包里的错误文案逐字一致（便于测试直接引用）。
const DshNotInstallableMsg = "这是 DSH 生态插件（npm / cordis 运行时形态），白泽不能直接安装。请用 POST /api/agent/ecology/dsh/sync 把所需插件转换成白泽插件源后再安装。"

// EcosystemID 与 ecology.EcosystemID 完全等价：去 @、斜杠换点、转小写、非法字符换
// 连字符、去首尾 .-_、截到 64 字符。独立实现是为了避免循环依赖（见包注释）。
func EcosystemID(name string) string {
	s := strings.TrimSpace(name)
	s = strings.TrimPrefix(s, "@")
	s = strings.ReplaceAll(s, "/", ".")
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), ".-_")
	if out == "" {
		return ""
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}
