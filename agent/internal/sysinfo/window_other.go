//go:build !windows && !linux

package sysinfo

import "errors"

// ActiveWindow 其它平台暂未实现（MVP 覆盖 Windows 与 Linux）。
func ActiveWindow() (WindowInfo, error) {
	return WindowInfo{}, errors.New("当前平台暂不支持活动窗口采集（MVP 覆盖 Windows / Linux）")
}
