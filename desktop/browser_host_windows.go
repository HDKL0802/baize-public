//go:build windows

// 电脑控制 · 浏览器双方案：**主进程侧**的浏览器宿主。
//
// 两路目标：
//   - builtin：内置浏览器（独立进程 WebView2，见 browser_windows.go）
//   - chrome ：用户已装的 Chrome/Edge（浏览器扩展接入，见 browser_ext.go）
//
// 设备动作 browser.* 先过 permGate（总开关），再到这里按 args.target 路由。
// 主进程只做「转发 + 记着那个子进程」，页面操作全部发生在子进程里。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"baize/shared/proto"
)

type builtinProc struct {
	cmd   *exec.Cmd
	port  int
	token string
}

var (
	builtinMu  sync.Mutex
	builtinCur *builtinProc
)

// init 把 server.go 的平台钩子接到真实实现上
func init() {
	browserStateHook = browserLocalState
	browserSettingHook = setBrowserControl
	browserOpenHook = func(raw string) (map[string]any, error) {
		return browserAction(proto.ActionBrowserOpen, map[string]any{"url": raw, "target": "builtin"})
	}
	browserHandoffHook = func(on bool) (map[string]any, error) {
		return browserAction(proto.ActionBrowserHandoff, map[string]any{"handoff": on, "target": "builtin"})
	}
	browserCloseHook = closeBuiltin
	browserRotateHook = func() string { return clearBrowserExtToken() }
	browserOpenDirHook = openBrowserExtDir
}

// currentBuiltin 当前内置浏览器进程（没有 = nil）
func currentBuiltin() *builtinProc {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	return builtinCur
}

// startBuiltin 确保内置浏览器进程在跑（已在跑就直接复用，不会开第二个窗口）。
// 新起时按 url 直接打开；后续导航走 call("open")。
func startBuiltin(rawURL string) (*builtinProc, error) {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	if builtinCur != nil {
		return builtinCur, nil
	}
	port, err := browserFreePort()
	if err != nil {
		return nil, fmt.Errorf("找不到空闲端口：%w", err)
	}
	token := newExtToken()
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("取自身路径失败：%w", err)
	}
	args := []string{"-browser", "-browser-port", strconv.Itoa(port), "-browser-token", token}
	if strings.TrimSpace(rawURL) != "" {
		args = append(args, "-browser-url", rawURL)
	}
	cmd := exec.Command(exe, args...)
	// GUI 子系统没有控制台；浏览器进程的日志和主进程一样落到 desktop.log
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动内置浏览器失败：%w", err)
	}
	p := &builtinProc{cmd: cmd, port: port, token: token}

	// 等控制口就绪（WebView2 冷启动偶尔要几秒）
	deadline := time.Now().Add(20 * time.Second)
	for {
		if builtinHealthy(p) {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			return nil, errors.New("内置浏览器启动超时（20 秒）：WebView2 可能没装好，或控制口被拦了")
		}
		time.Sleep(250 * time.Millisecond)
	}

	builtinCur = p
	go func() {
		_ = cmd.Wait()
		builtinMu.Lock()
		if builtinCur == p {
			builtinCur = nil
		}
		builtinMu.Unlock()
	}()
	return p, nil
}

// closeBuiltin 关掉内置浏览器（用户在主界面点「关闭」）
func closeBuiltin() {
	builtinMu.Lock()
	p := builtinCur
	builtinCur = nil
	builtinMu.Unlock()
	if p != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func builtinHealthy(p *builtinProc) bool {
	cli := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health", p.port), nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-Baize-Token", p.token)
	resp, err := cli.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var out struct {
		OK    bool `json:"ok"`
		Ready bool `json:"ready"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out) != nil {
		return false
	}
	return out.OK && out.Ready
}

// call 把动作发给内置浏览器进程
func (p *builtinProc) call(action string, args map[string]any) (map[string]any, error) {
	if args == nil {
		args = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{"action": action, "args": args})
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/ctl", p.port), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Baize-Token", p.token)
	cli := &http.Client{Timeout: 40 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上内置浏览器（它可能已经关了）：%w", err)
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool           `json:"ok"`
		Result map[string]any `json:"result"`
		Error  string         `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("内置浏览器的回执读不懂：%w", err)
	}
	if !out.OK {
		if out.Error == "" {
			out.Error = "内置浏览器执行失败（没有给出原因）"
		}
		return nil, errors.New(out.Error)
	}
	if out.Result == nil {
		out.Result = map[string]any{}
	}
	return out.Result, nil
}

