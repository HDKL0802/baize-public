// Package ecology 第六部分：探测阶段的并发池、仓库缓存与限流。
//
// 背景：DSH 生态索引上万条，ProbeRepo 会逐条去 GitHub 查 SKILL.md / mcp.json。
// 串行跑一轮要很久，且大量插件共用同一个仓库地址 —— 同一个仓库反复打 GitHub。
// 这里做三件事，输出仍与串行版一致（按 Kind 再 ID 排序，错误排序后截断）：
//   - 有界 worker pool：默认 8 并发（DefaultProbeConcurrency），打满就排队，不无限开协程；
//   - 同仓库探测结果缓存（含失败 = 负缓存）：每个仓库 URL 最多真调一次 probe；
//   - 双超时：每条探测一个上限（沿用 ghTimeout），整轮再压一个总上限（2 分钟）。
//     超时的条目**不丢弃**，由 convert() 按「探测失败」写进 Reason。
//
// 做法：把真正的 probe 包一层走缓存/超时的闭包，convert() 原封不动复用 ——
// 探测前的字段组装、探测后的 Kind/Reason 判定还是同一份代码，不会两边走偏。
package ecology

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultProbeConcurrency 默认并发探测的 worker 数。
	DefaultProbeConcurrency = 8
	// DefaultProbeTotalTimeout 整轮探测的总超时（含排队）。
	DefaultProbeTotalTimeout = 2 * time.Minute
	// DefaultProbeTimeout 单条探测的超时；沿用 ghclient.go 的 ghTimeout 口径。
	DefaultProbeTimeout = ghTimeout
)

// FetchOptions 可选配置（FetchWithOptions 用）。零值 = 纯目录模式（不探测）+ 默认并发/超时。
// 测试里可以把并发、超时、probe 都换掉，不联网也能验证行为。
type FetchOptions struct {
	Probe        Probe         // 探测函数；nil = 纯目录模式（不联网）
	Concurrency  int           // worker 数；<=0 用 DefaultProbeConcurrency
	ProbeTimeout time.Duration // 单条探测超时；<=0 用 DefaultProbeTimeout
	TotalTimeout time.Duration // 整轮超时；<=0 用 DefaultProbeTotalTimeout
}

// probePool 一轮 Fetch 的探测上下文：worker pool + 缓存 + 双超时。
type probePool struct {
	probe       Probe
	concurrency int
	perTimeout  time.Duration
	totalCtx    context.Context

	mu      sync.Mutex
	results map[string]*probeResult // key = 仓库 URL（trim 后）
}

// probeResult 一个仓库的探测结果（成功或失败都缓存 = 负缓存）。
// done 用于「同仓库只打一次」：第一个协程算完关掉 done，其余等它再读结果。
type probeResult struct {
	info *ProbeInfo
	err  error
	done chan struct{}
}

// newProbePool 造一轮探测池。probe==nil（纯目录模式）时池仍存在，
// 只是 wrapped() 返回 nil —— convert 拿到 nil probe 就只列目录、不联网。
func newProbePool(probe Probe, concurrency int, perTimeout, totalTimeout time.Duration) (*probePool, context.CancelFunc) {
	if concurrency <= 0 {
		concurrency = DefaultProbeConcurrency
	}
	if perTimeout <= 0 {
		perTimeout = DefaultProbeTimeout
	}
	if totalTimeout <= 0 {
		totalTimeout = DefaultProbeTotalTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), totalTimeout)
	return &probePool{
		probe:       probe,
		concurrency: concurrency,
		perTimeout:  perTimeout,
		totalCtx:    ctx,
		results:     make(map[string]*probeResult),
	}, cancel
}

// repoKey 缓存键：仓库地址去掉首尾空白；空地址不缓存（每条都如实报「没给仓库」）。
func repoKey(repo string) string { return strings.TrimSpace(repo) }

// wrapped 返回一个走缓存 + 双超时的 Probe，交给 convert() 调用。
// 纯目录模式（pp.probe == nil）返回 nil：convert 看到 nil probe 就只按元数据列目录。
func (pp *probePool) wrapped() Probe {
	if pp.probe == nil {
		return nil
	}
	return func(name string, v dshVersion, repo string) (*ProbeInfo, error) {
		return pp.probeOnce(name, v, repo)
	}
}

