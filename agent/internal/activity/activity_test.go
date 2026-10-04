package activity

import (
	"context"
	"testing"

	"baize/internal/hooks"
)

// 从 run.start 到 run.finish：阶段要跟着事件走，结束后立刻回到空闲
func TestActivityLifecycle(t *testing.T) {
	bus := hooks.NewBus()
	tr := New(10)
	tr.Attach(bus)
	ctx := context.Background()

	// 初始：空闲
	if s := tr.Snapshot(); s.Active || s.Stage != StageIdle {
		t.Fatalf("初始应为空闲：%+v", s)
	}
	if s := tr.Snapshot(); s.Level != Level {
		t.Fatalf("档位应恒为 always_on，实得 %q", s.Level)
	}

	// 运行开始
	bus.Emit(ctx, hooks.EventRunStart, hooks.Payload{"runId": "r1", "goal": "整理本周计划"})
	s := tr.Snapshot()
	if !s.Active || s.Stage != StageRunning || s.Detail != "整理本周计划" || s.RunID != "r1" {
		t.Fatalf("run.start 后状态不对：%+v", s)
	}
	if s.SinceMs == 0 {
		t.Fatal("应记录活动开始时间")
	}

	// 问模型
	bus.Emit(ctx, hooks.EventLLMRequest, hooks.Payload{"runId": "r1", "provider": "deepseek"})
	if s = tr.Snapshot(); s.Stage != StageThinking {
		t.Fatalf("llm.request 后应是 thinking：%+v", s)
	}

	// 调工具
	bus.Emit(ctx, hooks.EventToolBefore, hooks.Payload{"runId": "r1", "tool": "fs_list"})
	if s = tr.Snapshot(); s.Stage != StageTool || s.Detail != "fs_list" {
		t.Fatalf("tool.before 后状态不对：%+v", s)
	}
	bus.Emit(ctx, hooks.EventToolAfter, hooks.Payload{"runId": "r1", "tool": "fs_list"})
	if s = tr.Snapshot(); s.Stage != StageToolDone {
		t.Fatalf("tool.after 后应是 tool_done：%+v", s)
	}

	// 记忆落盘
	bus.Emit(ctx, hooks.EventMemoryWrite, hooks.Payload{"runId": "r1", "chunks": 2})
	if s = tr.Snapshot(); s.Stage != StageMemory {
		t.Fatalf("memory.write 后应是 memory：%+v", s)
	}

	// 收尾：回到空闲，但轨迹里留痕
	bus.Emit(ctx, hooks.EventRunFinish, hooks.Payload{"runId": "r1", "status": "done", "steps": 3})
	s = tr.Snapshot()
	if s.Active || s.Stage != StageIdle {
		t.Fatalf("run.finish 后应回到空闲：%+v", s)
	}
	if s.Note == "" {
		t.Fatal("空闲时应给一句说明")
	}
	if len(s.Steps) == 0 || s.Steps[0].Stage != StageDone {
		t.Fatalf("轨迹首条应是收尾阶段：%+v", s.Steps)
	}
	// 新的事件在前
	if s.Steps[0].At < s.Steps[len(s.Steps)-1].At {
		t.Fatal("轨迹应按时间倒序（新的在前）")
	}

	// 统计
	if s.Stats.Runs != 1 || s.Stats.ToolCalls != 1 {
		t.Fatalf("统计不对：%+v", s.Stats)
	}
	if s.Stats.LastRunAt == 0 {
		t.Fatal("应记录最后一次收尾时间")
	}
}

// 失败收尾要落到 error 阶段
func TestRunFinishFailedIsError(t *testing.T) {
	bus := hooks.NewBus()
	tr := New(10)
	tr.Attach(bus)
	ctx := context.Background()

	bus.Emit(ctx, hooks.EventRunStart, hooks.Payload{"runId": "r2", "goal": "干点活"})
	bus.Emit(ctx, hooks.EventRunFinish, hooks.Payload{"runId": "r2", "status": "failed", "err": "超过最大步数"})
	s := tr.Snapshot()
	if s.Active {
		t.Fatalf("失败收尾后不该还算在跑：%+v", s)
	}
	if len(s.Steps) == 0 || s.Steps[0].Stage != StageError {
		t.Fatalf("失败应记为 error 阶段：%+v", s.Steps)
	}
	if s.Stats.Errors == 0 {
		t.Fatal("失败应计入错误数")
	}
}

// 被审批闸门拦下要有独立阶段，并计入 blocked
func TestBlockedStage(t *testing.T) {
	bus := hooks.NewBus()
	tr := New(10)
	tr.Attach(bus)
	ctx := context.Background()

	bus.Emit(ctx, hooks.EventRunStart, hooks.Payload{"runId": "r3"})
	bus.Emit(ctx, hooks.EventToolBlocked, hooks.Payload{"runId": "r3", "tool": "fs_delete"})
	s := tr.Snapshot()
	if s.Stage != StageBlocked {
		t.Fatalf("应进入 blocked 阶段：%+v", s)
	}
	if s.Stats.Blocked != 1 {
		t.Fatalf("应计入 blocked：%+v", s.Stats)
	}
	// 闸门拦下不算"在跑结束"：活动仍然是活的（模型还会继续）
	if !s.Active {
		t.Fatal("被拦下后活动仍在进行（模型会继续）")
	}
}

// 静默过久、又没收尾的活动，按陈旧处理，不再算在跑
func TestStaleActivityIsDropped(t *testing.T) {
	bus := hooks.NewBus()
	tr := New(10)
	tr.Attach(bus)
	ctx := context.Background()

	base := int64(1_700_000_000_000)
	tr.now = func() int64 { return base }
	bus.Emit(ctx, hooks.EventRunStart, hooks.Payload{"runId": "r4", "goal": "卡住了"})
	// 把时钟拨到 30 分钟之后
	tr.now = func() int64 { return base + staleAfter.Milliseconds() + 1 }
	s := tr.Snapshot()
	if s.Active || s.Stage != StageIdle {
		t.Fatalf("陈旧活动应被丢弃：%+v", s)
	}
}

// 轨迹条数要有上限，别把内存撑爆
func TestStepsAreBounded(t *testing.T) {
	bus := hooks.NewBus()
	tr := New(3)
	tr.Attach(bus)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		bus.Emit(ctx, hooks.EventToolBefore, hooks.Payload{"tool": "fs_list"})
	}
	if s := tr.Snapshot(); len(s.Steps) != 3 {
		t.Fatalf("轨迹应被截断到 3 条，实得 %d", len(s.Steps))
	}
}

// 没有总线、没有追踪器都不能崩（要么静默、要么给个空闲快照）
func TestNilSafety(t *testing.T) {
	var tr *Tracker
	if s := tr.Snapshot(); s.Level != Level || s.Stage != StageIdle {
		t.Fatalf("空追踪器应给空闲快照：%+v", s)
	}
	tr2 := New(0)
	tr2.Attach(nil) // 不订阅，不报错
	if s := tr2.Snapshot(); s.Stage != StageIdle {
		t.Fatalf("没装总线时也应是空闲：%+v", s)
	}
}
