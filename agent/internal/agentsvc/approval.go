package agentsvc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Approval 一次危险操作审批
type Approval struct {
	ID        string         `json:"id"`
	RunID     string         `json:"runId,omitempty"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args,omitempty"`
	At        int64          `json:"at"`
	ExpiresAt int64          `json:"expiresAt,omitempty"`
	Status    string         `json:"status"` // pending | approved | rejected | timeout | canceled
	By        string         `json:"by,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	DecidedAt int64          `json:"decidedAt,omitempty"`

	ch chan bool
}

// 审批状态
const (
	ApprovalPending   = "pending"
	ApprovalApproved  = "approved"
	ApprovalRejected  = "rejected"
	ApprovalTimeout   = "timeout"
	ApprovalCancelled = "canceled"
)

// ApprovalQueue 审批队列（文档 §13 C4「审批中心」的核心）：
// 危险操作拦住等人批；超时或端侧断开一律按拒绝处理，绝不放行。
type ApprovalQueue struct {
	mu    sync.Mutex
	items map[string]*Approval
	order []string
	max   int
	seq   int64
}

// NewApprovalQueue 创建审批队列
func NewApprovalQueue(max int) *ApprovalQueue {
	if max <= 0 {
		max = 200
	}
	return &ApprovalQueue{items: map[string]*Approval{}, max: max}
}

// Request 发起一次审批并阻塞等待结果
func (q *ApprovalQueue) Request(ctx context.Context, runID, tool string, args map[string]any, timeout time.Duration) (bool, Approval) {
	now := time.Now()
	item := &Approval{
		ID:    newApprovalID(&q.seq),
		RunID: runID, Tool: tool, Args: args,
		At: now.UnixMilli(), Status: ApprovalPending,
		ch: make(chan bool, 1),
	}
	if timeout > 0 {
		item.ExpiresAt = now.Add(timeout).UnixMilli()
	}

	q.mu.Lock()
	q.items[item.ID] = item
	q.order = append(q.order, item.ID)
	q.pruneLocked()
	q.mu.Unlock()

	var timer <-chan time.Time
	if timeout > 0 {
		timer = time.After(timeout)
	}
	select {
	case ok := <-item.ch:
		q.mu.Lock()
		cur := q.items[item.ID]
		q.mu.Unlock()
		if cur != nil {
			return ok, *cur
		}
		return ok, *item
	case <-timer:
		q.mu.Lock()
		if cur, ok := q.items[item.ID]; ok && cur.Status == ApprovalPending {
			cur.Status = ApprovalTimeout
			cur.Reason = "超时未审批，按拒绝处理"
			cur.DecidedAt = time.Now().UnixMilli()
		}
		snapshot := *q.items[item.ID]
		q.mu.Unlock()
		return false, snapshot
	case <-ctx.Done():
		q.mu.Lock()
		if cur, ok := q.items[item.ID]; ok && cur.Status == ApprovalPending {
			cur.Status = ApprovalCancelled
			cur.Reason = "运行已取消，按拒绝处理"
			cur.DecidedAt = time.Now().UnixMilli()
		}
		snapshot := *q.items[item.ID]
		q.mu.Unlock()
		return false, snapshot
	}
}

// Decide 人工决定
func (q *ApprovalQueue) Decide(id string, ok bool, by, reason string) error {
	q.mu.Lock()
	item, exists := q.items[id]
	if !exists {
		q.mu.Unlock()
		return fmt.Errorf("没有这条审批：%s", id)
	}
	if item.Status != ApprovalPending {
		status := item.Status
		q.mu.Unlock()
		return fmt.Errorf("这条审批已经处理过了（当前状态：%s）", status)
	}
	if ok {
		item.Status = ApprovalApproved
	} else {
		item.Status = ApprovalRejected
	}
	item.By = strings.TrimSpace(by)
	item.Reason = strings.TrimSpace(reason)
	item.DecidedAt = time.Now().UnixMilli()
	ch := item.ch
	q.mu.Unlock()

	select {
	case ch <- ok:
	default:
	}
	return nil
}

// List 全部审批（新的在前）
func (q *ApprovalQueue) List() []Approval {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Approval, 0, len(q.order))
	for i := len(q.order) - 1; i >= 0; i-- {
		if item, ok := q.items[q.order[i]]; ok {
			cp := *item
			cp.ch = nil
			out = append(out, cp)
		}
	}
	return out
}

// Pending 还在等人工处理的审批
func (q *ApprovalQueue) Pending() []Approval {
	out := []Approval{}
	for _, a := range q.List() {
		if a.Status == ApprovalPending {
			out = append(out, a)
		}
	}
	return out
}

// Approve 便捷方法：通过
func (q *ApprovalQueue) Approve(id, by string) error { return q.Decide(id, true, by, "") }

// Reject 便捷方法：拒绝
func (q *ApprovalQueue) Reject(id, by, reason string) error {
	if strings.TrimSpace(reason) == "" {
		reason = "人工拒绝"
	}
	return q.Decide(id, false, by, reason)
}

func (q *ApprovalQueue) pruneLocked() {
	if len(q.order) <= q.max {
		return
	}
	drop := len(q.order) - q.max
	kept := make([]string, 0, q.max)
	for i, id := range q.order {
		if i < drop {
			if item, ok := q.items[id]; ok && item.Status == ApprovalPending {
				// 还在等的不能丢
				kept = append(kept, id)
				continue
			}
			delete(q.items, id)
			continue
		}
		kept = append(kept, id)
	}
	q.order = kept
}

func newApprovalID(seq *int64) string {
	*seq++
	return fmt.Sprintf("ap%d-%d", time.Now().UnixMilli(), *seq)
}
