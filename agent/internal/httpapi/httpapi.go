// Package httpapi 提供后端 Agent 的对外接口：HTTP 控制台 API + 设备 WebSocket 通道。
package httpapi

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"baize/internal/agentsvc"
	"baize/internal/hub"
	"baize/internal/kb"
	"baize/internal/logx"
	"baize/internal/slash"
	"baize/shared/proto"
)

//go:embed panel.html
var panelHTML []byte

// Server HTTP 服务
type Server struct {
	hub     *hub.Hub
	lg      *slog.Logger
	ring    *logx.Ring
	version string
	started time.Time
	agent   *agentsvc.Service // 可选：挂了才有 Agent 相关接口
	kb      *kb.Service       // 可选：挂了才有知识库接口
	cmds    *slash.Registry   // 魔法命令注册表（挂了 agent 才有）
}

// New 创建服务
func New(h *hub.Hub, lg *slog.Logger, ring *logx.Ring, version string) *Server {
	return &Server{hub: h, lg: lg, ring: ring, version: version, started: time.Now()}
}

// Handler 返回路由
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handlePanel)
	mux.HandleFunc("GET /api/health", s.api(s.handleHealth))
	mux.HandleFunc("GET /api/state", s.api(s.handleState))
	mux.HandleFunc("GET /api/logs", s.api(s.handleLogs))
	mux.HandleFunc("POST /api/tasks", s.api(s.handleCreateTask))
	mux.HandleFunc("POST /api/tasks/{id}/approve", s.api(s.handleApprove))
	mux.HandleFunc("POST /api/tasks/{id}/reject", s.api(s.handleReject))
	mux.HandleFunc("GET /ws/device", s.handleDeviceWS)
	s.registerAgent(mux)
	s.registerKB(mux)
	s.registerVoice(mux)
	s.registerDL(mux)
	return mux
}

/* ---------- 中间件与工具 ---------- */

type apiHandler func(w http.ResponseWriter, r *http.Request)

// api 包装：写访问日志 + 令牌校验
func (s *Server) api(next apiHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		if !s.authOK(r) {
			s.lg.Warn("接口拒绝：令牌不对", "method", r.Method, "path", r.URL.Path, "addr", r.RemoteAddr)
			writeErr(w, http.StatusUnauthorized, "配对令牌不正确（请在请求头带 X-Baize-Token，或面板页操作）")
			return
		}
		next(w, r)
		s.lg.Debug("HTTP", "method", r.Method, "path", r.URL.Path,
			"cost", time.Since(start).Round(time.Millisecond))
	}
}

func (s *Server) authOK(r *http.Request) bool {
	token := s.hub.Token()
	if token == "" {
		return true
	}
	if r.Header.Get("X-Baize-Token") == token {
		return true
	}
	return r.URL.Query().Get("token") == token
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func decodeBody(r *http.Request, v any) error {
	return decodeBodyMax(r, v, 1<<20)
}

// decodeBodyMax 同 decodeBody，但可指定请求体上限（截图这类大 body 用得上）
func decodeBodyMax(r *http.Request, v any, maxBytes int64) error {
	if r.Body == nil {
		return errors.New("请求体为空")
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBytes))
	if err := dec.Decode(v); err != nil {
		return errors.New("请求体解析失败：" + err.Error())
	}
	return nil
}

