//go:build !windows

// 白泽桌面端目前只支持 Windows（WebView2 是 Windows 组件）。
package main

import "fmt"

func main() {
	fmt.Println("白泽桌面端目前只支持 Windows（WebView2）。")
}
