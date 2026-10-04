//go:build windows

// 开机自启：往 HKCU\Software\Microsoft\Windows\CurrentVersion\Run 写一条，
// 值是本程序 exe 的绝对路径。用户级（HKCU）不需要管理员权限，也不会影响别人。
package main

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	autostartKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	autostartValue   = "BaizeDesktop"
)

// 挂到 server.go 的平台钩子上
func init() { autostartSet = setAutostart; autostartGet = autostartEnabled }

// setAutostart 打开/关闭开机自启。关的时候顺手容错：值本来就不在也算成功。
func setAutostart(enable bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, autostartKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if !enable {
		if err := k.DeleteValue(autostartValue); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// 带引号：路径里有空格（"C:\Program Files\..."）时才不会被拆成两段
	return k.SetStringValue(autostartValue, `"`+exe+`"`)
}

// autostartEnabled 现在是不是开着（只判断"有没有这一条"，不校验路径是否还是当前 exe）
func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(autostartValue)
	return err == nil && strings.TrimSpace(v) != ""
}