func atoiDefault(s string, def int) int {
	if strings.TrimSpace(s) == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

/* ---------- 面板与接口 ---------- */

// 面板页里嵌着配对令牌，所以这一页本身也要过令牌闸门：
// 本机回环访问免验证（本机进程本来就能直接读 data/token），跨机必须给对令牌，
// 不然局域网里谁打开首页都能从页面源码把令牌抄走。
func (s *Server) handlePanel(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !s.panelAccessOK(w, r) {
		return
	}
	body := bytes.ReplaceAll(panelHTML, []byte("__BAIZE_TOKEN__"), []byte(s.hub.Token()))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// panelCookie 面板页验证通过后种下的 cookie（省得每次打开都重输令牌）
const panelCookie = "bz_panel_token"

// panelAccessOK 判断这次访问能不能拿面板页；不能时已经替调用方写好了响应。
//   - ?token= 显式给令牌：对了种 cookie 并跳回干净的 /（令牌不留在地址栏），错了给登录页；
//   - cookie 或 X-Baize-Token 头：对了放行（脚本/curl 走头这条路）；
//   - 回环地址：免验证；
//   - 其余：登录页（401）。
func (s *Server) panelAccessOK(w http.ResponseWriter, r *http.Request) bool {
	token := s.hub.Token()
	if token == "" {
		return true // --no-token 调试模式：跟接口那边一样全放开
	}
	if v := r.URL.Query().Get("token"); v != "" {
		if v != token {
			writePanelLogin(w, true)
			return false
		}
		http.SetCookie(w, &http.Cookie{
			Name: panelCookie, Value: v, Path: "/", MaxAge: 30 * 24 * 3600,
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/", http.StatusFound)
		return false
	}
	if c, err := r.Cookie(panelCookie); err == nil && c.Value == token {
		return true
	}
	if r.Header.Get("X-Baize-Token") == token {
		return true
	}
	if isLoopback(r.RemoteAddr) {
		return true
	}
	writePanelLogin(w, false)
	return false
}

// isLoopback 判断这条连接是不是本机发起的
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// writePanelLogin 令牌闸门页（401）：告诉人令牌在哪、怎么进，别只甩一个空 401
func writePanelLogin(w http.ResponseWriter, wrong bool) {
	errBlock := ""
	if wrong {
		errBlock = `<p class="err">令牌不对，再看看？</p>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(strings.Replace(panelLoginHTML, "{{ERR}}", errBlock, 1)))
}

const panelLoginHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>白泽智能体 · 需要配对令牌</title>
<style>
  body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: #f9f8f4;
         color: rgba(20, 20, 19, .88);
         font: 14px/1.6 -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif; }
  .card { width: min(430px, 92vw); box-sizing: border-box; background: #fff; border: 1px solid #eae8e7;
          border-radius: 8px; padding: 24px; box-shadow: 0 2px 8px rgba(0, 0, 0, .06); }
  h1 { font-size: 16px; margin: 0 0 8px; }
  p { color: rgba(20, 20, 19, .58); font-size: 13px; margin: 0 0 14px; }
  input { width: 100%; box-sizing: border-box; height: 36px; padding: 0 11px; border: 1px solid #d8d4cf;
          border-radius: 8px; outline: none; font: inherit; }
  input:focus { border-color: #ff7f16; }
  button { margin-top: 12px; width: 100%; height: 36px; border: 0; border-radius: 8px;
           background: #ff7f16; color: #fff; font: inherit; cursor: pointer; }
  button:hover { background: #ff9d4d; }
  .err { color: #cf1322; margin: 10px 0 0; }
  code { background: rgba(0, 0, 0, .05); padding: 1px 5px; border-radius: 4px; font-size: 12px; }
</style>
</head>
<body>
<div class="card">
  <h1>白泽智能体 · 控制台</h1>
  <p>这一页里嵌着配对令牌，从局域网打开要先验证。令牌在服务器数据目录的 <code>token</code> 文件里，
     也就是手机端「跨端」里填的那串。</p>
  <form method="get" action="/">
    <input name="token" type="password" placeholder="配对令牌" autofocus autocomplete="off">
    <button type="submit">进入控制台</button>
  </form>
  {{ERR}}
  <p style="margin: 14px 0 0">本机访问免验证：把地址里的主机换成 127.0.0.1 即可（端口不变）。</p>
</div>
</body>
</html>`

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	st, err := s.hub.Store().Stats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取统计失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"version":       s.version,
		"protocol":      proto.Version,
		"uptimeSec":     int64(time.Since(s.started).Seconds()),
		"devicesOnline": st.DevicesOnline,
		"tasksPending":  st.TasksPending,
	})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	devices, err := s.hub.Store().Devices()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取设备失败："+err.Error())
		return
	}
	tasks, err := s.hub.Store().Tasks(atoiDefault(q.Get("tasks"), 50))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取任务失败："+err.Error())
		return
	}
	records, err := s.hub.Store().Records(atoiDefault(q.Get("records"), 60), q.Get("kind"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取记录失败："+err.Error())
		return
	}
	memories, err := s.hub.Store().Memories(atoiDefault(q.Get("memories"), 30))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取记忆失败："+err.Error())
		return
	}
	stats, err := s.hub.Store().Stats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取统计失败："+err.Error())
		return
	}
	// 主设备 = 后端自己跑在哪台机器上（用户要求"直接显示后端部署在哪台电脑/手机上"）
	host, _ := os.Hostname()
	remarks := map[string]string{}
	if s.agent != nil {
		if c := s.agent.Config(); c.DeviceRemarks != nil {
			remarks = c.DeviceRemarks
		}
	}
	out := map[string]any{
		"now":            time.Now().UnixMilli(),
		"version":        s.version,
		"protocol":       proto.Version,
		"uptimeSec":      int64(time.Since(s.started).Seconds()),
		"receiptTimeout": int64(s.hub.ReceiptTimeout().Seconds()),
		"dbPath":         s.hub.Store().Path(),
		"actions":        proto.KnownActions(),
		"self":           map[string]any{"hostname": host, "os": runtime.GOOS, "arch": runtime.GOARCH, "version": s.version},
		"deviceRemarks":  remarks,
		"records":        records,
		"devices":        devices,
		"tasks":          tasks,
		"memories":       memories,
		"stats":          stats,
	}
	if s.kb != nil {
		out["kb"] = s.kb.Stats()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	after := int64(atoiDefault(r.URL.Query().Get("after"), 0))
	lines := s.ring.Since(after)
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "next": s.ring.LastSeq()})
}

