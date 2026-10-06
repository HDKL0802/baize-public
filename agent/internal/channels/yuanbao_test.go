package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"baize/internal/config"
)

// pbBytesField 从 protobuf 字节里取某个 length-delimited 字段（测试用）
func pbBytesField(t *testing.T, b []byte, field int) []byte {
	t.Helper()
	r := &pbReader{b: b}
	for r.more() {
		f, w, err := r.tag()
		if err != nil {
			t.Fatalf("解 tag 失败：%v", err)
		}
		if f == field {
			if w != 2 {
				t.Fatalf("字段 %d 不是 length-delimited（wire=%d）", field, w)
			}
			v, err := r.bytes()
			if err != nil {
				t.Fatalf("读字段 %d 失败：%v", field, err)
			}
			return v
		}
		if err := r.skip(w); err != nil {
			t.Fatalf("跳过字段失败：%v", err)
		}
	}
	t.Fatalf("没找到字段 %d", field)
	return nil
}

func pbStrField(t *testing.T, b []byte, field int) string {
	t.Helper()
	return string(pbBytesField(t, b, field))
}

func pbUintField(t *testing.T, b []byte, field int) uint64 {
	t.Helper()
	r := &pbReader{b: b}
	for r.more() {
		f, w, err := r.tag()
		if err != nil {
			t.Fatalf("解 tag 失败：%v", err)
		}
		if f == field {
			if w != 0 {
				t.Fatalf("字段 %d 不是 varint（wire=%d）", field, w)
			}
			v, err := r.varint()
			if err != nil {
				t.Fatalf("读字段 %d 失败：%v", field, err)
			}
			return v
		}
		if err := r.skip(w); err != nil {
			t.Fatalf("跳过字段失败：%v", err)
		}
	}
	t.Fatalf("没找到字段 %d", field)
	return 0
}

func TestYuanbaoProtoRoundTrip(t *testing.T) {
	head := yuanbaoHead{
		cmdType: yuanbaoCmdTypePushAck, cmd: yuanbaoCmdSendC2C,
		seqNo: 7, msgID: "m1", module: yuanbaoModuleBiz, needAck: true,
	}
	raw := encodeConnMsg(head, []byte("hello"))
	cm, ok := decodeConnMsg(raw)
	if !ok {
		t.Fatal("ConnMsg 解不出来")
	}
	if cm.head.cmdType != yuanbaoCmdTypePushAck || cm.head.cmd != yuanbaoCmdSendC2C ||
		cm.head.seqNo != 7 || cm.head.msgID != "m1" ||
		cm.head.module != yuanbaoModuleBiz || !cm.head.needAck {
		t.Fatalf("head 往返不对：%+v", cm.head)
	}
	if string(cm.data) != "hello" {
		t.Fatalf("data 往返不对：%q", cm.data)
	}

	// status（field 10）只在应答侧出现（出站头不带 status），手工造一条服务端应答验证解码
	var hb []byte
	hb = pbPutString(hb, 2, yuanbaoCmdPing)
	hb = pbPutUint(hb, 10, 41104)
	statusRaw := pbPutBytes(nil, 1, hb)
	cm2, ok := decodeConnMsg(statusRaw)
	if !ok {
		t.Fatal("带 status 的 ConnMsg 解不出来")
	}
	if cm2.head.status != 41104 {
		t.Fatalf("status 不对：%d", cm2.head.status)
	}
	if len(cm2.data) != 0 {
		t.Fatalf("空 data 不该被编码：%q", cm2.data)
	}

	// AuthBindReq：bizId / authInfo{uid,source,token} / deviceInfo.instanceId
	ab := encodeAuthBindReq("ybBot", "bot_1", "bot", "tk-1")
	if got := pbStrField(t, ab, 1); got != "ybBot" {
		t.Fatalf("bizId 不对：%q", got)
	}
	auth := pbBytesField(t, ab, 2)
	if pbStrField(t, auth, 1) != "bot_1" || pbStrField(t, auth, 2) != "bot" || pbStrField(t, auth, 3) != "tk-1" {
		t.Fatalf("AuthInfo 不对：%x", auth)
	}
	dev := pbBytesField(t, ab, 3)
	if pbStrField(t, dev, 10) != yuanbaoDeviceInstanceID {
		t.Fatalf("deviceInfo.instanceId 不对：%x", dev)
	}

	// AuthBindRsp / PingRsp 解码
	if c, present := decodeAuthRspCode(pbPutUint(nil, 1, 41101)); !present || c != 41101 {
		t.Fatalf("AuthBindRsp code 解错：%d/%v", c, present)
	}
	if _, present := decodeAuthRspCode(nil); present {
		t.Fatal("空 AuthBindRsp 不该解出 code")
	}
	if iv := decodePingHeartInterval(pbPutUint(nil, 1, 30)); iv != 30 {
		t.Fatalf("heartInterval 解错：%d", iv)
	}

	// SendC2CMessageReq：toAccount/fromAccount/msgRandom/msgBody
	c2c := encodeSendC2CReq("user_1", "bot_1", 12345, "你好")
	if pbStrField(t, c2c, 2) != "user_1" || pbStrField(t, c2c, 3) != "bot_1" {
		t.Fatalf("C2C 账号字段不对：%x", c2c)
	}
	if pbUintField(t, c2c, 4) != 12345 {
		t.Fatalf("C2C msgRandom 不对")
	}
	elem := pbBytesField(t, c2c, 5)
	if pbStrField(t, elem, 1) != "TIMTextElem" {
		t.Fatalf("msgType 不对：%x", elem)
	}
	if pbStrField(t, pbBytesField(t, elem, 2), 1) != "你好" {
		t.Fatalf("C2C 文本不对")
	}

	// SendGroupMessageReq：groupCode/random/msgBody
	grp := encodeSendGroupReq("grp_9", "bot_1", "888", "收到")
	if pbStrField(t, grp, 2) != "grp_9" || pbStrField(t, grp, 5) != "888" {
		t.Fatalf("群消息字段不对：%x", grp)
	}
	ge := pbBytesField(t, grp, 6)
	if pbStrField(t, pbBytesField(t, ge, 2), 1) != "收到" {
		t.Fatalf("群文本不对")
	}
}

