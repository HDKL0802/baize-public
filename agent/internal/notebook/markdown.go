package notebook

// Markdown 解析层：与 Obsidian 对齐的那一套语法。
//
// 只做「提取」，不渲染：
//   - frontmatter（`---` 之间的极简 YAML：标量 / 内联数组 / 块级数组）
//   - wikilink `[[目标]]`、别名 `|`、锚点 `#`、块引用 `^`、嵌入 `![[...]]`
//   - 标准 Markdown 内链 `[文字](笔记.md)`
//   - 标签 `#tag`（含嵌套 `a/b`，frontmatter 里的 tags 也算）
//   - 标题 `#` ~ `######`
//
// 代码块与行内代码会被跳过——否则 `` `#不是标签` `` 这种会被误判。
// 与旧实现最大的差别：不再「先拼接再正则」，而是逐行状态机，因此能拿到行号、
// 能区分嵌入与普通链接、能正确处理 frontmatter 边界。

import (
	"strconv"
	"strings"
	"unicode"
)

// LinkRef 一处内部链接（wikilink 或 markdown 内链）
type LinkRef struct {
	Target string `json:"target"`           // 原始目标（未归一化）
	Alias  string `json:"alias,omitempty"`  // `|` 之后的显示文字
	Anchor string `json:"anchor,omitempty"` // `#` 之后的锚点（标题或块 id）
	Block  string `json:"block,omitempty"`  // `^块id` 的块引用
	Embed  bool   `json:"embed,omitempty"`  // `![[...]]` 或图片内链
	Line   int    `json:"line"`             // 1-based 行号（正文内）
}

// Heading 一个标题
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
	Line  int    `json:"line"`
}

// Parsed 一篇笔记的解析结果
type Parsed struct {
	Title       string         `json:"title"`
	Aliases     []string       `json:"aliases"`
	Tags        []string       `json:"tags"`
	Frontmatter map[string]any `json:"frontmatter"`
	Links       []LinkRef      `json:"links"`
	Headings    []Heading      `json:"headings"`
	Body        string         `json:"-"` // 去掉 frontmatter 的正文
	BodyLine    int            `json:"-"` // 正文首行在原文中的行号（1-based）
}

// ParseMarkdown 解析一篇笔记正文；fallbackTitle 一般是文件名（不含 .md）。
func ParseMarkdown(content, fallbackTitle string) Parsed {
	fm, body, bodyLine := splitFrontmatter(content)
	p := Parsed{
		Frontmatter: fm,
		Body:        body,
		BodyLine:    bodyLine,
		Aliases:     []string{},
		Tags:        []string{},
		Links:       []LinkRef{},
		Headings:    []Heading{},
	}

	// frontmatter 里声明的 tag / aliases / title
	p.Tags = append(p.Tags, frontmatterTags(fm)...)
	p.Aliases = append(p.Aliases, frontmatterStrings(fm, "aliases", "alias")...)
	if t := frontmatterString(fm, "title"); t != "" {
		p.Title = t
	}

	inFence := false
	fenceMark := ""
	for i, raw := range strings.Split(body, "\n") {
		lineNo := bodyLine + i
		line := strings.TrimRight(raw, "\r")
		trim := strings.TrimSpace(line)

		// 代码围栏：``` 或 ~~~（允许带语言后缀）
		if mark, ok := fenceOf(trim); ok {
			if !inFence {
				inFence, fenceMark = true, mark
			} else if strings.HasPrefix(trim, fenceMark) {
				inFence, fenceMark = false, ""
			}
			continue
		}
		if inFence {
			continue
		}

		// 标题（`# 标题`：井号后必须有空格，避免把 #tag 当标题）
		if h, ok := parseHeading(trim); ok {
			p.Headings = append(p.Headings, Heading{Level: h.Level, Text: h.Text, Line: lineNo})
			if p.Title == "" && h.Level == 1 && h.Text != "" {
				p.Title = h.Text
			}
			continue // 整行是标题，不再扫链接/标签
		}

		clean := stripInlineCode(line)
		p.Links = append(p.Links, scanWikiLinks(clean, lineNo)...)
		p.Links = append(p.Links, scanMarkdownLinks(clean, lineNo)...)
		p.Tags = append(p.Tags, scanTags(clean)...)
	}

	if p.Title == "" {
		p.Title = fallbackTitle
	}
	p.Aliases = dedupe(p.Aliases)
	p.Tags = dedupeNormalizedTags(p.Tags)
	p.Links = dedupeLinks(p.Links)
	return p
}

// ---------- frontmatter ----------

// splitFrontmatter 切出 `---` 包裹的 frontmatter，返回 (解析结果, 正文, 正文首行号)。
// 没有 frontmatter 时返回空 map 与原文。
func splitFrontmatter(content string) (map[string]any, string, int) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return map[string]any{}, content, 1
	}
	if strings.TrimSpace(lines[0]) != "---" {
		return map[string]any{}, content, 1
	}
	for i := 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "---" || t == "..." {
			fm := parseMiniYAML(lines[1:i])
			return fm, strings.Join(lines[i+1:], "\n"), i + 2
		}
	}
	// 没闭合：整篇当正文，别把内容吃掉
	return map[string]any{}, content, 1
}

