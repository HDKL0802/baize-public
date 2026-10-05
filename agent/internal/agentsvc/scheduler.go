package agentsvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"baize/internal/agentrt"
	"baize/internal/channels"
	"baize/internal/config"
	"baize/internal/cron"
)

const heartbeatFileName = "HEARTBEAT.md"

type scheduler struct {
	svc    *Service
	mu     sync.Mutex
	parsed map[string]*cron.Schedule
	next   map[string]time.Time
	errs   map[string]string

	hbExpr    string
	hbParsed  *cron.Schedule
	hbNext    time.Time
	hbErr     string
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
	sc.scheduleHeartbeat(sc.svc.Config().Heartbeat, now)
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

	sc.tickHeartbeat(cfg.Heartbeat, now)
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

// recordHeartbeatResult 把心跳任务的执行结果写回配置（与定时任务同一套落盘口径）
func (s *Service) recordHeartbeatResult(res agentrt.RunResult, err error) {
	s.mu.Lock()
	cfg := s.cfg
	cfg.Heartbeat.LastRunAt = time.Now().UnixMilli()
	cfg.Heartbeat.Runs++
	if err != nil {
		cfg.Heartbeat.LastStatus = "failed"
		cfg.Heartbeat.LastError = err.Error()
	} else {
		cfg.Heartbeat.LastStatus = "done"
		cfg.Heartbeat.LastError = ""
	}
	s.mu.Unlock()
	if serr := config.Save(s.dataDir, cfg); serr != nil {
		s.lg.Warn("心跳任务状态落盘失败", "err", serr)
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	if err != nil {
		s.lg.Error("心跳任务运行失败", "err", err)
		return
	}
	s.lg.Info("心跳任务完成", "runId", res.RunID, "steps", res.Steps)
}

func brief(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

func (sc *scheduler) scheduleHeartbeat(hb config.HeartbeatConfig, after time.Time) {
	sc.hbParsed = nil
	sc.hbNext = time.Time{}
	sc.hbErr = ""
	if !hb.Enabled {
		sc.hbExpr = ""
		return
	}
	sc.hbExpr = hb.Every
	sched, err := cron.Parse(hb.Every)
	if err != nil {
		sc.hbErr = err.Error()
		return
	}
	next, err := sched.Next(after)
	if err != nil {
		sc.hbErr = err.Error()
		return
	}
	sc.hbParsed = sched
	sc.hbNext = next
}

// heartbeatState 给控制台用的心跳状态（调用方需持锁）
func (sc *scheduler) heartbeatState() HeartbeatState {
	cfg := sc.svc.Config()
	hs := HeartbeatState{
		HeartbeatConfig: cfg.Heartbeat,
		Path:            sc.svc.HeartbeatPath(),
		ParseErr:        sc.hbErr,
	}
	if !cfg.Heartbeat.Enabled {
		hs.NextAt = 0
	} else if !sc.hbNext.IsZero() {
		hs.NextAt = sc.hbNext.UnixMilli()
	}
	if hs.Path != "" {
		if info, err := os.Stat(hs.Path); err == nil {
			hs.HasFile = true
			hs.Size = info.Size()
			hs.ModTime = info.ModTime().UnixMilli()
		}
	}
	return hs
}

func (s *Service) heartbeatState() HeartbeatState {
	if s == nil || s.sched == nil {
		return HeartbeatState{}
	}
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	return s.sched.heartbeatState()
}

// Heartbeat 心跳任务的当前状态（控制台展示用）
func (s *Service) Heartbeat() HeartbeatState { return s.heartbeatState() }

func (sc *scheduler) tickHeartbeat(hb config.HeartbeatConfig, now time.Time) {
	sc.mu.Lock()
	changed := sc.hbExpr != hb.Every
	scheduled := sc.hbParsed != nil
	next := sc.hbNext
	sc.mu.Unlock()

	// 关掉、间隔改了、或者还没排上（首次启用 / 上次解析失败）：重新排期后再判断
	if !hb.Enabled || changed || !scheduled {
		sc.mu.Lock()
		sc.scheduleHeartbeat(hb, now)
		sc.mu.Unlock()
		return
	}

	if !inActiveHours(hb.ActiveHours, now) {
		return
	}
	if now.Before(next) {
		return
	}

	sc.mu.Lock()
	sc.scheduleHeartbeat(hb, now)
	sc.mu.Unlock()

	sc.svc.lg.Info("心跳任务触发", "every", hb.Every, "target", hb.Target)
	go sc.fireHeartbeat(hb)
}

func (sc *scheduler) fireHeartbeat(hb config.HeartbeatConfig) {
	query, err := sc.svc.loadHeartbeatQuery()
	if err != nil {
		sc.svc.recordHeartbeatResult(agentrt.RunResult{}, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), heartbeatTimeout(hb))
	defer cancel()

	res, err := sc.svc.Run(ctx, query, "chat", "")
	if err == nil {
		sc.svc.dispatchHeartbeat(hb.Target, res)
	}
	sc.svc.recordHeartbeatResult(res, err)
}

/* ---------- HEARTBEAT.md：心跳要办的活 ---------- */

// HeartbeatPath HEARTBEAT.md 的完整路径（工作区根目录，和人设分开：它是"要办的事"而不是"你是谁"）
func (s *Service) HeartbeatPath() string {
	workdir := s.Workdir()
	if workdir == "" {
		return ""
	}
	return filepath.Join(workdir, heartbeatFileName)
}

// HeartbeatRead 读 HEARTBEAT.md 原文。文件不在时 exists=false 且不报错：
// 「还没写过」是正常状态，界面要据此显示空编辑器，而不是弹一个错误。
func (s *Service) HeartbeatRead() (content string, exists bool, err error) {
	path := s.HeartbeatPath()
	if path == "" {
		return "", false, errors.New("工作区未就绪")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("读取 %s 失败：%w", heartbeatFileName, err)
	}
	return strings.TrimPrefix(string(raw), "\ufeff"), true, nil
}

// HeartbeatWrite 写 HEARTBEAT.md（心跳每次触发要办的事就写在这里）
func (s *Service) HeartbeatWrite(content string) error {
	path := s.HeartbeatPath()
	if path == "" {
		return errors.New("工作区未就绪，写不了 " + heartbeatFileName)
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("内容不能为空（不想让心跳干活，请把它关掉，而不是清空 %s）", heartbeatFileName)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写入 %s 失败：%w", heartbeatFileName, err)
	}
	return nil
}

// loadHeartbeatQuery 读心跳要跑的活；文件不在或内容为空时返回错误。
func (s *Service) loadHeartbeatQuery() (string, error) {
	path := s.HeartbeatPath()
	if path == "" {
		return "", fmt.Errorf("工作区未就绪，找不到 %s", heartbeatFileName)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%s 不存在（在工作区写一个要定期办的活即可）", heartbeatFileName)
		}
		return "", fmt.Errorf("读取 %s 失败：%w", heartbeatFileName, err)
	}
	q := strings.TrimSpace(string(raw))
	if q == "" {
		return "", fmt.Errorf("%s 是空的", heartbeatFileName)
	}
	return q, nil
}

// StartHeartbeat 立刻跑一次心跳（不等排期）：任务取自 HEARTBEAT.md，异步执行并记结果。
// 返回运行 id，控制台靠它跟踪；与 Start 同一套顶层串行规矩，已有任务在跑时明确拒绝。
func (s *Service) StartHeartbeat() (string, error) {
	query, err := s.loadHeartbeatQuery()
	if err != nil {
		return "", err
	}
	s.mu.RLock()
	busy := s.running
	s.mu.RUnlock()
	if busy {
		return "", errors.New("已有任务在运行（顶层串行，等它跑完再派）")
	}
	hb := s.Config().Heartbeat
	id := agentrt.NewRunID()
	s.mu.Lock()
	s.running = true
	s.curRunID = id
	s.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), heartbeatTimeout(hb))
		defer cancel()
		res, runErr := s.runInner(ctx, id, query, "chat", false, false, 0)
		if runErr == nil {
			s.dispatchHeartbeat(hb.Target, res)
		}
		s.recordHeartbeatResult(res, runErr)
	}()
	return id, nil
}

