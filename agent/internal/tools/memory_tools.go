package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"baize/internal/memory"
)

// MemoryTool 长期记忆工具。对齐 OpenHuman 的做法：把"记住 / 想起 / 检索 / 看看有什么"
// 折叠成**一个**记忆工具，用 action 区分——工具表里少一个名字，模型就少一次选错。
//
// 唯一被拆出去的是 memory_forget（删除）：白泽的审批闸门只看工具名、看不到参数，
// 单个工具没法做到"查免审批、删要审批"，所以删除单独成工具。
type MemoryTool struct {
	store     *memory.Store
	namespace string // 默认分区（配置里配的）
}

// NewMemoryTool 创建记忆工具
func NewMemoryTool(store *memory.Store, namespace string) *MemoryTool {
	return &MemoryTool{store: store, namespace: memory.NormalizeNamespace(namespace)}
}

// Name 工具名
func (t *MemoryTool) Name() string { return "memory" }

// Description 说明
func (t *MemoryTool) Description() string {
	return "长期记忆：把值得记住的事写进去（store），按需想起相关记忆（recall / hybrid_search / vector_search），" +
		"也能看看都有哪些记忆（kinds / doctor）。记忆会按分区（namespace）隔离。"
}

// Mutating 会写库：执行前打快照
func (t *MemoryTool) Mutating() bool { return true }

// Schema 参数说明
func (t *MemoryTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"store", "recall", "hybrid_search", "vector_search", "kinds", "doctor", "reindex"},
				"description": "store=记住一段内容；recall=按查询自动想起最相关的几段（带上下文，适合直接参考）；" +
					"hybrid_search=关键词+语义+联想+新鲜度混合检索；vector_search=纯语义检索；" +
					"kinds=列出都有哪些分区/分类/类型；doctor=自检（向量通道在不在等）；" +
					"reindex=把向量补齐/重算（刚配好向量通道、或换过向量模型时用一次）",
			},
			"content": map[string]any{"type": "string", "description": "store：要记住的内容"},
			"query":   map[string]any{"type": "string", "description": "recall / *_search：查询文本"},
			"title":   map[string]any{"type": "string", "description": "store：标题，默认取内容首行"},
			"kind": map[string]any{"type": "string",
				"description": "store：类型 note/event/task/decision/error/doc，默认 note"},
			"importance": map[string]any{"type": "number", "description": "store：重要度 0-100，默认按类型推断"},
			"sourceRef":  map[string]any{"type": "string", "description": "store：来源引用（文件路径 / 任务 id / 会话 id）"},
			"docKey": map[string]any{"type": "string",
				"description": "store：文档键。**同一把键再写是覆盖更新**（用来记住「最新情况」，而不是堆旧账）；不传则按内容自动生成"},
			"namespace": map[string]any{"type": "string", "description": "记忆分区，默认用配置里的分区"},
			"category": map[string]any{"type": "string",
				"description": "分类 core/daily/conversation/custom，默认 core"},
			"profile": map[string]any{"type": "string",
				"description": "检索权重档位 balanced/semantic/lexical/graph_first，默认按配置"},
			"limit":          map[string]any{"type": "integer", "description": "返回条数，默认 5"},
			"minScore":       map[string]any{"type": "number", "description": "分数下限，低于它的不返回"},
			"timeWindowDays": map[string]any{"type": "integer", "description": "只看最近 N 天的记忆"},
			"diverse":        map[string]any{"type": "boolean", "description": "是否做多样性去重（避免返回一堆几乎一样的）"},
			"budgetTokens":   map[string]any{"type": "integer", "description": "recall：注入上下文的 token 预算，默认按配置"},
			"minVectorSim":   map[string]any{"type": "number", "description": "recall：最低语义相似度，默认 0.35"},
		},
		"required": []string{"action"},
	}
}

