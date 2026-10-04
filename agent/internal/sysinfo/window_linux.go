//go:build linux

package sysinfo

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ActiveWindow 取当前活动窗口。依赖 xdotool；纯 Wayland 会话（无 XWayland）暂不支持。
func ActiveWindow() (WindowInfo, error) {
	if _, err := exec.LookPath("xdotool"); err != nil {
		return WindowInfo{}, errors.New("未找到 xdotool，无法读取活动窗口（请安装 xdotool，或改用 Windows 端）")
	}
	out, err := exec.Command("xdotool", "getactivewindow").Output()
	if err != nil {
		return WindowInfo{}, fmt.Errorf("xdotool getactivewindow 失败：%v", err)
	}
	id := strings.TrimSpace(string(out))
	title, _ := exec.Command("xdotool", "getwindowname", id).Output()
	pidOut, _ := exec.Command("xdotool", "getwindowpid", id).Output()
	pid, _ := strconv.Atoi(strings.TrimSpace(string(pidOut)))

	return WindowInfo{
		Title:   strings.TrimSpace(string(title)),
		PID:     uint32(pid),
		Process: processName(pid),
		At:      time.Now().UnixMilli(),
	}, nil
}

func processName(pid int) string {
	if pid <= 0 {
		return ""
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