// isBrowserAction 是否属于浏览器双方案的动作
func isBrowserAction(action string) bool {
	switch action {
	case proto.ActionBrowserOpen, proto.ActionBrowserRead, proto.ActionBrowserClick,
		proto.ActionBrowserType, proto.ActionBrowserKey, proto.ActionBrowserShot,
		proto.ActionBrowserStatus, proto.ActionBrowserHandoff:
		return true
	}
	return false
}

// browserAction 设备动作 browser.* 的总入口（permGate 已过总开关）
func browserAction(action string, args map[string]any) (map[string]any, error) {
	if args == nil {
		args = map[string]any{}
	}
	target := ""
	if v, ok := args["target"]; ok && v != nil {
		target = strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
	}
	if target == "" || target == "builtin" || target == "native" {
		return builtinAction(action, args)
	}
	if target == "chrome" || target == "ext" || target == "extension" {
		if !browserChromeAllowed() {
			return nil, errors.New("本机没开「允许 Chrome 扩展接入」（设置 → 桌面控制 → 允许控制浏览器 → 允许 Chrome 扩展）")
		}
		return extCall(action, args)
	}
	return nil, fmt.Errorf("不认识的 target：%s（可用 builtin / chrome）", target)
}

func builtinAction(action string, args map[string]any) (map[string]any, error) {
	name := strings.TrimPrefix(action, "browser.")

	switch action {
	case proto.ActionBrowserStatus:
		p := currentBuiltin()
		if p == nil {
			return map[string]any{"open": false, "target": "builtin", "handoff": false}, nil
		}
		res, err := p.call("status", nil)
		if err != nil {
			return nil, err
		}
		res["open"], res["target"] = true, "builtin"
		return res, nil

	case proto.ActionBrowserOpen:
		raw := ""
		if v, ok := args["url"]; ok && v != nil {
			raw = strings.TrimSpace(fmt.Sprint(v))
		}
		if raw == "" {
			return nil, errors.New("缺少参数 url")
		}
		old := currentBuiltin()
		p, err := startBuiltin(raw)
		if err != nil {
			return nil, err
		}
		if old != nil { // 已经在跑：让现有窗口导航过去
			return p.call("open", map[string]any{"url": raw})
		}
		res, err := p.call("status", nil)
		if err != nil {
			return nil, err
		}
		res["open"], res["target"] = true, "builtin"
		return res, nil
	}

	p := currentBuiltin()
	if p == nil {
		return nil, errors.New("内置浏览器没打开：先让 AI 用 browser.open 打开，或在主界面「浏览器」面板里打开")
	}
	res, err := p.call(name, args)
	if err != nil {
		return nil, err
	}
	res["target"] = "builtin"
	return res, nil
}

// browserLocalState 给主界面「浏览器」面板看的状态（不含任何用户数据）
func browserLocalState() map[string]any {
	out := map[string]any{
		"control":      browserControlEnabled(),
		"allowChrome":  browserChromeAllowed(),
		"extPort":      browserExtPort(),
		"extToken":     browserExtToken(),
		"extConnected": extConnected(),
		"extDir":       browserExtDir(),
	}
	if p := currentBuiltin(); p != nil {
		if res, err := p.call("status", nil); err == nil {
			out["builtin"] = res
		} else {
			out["builtin"] = map[string]any{"open": true, "note": err.Error()}
		}
	} else {
		out["builtin"] = map[string]any{"open": false}
	}
	return out
}

// browserExtDir Chrome 扩展目录（安装包放在 exe 同级的 browser-ext；
// 开发时 exe 在 desktop\bin，扩展在 desktop\browser-ext）
func browserExtDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Join(filepath.Dir(exe), "browser-ext")
	if _, err := os.Stat(dir); err == nil {
		return dir
	}
	alt := filepath.Clean(filepath.Join(filepath.Dir(exe), "..", "browser-ext"))
	if fi, err := os.Stat(alt); err == nil && fi.IsDir() {
		return alt
	}
	return dir
}

// openBrowserExtDir 在资源管理器里打开扩展目录（设置页「打开扩展目录」）
func openBrowserExtDir() error {
	dir := browserExtDir()
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("扩展目录不存在：%s（安装包应带 browser-ext；开发时在 desktop\\browser-ext）", dir)
	}
	return exec.Command("explorer.exe", dir).Start()
}

// browserFreePort 找一个只监听 127.0.0.1 的空闲端口
func browserFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
