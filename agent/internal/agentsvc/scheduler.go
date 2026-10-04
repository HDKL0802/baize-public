package agentsvc

import (
	"context"
	"strings"
	"sync"
	"time"

	"baize/internal/agentrt"
	"baize/internal/config"
	"baize/internal/cron"
)

// scheduler 定时任务调度器：每 20 秒检查一次到点的任务并触发一次 Agent 运行。
type scheduler struct {
	svc    *Service
	mu     sync.Mutex
	parsed map[string]*cron.Schedule
	next   map[string]time.Time
	errs   map[string]string
}

func newScheduler(s *Service) *scheduler {
	return &scheduler{
		svc:    s,
		parsed: map[string]*cron.Schedule{},
		next:   map[string]time.Time{},
		errs:   map[string]string{},
	}
}

func (sc *scheduler) run(stop <-chan struct{}) {
	sc.prime()
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			sc.tick()
		}
	}
}

// prime 初始化各任务的触发时刻：从现在开始算，不补跑启动前的历史任务
func (sc *scheduler) prime() {
	now := time.Now()
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, j := range sc.svc.Config().Cron {
		sc.schedule(j, now)
	}
}

// schedule 解析表达式并算出下一次触发时间（调用方需持锁）
func (sc *scheduler) schedule(j config.CronJob, after time.Time) {
	sched, err := cron.Parse(j.Expr)
	if err != nil {
		sc.errs[j.ID] = err.Error()
		delete(sc.next, j.ID)
		delete(sc.parsed, j.ID)
		return
	}
	delete(sc.errs, j.ID)
	sc.parsed[j.ID] = sched
	next, err := sched.Next(after)
	if err != nil {
		sc.errs[j.ID] = err.Error()
		delete(sc.next, j.ID)
		return
	}
	sc.next[j.ID] = next
}

func (sc *scheduler) tick() {
	cfg := sc.svc.Config()
	now := time.Now()
	for _, j := range cfg.Cron {
		sc.mu.Lock()
		next, ok := sc.next[j.ID]
		sc.mu.Unlock()

		if !j.Enabled {
			if ok {
				sc.mu.Lock()
				delete(sc.next, j.ID)
				sc.mu.Unlock()
			}
			continue
		}
		if !ok {
			sc.mu.Lock()
			sc.schedule(j, now)
			sc.mu.Unlock()
			continue
		}
		if now.Before(next) {
			continue
		}
		// 到点了：先把下一次时间排好，再异步触发（避免慢任务把调度卡住）
		sc.mu.Lock()
		sc.schedule(j, now)
		sc.mu.Unlock()
		sc.svc.lg.Info("定时任务触发", "job", j.ID, "expr", j.Expr, "goal", brief(j.Goal, 80))
		go sc.fire(j)
	}
}

func (sc *scheduler) fire(j config.CronJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	recipe := strings.TrimSpace(j.Recipe)
	if recipe == "" {
		recipe = "chat"
	}
	// 定时任务的松紧度沿用老的 autoApprove 标志（人建任务时就定好的）；
	// 手机端/控制台「派活时单独选」走的是 Run/Start 的 strictness 参数
	mode := ""
	if j.AutoApprove {
		mode = "loose"
	}
	res, err := sc.svc.Run(ctx, j.Goal, recipe, mode)
	sc.svc.recordJobResult(j.ID, res, err)
}

// jobStates 给控制台用的任务状态
func (sc *scheduler) jobStates() []JobState {
	cfg := sc.svc.Config()
	out := make([]JobState, 0, len(cfg.Cron))
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, j := range cfg.Cron {
		js := JobState{CronJob: j, ParseErr: sc.errs[j.ID]}
		if next, ok := sc.next[j.ID]; ok {
			js.NextAt = next.UnixMilli()
		}
		out = append(out, js)
	}
	return out
}

func (s *Service) jobStates() []JobState {
	if s == nil || s.sched == nil {
		return nil
	}
	return s.sched.jobStates()
}

// recordJobResult 把定时任务的执行结果写回配置（含最后状态）
func (s *Service) recordJobResult(jobID string, res agentrt.RunResult, err error) {
	s.mu.Lock()
	cfg := s.cfg
	found := false
	for i := range cfg.Cron {
		if cfg.Cron[i].ID != jobID {
			continue
		}
		cfg.Cron[i].LastRunAt = time.Now().UnixMilli()
		cfg.Cron[i].Runs++
		if err != nil {
			cfg.Cron[i].LastStatus = "failed"
			cfg.Cron[i].LastError = err.Error()
		} else {
			cfg.Cron[i].LastStatus = "done"
			cfg.Cron[i].LastError = ""
		}
		found = true
		break
	}
	s.mu.Unlock()
	if !found {
		return
	}
	if err := config.Save(s.dataDir, cfg); err != nil {
		s.lg.Warn("定时任务状态落盘失败", "err", err)
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	if err != nil {
		s.lg.Error("定时任务运行失败", "job", jobID, "err", err)
		return
	}
	s.lg.Info("定时任务完成", "job", jobID, "runId", res.RunID, "steps", res.Steps)
}

func brief(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}