type createTaskReq struct {
	DeviceID string         `json:"deviceId"`
	Action   string         `json:"action"`
	Args     map[string]any `json:"args"`
	Origin   string         `json:"origin"`
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	t, err := s.hub.CreateTask(req.DeviceID, req.Action, req.Args, req.Origin)
	if err != nil {
		s.lg.Warn("创建任务失败", "err", err, "device", req.DeviceID, "action", req.Action)
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": t})
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	by := r.URL.Query().Get("by")
	if by == "" {
		var body struct {
			By string `json:"by"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
			by = body.By
		}
	}
	t, err := s.hub.Approve(id, by)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": t})
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		By     string `json:"by"`
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	t, err := s.hub.Reject(id, body.By, body.Reason)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": t})
}

/* ---------- 设备 WebSocket ---------- */

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true }, // 内网自用，来源不限制
}

const (
	pongWait   = 90 * time.Second
	helloWait  = 15 * time.Second
	writeWait  = 10 * time.Second
	maxMsgSize = 4 << 20
)

func (s *Server) handleDeviceWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.lg.Warn("WebSocket 升级失败", "err", err, "addr", r.RemoteAddr)
		return
	}
	defer ws.Close()
	ws.SetReadLimit(maxMsgSize)
	_ = ws.SetReadDeadline(time.Now().Add(helloWait))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(pongWait))
	})
	ws.SetPingHandler(func(data string) error {
		_ = ws.SetReadDeadline(time.Now().Add(pongWait))
		return ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(writeWait))
	})

	s.lg.Info("设备连接接入", "addr", r.RemoteAddr)
	var c *hub.Conn
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				s.lg.Warn("设备连接异常断开", "addr", r.RemoteAddr, "err", err)
			}
			break
		}
		var env proto.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			s.lg.Warn("消息解析失败，已忽略", "addr", r.RemoteAddr, "err", err)
			continue
		}
		switch env.Type {
		case proto.TypeHello:
			var hello proto.Hello
			if err := env.Decode(&hello); err != nil {
				s.lg.Warn("hello 解析失败", "err", err)
				s.sendErr(ws, "bad_hello", err.Error())
				return
			}
			conn, ack, err := s.hub.Register(ws, hello, r.RemoteAddr)
			if err != nil {
				s.lg.Warn("注册失败", "addr", r.RemoteAddr, "device", hello.DeviceID, "err", err)
				s.sendErr(ws, "register_failed", err.Error())
				return
			}
			c = conn
			if env, err := proto.New(proto.TypeHelloAck, c.DeviceID(), "", ack); err == nil {
				if err := c.Send(env); err != nil {
					s.lg.Warn("发送 hello_ack 失败", "err", err)
					break
				}
			}
			// 注册确认之后才补发积压指令，保证端侧先完成注册
			s.hub.StartDispatch(c)
			_ = ws.SetReadDeadline(time.Now().Add(pongWait))
		case proto.TypeHeartbeat:
			if c == nil {
				continue
			}
			var hb proto.Heartbeat
			if err := env.Decode(&hb); err != nil {
				s.lg.Debug("心跳解析失败", "err", err)
				continue
			}
			s.hub.OnHeartbeat(c, hb)
		case proto.TypeEvent:
			if c == nil {
				s.lg.Debug("未注册就上报事件，已忽略")
				continue
			}
			var ev proto.Event
			if err := env.Decode(&ev); err != nil {
				s.lg.Warn("事件解析失败", "err", err)
				continue
			}
			s.hub.OnEvent(c, ev)
		case proto.TypeReceipt:
			if c == nil {
				s.lg.Debug("未注册就回执，已忽略")
				continue
			}
			var rc proto.Receipt
			if err := env.Decode(&rc); err != nil {
				s.lg.Warn("回执解析失败", "err", err)
				continue
			}
			s.hub.OnReceipt(c, rc)
		default:
			s.lg.Warn("未知消息类型，已忽略", "type", env.Type, "addr", r.RemoteAddr)
		}
	}
	s.hub.Unregister(c)
}

func (s *Server) sendErr(ws *websocket.Conn, code, msg string) {
	env, err := proto.New(proto.TypeError, "", "", proto.ErrorMsg{Code: code, Message: msg})
	if err != nil {
		return
	}
	b, _ := json.Marshal(env)
	_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
	_ = ws.WriteMessage(websocket.TextMessage, b)
}
