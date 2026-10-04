//go:build windows

// 让 Windows 原生标题栏不再是刺眼的白条。
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
)

const (
	dwmwaImmersiveDarkOld = 19 // Win10 1809 ~ 20H1
	dwmwaImmersiveDark    = 20 // Win10 20H1+ / Win11
	dwmwaBorderColor      = 34 // Win11 22000+
	dwmwaCaptionColor     = 35
	dwmwaTextColor        = 36
)

func applyDarkTitleBar(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	set := func(attr uint32, val uint32) bool {
		hr, _, _ := pDwmSetWindowAttr.Call(hwnd, uintptr(attr), uintptr(unsafe.Pointer(&val)), unsafe.Sizeof(val))
		return hr == 0
	}

	// 1) 先开沉浸式深色（老系统就靠这个）
	if !set(dwmwaImmersiveDark, 1) {
		set(dwmwaImmersiveDarkOld, 1)
	}
	// 2) Win11 再精确对齐界面的配色（COLORREF = 0x00BBGGRR）
	caption := uint32(0x001d1612) // #12161d —— 界面的 --surface
	text := uint32(0x00eee4db)    // #dbe4ee —— 界面的 --text
	border := uint32(0x00232a35)  // #232a35 —— 界面的 --border
	ok := set(dwmwaCaptionColor, caption)
	set(dwmwaTextColor, text)
	set(dwmwaBorderColor, border)
	if ok {
		log.Printf("[壳] 标题栏已改为深色（对齐界面配色）")
	} else {
		log.Printf("[壳] 系统不支持自定义标题栏配色，已退回沉浸式深色")
	}
}
