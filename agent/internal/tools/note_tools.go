package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"baize/internal/notebook"
)

// NoteTool 笔记工具（记忆星图的「手」）：写笔记、看笔记、看关系、把相关笔记自动连起来。
//
// 笔记的正本是笔记库目录下的 .md 文件（Obsidian 兼容），索引是旁挂的 SQLite。
// 与 memory 工具的分工：memory 管「零散事实」，note 管「成篇的笔记」——
// 笔记之间有 `[[双向链接]]` 组成的关系网，可以画成星图。
type NoteTool struct {
	vault *notebook.Vault
}

// NewNoteTool 创建笔记工具
func NewNoteTool(vault *notebook.Vault) *NoteTool {
	return &NoteTool{vault: vault}
}

// Name 工具名
func (t *NoteTool) Name() string { return "note" }

// Description 说明
func (t *NoteTool) Description() string {
	return "笔记（记忆星图）：把成篇的内容写成 Markdown 笔记（write），正文里的 [[双向链接]] 会被自动解析成关系；" +
		"可以看一篇笔记（get）、列出笔记（list）、全文检索（search）、看它的出链与反向链接（links）、" +
		"看整张星图（graph）、按共引关系把相关笔记自动连起来（autolink）。"
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
				"enum": []string{"write", "get", "list", "search", "links", "graph", "tags", "autolink", "resolve"},
				"description": "write=写/覆盖一篇笔记；get=按 key 取一篇；list=列笔记；search=全文检索；" +
					"links=看某篇的出链与反向链接；graph=看星图（点+边）；tags=标签云；" +
					"autolink=按共引相似度自动连边；resolve=重新解析悬空链接",
			},
			"path":        map[string]any{"type": "string", "description": "write：笔记路径（如 项目/白泽.md），同一路径再写是覆盖更新；不传则用标题.md"},
			"title":       map[string]any{"type": "string", "description": "write：标题，默认取正文第一个一级标题"},
			"content":     map[string]any{"type": "string", "description": "write：Markdown 正文"},
			"tags":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "write：额外标签（正文里的 #标签 也会自动提取）"},
			"key":         map[string]any{"type": "string", "description": "get / links：笔记的文档键（list 结果里带，就是小写相对路径）"},
			"query":       map[string]any{"type": "string", "description": "list：按标题/别名/正文关键词过滤；search：检索词"},
			"tag":         map[string]any{"type": "string", "description": "list / graph：只看某个标签"},
			"limit":       map[string]any{"type": "integer", "description": "list 默认 30；search 默认 10；graph 默认 200（0=不限）"},
			"root":        map[string]any{"type": "string", "description": "graph：以这篇笔记为中心取局部图（笔记 key）"},
			"hops":        map[string]any{"type": "integer", "description": "graph：局部图跳数，默认 1"},
			"includeAuto": map[string]any{"type": "boolean", "description": "graph：是否带上自动相关边（默认 true）"},
			"minWeight":   map[string]any{"type": "number", "description": "graph：自动相关边的最小权重，默认 0"},
			"topK":        map[string]any{"type": "integer", "description": "autolink：每篇笔记最多连几个最像的，默认 5"},
			"minSim":      map[string]any{"type": "number", "description": "autolink：共引相似度（Jaccard）下限，默认 0.25"},
		},
		"required": []string{"action"},
	}
}

