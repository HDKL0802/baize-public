package agentsvc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"baize/internal/config"
)

/* ---------- activeHours ---------- */

// 活跃时间段的判断：空=全天；普通窗口含起不含止；跨夜窗口要能正确翻转；
// 解析不出来就放行（宁可多跑一次，也不能因为配置笔误把心跳静默关掉）
func TestInActiveHours(t *testing.T) {
	loc := time.Local
	cases := []struct {
		ah   string
		at   time.Time
		want bool
	}{
		{"", time.Date(2026, 10, 5, 3, 0, 0, 0, loc), true},
		{"08:00-22:00", time.Date(2026, 10, 5, 10, 0, 0, 0, loc), true},
		{"08:00-22:00", time.Date(2026, 10, 5, 23, 0, 0, 0, loc), false},
		{"08:00-22:00", time.Date(2026, 10, 5, 8, 0, 0, 0, loc), true},   // 起点含
		{"08:00-22:00", time.Date(2026, 10, 5, 22, 0, 0, 0, loc), false}, // 止点不含
		{"22:00-08:00", time.Date(2026, 10, 5, 23, 0, 0, 0, loc), true},  // 跨夜：深夜
		{"22:00-08:00", time.Date(2026, 10, 5, 7, 0, 0, 0, loc), true},   // 跨夜：清晨
		{"22:00-08:00", time.Date(2026, 10, 5, 12, 0, 0, 0, loc), false}, // 跨夜：白天
		{"bad", time.Date(2026, 10, 5, 12, 0, 0, 0, loc), true},          // 格式不对：放行
		{"08:00", time.Date(2026, 10, 5, 12, 0, 0, 0, loc), true},        // 缺一段：放行
	}
	for _, c := range cases {
		if got := inActiveHours(c.ah, c.at); got != c.want {
			t.Errorf("inActiveHours(%q, %s) = %v，应为 %v", c.ah, c.at.Format("15:04"), got, c.want)
		}
	}
}

/* ---------- HEARTBEAT.md 的读取 ---------- */

