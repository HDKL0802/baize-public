//go:build windows

// 让 Windows 原生标题栏不再是刺眼的白条，且**跟着主题走**（亮色界面配白条、暗色配深条）。
// Windows 11 支持精确指定 标题/文字/边框 颜色（属性 34/35/36）；
// 更老的系统只支持「沉浸式深色」开关（19/20）。两个都试，失败就算了（不影响功能）。
package main

import (
	"log"
	"syscall"
	"unsafe"
)

var (
	dwmapi            = syscall.NewLazyDLL("dwmapi.dll")
	pDwmSetWindowAttr = dwmapi.NewProc("DwmSetWindowAttribute")

	// 注册表（读系统深浅色偏好），和球/浮窗那套一样直接走 advapi32，不引新依赖
	advapi32          = syscall.NewLazyDLL("advapi32.dll")
	pRegOpenKeyExW    = advapi32.NewProc("RegOpenKeyExW")
	pRegCloseKey      = advapi32.NewProc("RegCloseKey")
	pRegQueryValueExW = advapi32.NewProc("RegQueryValueExW")
)

const (
	hkeyCurrentUser = 0x80000001 // HKEY_CURRENT_USER
)

const (
	dwmwaImmersiveDarkOld = 19 // Win10 1809 ~ 20H1
	dwmwaImmersiveDark    = 20 // Win10 20H1+ / Win11
	dwmwaBorderColor      = 34 // Win11 22000+
	dwmwaCaptionColor     = 35
	dwmwaTextColor        = 36
)

// applyTitleBarTheme 把主窗口的系统标题栏刷成与当前主题同侧的颜色。
// 只动颜色属性，不隐藏标题栏；刷新靠 swpFrameChanged 让非客户区重画（不会闪窗口）。
func applyTitleBarTheme(hwnd uintptr, dark bool) {
	if hwnd == 0 {
		return
	}
	set := func(attr uint32, val uint32) bool {
		hr, _, _ := pDwmSetWindowAttr.Call(hwnd, uintptr(attr), uintptr(unsafe.Pointer(&val)), unsafe.Sizeof(val))
		return hr == 0
	}
	var caption, text, border uint32
	if dark {
		if !set(dwmwaImmersiveDark, 1) {
			set(dwmwaImmersiveDarkOld, 1)
		}
		caption = 0x001d1612 // #12161d 界面 --surface
		text = 0x00eee4db    // #dbe4ee 界面 --text
		border = 0x00232a35  // #232a35 界面 --border
	} else {
		set(dwmwaImmersiveDark, 0) // Win10 上退出沉浸式深色
		set(dwmwaImmersiveDarkOld, 0)
		caption = 0x00f3f3f3 // 浅底
		text = 0x001a1a1a
		border = 0x00c8c8c8
	}
	if ok := set(dwmwaCaptionColor, caption); !ok {
		set(dwmwaTextColor, text)
		set(dwmwaBorderColor, border)
	}
	// 颜色属性改了不会自动重画，强制刷一次非客户区
	pSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate|swpFrameChanged)
}

// titleBarDarkNow 当前主题该不该走深色标题栏：
// 偏好 light/dark 直接按它；system 跟 Windows 个性化里的「应用使用浅色主题」。
func titleBarDarkNow() bool {
	t := themeNow()
	if t == "dark" {
		return true
	}
	if t == "light" {
		return false
	}
	return !systemUsesLightTheme()
}

// systemUsesLightTheme 读注册表里的系统外观偏好（读不到就按深色算，和界面 system 分支一致）
func systemUsesLightTheme() bool {
	const keyPath = "Software\\Microsoft\\Windows\\CurrentVersion\\Themes\\Personalize"
	path, _ := syscall.UTF16PtrFromString(keyPath)
	var kH uintptr
	if r, _, _ := pRegOpenKeyExW.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(path)), 0, 0x0001 /*KEY_READ*/, 0, uintptr(unsafe.Pointer(&kH))); r != 0 {
		return false
	}
	defer pRegCloseKey.Call(kH)
	name, _ := syscall.UTF16PtrFromString("AppsUseLightTheme")
	var typ, cb uint32
	var buf [4]byte
	if r, _, _ := pRegQueryValueExW.Call(kH, uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&cb))); r != 0 {
		return false
	}
	return cb == 4 && uint32(buf[0]) == 1
}

// refreshTitleBar 按当前主题重刷主窗口标题栏。
// ⚠️ 可能从 HTTP 线程被调（设置页改主题），必须 Dispatch 回 UI 线程再动窗口。
func refreshTitleBar() {
	if mainHWND == 0 {
		return
	}
	dark := titleBarDarkNow()
	if mainWebView != nil {
		mainWebView.Dispatch(func() { applyTitleBarTheme(mainHWND, dark) })
		return
	}
	applyTitleBarTheme(mainHWND, dark)
}

// applyDarkTitleBar 保留旧入口：安装时按**当前主题**刷一次（不再是写死深色）
func applyDarkTitleBar(hwnd uintptr) {
	applyTitleBarTheme(hwnd, titleBarDarkNow())
	log.Printf("[壳] 标题栏已按当前主题刷新")
}
