package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"baize/shared/proto"
)

func ackEnvelope(t *testing.T) []byte {
	t.Helper()
	env, err := proto.New(proto.TypeHelloAck, "dev-1", "", proto.HelloAck{
		DeviceID: "dev-1", Server: "test", Protocol: proto.Version,
	})
	if err != nil {
		t.Fatalf("构造 hello_ack 失败：%v", err)
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("序列化 hello_ack 失败：%v", err)
	}
	return b
}

// ackServer 收到 Hello 就回 HelloAck，稍等片刻再关连接
// （httptest 的 Close 不会断被 hijack 的 WS，所以由服务端主动关，客户端读循环才退得出）。
func ackServer(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		if _, _, err := c.ReadMessage(); err != nil { // 吃掉 Hello
			return
		}
		_ = c.WriteMessage(websocket.TextMessage, ackEnvelope(t))
		time.Sleep(500 * time.Millisecond) // 留足注册时间，然后由服务端关连接
	}))
}

func denyServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
}

func newTestConfig(url string, paths []string) Config {
	return Config{
		URL: url, Paths: paths, Token: "t",
		HelloTimeout: 2 * time.Second, Heartbeat: time.Hour,
		Info:   proto.Hello{DeviceID: "dev-1"},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestRun_SinglePath_RegistersAndReportsPath(t *testing.T) {
	srv := ackServer(t, nil)
	defer srv.Close()

	ready := make(chan *Session, 1)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		done <- Run(ctx, newTestConfig(srv.URL, nil), nil, func(s *Session) { ready <- s })
	}()

	want, _ := normalizeURL(srv.URL, "t")
	select {
	case s := <-ready:
		if got := s.Path(); got != want {
			t.Errorf("Path() = %q, want %q", got, want)
		}
		if s.Ack().DeviceID != "dev-1" {
			t.Errorf("Ack().DeviceID = %q, want dev-1", s.Ack().DeviceID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("注册超时")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Error("服务端断开后 Run 应返回错误")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未返回")
	}
}

func TestRun_MultiPath_FallsBackToNextCandidate(t *testing.T) {
	dead := denyServer(t, http.StatusNotFound)
	defer dead.Close()
	live := ackServer(t, nil)

	ready := make(chan *Session, 1)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(dead.URL, []string{dead.URL, live.URL})
	go func() {
		done <- Run(ctx, cfg, nil, func(s *Session) { ready <- s })
	}()

	want, _ := normalizeURL(live.URL, "t")
	select {
	case s := <-ready:
		if got := s.Path(); got != want {
			t.Errorf("Path() = %q, want %q（应落到第二条候选）", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("第一条候选失败后没有切到第二条")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未返回")
	}
}

func TestRun_Unauthorized_StopsBeforeTryingNextPath(t *testing.T) {
	hits := &atomic.Int32{}
	deny := denyServer(t, http.StatusUnauthorized)
	defer deny.Close()
	live := ackServer(t, hits)
	defer live.Close()

	cfg := newTestConfig(deny.URL, []string{deny.URL, live.URL})
	err := Run(context.Background(), cfg, nil, nil)
	if !errors.Is(err, ErrFatal) {
		t.Fatalf("401 应按不可重试直接停，err=%v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("401 后不应再试下一条地址，实际命中 %d 次", got)
	}
}

func TestRun_AllPathsFail_ReportsEveryCandidate(t *testing.T) {
	dead1 := denyServer(t, http.StatusNotFound)
	defer dead1.Close()
	dead2 := denyServer(t, http.StatusServiceUnavailable)
	defer dead2.Close()

	cfg := newTestConfig(dead1.URL, []string{dead1.URL, dead2.URL})
	err := Run(context.Background(), cfg, nil, nil)
	if err == nil || errors.Is(err, ErrFatal) {
		t.Fatalf("全部失败不应按致命错误处理，err=%v", err)
	}
	if !strings.Contains(err.Error(), dead1.URL) || !strings.Contains(err.Error(), dead2.URL) {
		t.Errorf("错误应列出两条候选地址，实际：%v", err)
	}
}

func TestRun_SinglePath_KeepsLegacyErrorText(t *testing.T) {
	dead := denyServer(t, http.StatusNotFound)
	defer dead.Close()

	cfg := newTestConfig(dead.URL, nil)
	err := Run(context.Background(), cfg, nil, nil)
	if err == nil {
		t.Fatal("应当报错")
	}
	if !strings.Contains(err.Error(), "连接后端失败（HTTP 404）") {
		t.Errorf("单路径错误文案应保持旧格式，实际：%v", err)
	}
}

func TestPathsAndDialerDefaults(t *testing.T) {
	c := Config{URL: "http://x"}
	if got := c.paths(); len(got) != 1 || got[0] != "http://x" {
		t.Errorf("未配 Paths 应退回 URL，得到 %v", got)
	}
	if c.dialer() == nil {
		t.Error("未指定 Dialer 应给默认直连拨号器")
	}

	c2 := Config{URL: "http://x", Paths: []string{"", "  ", "http://a", "http://b"}}
	got := c2.paths()
	if len(got) != 2 || got[0] != "http://a" || got[1] != "http://b" {
		t.Errorf("空段应被丢弃，得到 %v", got)
	}

	fake := &DirectDialer{}
	c3 := Config{URL: "http://x", Dialer: fake}
	if c3.dialer() != Dialer(fake) {
		t.Error("指定了 Dialer 就该用指定的")
	}
}

// DirectDialer 只是为了编译期确认接口形状可被外部实现。
type DirectDialer struct{}

func (d *DirectDialer) DialContext(ctx context.Context, urlStr string, header http.Header) (*websocket.Conn, *http.Response, error) {
	return nil, nil, errors.New("not implemented")
}
