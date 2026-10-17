//go:build windows

package main

import (
	"testing"

	"baize/shared/proto"
)

// setGuiForTest 改全局「允许控制 GUI」配置（测试用；用完恢复）
func setGuiForTest(t *testing.T, on bool, apps []string) {
	t.Helper()
	cfgMu.Lock()
	oldOn, oldApps := cfg.GuiControl, cfg.GuiControlAllowApps
	cfg.GuiControl, cfg.GuiControlAllowApps = on, cleanAppList(apps)
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.GuiControl, cfg.GuiControlAllowApps = oldOn, oldApps
		cfgMu.Unlock()
	})
}

// 目标窗口白名单：开关、空清单、命中、未命中、截图不受白名单约束 —— 逐条都要求正确。
func TestGuiGate(t *testing.T) {
	// 开关没开：一律拒绝
	setGuiForTest(t, false, []string{"notepad"})
	if err := guiGate(proto.ActionGuiClick, "notepad"); err == nil {
		t.Fatal("没开开关不该放行")
	}
	if err := guiGate(proto.ActionGuiScreenshot, ""); err == nil {
		t.Fatal("没开开关不该放行截图")
	}
	// 开了但清单空：点击/输入/按键全拒；截图（只读）放行
	setGuiForTest(t, true, nil)
	if err := guiGate(proto.ActionGuiClick, "notepad"); err == nil {
		t.Fatal("空白名单不该放行点击")
	}
	if err := guiGate(proto.ActionGuiInput, "notepad"); err == nil {
		t.Fatal("空白名单不该放行输入")
	}
	if err := guiGate(proto.ActionGuiKey, "notepad"); err == nil {
		t.Fatal("空白名单不该放行按键")
	}
	if err := guiGate(proto.ActionGuiScreenshot, ""); err != nil {
		t.Fatalf("空白名单下截图（只读）应放行：%v", err)
	}
	// 有清单
	setGuiForTest(t, true, []string{"notepad", "chrome"})
	if err := guiGate(proto.ActionGuiClick, "notepad"); err != nil {
		t.Fatalf("白名单内应放行：%v", err)
	}
	if err := guiGate(proto.ActionGuiKey, "Chrome.exe"); err != nil {
		t.Fatalf("进程名应归一后命中（Chrome.exe → chrome）：%v", err)
	}
	// 认不出目标窗口：拒绝盲发
	if err := guiGate(proto.ActionGuiInput, ""); err == nil {
		t.Fatal("认不出目标窗口不该放行")
	}
	// 不在清单里
	if err := guiGate(proto.ActionGuiClick, "mspaint"); err == nil {
		t.Fatal("不在白名单里不该放行")
	}
}

// 应用名归一：与进程名口径一致（小写、去 .exe/.dll）
func TestNormalizeAppName(t *testing.T) {
	cases := map[string]string{
		"NOTEPAD":        "notepad",
		"chrome.exe":     "chrome",
		" CHROME.EXE ":   "chrome",
		`"C:\x\win.dll"`: `c:\x\win`, // 路径归一不在这里做；只去后缀/小写
		"7z":             "7z",
		"":               "",
	}
	for in, want := range cases {
		if got := normalizeAppName(in); got != want {
			t.Errorf("normalizeAppName(%q)=%q，期望 %q", in, got, want)
		}
	}
}

// cleanAppList：去空、去重、归一
func TestCleanAppList(t *testing.T) {
	got := cleanAppList([]string{"notepad", " NOTEPAD ", "notepad.exe", "", "  ", "chrome.EXE"})
	if len(got) != 2 || got[0] != "notepad" || got[1] != "chrome" {
		t.Fatalf("归一结果不对：%+v", got)
	}
}

// 按键解析：单字符 / 具名键 / 功能键 / 组合键 / 认不出的要报错
func TestParseKeySpec(t *testing.T) {
	cases := []struct {
		in      string
		vk      uint16
		wantErr bool
	}{
		{"a", 0x41, false},
		{"Z", 0x5A, false},
		{"5", 0x35, false},
		{"enter", 0x0D, false},
		{"ctrl", 0x11, false},
		{"f1", 0x70, false},
		{"f24", 0x87, false},
		{"f25", 0, true}, // 超出范围
		{"bogus", 0, true},
		{"", 0, true},
	}
	for _, c := range cases {
		got, err := parseKeySpec(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseKeySpec(%q) 应报错，实际 vk=%#x", c.in, got.vk)
			}
			continue
		}
		if err != nil || got.vk != c.vk {
			t.Errorf("parseKeySpec(%q) = (%#x, %v)，期望 vk=%#x", c.in, got.vk, err, c.vk)
		}
	}
}

