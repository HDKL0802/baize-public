package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"baize/internal/memory"
)

// NoteTool 笔记工具（记忆星图的「手」）：写笔记、看笔记、看关系、把相关笔记自动连起来。
//
// 与 memory 工具的分工：memory 管「零散事实」，note 管「成篇的笔记」——
// 笔记之间有 `[[双向链接]]` 组成的关系网，可以画成星图。
type NoteTool struct {
	store     *memory.Store
	namespace string
}

// NewNoteTool 创建笔记工具
func NewNoteTool(store *memory.Store, namespace string) *NoteTool {
	return &NoteTool{store: store, namespace: memory.NormalizeNamespace(namespace)}
}

// Name 工具名
func (t *NoteTool) Name() string { return "note" }

// Description 说明
func (t *NoteTool) Description() string {
	return "笔记（记忆星图）：把成篇的内容写成 Markdown 笔记（write），正文里的 [[双向链接]] 会被自动解析成关系；" +
		"可以看一篇笔记（get）、列出笔记（list）、看它的出链与反向链接（links）、看整张星图（graph）、" +
		"用语义相似度把相关笔记自动连起来（autolink）。"
}

// Mutating 会写库
func (t *NoteTool) Mutating() bool { return true }

// Schema 参数说明
func (t *NoteTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"write", "get", "list", "links", "graph", "tags", "autolink", "resolve"},
				"description": "write=写/覆盖一篇笔记；get=按 key 取一篇；list=列笔记；links=看某篇的出链与反向链接；" +
					"graph=看星图（点+边）；tags=标签云；autolink=按语义相似度自动连边；resolve=重新解析悬空链接",
			},
			"path":        map[string]any{"type": "string", "description": "write：笔记路径（如 项目/白泽.md），同一路径再写是覆盖更新；不传则用标题.md"},
			"title":       map[string]any{"type": "string", "description": "write：标题，默认取正文第一个一级标题"},
			"content":     map[string]any{"type": "string", "description": "write：Markdown 正文"},
			"tags":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "write：额外标签（正文里的 #标签 也会自动提取）"},
			"key":         map[string]any{"type": "string", "description": "get / links：笔记的文档键（list 结果里带）"},
			"query":       map[string]any{"type": "string", "description": "list：按标题/正文关键词过滤"},
			"tag":         map[string]any{"type": "string", "description": "list：按标签过滤"},
			"limit":       map[string]any{"type": "integer", "description": "list/graph：条数上限"},
			"namespace":   map[string]any{"type": "string", "description": "分区，默认用配置里的"},
			"includeAuto": map[string]any{"type": "boolean", "description": "graph：是否带上自动边（默认 true）"},
			"topK":        map[string]any{"type": "integer", "description": "autolink：每篇笔记最多连几个最像的，默认 5"},
			"minSim":      map[string]any{"type": "number", "description": "autolink：相似度下限，默认 0.55"},
		},
		"required": []string{"action"},
	}
}