// probeOnce 拿一个仓库的探测结果，命中缓存直接返回；同仓库并发只真调一次。
func (pp *probePool) probeOnce(name string, v dshVersion, repo string) (*ProbeInfo, error) {
	key := repoKey(repo)
	if key == "" {
		// 没给仓库地址：不缓存（没有可去重的东西），但仍各自真调一次 probe
		// —— 保持与串行版一致：有些探测函数（包括测试里的）不依赖仓库地址。
		return pp.callProbe(name, v, repo)
	}

	pp.mu.Lock()
	if r, ok := pp.results[key]; ok {
		pp.mu.Unlock()
		select {
		case <-r.done:
			return r.info, r.err
		case <-pp.totalCtx.Done():
			return nil, probeTimeoutErr(pp.perTimeout, pp.totalCtx.Err())
		}
	}
	r := &probeResult{done: make(chan struct{})}
	pp.results[key] = r
	pp.mu.Unlock()

	info, err := pp.callProbe(name, v, repo)
	r.info, r.err = info, err
	close(r.done) // close 之前的写入对 <-done 的读者可见
	return info, err
}

// callProbe 真调一次 probe，带单条超时；超时/整轮结束都当成探测失败返回，绝不丢条目。
func (pp *probePool) callProbe(name string, v dshVersion, repo string) (*ProbeInfo, error) {
	// 整轮已经到点，剩下的条目直接记超时，不再起协程。
	if err := pp.totalCtx.Err(); err != nil {
		return nil, probeTimeoutErr(pp.perTimeout, err)
	}
	// 单条上限与总上限取更早的那个。
	deadline := pp.perTimeout
	if d, ok := pp.totalCtx.Deadline(); ok {
		if left := time.Until(d); left < deadline {
			deadline = left
		}
	}
	if deadline <= 0 {
		return nil, probeTimeoutErr(pp.perTimeout, context.DeadlineExceeded)
	}
	ctx, cancel := context.WithTimeout(pp.totalCtx, deadline)
	defer cancel()

	type out struct {
		info *ProbeInfo
		err  error
	}
	ch := make(chan out, 1) // 带缓冲：超时后 probe 仍能把结果写完，不泄漏协程
	go func() {
		info, err := pp.probe(name, v, repo)
		ch <- out{info: info, err: err}
	}()
	select {
	case o := <-ch:
		return o.info, o.err
	case <-ctx.Done():
		return nil, probeTimeoutErr(deadline, ctx.Err())
	}
}

func probeTimeoutErr(d time.Duration, cause error) error {
	if errors.Is(cause, context.Canceled) {
		return fmt.Errorf("探测被取消（整轮探测已结束）：%w", cause)
	}
	return fmt.Errorf("探测超时（单条上限 %s）：%w", d, cause)
}

// convertOutcome 一条转换结果（条目或错误），按下标写回保证与输入同序。
type convertOutcome struct {
	entry *Entry
	err   error
}

// run 并发转换全部条目，返回与输入**同序**的结果列表。
// 每个 worker 只往自己的下标写，无竞态；整体顺序由下标决定，与串行版一致。
func (pp *probePool) run(plugins []dshPlugin) []convertOutcome {
	out := make([]convertOutcome, len(plugins))
	if len(plugins) == 0 {
		return out
	}
	wrapped := pp.wrapped()
	jobs := make(chan int)
	var wg sync.WaitGroup
	n := pp.concurrency
	if n > len(plugins) {
		n = len(plugins)
	}
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				e, err := convert(plugins[i], wrapped)
				out[i] = convertOutcome{entry: e, err: err}
			}
		}()
	}
	for i := range plugins {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return out
}

// fetchError 一条转换错误，带上所属插件的 id，便于按 id 稳定排序。
type fetchError struct {
	id  string
	msg string
}

// sortedErrors 按 id 排序（同 id 再按错误文本），截断前 50 条。
// 并发下错误产生顺序不定，按 id 排才能保证 cat.Errors 稳定。
func sortedErrors(errs []fetchError) []string {
	sort.Slice(errs, func(i, j int) bool {
		if errs[i].id != errs[j].id {
			return errs[i].id < errs[j].id
		}
		return errs[i].msg < errs[j].msg
	})
	if len(errs) > 50 {
		errs = errs[:50]
	}
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.msg)
	}
	return out
}

// convertAll 按当前模式转换全部条目。
//   - 有探测函数：走有界 worker pool（并发 + 同仓库缓存 + 双超时）；
//   - 纯目录模式：串行即可（convert 不联网，微秒级），顺序天然稳定。
//
// 两种路径返回的切片都与输入同序，交给 Fetch 排序后输出。
func (pp *probePool) convertAll(plugins []dshPlugin) []convertOutcome {
	if pp.probe != nil {
		return pp.run(plugins)
	}
	out := make([]convertOutcome, len(plugins))
	for i, p := range plugins {
		entry, err := convert(p, nil)
		out[i] = convertOutcome{entry: entry, err: err}
	}
	return out
}
