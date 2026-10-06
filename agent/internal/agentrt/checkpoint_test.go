package agentrt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newCkHarness 建一个带工作目录的快照管理器
func newCkHarness(t *testing.T) (*CheckpointManager, string) {
	t.Helper()
	base := t.TempDir()
	work := filepath.Join(base, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := NewCheckpointManager(base, work, 5)
	if err != nil {
		t.Fatal(err)
	}
	return m, work
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 只改一个文件时，第二次快照应当只新增那一个 blob（增量去重），而不是把整目录再存一份
func TestCheckpointIncrementalDedup(t *testing.T) {
	m, work := newCkHarness(t)
	big := strings.Repeat("A", 4000)
	write(t, filepath.Join(work, "a.txt"), big)
	write(t, filepath.Join(work, "b.txt"), big)
	write(t, filepath.Join(work, "c.txt"), big)

	first, err := m.Create("第一次")
	if err != nil {
		t.Fatal(err)
	}
	if first.Files != 3 {
		t.Fatalf("应快照 3 个文件：%+v", first)
	}
	// a、b、c 内容完全一样 → 三个文件只该写一份 blob
	if first.NewBytes != 4000 {
		t.Fatalf("同内容文件应去重成一份 blob（期望 4000 字节，实际 %d）", first.NewBytes)
	}

	// 只改 a.txt
	write(t, filepath.Join(work, "a.txt"), strings.Repeat("B", 4000))
	second, err := m.Create("第二次")
	if err != nil {
		t.Fatal(err)
	}
	if second.NewBytes != 4000 {
		t.Fatalf("只改了一个文件，第二次快照应只新增 4000 字节（实际 %d）", second.NewBytes)
	}
	if second.Bytes != int64(4000*3) {
		t.Fatalf("引用的总字节仍应是 12000：%+v", second)
	}
}

// 同一个去重键：第二次不该再打一份
func TestCheckpointCreateOncePerKey(t *testing.T) {
	m, work := newCkHarness(t)
	write(t, filepath.Join(work, "x.txt"), "v1")

	first, created, err := m.CreateOnce("run:1", "变更前", 0)
	if err != nil || !created {
		t.Fatalf("第一次应当创建：%v %v", created, err)
	}
	write(t, filepath.Join(work, "x.txt"), "v2")
	again, created, err := m.CreateOnce("run:1", "变更前", 0)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("同一个 key 不该重复打快照")
	}
	if again.ID != first.ID {
		t.Fatalf("应复用同一个快照：%s vs %s", again.ID, first.ID)
	}

	// 换个 key 就会另打一个
	other, created, err := m.CreateOnce("run:2", "变更前", 0)
	if err != nil || !created || other.ID == first.ID {
		t.Fatalf("不同 key 应新建快照：%+v created=%v err=%v", other, created, err)
	}
}

// dry-run 只出计划不动文件；白名单只回滚指定的那个文件
func TestCheckpointDryRunAndWhitelist(t *testing.T) {
	m, work := newCkHarness(t)
	write(t, filepath.Join(work, "keep.txt"), "原始-keep")
	write(t, filepath.Join(work, "change.txt"), "原始-change")

	ck, err := m.Create("回滚点")
	if err != nil {
		t.Fatal(err)
	}
	// 两个文件都被改坏，另加一个"快照里没有"的新文件
	write(t, filepath.Join(work, "keep.txt"), "改坏-keep")
	write(t, filepath.Join(work, "change.txt"), "改坏-change")
	write(t, filepath.Join(work, "extra.txt"), "新增的")

	// ① dry-run：只算计划，不落盘
	plan, err := m.RollbackWith(ck.ID, RestoreOptions{DryRun: true, Files: []string{"change.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun || plan.Applied {
		t.Fatalf("dry-run 不该落盘：%+v", plan)
	}
	if len(plan.Restore) != 1 || plan.Restore[0] != "change.txt" {
		t.Fatalf("白名单应只计划还原 change.txt：%+v", plan.Restore)
	}
	if raw, _ := os.ReadFile(filepath.Join(work, "change.txt")); string(raw) != "改坏-change" {
		t.Fatalf("dry-run 不能改文件：%q", string(raw))
	}

	// ② 真回滚，但仍带白名单：只动 change.txt，keep.txt 保持"改坏"
	applied, err := m.RollbackWith(ck.ID, RestoreOptions{Files: []string{"change.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied {
		t.Fatalf("应已落盘：%+v", applied)
	}
	if raw, _ := os.ReadFile(filepath.Join(work, "change.txt")); string(raw) != "原始-change" {
		t.Fatalf("白名单内的文件应被还原：%q", string(raw))
	}
	if raw, _ := os.ReadFile(filepath.Join(work, "keep.txt")); string(raw) != "改坏-keep" {
		t.Fatalf("白名单外不该被动：%q", string(raw))
	}
	// 默认不删多余文件（PruneExtra=false）
	if _, err := os.Stat(filepath.Join(work, "extra.txt")); err != nil {
		t.Fatal("默认不该删掉快照里没有的文件")
	}

	// ③ 完整回滚（老语义）：keep.txt 还原，多余的 extra.txt 被清掉
	if err := m.Rollback(ck.ID); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(work, "keep.txt")); string(raw) != "原始-keep" {
		t.Fatalf("完整回滚应还原 keep.txt：%q", string(raw))
	}
	if _, err := os.Stat(filepath.Join(work, "extra.txt")); !os.IsNotExist(err) {
		t.Fatal("完整回滚应清掉快照里没有的文件")
	}
}

// 只保留 maxKeep 个快照，且没人引用的 blob 要被回收
func TestCheckpointPruneAndGC(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := NewCheckpointManager(base, work, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		write(t, filepath.Join(work, "f.txt"), strings.Repeat("x", 100+i))
		if _, err := m.Create("第" + string(rune('1'+i)) + "次"); err != nil {
			t.Fatal(err)
		}
	}
	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("应只保留 2 个快照：%+v", list)
	}
	// 被 prune 掉的快照引用的 blob 应该已经回收
	blobs := 0
	_ = filepath.Walk(m.blobDir(), func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi != nil && !fi.IsDir() {
			blobs++
		}
		return nil
	})
	if blobs != 2 {
		t.Fatalf("只剩 2 个快照，应只剩 2 个 blob（实际 %d）", blobs)
	}
}