func TestYuanbaoStreamInboundAndSend(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	var (
		sigMu sync.Mutex
		sigOK bool
	)

	authCh := make(chan yuanbaoConnMsg, 1)
	ackCh := make(chan yuanbaoConnMsg, 1)
	sendCh := make(chan yuanbaoConnMsg, 1)

	mux := http.NewServeMux()

	// sign-token：校验 HMAC 签名并回 token
	mux.HandleFunc(yuanbaoSignTokenPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		expected := yuanbaoSignature(req["nonce"], req["timestamp"], "appid", "secret")
		sigMu.Lock()
		sigOK = req["app_key"] == "appid" && expected == req["signature"]
		sigMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bot_id": "bot_1", "token": "tk-1", "source": "bot", "duration": 3600,
			},
		})
	})

	// 元宝 WebSocket：AuthBind → AuthBindRsp → 推入站 → 收 ACK + 出站
	mux.HandleFunc("/wss/connection", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_, b, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if cm, ok := decodeConnMsg(b); ok {
			select {
			case authCh <- cm:
			default:
			}
		}
		// AuthBindRsp：成功时 data 可以为空（与 channel.py 注释一致）
		rsp := encodeConnMsg(yuanbaoHead{
			cmdType: yuanbaoCmdTypeResponse, cmd: yuanbaoCmdAuthBind,
			seqNo: 1, msgID: "a1", module: yuanbaoModuleConnAccess,
		}, nil)
		_ = conn.WriteMessage(websocket.BinaryMessage, rsp)

		// 推一条群消息（data 是 JSON 字符串）
		inboundJSON := `{"callback_command":"Group.recv","from_account":"user_abcdef123",` +
			`"sender_nickname":"甲","group_code":"grp_9","msg_id":"m1",` +
			`"msg_body":[{"msg_type":"TIMTextElem","msg_content":{"text":"你好白泽"}}]}`
		push := encodeConnMsg(yuanbaoHead{
			cmdType: yuanbaoCmdTypePush, cmd: "Group.recv",
			seqNo: 2, msgID: "p1", module: yuanbaoModuleBiz, needAck: true,
		}, []byte(inboundJSON))
		_ = conn.WriteMessage(websocket.BinaryMessage, push)

		// 读后续帧：ACK（push-ack）+ 出站消息；跳过心跳 ping
		needAck, needSend := true, true
		deadline := time.Now().Add(6 * time.Second)
		for (needAck || needSend) && time.Now().Before(deadline) {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, fb, err := conn.ReadMessage()
			if err != nil {
				break
			}
			cm, ok := decodeConnMsg(fb)
			if !ok {
				continue
			}
			switch cm.head.cmdType {
			case yuanbaoCmdTypePushAck:
				if needAck {
					select {
					case ackCh <- cm:
					default:
					}
					needAck = false
				}
			case yuanbaoCmdTypeRequest:
				if cm.head.cmd == yuanbaoCmdSendGroup || cm.head.cmd == yuanbaoCmdSendC2C {
					if needSend {
						select {
						case sendCh <- cm:
						default:
						}
						needSend = false
					}
				}
			}
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	yb := NewYuanbao(config.ChannelConfig{
		ID: "yb", Kind: "yuanbao", Enabled: true,
		AppID: "appid", AppSecret: "secret",
		Domain:      srv.URL,
		OutboundURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/wss/connection",
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msgCh := make(chan Message, 4)
	go yb.Poll(ctx, func(m Message) { msgCh <- m })

	var msg Message
	select {
	case msg = <-msgCh:
	case <-time.After(5 * time.Second):
		t.Fatal("没收到腾讯元宝入站消息")
	}
	if msg.Session != "yuanbao:group:grp_9" {
		t.Fatalf("群会话键不对：%s", msg.Session)
	}
	if msg.Text != "你好白泽" {
		t.Fatalf("正文不对：%s", msg.Text)
	}
	if msg.Sender != "甲#f123" {
		t.Fatalf("发送者不对：%s", msg.Sender)
	}
	if v, _ := msg.Meta["is_group"].(bool); !v {
		t.Fatal("应当标成群")
	}
	if msg.Meta["sender_id"] != "user_abcdef123" {
		t.Fatalf("原始账号不对：%v", msg.Meta["sender_id"])
	}

	// 校验 AuthBind 内容
	select {
	case cm := <-authCh:
		if cm.head.cmd != yuanbaoCmdAuthBind || cm.head.module != yuanbaoModuleConnAccess {
			t.Fatalf("AuthBind 帧头不对：%+v", cm.head)
		}
		if pbStrField(t, cm.data, 1) != "ybBot" {
			t.Fatalf("AuthBind bizId 不对")
		}
		auth := pbBytesField(t, cm.data, 2)
		if pbStrField(t, auth, 1) != "bot_1" || pbStrField(t, auth, 2) != "bot" || pbStrField(t, auth, 3) != "tk-1" {
			t.Fatalf("AuthBind authInfo 不对：%x", auth)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("服务端没收到 AuthBind")
	}

	sigMu.Lock()
	ok := sigOK
	sigMu.Unlock()
	if !ok {
		t.Fatal("sign-token 签名不对")
	}

	// 校验推送 ACK
	select {
	case cm := <-ackCh:
		if cm.head.cmdType != yuanbaoCmdTypePushAck || cm.head.msgID != "p1" || cm.head.cmd != "Group.recv" {
			t.Fatalf("ACK 帧不对：%+v", cm.head)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("服务端没收到推送 ACK")
	}

	// 回发
	if err := yb.Send(ctx, msg, "收到"); err != nil {
		t.Fatalf("回发失败：%v", err)
	}
	select {
	case cm := <-sendCh:
		if cm.head.cmd != yuanbaoCmdSendGroup || cm.head.module != yuanbaoModuleBiz {
			t.Fatalf("出站帧头不对：%+v", cm.head)
		}
		if pbStrField(t, cm.data, 2) != "grp_9" {
			t.Fatalf("群号不对：%x", cm.data)
		}
		if pbStrField(t, cm.data, 3) != "bot_1" {
			t.Fatalf("fromAccount 不对：%x", cm.data)
		}
		elem := pbBytesField(t, cm.data, 6)
		if pbStrField(t, pbBytesField(t, elem, 2), 1) != "收到" {
			t.Fatalf("回发文本不对：%x", elem)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("服务端没收到出站消息")
	}
}

func TestYuanbaoParseInbound(t *testing.T) {
	yb := NewYuanbao(config.ChannelConfig{ID: "yb"}, nil)
	yb.setBotID("bot_1")

	grp := `{"callback_command":"Group.recv","from_account":"user_abcdef123",` +
		`"sender_nickname":"甲","group_code":"grp_9","msg_id":"m1",` +
		`"msg_body":[{"msg_type":"TIMTextElem","msg_content":{"text":"@bot_1 你好白泽"}}]}`
	msg, ok := yb.parseInbound([]byte(grp))
	if !ok {
		t.Fatal("群消息应当产出")
	}
	if msg.Session != "yuanbao:group:grp_9" {
		t.Fatalf("群会话键不对：%s", msg.Session)
	}
	if msg.Text != "你好白泽" {
		t.Fatalf("去 @ 后正文不对：%q", msg.Text)
	}
	if msg.Sender != "甲#f123" {
		t.Fatalf("发送者不对：%s", msg.Sender)
	}
	if v, _ := msg.Meta["is_group"].(bool); !v {
		t.Fatal("应当标成群")
	}

	// 单聊：会话键取账号后 8 位
	c2c := `{"callback_command":"C2C.recv","from_account":"user_abcdef123",` +
		`"sender_nickname":"乙","msg_id":"m2",` +
		`"msg_body":[{"msg_type":"TIMTextElem","msg_content":{"text":"hi"}}]}`
	msg2, ok := yb.parseInbound([]byte(c2c))
	if !ok {
		t.Fatal("单聊消息应当产出")
	}
	if msg2.Session != "yuanbao:c2c:bcdef123" {
		t.Fatalf("单聊会话键不对：%s", msg2.Session)
	}
	if v, _ := msg2.Meta["is_group"].(bool); v {
		t.Fatal("单聊不该标成群")
	}

	// 机器人消息（from_account 以 bot_ 开头）跳过
	if _, ok := yb.parseInbound([]byte(`{"callback_command":"C2C.recv","from_account":"bot_999",` +
		`"msg_body":[{"msg_type":"TIMTextElem","msg_content":{"text":"hi"}}]}`)); ok {
		t.Fatal("机器人消息不该产出")
	}
	// 空文本
	if _, ok := yb.parseInbound([]byte(`{"callback_command":"C2C.recv","from_account":"u1",` +
		`"msg_body":[{"msg_type":"TIMTextElem","msg_content":{"text":"   "}}]}`)); ok {
		t.Fatal("空文本不该产出消息")
	}
	// 非 JSON
	if _, ok := yb.parseInbound([]byte(`not json`)); ok {
		t.Fatal("非 JSON 不该产出消息")
	}
	// 没有 callback_command
	if _, ok := yb.parseInbound([]byte(`{"from_account":"u1","msg_body":[]}`)); ok {
		t.Fatal("没有 callback_command 不该产出消息")
	}
}

func TestYuanbaoSendErrors(t *testing.T) {
	// 没配凭证：Send 明确报错，Poll 直接返回不 panic
	noCreds := NewYuanbao(config.ChannelConfig{ID: "yb"}, nil)
	if err := noCreds.Send(context.Background(), Message{Channel: "yb"}, "hi"); err == nil ||
		!strings.Contains(err.Error(), "appId") {
		t.Fatalf("没配凭证应当明确报错，得到：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	noCreds.Poll(ctx, func(Message) {})

	// 配了凭证但没连接
	yb := NewYuanbao(config.ChannelConfig{ID: "yb", AppID: "appid", AppSecret: "secret"}, nil)
	err := yb.Send(context.Background(), Message{
		Channel: "yb", Session: "yuanbao:c2c:x",
		Meta: map[string]any{"sender_id": "u1"},
	}, "hi")
	if err == nil || !strings.Contains(err.Error(), "可用连接") {
		t.Fatalf("没连接应当明确报错，得到：%v", err)
	}

	// 没有可回发目标（无 Meta、无会话）
	if err := yb.Send(context.Background(), Message{Channel: "yb"}, "hi"); err == nil ||
		!strings.Contains(err.Error(), "可回发") {
		t.Fatalf("没有回发目标应当明确报错，得到：%v", err)
	}
}

func TestYuanbaoDefaultsAndChunk(t *testing.T) {
	yb := NewYuanbao(config.ChannelConfig{ID: "yb"}, nil)
	if yb.wsURL != yuanbaoDefaultWSURL {
		t.Fatalf("默认 ws 地址不对：%s", yb.wsURL)
	}
	if got := yb.signTokenURL(); got != "https://"+yuanbaoDefaultAPIDomain+yuanbaoSignTokenPath {
		t.Fatalf("默认 sign-token 地址不对：%s", got)
	}
	yb2 := NewYuanbao(config.ChannelConfig{ID: "yb", Domain: "bot.example.com/"}, nil)
	if got := yb2.signTokenURL(); got != "https://bot.example.com"+yuanbaoSignTokenPath {
		t.Fatalf("域名拼接不对：%s", got)
	}

	if got := yuanbaoChunkText("短文本"); len(got) != 1 || got[0] != "短文本" {
		t.Fatalf("短文本不该切：%v", got)
	}
	long := strings.Repeat("甲", yuanbaoTEXTChunkLimit+10)
	got := yuanbaoChunkText(long)
	if len(got) != 2 {
		t.Fatalf("超长单行应当硬切成 2 段，得到 %d 段", len(got))
	}
	if len([]rune(got[0])) != yuanbaoTEXTChunkLimit {
		t.Fatalf("首段长度不对：%d", len([]rune(got[0])))
	}
	if len([]rune(got[1])) != 10 {
		t.Fatalf("次段长度不对：%d", len([]rune(got[1])))
	}
}