// Run 执行
func (t *MemoryTool) Run(ctx context.Context, args map[string]any) (any, error) {
	if t.store == nil {
		return nil, errors.New("记忆库未启用")
	}
	action := strings.ToLower(ArgString(args, "action"))
	ns := firstNonEmptyStr(ArgString(args, "namespace"), t.namespace)

	switch action {
	case "store":
		content := ArgString(args, "content")
		if content == "" {
			return nil, errors.New("store 需要 content（要记住的内容）")
		}
		res, err := t.store.IngestContext(ctx, content, memory.IngestOptions{
			Namespace:  ns,
			Category:   ArgString(args, "category"),
			DocKey:     ArgString(args, "docKey"),
			Source:     "agent",
			SourceRef:  ArgString(args, "sourceRef"),
			Kind:       firstNonEmptyStr(ArgString(args, "kind"), "note"),
			Title:      ArgString(args, "title"),
			Importance: ArgFloat(args, "importance", 0),
		})
		if err != nil {
			return nil, err
		}
		out := map[string]any{
			"stored": res.Chunks, "tokens": res.Tokens, "docKey": res.DocKey,
			"namespace": ns, "embedded": res.Embedded,
		}
		if res.Skipped > 0 {
			out["skipped"] = true
			out["note"] = "同样的来源引用与内容已经记过了，没有重复入库"
		}
		if res.Replaced > 0 {
			out["replaced"] = res.Replaced
			out["note"] = fmt.Sprintf("同一把文档键，已用新内容覆盖旧的 %d 条", res.Replaced)
		}
		if res.Embedded == 0 {
			out["vectorNote"] = "这条没落向量（没配 embedding 通道或向量化失败）；语义检索时它只能靠关键词被找到"
		}
		return out, nil

	case "recall":
		q := ArgString(args, "query")
		if q == "" {
			return nil, errors.New("recall 需要 query")
		}
		res, err := t.store.AutoRecall(ctx, memory.RecallOptions{
			Text:         q,
			Namespace:    ns,
			Profile:      firstNonEmptyStr(ArgString(args, "profile"), ""),
			BudgetTokens: ArgInt(args, "budgetTokens", 0),
			MinVectorSim: ArgFloat(args, "minVectorSim", 0),
			Limit:        ArgInt(args, "limit", 20),
		})
		if err != nil {
			return nil, err
		}
		if len(res.Citations) == 0 {
			return map[string]any{
				"query": q, "found": 0, "context": "",
				"note": firstNonEmptyStr(res.Note, "没有足够相关的记忆（宁可不注入，也不塞不相关的内容）"),
			}, nil
		}
		return map[string]any{
			"query": q, "found": len(res.Citations), "context": res.Context,
			"tokens": res.Tokens, "profile": res.Profile, "vectorUsed": res.VectorUsed,
			"citations": res.Citations, "note": res.Note,
		}, nil

	case "hybrid_search", "vector_search":
		q := ArgString(args, "query")
		if q == "" {
			return nil, fmt.Errorf("%s 需要 query", action)
		}
		res, err := t.store.HybridSearch(ctx, memory.HybridQuery{
			Text:           q,
			Namespace:      ns,
			Category:       ArgString(args, "category"),
			Profile:        ArgString(args, "profile"),
			Limit:          ArgInt(args, "limit", 5),
			MinScore:       ArgFloat(args, "minScore", 0),
			TimeWindowDays: ArgInt(args, "timeWindowDays", 0),
			Diverse:        ArgBool(args, "diverse"),
			VectorOnly:     action == "vector_search",
		})
		if err != nil {
			return nil, err
		}
		items := make([]map[string]any, 0, len(res.Hits))
		for _, h := range res.Hits {
			items = append(items, map[string]any{
				"id": h.Chunk.ID, "docKey": h.Chunk.DocKey, "namespace": h.Chunk.Namespace,
				"title": h.Chunk.Title, "content": h.Chunk.Content, "kind": h.Chunk.Kind,
				"score": h.Score, "why": h.Why, "parts": h.Parts, "at": h.Chunk.CreatedAt,
			})
		}
		return map[string]any{
			"query": q, "count": len(items), "items": items,
			"profile": res.Profile, "weights": res.Weights,
			"vectorUsed": res.VectorUsed, "note": res.VectorNote,
			"candidates": res.Candidates,
		}, nil

	case "kinds":
		stats, err := t.store.Kinds()
		if err != nil {
			return nil, err
		}
		return map[string]any{"kinds": stats, "defaultNamespace": ns}, nil

	case "doctor":
		rep, err := t.store.Doctor()
		if err != nil {
			return nil, err
		}
		return rep, nil

	case "reindex":
		res, err := t.store.ReindexEmbeddings(ctx, ArgInt(args, "limit", 0))
		if err != nil {
			return nil, err
		}
		return res, nil
	}
	return nil, fmt.Errorf("不支持的 action：%q（可用：store、recall、hybrid_search、vector_search、kinds、doctor、reindex）", action)
}

// MemoryForgetTool 忘掉一条记忆。删除必须人工审批，所以单独成一个工具。
type MemoryForgetTool struct{ store *memory.Store }

// NewMemoryForgetTool 创建 memory_forget
func NewMemoryForgetTool(store *memory.Store) *MemoryForgetTool {
	return &MemoryForgetTool{store: store}
}

// Name 工具名
func (t *MemoryForgetTool) Name() string { return "memory_forget" }

// Description 说明
func (t *MemoryForgetTool) Description() string {
	return "忘掉一条（或一组）长期记忆。需要人工审批；删掉就真的没了。"
}

// Dangerous 需要人工审批
func (t *MemoryForgetTool) Dangerous() bool { return true }

// Mutating 会改库：执行前打快照
func (t *MemoryForgetTool) Mutating() bool { return true }

// Schema 参数说明
func (t *MemoryForgetTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":        map[string]any{"type": "integer", "description": "要忘掉的那条记忆 id"},
			"docKey":    map[string]any{"type": "string", "description": "或按文档键忘掉（连同它的所有分块）"},
			"namespace": map[string]any{"type": "string", "description": "记忆分区，配合 docKey 使用"},
		},
	}
}

// Run 执行
func (t *MemoryForgetTool) Run(_ context.Context, args map[string]any) (any, error) {
	if t.store == nil {
		return nil, errors.New("记忆库未启用")
	}
	id := int64(ArgInt(args, "id", 0))
	key := ArgString(args, "docKey")
	if id <= 0 && key == "" {
		return nil, errors.New("请给 id 或 docKey，说清要忘掉哪条记忆")
	}
	n, err := t.store.Forget(ArgString(args, "namespace"), key, id)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errors.New("没找到匹配的记忆，什么都没删")
	}
	return map[string]any{"forgot": n}, nil
}

// RegisterMemory 注册记忆类工具（记忆工具 + 删除工具）
func RegisterMemory(r *Registry, store *memory.Store, namespace string) {
	if store == nil {
		return
	}
	r.Register(NewMemoryTool(store, namespace))
	r.Register(NewMemoryForgetTool(store))
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