// heartbeatTimeout 单次心跳的超时；没配或配成非正数就按 10 分钟兜底
func heartbeatTimeout(hb config.HeartbeatConfig) time.Duration {
	if hb.TimeoutSec <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(hb.TimeoutSec) * time.Second
}

// dispatchHeartbeat 把心跳结果送到指定出口。
// main = 只留在运行记录里（默认）；last = 最近收到过消息的频道；inbox = id 为 inbox 的频道。
// 没有可用频道 / 没配 inbox 时只记一条日志，绝不假装已经发出去了。
func (s *Service) dispatchHeartbeat(target string, res agentrt.RunResult) {
	if target == "main" || target == "" {
		return
	}
	text := strings.TrimSpace(res.Text)
	if text == "" {
		s.lg.Info("心跳结果为空，不往外发", "target", target, "runId", res.RunID)
		return
	}
	mgr := s.Channels()
	if mgr == nil {
		s.lg.Info("心跳结果未分发（频道未装配）", "target", target, "runId", res.RunID)
		return
	}
	var id string
	switch target {
	case "last":
		id = mgr.LastChannel()
		if id == "" {
			s.lg.Info("心跳结果未分发（还没有任何频道收过消息）", "target", target, "runId", res.RunID)
			return
		}
	case "inbox":
		id = "inbox"
	default:
		s.lg.Info("未知的心跳分发目标，未分发", "target", target, "runId", res.RunID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	msg := channels.Message{Channel: id, Session: "heartbeat"}
	if err := mgr.Send(ctx, id, msg, text); err != nil {
		s.lg.Warn("心跳结果分发失败", "target", target, "channel", id, "err", err)
		return
	}
	s.lg.Info("心跳结果已分发", "target", target, "channel", id, "runId", res.RunID)
}

// activeWindow 把 "HH:MM-HH:MM" 解析成起止分钟数；ok=false 表示没配或配错了（按全天处理）
func activeWindow(activeHours string) (start, end int, ok bool) {
	s := strings.TrimSpace(activeHours)
	if s == "" {
		return 0, 0, false
	}
	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	st, err := time.Parse("15:04", strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, false
	}
	en, err := time.Parse("15:04", strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, false
	}
	return st.Hour()*60 + st.Minute(), en.Hour()*60 + en.Minute(), true
}

// inActiveHours 当前时刻是否落在活跃时段内。没配或配错都放行：
// 宁可多跑一次，也不能因为一个笔误把心跳静默关掉（配置写错由 ParseActiveHours 在入口处挡）。
func inActiveHours(activeHours string, t time.Time) bool {
	start, end, ok := activeWindow(activeHours)
	if !ok {
		return true
	}
	cur := t.Hour()*60 + t.Minute()
	if start <= end {
		return cur >= start && cur < end
	}
	return cur >= start || cur < end // 跨夜窗口，如 22:00-08:00
}

// ParseActiveHours 校验活跃时段配置。空串 = 全天，合法。
func ParseActiveHours(activeHours string) error {
	if strings.TrimSpace(activeHours) == "" {
		return nil
	}
	if _, _, ok := activeWindow(activeHours); !ok {
		return fmt.Errorf("活跃时段应为 HH:MM-HH:MM（例如 08:00-22:00，可跨夜如 22:00-08:00），实际是 %q", activeHours)
	}
	return nil
}
