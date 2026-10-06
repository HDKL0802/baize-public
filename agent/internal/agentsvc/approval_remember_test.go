package agentsvc

import (
	"context"
	"strings"
	"testing"
	"time"
)

// requestAndGetID 起一个审批请求（Request 会阻塞到有人批），
// 从队列里捞出它的 id 交给调用方去批；返回的 channel 是这次请求的最终结果。
//
// 注意：Request 是"阻塞直到被批"，所以不能等它返回再拿 id（那会死等）——
// 只能起一个 goroutine 让它挂着，然后在外面轮询队列取 pending 的那条。
func requestAndGetID(t *testing.T, s *Service, tool string) (string, chan bool) {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		ok, _ := s.approvals.Request(context.Background(), "run-test", tool, map[string]any{"x": 1}, 5*time.Second)
		done <- ok
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, a := range s.Approvals() {
			if a.Tool == tool && a.Status == ApprovalPending {
				return a.ID, done
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("审批请求没有登记进队列")
	return "", nil
}

func TestApproveWithSessionRemember(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, testLogger())
	if err != nil {
		t.Fatalf("创建服务失败：%v", err)
	}
	defer s.Close()

	id, done := requestAndGetID(t, s, "fs_delete")
	if s.toolRemembered("fs_delete") {
		t.Fatal("还没批准就「记住」了？")
	}
	if err := s.ApproveWith(id, "tester", RememberSession); err != nil {
		t.Fatalf("批准失败：%v", err)
	}
	if ok := <-done; !ok {
		t.Fatal("批准后这次调用应当放行")
	}
	// 会话级记住：进程内该工具不再问
	if !s.toolRemembered("fs_delete") {
		t.Fatal("选了 session 之后应当记住放行")
	}
	allow := s.ApprovalAllow()
	if got := allow["session"].([]string); len(got) != 1 || got[0] != "fs_delete" {
		t.Fatalf("放行清单的 session 部分不对：%v", allow)
	}
	// 会话级不能写进配置（重启就没）
	if cfg := s.Config(); len(cfg.ApprovalAllow) != 0 {
		t.Fatalf("session 级不该写进配置：%v", cfg.ApprovalAllow)
	}

	// 撤销后要立刻恢复"要问"
	if err := s.ClearApprovalAllow("session", ""); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}
	if s.toolRemembered("fs_delete") {
		t.Fatal("撤销后不该还记得放行")
	}
}

func TestApproveWithAlwaysRememberWritesConfigAndCanBeRevoked(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, testLogger())
	if err != nil {
		t.Fatalf("创建服务失败：%v", err)
	}
	defer s.Close()

	id, _ := requestAndGetID(t, s, "note_forget")
	if err := s.ApproveWith(id, "tester", RememberAlways); err != nil {
		t.Fatalf("批准失败：%v", err)
	}
	if !s.toolRemembered("note_forget") {
		t.Fatal("选了 always 之后应当记住放行")
	}
	cfg := s.Config()
	if len(cfg.ApprovalAllow) != 1 || cfg.ApprovalAllow[0] != "note_forget" {
		t.Fatalf("always 级必须写进配置：%v", cfg.ApprovalAllow)
	}
	allow := s.ApprovalAllow()
	if got := allow["always"].([]string); len(got) != 1 {
		t.Fatalf("放行清单的 always 部分不对：%v", allow)
	}

	// 只撤这一个工具
	if err := s.ClearApprovalAllow("always", "note_forget"); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}
	if s.toolRemembered("note_forget") {
		t.Fatal("撤销后不该还记得放行")
	}
	if cfg := s.Config(); len(cfg.ApprovalAllow) != 0 {
		t.Fatalf("撤销后配置里不该还有它：%v", cfg.ApprovalAllow)
	}
}

func TestApproveWithRejectsUnknownRemember(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, testLogger())
	if err != nil {
		t.Fatalf("创建服务失败：%v", err)
	}
	defer s.Close()

	id, _ := requestAndGetID(t, s, "fs_delete")
	err = s.ApproveWith(id, "tester", "forever")
	if err == nil {
		t.Fatal("不认识的 remember 取值应当报错")
	}
	if !strings.Contains(err.Error(), "取值") {
		t.Fatalf("错误信息应指出取值不对：%v", err)
	}
	// 报错归报错，这次审批本身已经通过了（先批后记），不该被"记住"
	if s.toolRemembered("fs_delete") {
		t.Fatal("取值非法时不该写入任何放行记录")
	}
}

// 批一次（不选记住）不该产生任何放行记录 —— 默认必须是最保守的
func TestApproveOnceDoesNotRemember(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, testLogger())
	if err != nil {
		t.Fatalf("创建服务失败：%v", err)
	}
	defer s.Close()

	id, done := requestAndGetID(t, s, "fs_delete")
	if err := s.Approve(id, "tester"); err != nil {
		t.Fatalf("批准失败：%v", err)
	}
	if ok := <-done; !ok {
		t.Fatal("批准后这次调用应当放行")
	}
	if s.toolRemembered("fs_delete") {
		t.Fatal("只批一次不该被记住")
	}
	allow := s.ApprovalAllow()
	if len(allow["session"].([]string)) != 0 || len(allow["always"].([]string)) != 0 {
		t.Fatalf("放行清单应保持为空：%v", allow)
	}
}