// Run 执行
func (t *NoteTool) Run(_ context.Context, args map[string]any) (any, error) {
	if t.vault == nil {
		return nil, errors.New("笔记库未启用")
	}
	action := strings.ToLower(ArgString(args, "action"))

	switch action {
	case "write":
		content := ArgString(args, "content")
		if content == "" {
			return nil, errors.New("write 需要 content")
		}
		note, err := t.vault.Write(notebook.WriteOptions{
			Path: ArgString(args, "path"), Title: ArgString(args, "title"),
			Content: content, Tags: argStrings(args, "tags"),
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok": true, "key": note.Key, "title": note.Title, "path": note.Path,
			"links": linkTargets(note.Links), "tags": note.Tags,
		}, nil

	case "get":
		key := ArgString(args, "key")
		if key == "" {
			return nil, errors.New("get 需要 key（list 结果里带）")
		}
		note, ok, err := t.vault.Read(key)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("没有这篇笔记：%s", key)
		}
		return note, nil

	case "list":
		notes, total, err := t.vault.List(notebook.ListOptions{
			Tag: ArgString(args, "tag"), Query: ArgString(args, "query"),
			Limit: ArgInt(args, "limit", 30),
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"count": len(notes), "total": total, "notes": notes}, nil

	case "search":
		q := firstNonEmptyStr(ArgString(args, "query"), ArgString(args, "key"))
		if q == "" {
			return nil, errors.New("search 需要 query")
		}
		hits, err := t.vault.Search(q, ArgInt(args, "limit", 10))
		if err != nil {
			return nil, err
		}
		return map[string]any{"count": len(hits), "hits": hits}, nil

	case "links":
		key := ArgString(args, "key")
		if key == "" {
			return nil, errors.New("links 需要 key")
		}
		out, err := t.vault.Outlinks(key)
		if err != nil {
			return nil, err
		}
		bl, err := t.vault.Backlinks(key)
		if err != nil {
			return nil, err
		}
		return map[string]any{"key": key, "outlinks": out, "backlinks": bl}, nil

	case "graph":
		hop := ArgInt(args, "hops", 0)
		var h int
		if hop > 0 {
			h = hop
		}
		g, err := t.vault.Graph(notebook.GraphOptions{
			Limit:          ArgInt(args, "limit", 200),
			IncludeTags:    argBoolDefault(args, "includeTags", true),
			IncludeRelated: argBoolDefault(args, "includeAuto", true), // 默认带自动边；显式传 false 才关
			MinWeight:      ArgFloat(args, "minWeight", 0),
			Root:           ArgString(args, "root"),
			Hops:           h,
			Tag:            ArgString(args, "tag"),
		})
		if err != nil {
			return nil, err
		}
		return g, nil

	case "tags":
		tags, err := t.vault.Tags()
		if err != nil {
			return nil, err
		}
		stats, err := t.vault.Stats()
		if err != nil {
			return nil, err
		}
		return map[string]any{"tags": tags, "stats": stats}, nil

	case "autolink":
		res, err := t.vault.AutoRelated(notebook.RelatedOptions{
			TopK: ArgInt(args, "topK", 0), MinSim: ArgFloat(args, "minSim", 0),
		})
		if err != nil {
			return nil, err
		}
		return res, nil

	case "resolve":
		n, err := t.vault.ResolveLinks()
		if err != nil {
			return nil, err
		}
		return map[string]any{"resolved": n}, nil
	}
	return nil, fmt.Errorf("不支持的 action：%q（可用：write、get、list、search、links、graph、tags、autolink、resolve）", action)
}

// NoteForgetTool 删除一篇笔记。删除必须人工审批，所以单独成一个工具。
type NoteForgetTool struct {
	vault *notebook.Vault
}

// NewNoteForgetTool 创建 note_forget
func NewNoteForgetTool(vault *notebook.Vault) *NoteForgetTool {
	return &NoteForgetTool{vault: vault}
}

// Name 工具名
func (t *NoteForgetTool) Name() string { return "note_forget" }

// Description 说明
func (t *NoteForgetTool) Description() string {
	return "删掉一篇笔记（连同它的 .md 文件和索引里的链接）。需要人工审批；删掉就真的没了。"
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
			"key": map[string]any{"type": "string", "description": "要删的笔记文档键（list 结果里带）"},
		},
		"required": []string{"key"},
	}
}

// Run 执行
func (t *NoteForgetTool) Run(_ context.Context, args map[string]any) (any, error) {
	if t.vault == nil {
		return nil, errors.New("笔记库未启用")
	}
	key := ArgString(args, "key")
	if key == "" {
		return nil, errors.New("请给 key，说清要删哪篇笔记")
	}
	ok, err := t.vault.Delete(key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("没找到这篇笔记，什么都没删")
	}
	return map[string]any{"forgot": 1, "key": key}, nil
}

// RegisterNotes 注册笔记类工具（笔记工具 + 删除工具）
func RegisterNotes(r *Registry, vault *notebook.Vault) {
	if vault == nil {
		return
	}
	r.Register(NewNoteTool(vault))
	r.Register(NewNoteForgetTool(vault))
}

// linkTargets 把解析出来的链接拍成目标字符串，给模型看
func linkTargets(links []notebook.LinkRef) []string {
	out := make([]string, 0, len(links))
	for _, l := range links {
		s := l.Target
		if l.Anchor != "" {
			s += "#" + l.Anchor
		}
		if l.Embed {
			s = "!" + s
		}
		out = append(out, s)
	}
	return out
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
