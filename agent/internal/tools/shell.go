package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ShellRun 执行系统命令（默认关闭：要显式开启，且每次都要过审批闸门）
type ShellRun struct {
	ws        *Workspace
	enabled   bool
	allowCmds []string // 命令白名单（空 = 不限制；非空时只允许单条简单命令）
}

// NewShellRun 创建 shell_run。enabled=false 时调用会明确报错，不会静默假装成功。
func NewShellRun(ws *Workspace, enabled bool) *ShellRun {
	return &ShellRun{ws: ws, enabled: enabled}
}

// WithAllowCmds 设置命令白名单（只比对命令名，大小写不敏感）。
//
// 语义（务必理解，否则会以为"配了白名单就安全了"）：
//   - 空白名单 = **不限制**：任何命令都能跑，只有人工审批这一道闸门（旧行为）。
//   - 非空白名单 = 只允许这些命令，并且**拒绝管道 / 重定向 / 串联 / 命令替换**
//     （`;` `|` `&` “ ` “ `$()` `>` `<` 换行）。
//     不拒绝的话，`ls; rm -rf ~` 里第一个词是 ls、照样能通过白名单比对——
//     那种白名单只是给人心理安慰，不如没有。
func (t *ShellRun) WithAllowCmds(cmds []string) *ShellRun {
	out := make([]string, 0, len(cmds))
	seen := map[string]bool{}
	for _, c := range cmds {
		c = normalizeCmdName(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	t.allowCmds = out
	return t
}

// AllowCmds 当前白名单（给日志/自检看）
func (t *ShellRun) AllowCmds() []string { return append([]string{}, t.allowCmds...) }

// Name 工具名
func (t *ShellRun) Name() string { return "shell_run" }

// Description 说明
func (t *ShellRun) Description() string {
	return "在工作目录内执行一条系统命令并返回输出（危险：默认关闭，需要人工审批）"
}

// Schema 参数说明
func (t *ShellRun) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cmd":        map[string]any{"type": "string", "description": "要执行的命令"},
			"cwd":        map[string]any{"type": "string", "description": "工作子目录，默认工作目录根"},
			"timeoutSec": map[string]any{"type": "integer", "description": "超时秒数，默认 60"},
		},
		"required": []string{"cmd"},
	}
}

// Dangerous 执行命令必须人工审批
func (t *ShellRun) Dangerous() bool { return true }

// Mutating 可能改工作目录 → 执行前自动打快照
func (t *ShellRun) Mutating() bool { return true }

// Enabled 是否已开启
func (t *ShellRun) Enabled() bool { return t.enabled }

// Run 执行
func (t *ShellRun) Run(ctx context.Context, args map[string]any) (any, error) {
	if !t.enabled {
		return nil, errors.New("本端未开启命令执行：需要显式加 --allow-shell 才会执行 shell_run")
	}
	cmdline := ArgString(args, "cmd")
	if cmdline == "" {
		return nil, errors.New("缺少参数 cmd")
	}
	// 命令白名单：非空时只允许"单条简单命令"，且命令名必须在白名单里
	if len(t.allowCmds) > 0 {
		if bad := shellMetaIn(cmdline); bad != "" {
			return nil, fmt.Errorf("命令白名单已启用，不接受 %q：只允许单条简单命令（不支持管道 / 重定向 / 串联 / 命令替换）", bad)
		}
		name := firstCmdName(cmdline)
		if !cmdAllowed(t.allowCmds, name) {
			return nil, fmt.Errorf("命令 %q 不在白名单里（允许：%s）", name, strings.Join(t.allowCmds, ", "))
		}
	}
	dir := t.ws.Root()
	if cwd := ArgString(args, "cwd"); cwd != "" {
		abs, err := t.ws.Resolve(cwd)
		if err != nil {
			return nil, err
		}
		dir = abs
	}
	timeout := ArgInt(args, "timeoutSec", 60)
	if timeout <= 0 {
		timeout = 60
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(runCtx, "cmd", "/C", cmdline)
	} else {
		cmd = exec.CommandContext(runCtx, "sh", "-c", cmdline)
	}
	cmd.Dir = dir
	buf := &limitedBuffer{limit: 64 * 1024}
	cmd.Stdout = buf
	cmd.Stderr = buf

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start).Milliseconds()

	res := map[string]any{
		"cmd":        cmdline,
		"cwd":        dir,
		"durationMs": dur,
		"output":     buf.String(),
		"truncated":  buf.truncated,
	}
	if runCtx.Err() == context.DeadlineExceeded {
		res["exitCode"] = -1
		res["timeout"] = true
		return res, fmt.Errorf("命令超时（%d 秒）", timeout)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res["exitCode"] = exitErr.ExitCode()
			return res, fmt.Errorf("命令退出码 %d", exitErr.ExitCode())
		}
		return res, err
	}
	res["exitCode"] = 0
	return res, nil
}

/* ---------- 命令白名单的判定 ---------- */

// shellMetaChars 白名单模式下不允许出现的 shell 元字符（出现即拒绝，理由见 WithAllowCmds）
var shellMetaChars = []string{";", "|", "&", "`", "$", ">", "<", "\n", "\r", "(", ")"}

// shellMetaIn 返回命令里出现的第一个危险元字符（没有则空串）
func shellMetaIn(cmdline string) string {
	for _, m := range shellMetaChars {
		if strings.Contains(cmdline, m) {
			return m
		}
	}
	return ""
}

// firstCmdName 取命令名（第一个空白分隔的词），并做归一
func firstCmdName(cmdline string) string {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return ""
	}
	return normalizeCmdName(fields[0])
}

// normalizeCmdName 命令名归一：小写、去引号、去 Windows 上常见的 .exe/.cmd/.bat 后缀
func normalizeCmdName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Trim(s, `"'`)
	// 带路径时只取文件名（/bin/ls → ls）
	if i := strings.LastIndexAny(s, `/\`); i >= 0 && i+1 < len(s) {
		s = s[i+1:]
	}
	for _, ext := range []string{".exe", ".cmd", ".bat", ".com"} {
		s = strings.TrimSuffix(s, ext)
	}
	return strings.TrimSpace(s)
}

// cmdAllowed 命令名是否在白名单里
func cmdAllowed(allow []string, name string) bool {
	if name == "" {
		return false
	}
	for _, a := range allow {
		if a == name {
			return true
		}
	}
	return false
}

// limitedBuffer 限长缓冲，避免命令输出把内存打满
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.limit = 64 * 1024
	}
	if b.buf.Len() >= b.limit {
		b.truncated = true
		return len(p), nil
	}
	remain := b.limit - b.buf.Len()
	if len(p) > remain {
		b.buf.Write(p[:remain])
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string { return strings.TrimRight(b.buf.String(), "\n") }
