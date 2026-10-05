package channels

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"baize/internal/config"
)

/* 频道核心单元测试。
   关注：出站请求体按 format 组装正确；非 2xx 要报错；Manager 的入站校验（令牌/停用/不存在）、
   入站真的跑 Agent 并回发、last 频道记录、主动发送。 */

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestWebhookSendFormats(t *testing.T) {
	cases := []struct {
		format string
		want   string
	}{
		{"generic", `"text":"`},
		{"feishu", `"msg_type":"text"`},
		{"dingtalk", `"msgtype":"text"`},
		{"slack", `"text":"`},
	}
	for _, c := range cases {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = string(b)
			w.WriteHeader(200)
		}))
		wh := NewWebhook(config.ChannelConfig{ID: "c1", OutboundURL: srv.URL, Format: c.format, BotPrefix: "[白泽] "})
		if err := wh.Send(context.Background(), Message{Channel: "c1", Session: "s", Sender: "u"}, "你好"); err != nil {
			t.Fatalf("%s 发送失败：%v", c.format, err)
		}
		srv.Close()
		if !strings.Contains(got, c.want) {
			t.Fatalf("%s 请求体形状不对：%s", c.format, got)
		}
		if !strings.Contains(got, "[白泽] 你好") {
			t.Fatalf("%s 前缀/正文没进请求体：%s", c.format, got)
		}
	}
}

func TestWebhookSendRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	wh := NewWebhook(config.ChannelConfig{ID: "c", OutboundURL: srv.URL})
	if err := wh.Send(context.Background(), Message{}, "x"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("非 2xx 应报错并带状态码：%v", err)
	}
}

func TestWebhookSendWithoutURL(t *testing.T) {
	wh := NewWebhook(config.ChannelConfig{ID: "c"})
	if err := wh.Send(context.Background(), Message{}, "x"); err == nil {
		t.Fatal("没配 outboundUrl 应报错")
	}
}

func TestNormalizeFormat(t *testing.T) {
	if NormalizeFormat("") != "generic" || NormalizeFormat("FEISHU") != "feishu" || NormalizeFormat("nope") != "generic" {
		t.Fatalf("format 归一不对：%q %q %q", NormalizeFormat(""), NormalizeFormat("FEISHU"), NormalizeFormat("nope"))
	}
}

func TestManagerInboundAndSend(t *testing.T) {
	outCh := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		select {
		case outCh <- string(b):
		default:
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	ran := make(chan string, 4)
	mgr := NewManager(func(_ context.Context, _ string, text string) (string, error) {
		ran <- text
		return "回复：" + text, nil
	}, quiet())
	mgr.Register(
		config.ChannelConfig{ID: "c1", Kind: "webhook", Enabled: true, Token: "sec", OutboundURL: srv.URL},
		NewWebhook(config.ChannelConfig{ID: "c1", OutboundURL: srv.URL}),
	)

	// 令牌不对 → 401
	if err := mgr.Inbound(Message{Channel: "c1", Text: "hi"}, "bad"); err == nil {
		t.Fatal("令牌不对应报错")
	} else {
		var ie *InboundError
		if !errors.As(err, &ie) || ie.Code != 401 {
			t.Fatalf("令牌错应是 401：%v", err)
		}
	}
	// 不存在的频道 → 404
	if err := mgr.Inbound(Message{Channel: "nope", Text: "hi"}, "x"); err == nil {
		t.Fatal("不存在的频道应报错")
	} else {
		var ie *InboundError
		if !errors.As(err, &ie) || ie.Code != 404 {
			t.Fatalf("不存在应是 404：%v", err)
		}
	}
	// 正确令牌 → 跑 Agent + 回发
	if err := mgr.Inbound(Message{Channel: "c1", Text: "你好", Sender: "u1"}, "sec"); err != nil {
		t.Fatalf("入站应通过：%v", err)
	}
	select {
	case txt := <-ran:
		if txt != "你好" {
			t.Fatalf("runner 收到的文本不对：%q", txt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runner 没被调用")
	}
	select {
	case body := <-outCh:
		if !strings.Contains(body, "回复：你好") {
			t.Fatalf("回发内容不对：%s", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("没收到回发")
	}
	if mgr.LastChannel() != "c1" {
		t.Fatalf("LastChannel 应为 c1，实际 %q", mgr.LastChannel())
	}

	// 停用的频道：不能入站、也不能主动发
	mgr.Register(
		config.ChannelConfig{ID: "c2", Kind: "webhook", Enabled: false, Token: "s2", OutboundURL: srv.URL},
		NewWebhook(config.ChannelConfig{ID: "c2", OutboundURL: srv.URL}),
	)
	if err := mgr.Inbound(Message{Channel: "c2", Text: "hi"}, "s2"); err == nil {
		t.Fatal("停用频道不该接受入站")
	}
	if err := mgr.Send(context.Background(), "c2", Message{}, "x"); err == nil {
		t.Fatal("停用频道不该能主动发")
	}
	// 主动发到启用频道
	if err := mgr.Send(context.Background(), "c1", Message{Session: "s"}, "主动一句"); err != nil {
		t.Fatalf("主动发应成功：%v", err)
	}
}

func TestManagerNoTokenRejectsInbound(t *testing.T) {
	mgr := NewManager(func(context.Context, string, string) (string, error) { return "x", nil }, quiet())
	mgr.Register(config.ChannelConfig{ID: "c", Kind: "webhook", Enabled: true}, NewWebhook(config.ChannelConfig{ID: "c"}))
	err := mgr.Inbound(Message{Channel: "c", Text: "hi"}, "")
	var ie *InboundError
	if !errors.As(err, &ie) || ie.Code != 409 {
		t.Fatalf("没设令牌应拒绝（409）：%v", err)
	}
}