// parseMiniYAML 极简 YAML：只支持笔记 frontmatter 里真正会出现的那几种写法。
//
//	key: value
//	key: "quoted"
//	key: [a, b, c]
//	key:
//	  - a
//	  - b
//
// 其它结构（嵌套 map、多行字符串）按原始字符串保存，不猜。
func parseMiniYAML(lines []string) map[string]any {
	out := map[string]any{}
	curKey := ""
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indented := line != strings.TrimLeft(line, " \t")
		trim := strings.TrimSpace(line)

		// 块级数组项：`- x`
		if indented && strings.HasPrefix(trim, "- ") && curKey != "" {
			item := unquote(strings.TrimSpace(trim[2:]))
			switch v := out[curKey].(type) {
			case []string:
				out[curKey] = append(v, item)
			default:
				out[curKey] = []string{item}
			}
			continue
		}
		if indented {
			continue // 嵌套结构不解析
		}

		idx := strings.Index(trim, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(trim[:idx])
		val := strings.TrimSpace(trim[idx+1:])
		curKey = key
		if val == "" {
			out[key] = []string{}
			continue
		}
		if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
			out[key] = splitInlineList(val[1 : len(val)-1])
			continue
		}
		out[key] = unquote(val)
	}
	return out
}

func splitInlineList(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if v := unquote(strings.TrimSpace(part)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func frontmatterString(fm map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := fm[k].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case []string:
			if len(v) > 0 {
				return strings.TrimSpace(v[0])
			}
		}
	}
	return ""
}