func TestParseKeyCombo(t *testing.T) {
	specs, err := parseKeyCombo("ctrl+shift+s")
	if err != nil || len(specs) != 3 {
		t.Fatalf("组合键解析不对：%v %v", specs, err)
	}
	if specs[0].vk != 0x11 || specs[1].vk != 0x10 || specs[2].vk != 0x53 {
		t.Fatalf("虚拟键码不对：%+v", specs)
	}
	if _, err := parseKeyCombo("ctrl++s"); err == nil {
		t.Fatal("空片段应报错")
	}
	if _, err := parseKeyCombo("ctrl+bogus"); err == nil {
		t.Fatal("认不出的键应报错")
	}
}

// 输入上限与参数缺失：如实拒绝，不假装成功
func TestGuiInputArgs(t *testing.T) {
	if r := guiInput(map[string]any{}); r["ok"] != false {
		t.Fatal("缺 text 参数应失败")
	}
	long := make([]rune, guiInputMaxChars+1)
	for i := range long {
		long[i] = 'a'
	}
	if r := guiInput(map[string]any{"text": string(long)}); r["ok"] != false {
		t.Fatalf("超长输入应拒绝：%v", r)
	}
}

// 点击：button 认不出要报错（不默认偷跑 left）
func TestGuiClickButton(t *testing.T) {
	if r := guiClick(map[string]any{"x": 1, "y": 2, "button": "thumb"}); r["ok"] != false {
		t.Fatal("不认识的鼠标键应失败")
	}
}

// 设置侧：开开关必须给 ack；白名单可以为空（= 只剩截图）
func TestSetGuiControlValidation(t *testing.T) {
	cfgMu.Lock()
	oldOn, oldApps := cfg.GuiControl, cfg.GuiControlAllowApps
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.GuiControl, cfg.GuiControlAllowApps = oldOn, oldApps
		cfgMu.Unlock()
	})
	// 没有 ack：拒绝
	if err := setGuiControl(true, []string{"notepad"}, false); err == nil {
		t.Fatal("开启时应要求确认风险")
	}
	// 合规开启（带白名单）
	if err := setGuiControl(true, []string{"notepad", "Chrome.EXE"}, true); err != nil {
		t.Fatalf("合规开启应成功：%v", err)
	}
	if !guiControlEnabled() {
		t.Fatal("开启后 guiControlEnabled 应为 true")
	}
	allow := guiAllowApps()
	if len(allow) != 2 || allow[0] != "notepad" || allow[1] != "chrome" {
		t.Fatalf("白名单应归一（Chrome.EXE → chrome）：%v", allow)
	}
	// 合规开启（空白名单 = 只许截图）
	if err := setGuiControl(true, nil, true); err != nil {
		t.Fatalf("空白名单也应允许开启（截图仍可用）：%v", err)
	}
	// 关掉（不需要 ack）
	if err := setGuiControl(false, nil, false); err != nil {
		t.Fatalf("关闭不该报错：%v", err)
	}
	if guiControlEnabled() {
		t.Fatal("关闭后 guiControlEnabled 应为 false")
	}
}

// permCaps：开关开了才上报 gui.control（没开就"根本派不过来"）。
// 注意：permCaps 先按文件权限档位分（PermOff 直接返回空），所以测试里把档位抬到只读。
func TestPermCapsGui(t *testing.T) {
	cfgMu.Lock()
	oldPerm := cfg.GuiPerm
	cfg.GuiPerm = PermReadOnly // 真实进程 loadConfig 后至少是只读
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.GuiPerm = oldPerm
		cfgMu.Unlock()
	})

	setGuiForTest(t, false, []string{"notepad"})
	if contains(permCaps(), proto.CapGuiControl) {
		t.Fatal("没开 GUI 控制不该上报 gui.control")
	}
	setGuiForTest(t, true, []string{"notepad"})
	if !contains(permCaps(), proto.CapGuiControl) {
		t.Fatal("开了 GUI 控制应上报 gui.control")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
