package tools

import (
	"context"
	"strings"
	"testing"
)

/* ---------- shell 命令白名单 ---------- */

func TestShellWhitelistRejectsMetacharsAndUnknownCmd(t *testing.T) {
	ws := newTestWorkspace(t)
	sh := NewShellRun(ws, true).WithAllowCmds([]string{"echo", "ls"})
	ctx := context.Background()

	// 1) 白名单外的命令：拒绝（且错误里要列出允许什么）
	if _, err := sh.Run(ctx, map[string]any{"cmd": "rm -rf /"}); err == nil {
		t.Fatal("白名单外的命令必须被拒绝")
	} else if !strings.Contains(err.Error(), "不在白名单") {
		t.Fatalf("错误信息应说明不在白名单：%v", err)
	}

	// 2) 串联绕过：`ls; rm -rf ~` 的第一词是 ls，但整体必须被拒（这是白名单的关键）
	for _, bad := range []string{
		"ls; rm -rf /tmp/x",
		"ls | rm -rf /tmp/x",
		"ls && rm -rf /tmp/x",
		"ls > /etc/passwd",
		"echo $(rm -rf /tmp/x)",
		"echo `whoami`",
	} {
		if _, err := sh.Run(ctx, map[string]any{"cmd": bad}); err == nil {
			t.Fatalf("含有串联/重定向/命令替换的命令必须被拒绝：%q", bad)
		} else if !strings.Contains(err.Error(), "简单命令") {
			t.Fatalf("错误信息应说明只允许单条简单命令：%q → %v", bad, err)
		}
	}

	// 3) 白名单内的简单命令：放行（这里只验证"过了白名单闸门"，用 echo 断言输出）
	res, err := sh.Run(ctx, map[string]any{"cmd": "echo hello"})
	if err != nil {
		t.Fatalf("白名单内的命令应放行：%v", err)
	}
	if out, _ := res.(map[string]any)["output"].(string); !strings.Contains(out, "hello") {
		t.Fatalf("命令应有输出：%v", res)
	}
}

func TestShellWhitelistAcceptsPathAndExeSuffix(t *testing.T) {
	ws := newTestWorkspace(t)
	// 白名单里写 /bin/echo 与 notepad.exe：归一后应能匹配 echo / notepad
	sh := NewShellRun(ws, true).WithAllowCmds([]string{"/bin/echo", "notepad.exe"})
	got := sh.AllowCmds()
	if len(got) != 2 || got[0] != "echo" || got[1] != "notepad" {
		t.Fatalf("命令名归一不对：%v", got)
	}
	if _, err := sh.Run(context.Background(), map[string]any{"cmd": "echo ok"}); err != nil {
		t.Fatalf("带路径的白名单项应能匹配裸命令名：%v", err)
	}
}

func TestShellWithoutWhitelistKeepsOldBehaviour(t *testing.T) {
	ws := newTestWorkspace(t)
	sh := NewShellRun(ws, true) // 不设白名单
	if len(sh.AllowCmds()) != 0 {
		t.Fatal("没设白名单时不该凭空出现白名单")
	}
	// 不限制时，串联命令照旧能跑（旧行为），只是仍要过人工审批这一道闸门
	if _, err := sh.Run(context.Background(), map[string]any{"cmd": "echo 1 ; echo 2"}); err != nil {
		t.Fatalf("没设白名单时不该拦截：%v", err)
	}
}

func TestShellWhitelistDisabledStillRefuses(t *testing.T) {
	ws := newTestWorkspace(t)
	sh := NewShellRun(ws, false).WithAllowCmds([]string{"echo"})
	if _, err := sh.Run(context.Background(), map[string]any{"cmd": "echo hi"}); err == nil {
		t.Fatal("没开 allowShell 时必须明确报错，不能因为配了白名单就放行")
	}
}

/* ---------- 审批「记住放行」的判定口径 ---------- */

func TestCronRemoveAndShellNeedApproval(t *testing.T) {
	reg := NewRegistry()
	ws := newTestWorkspace(t)
	reg.Register(NewShellRun(ws, true))
	if !reg.IsDangerous("shell_run") {
		t.Fatal("shell_run 必须需要人工审批")
	}
}
