package calls

import (
	"testing"
)

func TestOpenCallIdempotentPerPair(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()

	// A 发起；B 再开同一对，应当拿到同一条
	c1, err := st.OpenCall("g", "uA", "甲", "uB", "乙", "")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := st.OpenCall("g", "uB", "乙", "uA", "甲", "")
	if err != nil {
		t.Fatal(err)
	}
	if c1.ID != c2.ID {
		t.Fatalf("同一对人重复开应当复用：%s vs %s", c1.ID, c2.ID)
	}
	// 复用时补挂到某次协商（自由通话中途关联）
	if c1.ConflictSID != "" {
		t.Fatalf("新开不该有协商：%+v", c1)
	}
	c3, err := st.OpenCall("g", "uB", "乙", "uA", "甲", "conf-1")
	if err != nil {
		t.Fatal(err)
	}
	if c3.ID != c1.ID || c3.ConflictSID != "conf-1" {
		t.Fatalf("复用并挂协商：%+v", c3)
	}
	// 不同对 = 新通话
	c4, _ := st.OpenCall("g", "uA", "甲", "uC", "丙", "")
	if c4.ID == c1.ID {
		t.Fatal("不同对不该复用")
	}
	// 同一个人不能自己跟自己通话
	if _, err := st.OpenCall("g", "uA", "甲", "uA", "甲", ""); err == nil {
		t.Fatal("自己给自己通话应当拒绝")
	}
}

func TestCallsListViewAndSignals(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()

	c, _ := st.OpenCall("g", "uA", "甲", "uB", "乙", "")

	// 查看者视角：uA 看，对方是 uB
	list, err := st.Calls("g", "uA", true)
	if err != nil || len(list) != 1 {
		t.Fatalf("uA 应有 1 条进行中通话：%v / %v", list, err)
	}
	if list[0].PeerID != "uB" || list[0].PeerName != "乙" {
		t.Fatalf("对方视角不对：%+v", list[0])
	}
	// 第三人看不到（a/b 都不是 uC）
	other, _ := st.Calls("g", "uC", true)
	if len(other) != 0 {
		t.Fatalf("组内第三人不该列出来：%+v", other)
	}

	// 留言与留言计数
	m, err := st.AddMessage(c.ID, "uA", "甲", "在吗？")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMessage(c.ID, "uA", "甲", "   "); err == nil {
		t.Fatal("空白留言应当拒绝")
	}
	if all, _ := st.Messages(c.ID, 0, 0); len(all) != 1 || all[0].ID != m.ID {
		t.Fatalf("留言不对：%+v", all)
	}
	if inc, _ := st.Messages(c.ID, m.ID, 0); len(inc) != 0 {
		t.Fatalf("增量拉取不应再有新消息：%+v", inc)
	}

	// 信令：定向给 uB 的 offer，uA 拉自己看不到自己发的（过滤）；uB 看得到
	_ = st.PostSignal(c.ID, "uA", "uB", "offer", `{"sdp":"1"}`)
	_ = st.PostSignal(c.ID, "uB", "uA", "answer", `{"sdp":"2"}`)
	seenA, err := st.Signals(c.ID, "uA", 0, 0)
	if err != nil || len(seenA) != 1 || seenA[0].Kind != "answer" {
		t.Fatalf("uA 应只看到 uB 的 answer：%+v / %v", seenA, err)
	}
	seenB, _ := st.Signals(c.ID, "uB", 0, 0)
	if len(seenB) != 1 || seenB[0].Kind != "offer" {
		t.Fatalf("uB 应只看到 uA 的 offer：%+v", seenB)
	}
	// 空 kind 拒绝
	if err := st.PostSignal(c.ID, "uA", "uB", " ", "{}"); err == nil {
		t.Fatal("空 kind 应当拒绝")
	}
}

func TestEndAndByConflict(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()

	c, _ := st.OpenCall("g", "uA", "甲", "uB", "乙", "conf-9")
	if got, err := st.ByConflict("g", "conf-9"); err != nil || len(got) != 1 {
		t.Fatalf("按协商查通话：%v / %v", got, err)
	}
	// 挂断后：列表（只看进行中）里没有了，全量里有
	ended, err := st.End(c.ID, "uA")
	if err != nil || ended.Status != StatusEnded {
		t.Fatalf("挂断状态不对：%+v / %v", ended, err)
	}
	live, _ := st.Calls("g", "uA", true)
	if len(live) != 0 {
		t.Fatalf("挂断后不该在进行中列表：%+v", live)
	}
	all, _ := st.Calls("g", "uA", false)
	if len(all) != 1 {
		t.Fatalf("全量应还有 1 条：%+v", all)
	}
	// 挂断后再开同一对 = 新通话（旧的不再复用）
	again, _ := st.OpenCall("g", "uA", "甲", "uB", "乙", "")
	if again.ID == c.ID {
		t.Fatal("挂断后重开应当是新通话")
	}
	// 组隔离
	if got, _ := st.CallInGroup("other", c.ID); got.ID != "" {
		t.Fatalf("别组不该取到这条通话：%+v", got)
	}
}