// 心跳要跑的活取自工作区的 HEARTBEAT.md：文件不在 / 空着都要明确报错，不能空跑
func TestLoadHeartbeatQuery(t *testing.T) {
	s := newService(t, nil, nil)
	path := filepath.Join(s.Workdir(), heartbeatFileName)

	if _, err := s.loadHeartbeatQuery(); err == nil {
		t.Fatal("HEARTBEAT.md 不在时应报错")
	}
	if err := os.WriteFile(path, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.loadHeartbeatQuery(); err == nil {
		t.Fatal("HEARTBEAT.md 为空时应报错")
	}
	if err := os.WriteFile(path, []byte("  检查今天的待办  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	q, err := s.loadHeartbeatQuery()
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if q != "检查今天的待办" {
		t.Fatalf("应返回去掉首尾空白的内容，实际 %q", q)
	}
}

// 控制台要能读写 HEARTBEAT.md：文件不存在是正常状态（exists=false 且不报错），
// 写入空内容要被拒（不想跑就关掉心跳，而不是留个空文件让它每次都失败）
func TestHeartbeatReadWrite(t *testing.T) {
	s := newService(t, nil, nil)

	content, exists, err := s.HeartbeatRead()
	if err != nil {
		t.Fatalf("文件不在时不该报错：%v", err)
	}
	if exists || content != "" {
		t.Fatalf("文件不在时应返回 exists=false：exists=%v content=%q", exists, content)
	}

	if err := s.HeartbeatWrite("   "); err == nil {
		t.Fatal("写入空内容应被拒")
	}
	if err := s.HeartbeatWrite("# 心跳\n\n检查今天的待办\n"); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	content, exists, err = s.HeartbeatRead()
	if err != nil || !exists {
		t.Fatalf("写完应能读回：exists=%v err=%v", exists, err)
	}
	if !strings.Contains(content, "检查今天的待办") {
		t.Fatalf("读回的内容不对：%q", content)
	}
	if got := filepath.Base(s.HeartbeatPath()); got != heartbeatFileName {
		t.Fatalf("路径应指向工作区的 %s，实际 %q", heartbeatFileName, got)
	}
}

// 「立刻跑一次」要真的异步跑起来并记结果；文件不在时要当场报错（不排队干等）
func TestStartHeartbeat(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("心跳完成") })
	s := newService(t, f, nil)

	if _, err := s.StartHeartbeat(); err == nil {
		t.Fatal("HEARTBEAT.md 不在时应立刻报错")
	}
	if err := s.HeartbeatWrite("检查今天的待办"); err != nil {
		t.Fatal(err)
	}
	id, err := s.StartHeartbeat()
	if err != nil {
		t.Fatalf("派发失败：%v", err)
	}
	if id == "" {
		t.Fatal("应返回运行 id")
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cfg := s.Config(); cfg.Heartbeat.Runs > 0 {
			if cfg.Heartbeat.LastStatus != "done" {
				t.Fatalf("心跳状态应为 done：%+v", cfg.Heartbeat)
			}
			runs, _ := s.Runs(5)
			if len(runs) == 0 || runs[0].RunID != id {
				t.Fatalf("运行记录应能按派发时给的 id 对上：%+v", runs)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("立刻跑一次没有生效")
}

/* ---------- 排期 ---------- */

// 关掉 → 不排期；表达式合法 → 排出下次时间；非法 → 记录解析错误
func TestScheduleHeartbeat(t *testing.T) {
	s := newService(t, nil, nil)
	sc := s.sched
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.Local)

	sc.mu.Lock()
	sc.scheduleHeartbeat(config.HeartbeatConfig{Enabled: false}, now)
	if sc.hbParsed != nil || !sc.hbNext.IsZero() || sc.hbExpr != "" {
		t.Errorf("心跳关闭时不应有排期：parsed=%v next=%v expr=%q", sc.hbParsed, sc.hbNext, sc.hbExpr)
	}
	sc.mu.Unlock()

	sc.mu.Lock()
	sc.scheduleHeartbeat(config.HeartbeatConfig{Enabled: true, Every: "@every 30m"}, now)
	if sc.hbParsed == nil || sc.hbErr != "" || sc.hbNext.IsZero() {
		t.Errorf("合法表达式应排期成功：err=%q next=%v", sc.hbErr, sc.hbNext)
	}
	if want := now.Add(30 * time.Minute); !sc.hbNext.Equal(want) {
		t.Errorf("@every 30m 的下次应为 %s，实际 %s", want, sc.hbNext)
	}
	sc.mu.Unlock()

	sc.mu.Lock()
	sc.scheduleHeartbeat(config.HeartbeatConfig{Enabled: true, Every: "bad expr"}, now)
	if sc.hbErr == "" || sc.hbParsed != nil {
		t.Errorf("非法表达式应记录解析错误：err=%q parsed=%v", sc.hbErr, sc.hbParsed)
	}
	sc.mu.Unlock()
}

/* ---------- 状态 ---------- */

// 控制台状态里要能看到开关、下次触发时间，以及 HEARTBEAT.md 的落地情况
func TestHeartbeatState(t *testing.T) {
	s := newService(t, nil, func(c *config.Config) {
		c.Heartbeat.Enabled = true
		c.Heartbeat.Every = "@every 30m"
	})
	if err := os.WriteFile(filepath.Join(s.Workdir(), heartbeatFileName), []byte("内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 手动排一次期，避免受后台 goroutine 的 prime 时机影响
	s.sched.mu.Lock()
	s.sched.scheduleHeartbeat(s.Config().Heartbeat, time.Now())
	s.sched.mu.Unlock()

	hs := s.Heartbeat()
	if !hs.Enabled {
		t.Fatal("状态里应反映心跳已启用")
	}
	if hs.NextAt == 0 {
		t.Fatal("应有下次触发时间")
	}
	if hs.Path == "" || !hs.HasFile {
		t.Fatalf("应检测到工作区的 HEARTBEAT.md：path=%q hasFile=%v", hs.Path, hs.HasFile)
	}
	if hs.Size == 0 {
		t.Fatal("应带上文件大小")
	}
}

/* ---------- 真跑一次 ---------- */

// 心跳到点要真的读 HEARTBEAT.md 跑一次活，并把结果写回配置、留在运行记录里
func TestHeartbeatFires(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("心跳巡检完成") })
	s := newService(t, f, func(c *config.Config) {
		c.Heartbeat.Enabled = true
		c.Heartbeat.Every = "@every 1m"
		c.Heartbeat.ActiveHours = "" // 不限时段，避免用例受当前时刻影响
	})
	if err := os.WriteFile(filepath.Join(s.Workdir(), heartbeatFileName), []byte("检查今天的待办"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.sched.fireHeartbeat(s.Config().Heartbeat)

	cfg := s.Config()
	if cfg.Heartbeat.Runs == 0 {
		t.Fatal("心跳应至少运行一次")
	}
	if cfg.Heartbeat.LastStatus != "done" {
		t.Fatalf("心跳状态应为 done：%+v", cfg.Heartbeat)
	}
	if cfg.Heartbeat.LastRunAt == 0 {
		t.Fatal("应记录最后运行时间")
	}
	// 结果要留在运行记录里，目标就是 HEARTBEAT.md 的内容
	runs, _ := s.Runs(5)
	if len(runs) == 0 || runs[0].Goal != "检查今天的待办" {
		t.Fatalf("心跳的运行记录不对：%+v", runs)
	}
}

// HEARTBEAT.md 不在时心跳要失败得有据可查，而不是静默跑个空活
func TestHeartbeatMissingFileRecordsFailure(t *testing.T) {
	s := newService(t, nil, func(c *config.Config) {
		c.Heartbeat.Enabled = true
		c.Heartbeat.ActiveHours = ""
	})
	s.sched.fireHeartbeat(s.Config().Heartbeat)

	cfg := s.Config()
	if cfg.Heartbeat.Runs == 0 || cfg.Heartbeat.LastStatus != "failed" {
		t.Fatalf("缺文件应记为失败：%+v", cfg.Heartbeat)
	}
	if !strings.Contains(cfg.Heartbeat.LastError, heartbeatFileName) {
		t.Fatalf("错误信息应点明缺的是 %s：%s", heartbeatFileName, cfg.Heartbeat.LastError)
	}
}

/* ---------- 心跳对人设的影响 ---------- */

// 心跳启用后，人设里 <!-- heartbeat:start/end --> 段应进入系统提示
// （与 service_test.go 里"未启用时不注入"的用例互为正反）
func TestPersonaHeartbeatSectionInjectedWhenEnabled(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("好") })
	s := newService(t, f, func(c *config.Config) { c.Heartbeat.Enabled = true })

	lib := s.Persona()
	if lib == nil {
		t.Fatal("人设库没建起来")
	}
	if err := lib.Write("AGENTS.md", "工作方式：先列清单。\n\n<!-- heartbeat:start -->\n心跳专属规则\n<!-- heartbeat:end -->\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "随便", "chat", "loose"); err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	f.mu.Lock()
	joined, _ := json.Marshal(f.bodies[len(f.bodies)-1]["messages"])
	f.mu.Unlock()
	if !strings.Contains(string(joined), "心跳专属规则") {
		t.Fatalf("心跳启用时心跳段应注入系统提示：%s", string(joined))
	}
}
