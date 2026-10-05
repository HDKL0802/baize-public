// Package slash 是「魔法命令」的注册与分发（QwenPaw SlashCommandRegistry 的 Go 重写）。
//
// 命名用 slash 而不是 command：IM 频道那边另有一套消息优先级调度也叫 CommandRegistry，
// 两回事，别混。这里只干一件事——把以 "/" 开头的用户输入，在派给模型之前先认成一条命令，
// 命中就直接回话（不烧模型），没命中就原样透传。
//
// 关键约定：
//   - 命令名大小写不敏感；内置命令优先于技能回退（不会让同名技能遮住 /help）
//   - 单个 "/" 视为 /help（用户敲个斜杠就是想看有哪些命令）
//   - 处理器只产出「文本回复」或「改写后的目标」，不直接碰模型
package slash

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// Result 一条命令的执行结果，三种去向：
//   - Reply 非空：命令已处理完，直接把 Reply 回给用户，不跑模型
//   - Goal 非空：命令改写了目标（如技能注入），调用方用新 Goal 照常跑模型
//   - 两者皆空：命令认了但没输出，按"已处理"对待
type Result struct {
	Name   string `json:"name,omitempty"`   // 命中的命令名（别名会归一成主名）
	Reply  string `json:"reply,omitempty"`  // 给用户的回复
	Goal   string `json:"goal,omitempty"`   // 改写后的目标（技能注入用）
	Action string `json:"action,omitempty"` // 可选：客户端动作（clear / new）
}

// Handler 命令处理器。args 是命令名后面的原始参数（已去首尾空白），raw 是整条原始输入。
type Handler func(ctx context.Context, args, raw string) (Result, error)

// Spec 一条命令的定义
type Spec struct {
	Name     string   // 主名（不带斜杠）
	Aliases  []string // 别名（可选）
	Category string   // 分类，给 /help 分组用
	Help     string   // 一句话帮助
	Handler  Handler
}

// CommandInfo 广播给前端/控制台的命令说明（不含处理器）
type CommandInfo struct {
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases,omitempty"`
	Category string   `json:"category,omitempty"`
	Help     string   `json:"help"`
}

// Fallback 技能回退：内置命令都没命中、且输入以 "/" 开头时调用。
// matched=false 表示"这也不是技能"，调用方应当把它当普通文本透传给模型。
type Fallback func(ctx context.Context, raw string) (Result, bool)

// Registry 命令注册表。可并发读（解析/广播），注册只在启动期发生。
type Registry struct {
	mu       sync.RWMutex
	specs    []Spec         // 主表，按注册顺序
	index    map[string]int // 小写命令名/别名 -> specs 下标
	fallback Fallback
}

// New 建一个空注册表
func New() *Registry {
	return &Registry{index: map[string]int{}}
}

// Register 注册一条命令。同名（主名）重复注册按"更新"处理：保留原位置，换掉内容与别名。
// 主名为空或没给处理器的一律忽略（宁可少一条，也不让坏定义进表）。
func (r *Registry) Register(s Spec) {
	name := strings.ToLower(strings.TrimSpace(s.Name))
	if name == "" || s.Handler == nil {
		return
	}
	s.Name = name
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.index == nil {
		r.index = map[string]int{}
	}
	if idx, ok := r.index[name]; ok {
		// 更新的情况：先把旧别名的索引清掉（只清仍指向这条的）
		for _, a := range r.specs[idx].Aliases {
			if k := strings.ToLower(strings.TrimSpace(a)); k != "" && r.index[k] == idx {
				delete(r.index, k)
			}
		}
		r.specs[idx] = s
	} else {
		r.specs = append(r.specs, s)
		r.index[name] = len(r.specs) - 1
	}
	idx := r.index[name]
	for _, a := range s.Aliases {
		if k := strings.ToLower(strings.TrimSpace(a)); k != "" {
			r.index[k] = idx
		}
	}
}

// RegisterFallback 注册技能回退（全局仅一个，后注册的覆盖先注册的）
func (r *Registry) RegisterFallback(fb Fallback) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallback = fb
}

// Resolve 把一条以 "/" 开头的文本解析成 (spec, args)。
// 单个 "/" 视为 /help。命中不到（不是内置命令）返回 ok=false，交由回退或透传处理。
func (r *Registry) Resolve(raw string) (Spec, string, bool) {
	t := strings.TrimSpace(raw)
	if !strings.HasPrefix(t, "/") {
		return Spec{}, "", false
	}
	body := strings.TrimSpace(t[1:])
	if body == "" {
		body = "help" // 光一个斜杠 = 要看帮助
	}
	name, args, _ := strings.Cut(body, " ")
	name = strings.ToLower(strings.TrimSpace(name))
	r.mu.RLock()
	defer r.mu.RUnlock()
	idx, ok := r.index[name]
	if !ok {
		return Spec{}, "", false
	}
	return r.specs[idx], strings.TrimSpace(args), true
}

// Dispatch 解析并执行一条命令。
// matched=false：既不是内置命令也不是技能（调用方按普通文本处理）。
// matched=true 且 err!=nil：命令认了但执行出错，err 里带原因。
func (r *Registry) Dispatch(ctx context.Context, raw string) (Result, bool, error) {
	spec, args, ok := r.Resolve(raw)
	if ok {
		res, err := spec.Handler(ctx, args, raw)
		if res.Name == "" {
			res.Name = spec.Name
		}
		return res, true, err
	}
	r.mu.RLock()
	fb := r.fallback
	r.mu.RUnlock()
	if fb != nil && strings.HasPrefix(strings.TrimSpace(raw), "/") {
		if res, matched := fb(ctx, raw); matched {
			return res, true, nil
		}
	}
	return Result{}, false, nil
}

// Advertise 全部命令说明（按分类、名字排序），给控制台与前端做帮助/补全用
func (r *Registry) Advertise() []CommandInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CommandInfo, 0, len(r.specs))
	for _, s := range r.specs {
		out = append(out, CommandInfo{Name: s.Name, Aliases: s.Aliases, Category: s.Category, Help: s.Help})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}
