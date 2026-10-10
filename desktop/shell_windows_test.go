//go:build windows

package main

import (
	"testing"
)

// setShellForTest 改全局「允许执行命令」配置（测试用；用完恢复）
func setShellForTest(t *testing.T, on bool, cmds []string) {
	t.Helper()
	cfgMu.Lock()
	oldOn, oldCmds := cfg.GuiShell, cfg.GuiShellAllowCmds
	cfg.GuiShell, cfg.GuiShellAllowCmds = on, cleanCmdList(cmds)
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.GuiShell, cfg.GuiShellAllowCmds = oldOn, oldCmds
		cfgMu.Unlock()
	})
}

// 命令白名单：开关、空清单、命中、元字符、未命中 —— 逐条都要求正确。
func TestShellGate(t *testing.T) {
	// 开关没开：一律拒绝
	setShellForTest(t, false, []string{"dir"})
	if err := shellGate("dir"); err == nil {
		t.Fatal("没开开关不该放行")
	}
	// 开了但清单空：等于一条都不许跑
	setShellForTest(t, true, nil)
	if err := shellGate("dir"); err == nil {
		t.Fatal("空清单不该放行")
	}
	// 开了且有清单
	setShellForTest(t, true, []string{"dir", "ipconfig"})
	if err := shellGate("dir"); err != nil {
		t.Fatalf("白名单内应放行：%v", err)
	}
	if err := shellGate("DIR.EXE /w"); err != nil {
		t.Fatalf("命令名应归一后命中（DIR.EXE → dir）：%v", err)
	}
	if err := shellGate(`C:\Windows\System32\ipconfig.exe /all`); err != nil {
		t.Fatalf("带路径应取 basename 命中：%v", err)
	}
	// 元字符：白名单只比对第一个词，放行等于开后门
	for _, bad := range []string{
		"dir; rd /s /q D:\\",
		"dir && format d:",
		"dir | findstr x",
		"dir > out.txt",
		"dir $(whoami)",
		"dir `whoami`",
		"dir\nrd /s /q D:\\",
	} {
		if err := shellGate(bad); err == nil {
			t.Fatalf("含元字符的命令不该放行：%q", bad)
		}
	}
	// 不在清单里
	if err := shellGate("format"); err == nil {
		t.Fatal("不在白名单里不该放行")
	}
	// 空命令
	if err := shellGate("   "); err == nil {
		t.Fatal("空命令应报错")
	}
}

// 命令名归一：与后端 shell 白名单一个口径
func TestNormalizeCmdName(t *testing.T) {
	cases := map[string]string{
		"DIR":                            "dir",
		"dir.exe":                        "dir",
		`"C:\Windows\System32\PING.EXE"`: "ping",
		"/usr/bin/echo":                  "echo",
		"  notepad.cmd ":                 "notepad",
		"7z.bat":                         "7z",
		"":                               "",
	}
	for in, want := range cases {
		if got := normalizeCmdName(in); got != want {
			t.Errorf("normalizeCmdName(%q)=%q，期望 %q", in, got, want)
		}
	}
}

// cleanCmdList：去空、去重、归一
func TestCleanCmdList(t *testing.T) {
	got := cleanCmdList([]string{"dir", " DIR ", "dir.exe", "", "  ", "ping.EXE"})
	if len(got) != 2 || got[0] != "dir" || got[1] != "ping" {
		t.Fatalf("归一结果不对：%+v", got)
	}
}

// 设置侧：开开关必须给 ack，且清单不能空
func TestSetGuiShellValidation(t *testing.T) {
	cfgMu.Lock()
	oldOn, oldCmds := cfg.GuiShell, cfg.GuiShellAllowCmds
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.GuiShell, cfg.GuiShellAllowCmds = oldOn, oldCmds
		cfgMu.Unlock()
	})
	// 没有 ack：拒绝
	if err := setGuiShell(true, []string{"dir"}, false); err == nil {
		t.Fatal("开启时应要求确认风险")
	}
	// 清单为空：拒绝
	if err := setGuiShell(true, nil, true); err == nil {
		t.Fatal("开启时空清单应被拒")
	}
	// 正常开启
	if err := setGuiShell(true, []string{"dir"}, true); err != nil {
		t.Fatalf("合规开启应成功：%v", err)
	}
	if !shellEnabled() {
		t.Fatal("开启后 shellEnabled 应为 true")
	}
	// 关掉（不需要 ack）
	if err := setGuiShell(false, nil, false); err != nil {
		t.Fatalf("关闭不该报错：%v", err)
	}
	if shellEnabled() {
		t.Fatal("关闭后 shellEnabled 应为 false")
	}
}
