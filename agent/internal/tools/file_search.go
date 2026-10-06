package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// FileSearch 在工作目录内按「文件名 / 文件内容」搜索（纯 Go，无外部依赖）。
//
// 为什么要它：fs_list 只能看一层目录，shell 里的 grep/find 又依赖平台（NAS 容器里
// 不一定有 grep，Windows 上更没有）。检索自己的笔记与产物是高频动作，所以单独做一个
// 跨平台、只看工作目录的搜索工具。
type FileSearch struct{ ws *Workspace }

// NewFileSearch 创建 file_search
func NewFileSearch(ws *Workspace) *FileSearch { return &FileSearch{ws: ws} }

// Name 工具名
func (t *FileSearch) Name() string { return "file_search" }

// Description 说明
func (t *FileSearch) Description() string {
	return "在工作目录内搜索文件：按文件名（name，支持 * 通配）和/或内容（query，默认子串匹配，可开 regex）" +
		"查找，返回命中文件与命中的行（带行号）。只读，不会改任何文件。"
}

// Schema 参数说明
func (t *FileSearch) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":         map[string]any{"type": "string", "description": "要搜索的内容（子串或正则）；留空则只按文件名搜"},
			"name":          map[string]any{"type": "string", "description": "文件名过滤：glob（如 *.md、src/*.go）或子串（如 notes）"},
			"path":          map[string]any{"type": "string", "description": "在哪个子目录下搜，默认工作目录根"},
			"regex":         map[string]any{"type": "boolean", "description": "query 是否按正则解释，默认 false（子串，区分大小写见 caseSensitive）"},
			"caseSensitive": map[string]any{"type": "boolean", "description": "是否区分大小写，默认 false（不区分）"},
			"maxResults":    map[string]any{"type": "integer", "description": "最多返回多少个命中文件，默认 50"},
			"maxPerFile":    map[string]any{"type": "integer", "description": "每个文件最多返回几行命中，默认 5"},
			"maxFileBytes":  map[string]any{"type": "integer", "description": "内容搜索时单个文件最大读取字节，默认 1048576（更大的跳过）"},
		},
	}
}

// 内容搜索时跳过的目录：这些目录里的东西不是"用户的资料"，扫它们只会把结果淹掉。
var fileSearchSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true, ".idea": true, ".vscode": true,
}

// FileSearchHit 一个命中文件
type FileSearchHit struct {
	Path    string           `json:"path"` // 相对工作目录的路径
	Size    int64            `json:"size"`
	Matches []FileSearchLine `json:"matches,omitempty"`
}