func frontmatterStrings(fm map[string]any, keys ...string) []string {
	out := []string{}
	for _, k := range keys {
		switch v := fm[k].(type) {
		case string:
			for _, p := range strings.Split(v, ",") {
				if s := strings.TrimSpace(p); s != "" {
					out = append(out, s)
				}
			}
		case []string:
			for _, s := range v {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// frontmatterTags 取 frontmatter 里的 tags/tag，统一去掉前导 #。
func frontmatterTags(fm map[string]any) []string {
	raw := frontmatterStrings(fm, "tags", "tag")
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		t = normalizeTag(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ---------- 行级扫描 ----------

func fenceOf(trim string) (string, bool) {
	for _, m := range []string{"```", "~~~"} {
		if strings.HasPrefix(trim, m) {
			return m, true
		}
	}
	return "", false
}

// stripInlineCode 去掉行内代码 “ `x` “ / “ “x“ “，避免里面的 # [[]] 被误判。
func stripInlineCode(line string) string {
	var b strings.Builder
	b.Grow(len(line))
	for i := 0; i < len(line); {
		if line[i] != '`' {
			b.WriteByte(line[i])
			i++
			continue
		}
		// 数一下连续反引号个数
		n := 1
		for i+n < len(line) && line[i+n] == '`' {
			n++
		}
		closeIdx := strings.Index(line[i+n:], strings.Repeat("`", n))
		if closeIdx < 0 {
			b.WriteString(line[i:])
			break
		}
		i = i + n + closeIdx + n // 跳过整段，用空格占位保持列位置无意义但行号不变
		b.WriteByte(' ')
	}
	return b.String()
}

func parseHeading(trim string) (Heading, bool) {
	if !strings.HasPrefix(trim, "#") {
		return Heading{}, false
	}
	lvl := 0
	for lvl < len(trim) && trim[lvl] == '#' {
		lvl++
	}
	if lvl > 6 || lvl == len(trim) || trim[lvl] != ' ' {
		return Heading{}, false
	}
	text := strings.TrimSpace(trim[lvl+1:])
	if text == "" {
		return Heading{}, false
	}
	return Heading{Level: lvl, Text: text}, true
}

// scanWikiLinks 扫 `[[...]]` 与 `![[...]]`。
func scanWikiLinks(line string, lineNo int) []LinkRef {
	var out []LinkRef
	i := 0
	for i < len(line) {
		rel := strings.Index(line[i:], "[[")
		if rel < 0 {
			break
		}
		start := i + rel
		endRel := strings.Index(line[start+2:], "]]")
		if endRel < 0 {
			break
		}
		end := start + 2 + endRel
		raw := line[start+2 : end]
		embed := start > 0 && line[start-1] == '!'
		if ref, ok := splitWikiRef(raw, embed, lineNo); ok {
			out = append(out, ref)
		}
		i = end + 2
	}
	return out
}

func splitWikiRef(raw string, embed bool, lineNo int) (LinkRef, bool) {
	ref := LinkRef{Embed: embed, Line: lineNo}
	// 别名：`|` 之后；也兼容转义 `\|`
	if p := indexUnescaped(raw, '|'); p >= 0 {
		ref.Alias = strings.TrimSpace(strings.ReplaceAll(raw[p+1:], `\|`, "|"))
		raw = raw[:p]
	}
	// 锚点：`#` 之后；块引用 `^id`
	if p := strings.Index(raw, "#"); p >= 0 {
		ref.Anchor = strings.TrimSpace(raw[p+1:])
		raw = raw[:p]
	}
	if p := strings.Index(ref.Anchor, "^"); p >= 0 {
		ref.Block = strings.TrimSpace(ref.Anchor[p+1:])
		ref.Anchor = strings.TrimSpace(ref.Anchor[:p])
	}
	ref.Target = strings.TrimSpace(raw)
	if ref.Target == "" && ref.Anchor == "" && ref.Block == "" {
		return ref, false
	}
	return ref, true
}

func indexUnescaped(s string, ch byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++ // 跳过被转义的字符
			continue
		}
		if s[i] == ch {
			return i
		}
	}
	return -1
}

// scanMarkdownLinks 扫 `[文字](目标)`，只收「看起来是库内文件」的：
// 不含协议头、不是纯锚点、以 .md 结尾或带 / 的路径。
func scanMarkdownLinks(line string, lineNo int) []LinkRef {
	var out []LinkRef
	i := 0
	for i < len(line) {
		open := strings.Index(line[i:], "](")
		if open < 0 {
			break
		}
		open += i
		closeRel := strings.Index(line[open+2:], ")")
		if closeRel < 0 {
			break
		}
		target := strings.TrimSpace(line[open+2 : open+2+closeRel])
		// 去掉 `标题` / "标题"
		if p := strings.IndexAny(target, " \t"); p >= 0 {
			target = target[:p]
		}
		// 图片语法 `![alt](目标)`：`!` 在同级的 `[` 之前
		embed := false
		if lb := strings.LastIndex(line[:open], "["); lb > 0 && line[lb-1] == '!' {
			embed = true
		}
		if isLocalNoteTarget(target) {
			out = append(out, LinkRef{Target: target, Embed: embed, Line: lineNo})
		}
		i = open + 2 + closeRel + 1
	}
	return out
}

func isLocalNoteTarget(t string) bool {
	if t == "" || strings.HasPrefix(t, "#") {
		return false
	}
	lower := strings.ToLower(t)
	for _, p := range []string{"http://", "https://", "mailto:", "obsidian://", "file://", "data:"} {
		if strings.HasPrefix(lower, p) {
			return false
		}
	}
	return strings.HasSuffix(lower, ".md") || strings.HasPrefix(t, "./") || strings.HasPrefix(t, "../")
}

// scanTags 扫 `#标签`。规则与 Obsidian 一致：
//   - 井号前必须是行首/空白/标点（不能紧贴字母数字，避免 URL 里的 #）
//   - 井号后必须是字母（中文算字母）或数字之外的「字符起始」
//   - 允许 字母/数字/_/-/ 嵌套层级
func scanTags(line string) []string {
	var out []string
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		if rs[i] != '#' {
			continue
		}
		if i > 0 && !isTagBoundary(rs[i-1]) {
			continue
		}
		j := i + 1
		if j >= len(rs) || !isTagStart(rs[j]) {
			continue
		}
		k := j
		for k < len(rs) && isTagChar(rs[k]) {
			k++
		}
		// 结尾不能是分隔符
		for k > j && (rs[k-1] == '/' || rs[k-1] == '-' || rs[k-1] == '_') {
			k--
		}
		if t := normalizeTag(string(rs[j:k])); t != "" {
			out = append(out, t)
		}
		i = k - 1
	}
	return out
}

func isTagBoundary(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("([{<,.;:!?\"'“”‘’（【《，。；：！？、", r)
}

func isTagStart(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isTagChar(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	switch r {
	case '_', '-', '/', '·':
		return true
	}
	return false
}

// normalizeTag 去掉前导 #、首尾空白，保留嵌套层级。
func normalizeTag(t string) string {
	t = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#"))
	t = strings.Trim(t, "-_/")
	return t
}

// ---------- 去重 ----------

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func dedupeNormalizedTags(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = normalizeTag(t)
		if t == "" {
			continue
		}
		low := strings.ToLower(t)
		if seen[low] {
			continue
		}
		seen[low] = true
		out = append(out, t)
	}
	return out
}

func dedupeLinks(in []LinkRef) []LinkRef {
	seen := make(map[string]bool, len(in))
	out := make([]LinkRef, 0, len(in))
	for _, l := range in {
		if strings.TrimSpace(l.Target) == "" {
			continue
		}
		k := strings.ToLower(l.Target) + "\x00" + strings.ToLower(l.Anchor) + "\x00" + strconv.FormatBool(l.Embed)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, l)
	}
	return out
}

// firstLine 取正文第一段非空文字（没有标题时的兜底标题）
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(strings.TrimLeft(line, "#>-* ")); t != "" {
			return utf8Truncate(t, 120)
		}
	}
	return ""
}

func utf8Truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

// tokenEstimate 粗估 token：中文按 1 rune ≈ 1 token，西文按 4 字符 ≈ 1 token。
func tokenEstimate(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if isCJK(r) {
			cjk++
		} else if !unicode.IsSpace(r) {
			other++
		}
	}
	return cjk + other/4 + 1
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}
