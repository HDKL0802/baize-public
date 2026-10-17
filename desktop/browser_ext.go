// 浏览器双方案之二：**Chrome 扩展**接入通道（跨平台纯 Go，不依赖 Win32）。
//
// 用户已装好的 Chrome/Edge 里加载 browser-ext 扩展后，扩展会连到：
//
//	ws://127.0.0.1:<BrowserExtPort>/ext/ws?token=<BrowserExtToken>
//
// 主进程把 browser.* 动作（target=chrome）下发给扩展，扩展用 chrome.debugger
// 在**当前标签页**执行，再把结果回传。控制口只监听 127.0.0.1，令牌不对直接拒绝。
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// newExtToken 生成接入令牌（18 字节随机 → 24 字符 URL-safe）
func newExtToken() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		// 极少数环境读不到系统随机源：退化成时间戳，仍然不可猜（并写日志）
		log.Printf("生成扩展令牌时随机源不可用（%v），已退化为时间戳", err)
		return strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatInt(time.Now().Unix(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

/* ---------------- 本机 WS 服务 ---------------- */

var (
	extSrvMu sync.Mutex
	extSrv   *http.Server
	extLn    net.Listener

	extConnMu  sync.Mutex
	extWriteMu sync.Mutex
	extClient  *websocket.Conn
	extSeq     atomic.Int64
	extPending sync.Map // id(string) -> chan extReply
)

type extReply struct {
	ok     bool
	result map[string]any
	err    string
}

// restartExtServer 按当前配置启动/停止扩展接入服务（开关关闭 = 不监听端口）
func restartExtServer() {
	extSrvMu.Lock()
	if extSrv != nil {
		_ = extSrv.Close()
		extSrv = nil
	}
	if extLn != nil {
		_ = extLn.Close()
		extLn = nil
	}
	extSrvMu.Unlock()

	if !browserChromeAllowed() {
		closeExtClient()
		return
	}
	token := browserExtToken()
	if strings.TrimSpace(token) == "" {
		return
	}
	base := browserExtPort()
	ln, port, err := listenExtPort(base)
	if err != nil {
		log.Printf("Chrome 扩展接入服务启动失败（127.0.0.1:%d）：%v", base, err)
		return
	}
	if port != base {
		// 端口被占时向上找一个可用的，并把实际端口写回配置（设置页显示的就是它）
		cfgMu.Lock()
		cfg.BrowserExtPort = port
		saveConfigLocked()
		cfgMu.Unlock()
		log.Printf("Chrome 扩展接入端口 %d 被占用，改用 %d", base, port)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ext/ws", handleExtWS)
	srv := &http.Server{Handler: mux}
	extSrvMu.Lock()
	extSrv, extLn = srv, ln
	extSrvMu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Chrome 扩展接入服务退出：%v", err)
		}
	}()
	log.Printf("Chrome 扩展接入服务已启动：ws://127.0.0.1:%d/ext/ws", port)
}

func listenExtPort(base int) (net.Listener, int, error) {
	var lastErr error
	for i := 0; i < 10; i++ {
		p := base + i
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			return ln, p, nil
		}
		lastErr = err
	}
	return nil, 0, lastErr
}

func closeExtClient() {
	extConnMu.Lock()
	c := extClient
	extClient = nil
	extConnMu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

func handleExtWS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("token") != browserExtToken() {
		http.Error(w, "令牌不对", http.StatusForbidden)
		return
	}
	up := websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		// Chrome 扩展的 Origin 是 chrome-extension://<id>，没有固定域名可校验；
		// 真正的门槛是上面那串随机令牌，且只监听 127.0.0.1。
		CheckOrigin: func(*http.Request) bool { return true },
	}
	c, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(32 << 20) // 截图 base64 可能几 MB

	extConnMu.Lock()
	old := extClient
	extClient = c
	extConnMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	log.Printf("Chrome 扩展已接入：%s", r.RemoteAddr)

	for {
		var msg struct {
			ID     string         `json:"id"`
			OK     bool           `json:"ok"`
			Result map[string]any `json:"result"`
			Error  string         `json:"error"`
		}
		if err := c.ReadJSON(&msg); err != nil {
			break
		}
		if msg.ID == "" {
			continue
		}
		if v, ok := extPending.Load(msg.ID); ok {
			select {
			case v.(chan extReply) <- extReply{ok: msg.OK, result: msg.Result, err: msg.Error}:
			default:
			}
		}
	}

	extConnMu.Lock()
	if extClient == c {
		extClient = nil
	}
	extConnMu.Unlock()
	_ = c.Close()
	log.Print("Chrome 扩展已断开")
}

// extConnected 扩展当前是否连着（界面显示用）
func extConnected() bool {
	extConnMu.Lock()
	defer extConnMu.Unlock()
	return extClient != nil
}

/* ---------------- 主进程 → 扩展 ---------------- */

// extCall 把一个 browser.* 动作发给扩展并等回执。
// 扩展侧的动作名是裸名（open/read/click/type/key/screenshot/status）。
func extCall(action string, args map[string]any) (map[string]any, error) {
	name := strings.TrimPrefix(action, "browser.")
	if name == "handoff" {
		return nil, errors.New("外部 Chrome 不需要移交：你直接在浏览器里操作即可；AI 的下一个动作会等你操作完")
	}
	if args == nil {
		args = map[string]any{}
	}

	extConnMu.Lock()
	c := extClient
	extConnMu.Unlock()
	if c == nil {
		return nil, errors.New("Chrome 扩展还没连上：在 Chrome 里加载 browser-ext 扩展，填好地址与令牌后点「连接」")
	}

	id := strconv.FormatInt(extSeq.Add(1), 10)
	ch := make(chan extReply, 1)
	extPending.Store(id, ch)
	defer extPending.Delete(id)

	extWriteMu.Lock()
	err := c.WriteJSON(map[string]any{"id": id, "action": name, "args": args})
	extWriteMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("给 Chrome 扩展发命令失败：%w", err)
	}

	select {
	case rep := <-ch:
		if !rep.ok {
			if strings.TrimSpace(rep.err) == "" {
				rep.err = "Chrome 扩展执行失败（没有给出原因）"
			}
			return nil, errors.New(rep.err)
		}
		res := rep.result
		if res == nil {
			res = map[string]any{}
		}
		res["target"] = "chrome"
		// 截图：扩展回的是 base64，落到本地临时文件（回执只带路径，和内置浏览器一个口径）
		if name == "screenshot" {
			if b64, _ := res["pngBase64"].(string); b64 != "" {
				bin, decErr := base64.StdEncoding.DecodeString(b64)
				if decErr != nil {
					return nil, fmt.Errorf("Chrome 截图数据解码失败：%w", decErr)
				}
				path := filepath.Join(os.TempDir(), fmt.Sprintf("baize-chrome-shot-%d.png", time.Now().UnixMilli()))
				if err := os.WriteFile(path, bin, 0o644); err != nil {
					return nil, fmt.Errorf("写 Chrome 截图失败：%w", err)
				}
				delete(res, "pngBase64")
				res["path"], res["bytes"] = path, len(bin)
			}
		}
		return res, nil
	case <-time.After(35 * time.Second):
		return nil, errors.New("Chrome 扩展 35 秒没回话（页面卡住，或调试权限被撤销了）")
	}
}

// extResultJSON 只是给日志/调试用的紧凑输出（不参与业务）
func extResultJSON(v map[string]any) string {
	b, _ := json.Marshal(v)
	if len(b) > 600 {
		b = b[:600]
	}
	return string(b)
}
