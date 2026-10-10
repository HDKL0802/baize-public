//go:build windows

// 桌面端「常驻」三件套（纯 Win32 syscall，不引新依赖）：
//
//	1) 单实例：命名互斥体。第二次启动不新开窗口，而是把已有窗口（可能是隐藏的）叫到前台；
//	2) 关窗不退出：子类化主窗口 WndProc，拦 WM_CLOSE 只把窗口隐藏 —— 这样设备连接不会断，
//	   也就不会再出现「以为关掉了、其实设备掉线了」这种事；
//	3) 托盘图标：Shell_NotifyIcon + 右键菜单（打开 / 退出）。
//
// 设计原则：每一步失败都只记日志、不致命 —— 壳必须能起来（VS 调试时托盘失败是常事）。
package main

import (
	"log"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

const (
	appWindowTitle = "白泽"
	appInstanceID  = "BaizeDesktop-single"

	wmClose     = 0x0010
	wmDestroy   = 0x0002
	wmSetIcon   = 0x0080 // 给窗口换图标：不设的话 Windows 会画成系统默认那张（和 exe 图标不是一回事）
	iconSmall   = 0
	iconBig     = 1
	wmLButtonUp = 0x0202
	wmRButtonUp = 0x0205
	wmApp       = 0x8000
	wmTray      = wmApp + 1

	gwlpWndProc = ^uintptr(3) // GWLP_WNDPROC = -4

	swHide    = 0
	swRestore = 9

	nimAdd     = 0
	nimDelete  = 2
	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04

	idiApplication = 32512

	mfString       = 0x0000
	mfSeparator    = 0x0800
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	menuOpen = 1001
	menuQuit = 1002

	errAlreadyExists = 183
)

var (
	shUser32   = syscall.NewLazyDLL("user32.dll")
	shShell32  = syscall.NewLazyDLL("shell32.dll")
	shKernel32 = syscall.NewLazyDLL("kernel32.dll")

	pCreateMutexW        = shKernel32.NewProc("CreateMutexW")
	pSetWindowLongPtrW   = shUser32.NewProc("SetWindowLongPtrW")
	pCallWindowProcW     = shUser32.NewProc("CallWindowProcW")
	pShowWindow          = shUser32.NewProc("ShowWindow")
	pIsWindowVisible     = shUser32.NewProc("IsWindowVisible")
	pSetForegroundWindow = shUser32.NewProc("SetForegroundWindow")
	pFindWindowW         = shUser32.NewProc("FindWindowW")
	pShellNotifyIconW    = shShell32.NewProc("Shell_NotifyIconW")
	pLoadIconW           = shUser32.NewProc("LoadIconW")
	pCreatePopupMenu     = shUser32.NewProc("CreatePopupMenu")
	pAppendMenuW         = shUser32.NewProc("AppendMenuW")
	pDestroyMenu         = shUser32.NewProc("DestroyMenu")
	pTrackPopupMenu      = shUser32.NewProc("TrackPopupMenu")
	pGetCursorPos        = shUser32.NewProc("GetCursorPos")
	pGetModuleHandleW    = shKernel32.NewProc("GetModuleHandleW")
	pSendMessageW        = shUser32.NewProc("SendMessageW")
)

var (
	singleMutex uintptr // 句柄故意不释放：释放了别的实例就能再起一个
	mainHWND    uintptr
	oldWndProc  uintptr
	trayAdded   bool
	quittingNow bool
	mainWebView webview2.WebView

	wndProcCb = syscall.NewCallback(trayWndProc)
)

// 挂到 server.go 的平台钩子上（设置页的「退出」走这里）
func init() { appQuit = quitApp }

func windowTitlePtr() uintptr {
	p, _ := syscall.UTF16PtrFromString(appWindowTitle)
	return uintptr(unsafe.Pointer(p))
}

// ensureSingleInstance 返回 false = 已经有一个实例在跑（顺便把它的窗口叫到前台）。
func ensureSingleInstance() bool {
	name, err := syscall.UTF16PtrFromString(`Local\` + appInstanceID)
	if err != nil {
		return true
	}
	h, _, callErr := pCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		log.Printf("[壳] 互斥体创建失败，跳过单实例检查：%v", callErr)
		return true
	}
	singleMutex = h
	if e, ok := callErr.(syscall.Errno); ok && e == errAlreadyExists {
		if hwnd, _, _ := pFindWindowW.Call(0, windowTitlePtr()); hwnd != 0 {
			pShowWindow.Call(hwnd, swRestore)
			pSetForegroundWindow.Call(hwnd)
		}
		return false
	}
	return true
}

// installShell 在窗口就绪后装托盘、并让「关窗」变成「隐藏」。
func installShell(w webview2.WebView) {
	if w == nil {
		return
	}
	mainWebView = w
	hwnd := uintptr(w.Window())
	if hwnd == 0 {
		log.Printf("[壳] 拿不到窗口句柄：托盘与关窗隐藏都不启用（关窗即退出）")
		return
	}
	mainHWND = hwnd
	applyDarkTitleBar(hwnd) // 原生标题栏默认是白条，按当前主题刷一次
	// 「跟随系统」时 Windows 侧外观随时可能变（用户切了个性化里的深浅色）：
	// 每 20 秒对一遍，变了就重刷（成本极低；不这么做标题栏颜色就永远停在旧值）
	go func() {
		for {
			time.Sleep(20 * time.Second)
			refreshTitleBar()
		}
	}()

	// 窗口图标：WebView2 的窗口类没带图标，不显式设的话标题栏/任务栏会画成
	// 系统默认那张白纸（跟 exe 里嵌的图标完全无关）。
	if ic := loadAppIcon(); ic != 0 {
		pSendMessageW.Call(hwnd, wmSetIcon, iconSmall, ic)
		pSendMessageW.Call(hwnd, wmSetIcon, iconBig, ic)
		log.Printf("[壳] 窗口图标已设为 exe 自带图标（标题栏/任务栏）")
	}

	old, _, callErr := pSetWindowLongPtrW.Call(hwnd, gwlpWndProc, wndProcCb)
	if old == 0 {
		log.Printf("[壳] 子类化窗口失败（%v）：关窗将直接退出", callErr)
		return
	}
	oldWndProc = old
	addTrayIcon(hwnd)
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

// loadAppIcon 取 exe 自带图标（由 rsrc.syso 嵌入，图标资源 id = 1）。
// 取不到就退回系统默认图标，保证调用方拿到一个能用的句柄（返回 0 = 彻底失败）。
func loadAppIcon() uintptr {
	if hInst, _, _ := pGetModuleHandleW.Call(0); hInst != 0 {
		if ic, _, _ := pLoadIconW.Call(hInst, 1); ic != 0 {
			return ic
		}
	}
	ic, _, _ := pLoadIconW.Call(0, uintptr(idiApplication))
	if ic != 0 {
		log.Printf("[壳] 没取到 exe 自带图标，退回系统默认图标")
	}
	return ic
}

func addTrayIcon(hwnd uintptr) {
	icon := loadAppIcon()
	if icon == 0 {
		log.Printf("[壳] 取图标失败：托盘不启用")
		return
	}
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	nid.UFlags = nifMessage | nifIcon | nifTip
	nid.UCallbackMessage = wmTray
	nid.HIcon = icon
	if tip, err := syscall.UTF16FromString(appWindowTitle + "（左键开合 / 右键菜单）"); err == nil {
		copy(nid.SzTip[:], tip)
	}
	ok, _, callErr := pShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	if ok == 0 {
		log.Printf("[壳] 托盘图标没加上（%v）：关窗仍会隐藏到后台，重新启动本程序即可唤出", callErr)
		return
	}
	trayAdded = true
	log.Printf("[壳] 托盘图标已就绪（左键开合，右键菜单）")
}

func removeTrayIcon() {
	if !trayAdded {
		return
	}
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = mainHWND
	nid.UID = 1
	pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	trayAdded = false
}

// trayWndProc 子类化的窗口过程。参数全是 uintptr：syscall.NewCallback 要求如此。
func trayWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch uint32(msg) {
	case wmClose:
		if !quittingNow {
			pShowWindow.Call(hwnd, swHide)
			log.Printf("[壳] 窗口已隐藏：白泽仍在后台跑，设备不掉线（托盘菜单或重新启动可唤出）")
			return 0 // 不放行，窗口就不销毁
		}
	case wmTray:
		switch uint32(lparam) & 0xffff {
		case wmLButtonUp:
			toggleWindow(hwnd)
		case wmRButtonUp:
			showTrayMenu(hwnd)
		}
		return 0
	case wmDestroy:
		removeTrayIcon()
	}
	if oldWndProc == 0 {
		return 0
	}
	r, _, _ := pCallWindowProcW.Call(oldWndProc, hwnd, msg, wparam, lparam)
	return r
}

func toggleWindow(hwnd uintptr) {
	if vis, _, _ := pIsWindowVisible.Call(hwnd); vis != 0 {
		pShowWindow.Call(hwnd, swHide)
		return
	}
	// 从托盘唤出时一律恢复「工作模式」的完整窗口（不要在迷你小窗里出）
	if ballMini {
		exitMiniChat()
	}
	pShowWindow.Call(hwnd, swRestore)
	pSetForegroundWindow.Call(hwnd)
}

func showTrayMenu(hwnd uintptr) {
	hmenu, _, _ := pCreatePopupMenu.Call()
	if hmenu == 0 {
		return
	}
	defer pDestroyMenu.Call(hmenu)
	appendMenu(hmenu, mfString, menuOpen, "打开白泽")
	appendMenu(hmenu, mfSeparator, 0, "")
	appendMenu(hmenu, mfString, menuQuit, "退出")

	var pt struct{ X, Y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// MSDN 要求：弹菜单前先把本窗口设为前台，否则点菜单外面菜单不会消失
	pSetForegroundWindow.Call(hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(hmenu, tpmRightButton|tpmReturnCmd,
		uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	switch cmd {
	case menuOpen:
		pShowWindow.Call(hwnd, swRestore)
		pSetForegroundWindow.Call(hwnd)
	case menuQuit:
		quitApp()
	}
}

func appendMenu(hmenu, flags, id uintptr, text string) {
	var p uintptr
	if text != "" {
		if s, err := syscall.UTF16PtrFromString(text); err == nil {
			p = uintptr(unsafe.Pointer(s))
		}
	}
	pAppendMenuW.Call(hmenu, flags, id, p)
}

// releaseSingleInstance 松开单实例锁。
// 自动更新起新进程前必须调：不然新进程会把自己判成"已有实例"，唤出旧窗口然后退出，
// 而旧进程紧接着也退了 —— 两个都没了。
func releaseSingleInstance() {
	if singleMutex != 0 {
		pCloseHandle.Call(singleMutex)
		singleMutex = 0
	}
}

// quitApp 真正退出（托盘菜单与设置页都走这里）
//
// ★坑：binding 的 Terminate() 内部是 PostQuitMessage —— **只对「调用线程」的消息队列生效**。
// HTTP 处理器（设置页那个按钮）跑在别的线程上，直接调等于没关；必须 Dispatch 回 UI 线程。
// 从托盘菜单调时本来就在 UI 线程上，走 Dispatch 也一样安全（只是排到队列后面执行）。
func quitApp() {
	if quittingNow {
		return
	}
	quittingNow = true
	log.Printf("[壳] 收到退出指令，正在关闭")
	if mainWebView == nil {
		removeTrayIcon()
		return
	}
	mainWebView.Dispatch(func() {
		removeTrayIcon()
		mainWebView.Terminate()
	})
}
