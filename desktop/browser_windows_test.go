//go:build windows

package main

import (
	"strings"
	"testing"

	"baize/shared/proto"
)

// setBrowserForTest 改全局浏览器控制配置（测试用；用完恢复）
func setBrowserForTest(t *testing.T, on, allowChrome bool) {
	t.Helper()
	cfgMu.Lock()
	oldOn, oldChrome, oldToken := cfg.BrowserControl, cfg.BrowserAllowChrome, cfg.BrowserExtToken
	cfg.BrowserControl, cfg.BrowserAllowChrome = on, allowChrome
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.BrowserControl, cfg.BrowserAllowChrome, cfg.BrowserExtToken = oldOn, oldChrome, oldToken
		cfgMu.Unlock()
	})
}

// resetBuiltinForTest 把「当前内置浏览器进程」清空（测试里不想真的连一个窗口）
func resetBuiltinForTest(t *testing.T) {
	t.Helper()
	builtinMu.Lock()
	old := builtinCur
	builtinCur = nil
	builtinMu.Unlock()
	t.Cleanup(func() {
		builtinMu.Lock()
		builtinCur = old
		builtinMu.Unlock()
	})
}

func TestIsBrowserAction(t *testing.T) {
	for _, a := range []string{
		proto.ActionBrowserOpen, proto.ActionBrowserRead, proto.ActionBrowserClick,
		proto.ActionBrowserType, proto.ActionBrowserKey, proto.ActionBrowserShot,
		proto.ActionBrowserStatus, proto.ActionBrowserHandoff,
	} {
		if !isBrowserAction(a) {
			t.Fatalf("%s 应被识别为浏览器动作", a)
		}
	}
	for _, a := range []string{proto.ActionPing, proto.ActionGuiClick, "browser.unknown"} {
		if isBrowserAction(a) {
			t.Fatalf("%s 不该被识别为浏览器动作", a)
		}
	}
}

// 总开关：没开时所有 browser.* 一律拒绝，开了才放行（与 gui 同一套纪律）
func TestBrowserPermGate(t *testing.T) {
	setBrowserForTest(t, false, false)
	for _, a := range []string{proto.ActionBrowserOpen, proto.ActionBrowserRead, proto.ActionBrowserStatus} {
		if err := permGate(a, nil); err == nil {
			t.Fatalf("没开「允许控制浏览器」不该放行 %s", a)
		}
	}
	setBrowserForTest(t, true, false)
	for _, a := range []string{proto.ActionBrowserOpen, proto.ActionBrowserClick, proto.ActionBrowserStatus} {
		if err := permGate(a, nil); err != nil {
			t.Fatalf("开了之后应放行 %s：%v", a, err)
		}
	}
}

// 开启必须过风险确认；allowChrome 只在总开关打开时生效；令牌自动生成
func TestSetBrowserControlValidation(t *testing.T) {
	cfgMu.Lock()
	oldOn, oldChrome, oldToken, oldAck := cfg.BrowserControl, cfg.BrowserAllowChrome, cfg.BrowserExtToken, cfg.DisclaimerAck
	cfg.BrowserExtToken = ""
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.BrowserControl, cfg.BrowserAllowChrome, cfg.BrowserExtToken, cfg.DisclaimerAck = oldOn, oldChrome, oldToken, oldAck
		cfgMu.Unlock()
	})

	if err := setBrowserControl(true, false, false); err == nil {
		t.Fatal("没有 ack 不该允许开启")
	}
	if err := setBrowserControl(true, true, true); err != nil {
		t.Fatalf("带 ack 开启应成功：%v", err)
	}
	cfgMu.RLock()
	on, chrome, token := cfg.BrowserControl, cfg.BrowserAllowChrome, cfg.BrowserExtToken
	cfgMu.RUnlock()
	if !on || !chrome {
		t.Fatalf("开关状态不对：on=%v allowChrome=%v", on, chrome)
	}
	if len(token) < 16 {
		t.Fatalf("应自动生成令牌，实际 %q", token)
	}
	// 关掉总开关时，Chrome 扩展也必须跟着关（不能留个后门）
	if err := setBrowserControl(false, true, false); err != nil {
		t.Fatalf("关闭不该要求 ack：%v", err)
	}
	cfgMu.RLock()
	on, chrome = cfg.BrowserControl, cfg.BrowserAllowChrome
	cfgMu.RUnlock()
	if on || chrome {
		t.Fatalf("关闭后仍开着：on=%v allowChrome=%v", on, chrome)
	}
}

