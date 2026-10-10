//go:build windows

// 电脑控制 · 第 2 档：让白泽在本机执行一条命令。
//
// 高危能力，三重闸门叠加，缺一不可：
//  1. **本机开关**：必须在「设置 → 桌面控制 → 允许执行命令」显式打开（默认关，要过风险确认）；
//  2. **命令白名单**：命令名（basename、去掉 .exe）必须在你圈定的清单里，**空清单 = 一条都不许跑**；
//     并且拒绝管道 / 重定向 / 串联 / 命令替换（`;` `|` `&` 反引号 `$` `>` `<` 换行）——
//     不拒绝的话 `dir; rd /s /q D:\` 的第一个词是 dir、照样能过白名单，那种白名单只是心理安慰；
//  3. **后端人工审批**：`sys.exec` 是危险动作，每次派下来都要在界面上点「批准并执行」。
//
// 另外还有执行侧的护栏：固定超时、输出上限、不经 cmd 的引号拼接（整条命令原样交给 cmd /c，
// 因为用户本来就白名单了这条命令的确切名字）。
package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	shellTimeoutMax = 300       // 单次执行最长 300 秒（要更久就自己拆）
	shellOutputMax  = 256 << 10 // 输出上限 256KB（超出截断并标注）
)

// shellMeta 这些字符一律拒绝：它们能把"一条命令"变成"一串命令"，
// 白名单只比对第一个词，放行就等于开后门。
var shellMeta = []string{";", "|", "&", "`", "$", ">", "<", "\n", "\r", "&&", "||"}

// shellGate 逐条校验命令：开关 + 白名单 + 元字符。
func shellGate(command string) error {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return fmt.Errorf("命令不能为空")
	}
	if !shellEnabled() {
		return fmt.Errorf("本机没开「允许执行命令」（设置 → 桌面控制）")
	}
	allow := shellAllowList()
	if len(allow) == 0 {
		return fmt.Errorf("本机命令白名单是空的（一条都不允许）")
	}
	for _, m := range shellMeta {
		if strings.Contains(cmd, m) {
			return fmt.Errorf("命令里不允许出现 %q（管道/重定向/串联/命令替换都会被拒；白名单只比对第一个词，放行等于开后门）", m)
		}
	}
	first := cmd
	if i := strings.IndexAny(first, " \t"); i >= 0 {
		first = first[:i]
	}
	name := normalizeCmdName(first)
	if name == "" {
		return fmt.Errorf("认不出要执行什么命令")
	}
	for _, a := range allow {
		if name == a {
			return nil
		}
	}
	return fmt.Errorf("命令 %q 不在本机白名单里（当前允许：%s）；要放开请到「设置 → 桌面控制 → 允许执行命令」里加",
		name, strings.Join(allow, "、"))
}

// runShellCommand 在本机执行一条命令（整条命令原样交给 cmd /c），回退出码与合并输出。
func runShellCommand(command, cwd string, timeoutSec int) map[string]any {
	if timeoutSec <= 0 || timeoutSec > shellTimeoutMax {
		timeoutSec = 60
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "cmd", "/c", command)
	if strings.TrimSpace(cwd) != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	truncated := false
	if len(out) > shellOutputMax {
		out = out[:shellOutputMax]
		truncated = true
	}
	res := map[string]any{
		"command":    command,
		"output":     string(out),
		"truncated":  truncated,
		"timeoutSec": timeoutSec,
	}
	if ctx.Err() == context.DeadlineExceeded {
		res["timedOut"] = true
		res["error"] = fmt.Sprintf("命令超时（超过 %d 秒），已终止", timeoutSec)
		return res
	}
	if err != nil {
		res["error"] = err.Error()
		if ee, ok := err.(*exec.ExitError); ok {
			res["exitCode"] = ee.ExitCode()
		}
		return res
	}
	res["exitCode"] = 0
	res["ok"] = true
	return res
}
