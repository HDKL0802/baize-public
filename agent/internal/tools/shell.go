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
	ws      *Workspace
	enabled bool
}

// NewShellRun 创建 shell_run。enabled=false 时调用会明确报错，不会静默假装成功。
func NewShellRun(ws *Workspace, enabled bool) *ShellRun {
	return &ShellRun{ws: ws, enabled: enabled}
}

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
