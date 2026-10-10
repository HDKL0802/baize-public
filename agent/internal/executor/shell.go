package executor

// 执行一条命令（电脑控制第 2 档）。
//
// ⚠️ 这个动作**默认是关着的**：调用方（Run）只有在 DryRun 或显式 AllowDanger 时才会走到这里。
// 桌面端那条独立的路由另有一套更严的闸门（用户白名单 + 界面审批），见 desktop/shell_windows.go。
//
// 这里不做命令白名单（那是上层策略），只负责：超时、输出上限、跨平台起 shell。

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	execOutputMax   = 256 << 10 // 输出上限 256KB
	execTimeoutMax  = 300       // 单次最长 300 秒
	execTimeoutDflt = 60
)

// execCommand 执行命令并回「退出码 + 合并输出」；dryRun 时只回计划不真跑。
func execCommand(cmdArg, cwd, timeoutArg any, dryRun bool) map[string]any {
	command := strings.TrimSpace(fmt.Sprint(cmdArg))
	if command == "" {
		return map[string]any{"ok": false, "error": "缺少参数 cmd"}
	}
	timeout := execTimeoutDflt
	switch v := timeoutArg.(type) {
	case float64:
		timeout = int(v)
	case int:
		timeout = v
	}
	if timeout <= 0 || timeout > execTimeoutMax {
		timeout = execTimeoutDflt
	}
	dir := strings.TrimSpace(fmt.Sprint(cwd))
	if dryRun {
		return map[string]any{"ok": true, "dryRun": true, "command": command, "cwd": dir, "timeoutSec": timeout}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.CommandContext(ctx, "cmd", "/c", command)
	} else {
		c = exec.CommandContext(ctx, "sh", "-c", command)
	}
	if dir != "" {
		c.Dir = dir
	}
	out, err := c.CombinedOutput()
	truncated := false
	if len(out) > execOutputMax {
		out = out[:execOutputMax]
		truncated = true
	}
	res := map[string]any{"command": command, "output": string(out), "truncated": truncated, "timeoutSec": timeout}
	if ctx.Err() == context.DeadlineExceeded {
		res["timedOut"] = true
		res["ok"] = false
		res["error"] = fmt.Sprintf("命令超时（超过 %d 秒），已终止", timeout)
		return res
	}
	if err != nil {
		res["ok"] = false
		res["error"] = err.Error()
		if ee, ok := err.(*exec.ExitError); ok {
			res["exitCode"] = ee.ExitCode()
		}
		return res
	}
	res["ok"] = true
	res["exitCode"] = 0
	return res
}