// FileSearchLine 命中行
type FileSearchLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Run 执行
func (t *FileSearch) Run(ctx context.Context, args map[string]any) (any, error) {
	if t.ws == nil {
		return nil, errors.New("没有工作目录，无法搜索")
	}
	query := ArgString(args, "query")
	nameFilter := strings.TrimSpace(ArgString(args, "name"))
	if query == "" && nameFilter == "" {
		return nil, errors.New("请至少给 query（搜内容）或 name（搜文件名）")
	}
	root := t.ws.Root()
	start, err := t.ws.Resolve(firstNonEmptyStr(ArgString(args, "path"), "."))
	if err != nil {
		return nil, err
	}
	maxResults := ArgInt(args, "maxResults", 50)
	if maxResults <= 0 {
		maxResults = 50
	}
	maxPerFile := ArgInt(args, "maxPerFile", 5)
	if maxPerFile <= 0 {
		maxPerFile = 5
	}
	maxFileBytes := ArgInt(args, "maxFileBytes", 1<<20)
	if maxFileBytes <= 0 {
		maxFileBytes = 1 << 20
	}
	caseSensitive := ArgBool(args, "caseSensitive")

	var contentRe *regexp.Regexp
	if query != "" {
		pattern := regexp.QuoteMeta(query)
		if ArgBool(args, "regex") {
			pattern = query
		}
		if !caseSensitive {
			pattern = "(?i)" + pattern
		}
		contentRe, err = regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("搜索式不合法：%w", err)
		}
	}
	nameGlob, nameSub := parseNameFilter(nameFilter)

	hits := []FileSearchHit{}
	scanned, skippedBig, skippedBinary, truncated := 0, 0, 0, false
	walkErr := filepath.WalkDir(start, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 单个条目读不了就跳过，别让整趟搜索失败
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			rel = p
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if p != start && fileSearchSkipDirs[strings.ToLower(d.Name())] {
				return filepath.SkipDir
			}
			// name 过滤是 glob（含路径分隔符）时，用相对路径判断目录是否还要进
			return nil
		}
		scanned++

		nameOK := nameFilter == ""
		if nameFilter != "" {
			nameOK = matchName(nameGlob, nameSub, rel, d.Name(), caseSensitive)
		}
		// 只按文件名搜：命中就算，不读内容
		if query == "" {
			if nameOK {
				info, _ := d.Info()
				var size int64
				if info != nil {
					size = info.Size()
				}
				hits = append(hits, FileSearchHit{Path: rel, Size: size})
			}
			if len(hits) >= maxResults {
				truncated = true
				return filepath.SkipAll
			}
			return nil
		}

		// 内容搜索：先看类型与大小，二进制/超大直接跳过（并如实计数）
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.Size() > int64(maxFileBytes) {
			skippedBig++
			return nil
		}
		f, oerr := os.Open(p)
		if oerr != nil {
			return nil
		}
		defer f.Close()
		if isBinaryHead(f) {
			skippedBinary++
			return nil
		}
		lines, lerr := grepFile(ctx, f, contentRe, maxPerFile)
		f.Close()
		if lerr != nil {
			return nil
		}
		if len(lines) == 0 {
			return nil
		}
		// 既给了 name 又给了 query 时：name 必须同时命中
		if !nameOK {
			return nil
		}
		hits = append(hits, FileSearchHit{Path: rel, Size: info.Size(), Matches: lines})
		if len(hits) >= maxResults {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		return nil, fmt.Errorf("搜索失败：%w", walkErr)
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Path < hits[j].Path })
	out := map[string]any{
		"root": root, "query": query, "name": nameFilter,
		"count": len(hits), "scanned": scanned, "hits": hits, "truncated": truncated,
	}
	if skippedBig > 0 {
		out["skippedTooBig"] = skippedBig
	}
	if skippedBinary > 0 {
		out["skippedBinary"] = skippedBinary
	}
	if len(hits) == 0 {
		out["note"] = "没有命中；可以把 path 换成子目录、或用 name 只按文件名搜"
	}
	return out, nil
}

// parseNameFilter 判断 name 是 glob（含 * ? [ 或路径分隔符）还是纯子串
func parseNameFilter(name string) (glob, sub string) {
	if name == "" {
		return "", ""
	}
	if strings.ContainsAny(name, "*?[") || strings.ContainsAny(name, "/\\") {
		return filepath.ToSlash(name), ""
	}
	return "", name
}

// matchName 文件名是否命中（glob 按相对路径匹配，也接受只匹配 basename；子串按不区分大小写匹配）
func matchName(glob, sub, rel, base string, caseSensitive bool) bool {
	if sub != "" {
		if caseSensitive {
			return strings.Contains(base, sub) || strings.Contains(rel, sub)
		}
		l := strings.ToLower
		return strings.Contains(l(base), l(sub)) || strings.Contains(l(rel), l(sub))
	}
	if glob == "" {
		return true
	}
	if ok, _ := filepath.Match(glob, rel); ok {
		return true
	}
	if ok, _ := filepath.Match(glob, base); ok {
		return true
	}
	// ** 语义：当作"任意层级下匹配结尾"
	if strings.HasPrefix(glob, "**/") {
		if ok, _ := filepath.Match(strings.TrimPrefix(glob, "**/"), base); ok {
			return true
		}
	}
	return false
}

// isBinaryHead 头 8KB 里有 NUL 就当成二进制（不去做内容匹配）
func isBinaryHead(f *os.File) bool {
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	if _, err := f.Seek(0, 0); err != nil {
		return true // 回不到开头就没法安心读，按二进制处理
	}
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return true
		}
	}
	return false
}

// grepFile 逐行匹配，最多返回 limit 行命中
func grepFile(ctx context.Context, f *os.File, re *regexp.Regexp, limit int) ([]FileSearchLine, error) {
	out := []FileSearchLine{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 单行上限 1MB，防超长行把内存顶爆
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if lineNo%512 == 0 {
			if err := ctx.Err(); err != nil {
				return out, err
			}
		}
		line := sc.Text()
		if !re.MatchString(line) {
			continue
		}
		out = append(out, FileSearchLine{Line: lineNo, Text: truncateRunes(line, 300)})
		if len(out) >= limit {
			break
		}
	}
	return out, sc.Err()
}

// truncateRunes 按字符截断（避免把中文从中间截断成乱码）
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// RegisterFileSearch 注册 file_search
func RegisterFileSearch(r *Registry, ws *Workspace) {
	if ws == nil {
		return
	}
	r.Register(NewFileSearch(ws))
}