// Run 执行
func (t *NoteTool) Run(ctx context.Context, args map[string]any) (any, error) {
	if t.store == nil {
		return nil, errors.New("记忆库未启用")
	}
	action := strings.ToLower(ArgString(args, "action"))
	ns := firstNonEmptyStr(ArgString(args, "namespace"), t.namespace)

	switch action {
	case "write":
		content := ArgString(args, "content")
		if content == "" {
			return nil, errors.New("write 需要 content")
		}
		note, err := t.store.WriteNote(ctx, memory.WriteNoteOptions{
			Namespace: ns, Path: ArgString(args, "path"), Title: ArgString(args, "title"),
			Content: content, Tags: argStrings(args, "tags"),
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok": true, "key": note.DocKey, "title": note.Title, "path": note.Path,
			"links": note.Links, "tags": note.Tags, "embedded": note.HasVector,
		}, nil

	case "get":
		key := ArgString(args, "key")
		if key == "" {
			return nil, errors.New("get 需要 key（list 结果里带）")
		}
		note, ok, err := t.store.GetNote(ns, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("没有这篇笔记：%s", key)
		}
		return note, nil

	case "list":
		notes, err := t.store.ListNotes(ns, ArgString(args, "tag"), ArgString(args, "query"),
			ArgInt(args, "limit", 30), 0)
		if err != nil {
			return nil, err
		}
		items := make([]map[string]any, 0, len(notes))
		for _, n := range notes {
			items = append(items, map[string]any{
				"key": n.DocKey, "title": n.Title, "path": n.Path, "tags": n.Tags,
				"links": len(n.Links), "at": n.UpdatedAt,
			})
		}
		return map[string]any{"count": len(items), "notes": items, "namespace": ns}, nil

	case "links":
		key := ArgString(args, "key")
		if key == "" {
			return nil, errors.New("links 需要 key")
		}
		out, err := t.store.Outlinks(ns, key)
		if err != nil {
			return nil, err
		}
		bl, err := t.store.Backlinks(ns, key)
		if err != nil {
			return nil, err
		}
		return map[string]any{"key": key, "outlinks": out, "backlinks": bl}, nil

	case "graph":
		g, err := t.store.Graph(memory.GraphOptions{
			Namespace:   ns,
			Limit:       ArgInt(args, "limit", 200),
			IncludeAuto: argBoolDefault(args, "includeAuto", true), // 默认带自动边；显式传 false 才关
		})
		if err != nil {
			return nil, err
		}
		return g, nil

	case "tags":
		tags, err := t.store.Tags(ns)
		if err != nil {
			return nil, err
		}
		stats, err := t.store.NoteStats(ns)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tags": tags, "stats": stats, "namespace": ns}, nil

	case "autolink":
		res, err := t.store.AutoLink(ctx, memory.AutoLinkOptions{
			Namespace: ns, TopK: ArgInt(args, "topK", 0), MinSim: ArgFloat(args, "minSim", 0),
		})
		if err != nil {
			return nil, err
		}
		return res, nil

	case "resolve":
		n, err := t.store.ResolveLinks(ns)
		if err != nil {
			return nil, err
		}
		return map[string]any{"resolved": n, "namespace": ns}, nil
	}
	return nil, fmt.Errorf("不支持的 action：%q（可用：write、get、list、links、graph、tags、autolink、resolve）", action)
}

// NoteForgetTool 删除一篇笔记。删除必须人工审批，所以单独成一个工具。
type NoteForgetTool struct {
	store     *memory.Store
	namespace string
}

// NewNoteForgetTool 创建 note_forget
func NewNoteForgetTool(store *memory.Store, namespace string) *NoteForgetTool {
	return &NoteForgetTool{store: store, namespace: memory.NormalizeNamespace(namespace)}
}

// Name 工具名
func (t *NoteForgetTool) Name() string { return "note_forget" }

// Description 说明
func (t *NoteForgetTool) Description() string {
	return "删掉一篇笔记（连同它的双向链接）。需要人工审批；删掉就真的没了。"
}

// Dangerous 需要人工审批
func (t *NoteForgetTool) Dangerous() bool { return true }

// Mutating 会改库
func (t *NoteForgetTool) Mutating() bool { return true }

// Schema 参数说明
func (t *NoteForgetTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":       map[string]any{"type": "string", "description": "要删的笔记文档键（list 结果里带）"},
			"namespace": map[string]any{"type": "string", "description": "分区，默认用配置里的"},
		},
		"required": []string{"key"},
	}
}

// Run 执行
func (t *NoteForgetTool) Run(_ context.Context, args map[string]any) (any, error) {
	if t.store == nil {
		return nil, errors.New("记忆库未启用")
	}
	key := ArgString(args, "key")
	if key == "" {
		return nil, errors.New("请给 key，说清要删哪篇笔记")
	}
	ns := firstNonEmptyStr(ArgString(args, "namespace"), t.namespace)
	n, err := t.store.ForgetNote(ns, key)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errors.New("没找到这篇笔记，什么都没删")
	}
	return map[string]any{"forgot": n}, nil
}

// RegisterNotes 注册笔记类工具（笔记工具 + 删除工具）
func RegisterNotes(r *Registry, store *memory.Store, namespace string) {
	if store == nil {
		return
	}
	r.Register(NewNoteTool(store, namespace))
	r.Register(NewNoteForgetTool(store, namespace))
}

// argStrings 取字符串数组参数
func argStrings(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	out := []string{}
	switch arr := v.(type) {
	case []any:
		for _, it := range arr {
			if s := strings.TrimSpace(fmt.Sprint(it)); s != "" {
				out = append(out, s)
			}
		}
	case []string:
		for _, s := range arr {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, s := range strings.Split(arr, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// argBoolDefault 缺省为 def 的布尔参数
func argBoolDefault(args map[string]any, key string, def bool) bool {
	if _, ok := args[key]; !ok {
		return def
	}
	return ArgBool(args, key)
}
