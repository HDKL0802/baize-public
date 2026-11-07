//go:build windows

// 内置浏览器用的内核探测：只读系统里已装的 WebView2 Runtime 版本，不改任何设置。
//
// 选型口径（给界面和文档用）：
//   - 内置浏览器 = WebView2 **Evergreen**（Microsoft Edge 的 Chromium 内核随系统更新），
//     不是白泽自己打包的老内核。版本由系统上的 Edge Update 决定，白泽只负责读出来。
//   - 如果机器用「固定版本」部署（设置了 WEBVIEW2_BROWSER_EXECUTABLE_FOLDER），
//     就按那个目录里的版本走，不会自动更新——界面会明确标成 fixed。
//
// 只能证明「本机装了什么」，不能证明「一定是最新版」：最新与否取决于 Microsoft Edge
// Update 是否在跑、是否被策略/离线环境挡住。离线装机的退化见 docs 里的说明。
package main

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// WebView2 Runtime 在 Edge Update 的客户端 GUID（微软固定值）
const webView2ClientGUID = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`

// Edge 稳定版在 Edge Update 的客户端 GUID（只用来对比“内核有没有明显落后”）
const edgeStableClientGUID = `{56EB18F8-B008-4CBD-B6D2-8C97FE7E9062}`

// edgeStableVersion 读本机 Edge 稳定版版本号（读不到就回空串）。
func edgeStableVersion() string {
	refs := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + edgeStableClientGUID},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + edgeStableClientGUID},
		{registry.CURRENT_USER, `Software\Microsoft\EdgeUpdate\Clients\` + edgeStableClientGUID},
	}
	for _, ref := range refs {
		k, err := registry.OpenKey(ref.root, ref.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		pv, _, err := k.GetStringValue("pv")
		_ = k.Close()
		if err == nil && strings.TrimSpace(pv) != "" {
			return strings.TrimSpace(pv)
		}
	}
	return ""
}

// majorOf 取版本号第一段（"155.0.4283.62" → 155）；解析不了回 0。
func majorOf(v string) int {
	n, i := 0, 0
	for ; i < len(v) && v[i] >= '0' && v[i] <= '9'; i++ {
		n = n*10 + int(v[i]-'0')
	}
	return n
}

// browserEngineInfo 读已装的 WebView2 内核信息（找不到就如实说 found=false）。
func browserEngineInfo() map[string]any {
	info := map[string]any{
		"kind":     "Microsoft Edge WebView2（Chromium 内核）",
		"channel":  "evergreen",
		"found":    false,
		"version":  "",
		"location": "",
		"note":     "Evergreen 由 Microsoft Edge Update 自动更新；白泽不自带内核，也不静默下载。",
	}

	// 固定版本部署：环境变量优先（企业/离线常用这种）
	if dir := strings.TrimSpace(os.Getenv("WEBVIEW2_BROWSER_EXECUTABLE_FOLDER")); dir != "" {
		info["found"] = true
		info["channel"] = "fixed"
		info["location"] = dir
		info["version"] = filepath.Base(strings.TrimRight(dir, `\/`))
		info["note"] = "检测到固定版本部署（WEBVIEW2_BROWSER_EXECUTABLE_FOLDER）：版本不会随系统自动更新，需要人工升级这个目录。"
		return info
	}

	refs := []struct {
		root  registry.Key
		path  string
		where string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + webView2ClientGUID, "machine"},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + webView2ClientGUID, "machine"},
		{registry.CURRENT_USER, `Software\Microsoft\EdgeUpdate\Clients\` + webView2ClientGUID, "user"},
	}
	for _, ref := range refs {
		k, err := registry.OpenKey(ref.root, ref.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		pv, _, pvErr := k.GetStringValue("pv")
		loc, _, _ := k.GetStringValue("location")
		_ = k.Close()
		if pvErr != nil || strings.TrimSpace(pv) == "" {
			continue
		}
		info["found"] = true
		info["version"] = pv
		info["location"] = loc
		info["scope"] = ref.where
		if edge := edgeStableVersion(); edge != "" {
			info["edgeVersion"] = edge
			wvMajor, edgeMajor := majorOf(pv), majorOf(edge)
			info["behind"] = wvMajor > 0 && edgeMajor > 0 && wvMajor < edgeMajor
		}
		return info
	}
	return info
}