func TestExtToken(t *testing.T) {
	a, b := newExtToken(), newExtToken()
	if len(a) < 16 {
		t.Fatalf("令牌太短：%q", a)
	}
	if a == b {
		t.Fatal("两次生成的令牌不该相同")
	}
}

// 扩展没连上时，必须如实说「还没连上」，不能假装成功
func TestExtCallNotConnected(t *testing.T) {
	closeExtClient()
	if _, err := extCall(proto.ActionBrowserRead, nil); err == nil {
		t.Fatal("扩展没连上时不该成功")
	} else if !strings.Contains(err.Error(), "还没连上") {
		t.Fatalf("错误信息要说清原因，实际：%v", err)
	}
}

// 外部 Chrome 没有「移交」概念：要明确拒绝，不能静默当成功
func TestExtHandoffRejected(t *testing.T) {
	if _, err := extCall(proto.ActionBrowserHandoff, map[string]any{"handoff": true}); err == nil {
		t.Fatal("外部 Chrome 的 handoff 应被拒绝")
	}
}

// 不认识的 target：明确报错（避免 AI 以为动作生效了）
func TestBrowserUnknownTarget(t *testing.T) {
	setBrowserForTest(t, true, true)
	if _, err := browserAction(proto.ActionBrowserRead, map[string]any{"target": "wechat"}); err == nil {
		t.Fatal("不认识的 target 应报错")
	}
}

// 内置浏览器没打开时，status 返回 open=false（不自动拉起窗口）
func TestBrowserStatusWhenClosed(t *testing.T) {
	resetBuiltinForTest(t)
	res, err := browserAction(proto.ActionBrowserStatus, map[string]any{"target": "builtin"})
	if err != nil {
		t.Fatalf("status 不该失败：%v", err)
	}
	if res["open"] != false {
		t.Fatalf("没打开时应返回 open=false，实际 %v", res["open"])
	}
}

// 没打开时，其他动作要明确提示「先打开」
func TestBuiltinActionWithoutWindow(t *testing.T) {
	resetBuiltinForTest(t)
	if _, err := browserAction(proto.ActionBrowserRead, nil); err == nil {
		t.Fatal("没打开内置浏览器时 read 应报错")
	} else if !strings.Contains(err.Error(), "没打开") {
		t.Fatalf("错误信息要说清原因，实际：%v", err)
	}
}

// 扩展目录：开发/安装两种布局都要能定位到 browser-ext（找不到也要给出明确路径）
func TestBrowserExtDir(t *testing.T) {
	dir := browserExtDir()
	if dir == "" || !strings.Contains(strings.ToLower(dir), "browser-ext") {
		t.Fatalf("扩展目录不对：%q", dir)
	}
}

// 截图入口的护栏：无效窗口句柄要报错，不能崩
func TestCaptureInvalidWindow(t *testing.T) {
	if _, _, _, err := captureBrowserWindow(0); err == nil {
		t.Fatal("句柄 0 应报错")
	}
}

// 能力上报：开了浏览器控制才带 browser.control
func TestPermCapsBrowser(t *testing.T) {
	cfgMu.Lock()
	oldPerm, oldOn := cfg.GuiPerm, cfg.BrowserControl
	cfg.GuiPerm = PermReadOnly
	cfg.BrowserControl = false
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.GuiPerm, cfg.BrowserControl = oldPerm, oldOn
		cfgMu.Unlock()
	})

	for _, c := range permCaps() {
		if c == proto.CapBrowser {
			t.Fatal("没开浏览器控制时不该上报 browser.control")
		}
	}
	cfgMu.Lock()
	cfg.BrowserControl = true
	cfgMu.Unlock()
	found := false
	for _, c := range permCaps() {
		if c == proto.CapBrowser {
			found = true
		}
	}
	if !found {
		t.Fatal("开了浏览器控制后应上报 browser.control")
	}
}
