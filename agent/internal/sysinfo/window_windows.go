//go:build windows

package sysinfo

import (
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW             = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessID   = user32.NewProc("GetWindowThreadProcessId")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
)

const (
	processQueryLimitedInformation = 0x1000
	maxWindowTitle                 = 512
)

// ActiveWindow 取当前前台窗口（标题 + 所属进程）
func ActiveWindow() (WindowInfo, error) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return WindowInfo{}, errors.New("没有前台窗口（可能已锁屏或当前会话不可见）")
	}

	buf := make([]uint16, maxWindowTitle)
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	title := strings.TrimSpace(syscall.UTF16ToString(buf[:int(n)]))

	var pid uint32
	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))

	return WindowInfo{
		Title:   title,
		PID:     pid,
		Process: processName(pid),
		At:      time.Now().UnixMilli(),
	}, nil
}

// processName 由 pid 取可执行文件名
func processName(pid uint32) string {
	if pid == 0 {
		return ""
	}
	h, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)

	buf := make([]uint16, syscall.MAX_PATH)
	size := uint32(len(buf))
	r, _, _ := procQueryFullProcessImageNameW.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return ""
	}
	return filepath.Base(syscall.UTF16ToString(buf[:size]))
}
