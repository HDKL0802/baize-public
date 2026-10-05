package channels

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"baize/internal/config"
)

/* 飞书频道单元测试：事件回调解析（握手 / 令牌 / 文本 / 非消息）、AES 解密、出站发消息。 */

func TestFeishuHandleEvent(t *testing.T) {
	f := NewFeishu(config.ChannelConfig{ID: "fs", Token: "vtok", AppID: "cli_x", AppSecret: "sec"})

	// URL 验证握手
	ch, msgs, err := f.HandleEvent([]byte(`{"type":"url_verification","challenge":"abc","token":"vtok"}`), nil)
	if err != nil || ch != "abc" || len(msgs) != 0 {
		t.Fatalf("握手不对：ch=%q msgs=%d err=%v", ch, len(msgs), err)
	}
	// 令牌不对 → 401
	_, _, err = f.HandleEvent([]byte(`{"header":{"event_type":"im.message.receive_v1","token":"bad"}}`), nil)
	var ie *InboundError
	if !errors.As(err, &ie) || ie.Code != 401 {
		t.Fatalf("令牌不对应 401：%v", err)
	}
	// 正常消息事件（content 里带 @ 占位要去掉）
	body := `{"schema":"2.0","header":{"event_type":"im.message.receive_v1","token":"vtok"},` +
		`"event":{"sender":{"sender_id":{"open_id":"ou_1"}},` +
		`"message":{"message_id":"m1","chat_id":"oc_1","chat_type":"group","message_type":"text",` +
		`"content":"{\"text\":\"@_user_1 帮我看看\"}"}}}`
	_, msgs, err = f.HandleEvent([]byte(body), nil)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("消息事件解析失败：%v msgs=%d", err, len(msgs))
	}
	m := msgs[0]
	if m.Channel != "fs" || m.Text != "帮我看看" || m.Session != "feishu:oc_1" || m.Sender != "ou_1" {
		t.Fatalf("消息解析不对：%+v", m)
	}
	if m.Meta["receive_id"] != "oc_1" {
		t.Fatalf("receive_id 不对：%+v", m.Meta)
	}
	// 非消息事件 → 0 条
	if _, msgs, _ := f.HandleEvent([]byte(`{"header":{"event_type":"im.chat.updated","token":"vtok"}}`), nil); len(msgs) != 0 {
		t.Fatal("非消息事件不该产出消息")
	}
}

func TestFeishuDecrypt(t *testing.T) {
	encKey := "my-encrypt-key"
	plain := `{"type":"url_verification","challenge":"xyz","token":"vtok"}`
	enc := encryptForTest(t, encKey, plain)
	f := NewFeishu(config.ChannelConfig{ID: "fs", Token: "vtok", EncryptKey: encKey})
	body, _ := json.Marshal(map[string]string{"encrypt": enc})
	ch, _, err := f.HandleEvent(body, nil)
	if err != nil || ch != "xyz" {
		t.Fatalf("解密后握手失败：ch=%q err=%v", ch, err)
	}
	// 没配 encryptKey 时拿到加密体应被当作"令牌不对"挡住（而不是崩）
	g := NewFeishu(config.ChannelConfig{ID: "fs", Token: "vtok"})
	if _, _, err := g.HandleEvent(body, nil); err == nil {
		t.Fatal("没配加密钥匙时加密事件应被挡")
	}
}

func TestFeishuSend(t *testing.T) {
	var gotMsg map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/v3/tenant_access_token/internal"):
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"t-123","expire":7200}`))
		case strings.HasSuffix(r.URL.Path, "/im/v1/messages"):
			auth = r.Header.Get("Authorization")
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotMsg)
			_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	f := NewFeishu(config.ChannelConfig{ID: "fs", AppID: "cli_x", AppSecret: "sec", Domain: srv.URL, BotPrefix: "[泽] "})
	if err := f.Send(context.Background(), Message{Channel: "fs", Meta: map[string]any{"receive_id": "oc_1"}}, "你好"); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if auth != "Bearer t-123" {
		t.Fatalf("Authorization 不对：%q", auth)
	}
	if gotMsg["receive_id"] != "oc_1" || gotMsg["msg_type"] != "text" {
		t.Fatalf("请求体不对：%+v", gotMsg)
	}
	if c, _ := gotMsg["content"].(string); !strings.Contains(c, "[泽] 你好") {
		t.Fatalf("content 不对：%q", gotMsg["content"])
	}
}

func TestFeishuSendRejectsBizError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "tenant_access_token") {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"t","expire":7200}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":99991,"msg":"no permission"}`))
	}))
	defer srv.Close()
	f := NewFeishu(config.ChannelConfig{ID: "fs", AppID: "a", AppSecret: "b", Domain: srv.URL})
	if err := f.Send(context.Background(), Message{Meta: map[string]any{"receive_id": "oc"}}, "x"); err == nil {
		t.Fatal("飞书返回 code!=0 应报错")
	}
}

// encryptForTest 按飞书算法加密（key=sha256，AES-256-CBC，IV 前置）
func encryptForTest(t *testing.T, encKey, plain string) string {
	t.Helper()
	key := sha256.Sum256([]byte(encKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	pt := []byte(plain)
	if pad := aes.BlockSize - len(pt)%aes.BlockSize; pad > 0 {
		pt = append(pt, bytes.Repeat([]byte{byte(pad)}, pad)...)
	}
	iv := []byte("0123456789abcdef")
	out := make([]byte, len(pt))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, pt)
	return base64.StdEncoding.EncodeToString(append(append([]byte{}, iv...), out...))
}
