package observe

import "testing"

// 外部 Agent（委托目标）的按 target 统计：次数/失败/平均耗时/最近出错，且空名不建条目
func TestMetricsTargets(t *testing.T) {
	m := NewMetrics()

	// 空名/纯空白不建条目（免得界面多一行没名字的）
	m.NoteTarget("", true, 5, "")
	m.NoteTarget("   ", true, 5, "")

	m.NoteTarget("remote-a", true, 100, "")
	m.NoteTarget("remote-a", false, 300, "连不上") // 失败那次也要计进 Calls 与耗时
	m.NoteTarget("remote-b", true, 50, "")

	snap := m.TargetSnapshot()
	if len(snap) != 2 {
		t.Fatalf("应当统计到 2 个目标，实际 %d：%+v", len(snap), snap)
	}
	// 调用数多的排前面
	if snap[0].Target != "remote-a" {
		t.Fatalf("remote-a 应排在前：%+v", snap)
	}
	if snap[0].Calls != 2 || snap[0].Failed != 1 || snap[0].TotalMs != 400 || snap[0].AvgMs != 200 {
		t.Fatalf("remote-a 统计不对：%+v", snap[0])
	}
	if snap[0].LastErr != "连不上" || snap[0].LastAt == 0 {
		t.Fatalf("最近出错/时间没记下：%+v", snap[0])
	}
	if snap[1].Target != "remote-b" || snap[1].Calls != 1 || snap[1].Failed != 0 || snap[1].AvgMs != 50 {
		t.Fatalf("remote-b 统计不对：%+v", snap[1])
	}

	// nil 接收者安全（服务层可能没接计数器）
	var nilm *Metrics
	nilm.NoteTarget("x", true, 1, "")
	if len(nilm.TargetSnapshot()) != 0 {
		t.Fatal("nil 计数器应回空快照")
	}
}
