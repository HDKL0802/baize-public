package notebook

// 全文检索：自己维护倒排表（note_terms），不依赖 SQLite FTS5（不同构建不一定带）。
//
// 分词策略针对「中英混排」：
//   - 中文按 2 字 bigram（"双向链接" → 双向 / 向链 / 链接）
//   - 英文/数字按单词
//   - 单字查询退化为 unigram，避免"搜一个字搜不到"
//
// 打分是简化版 BM25：tf 累加 + idf 加权，再叠加标题/别名命中加成。
// 比旧实现的 `LIKE '%关键词%'` 强在：分词、排序、可扩展、不随笔记数线性变慢。

import (
	"database/sql"
	"math"
	"sort"
	"strings"
	"unicode"
)

// SearchHit 一条检索结果
type SearchHit struct {
	Key     string   `json:"key"`
	Path    string   `json:"path"`
	Title   string   `json:"title"`
	Score   float64  `json:"score"`
	Excerpt string   `json:"excerpt"`
	Terms   []string `json:"terms"`
}

// termFreq 把文本切成词并统计词频
func termFreq(text string) map[string]int {
	out := map[string]int{}
	rs := []rune(strings.ToLower(text))
	var word []rune

	flushWord := func() {
		if len(word) >= 2 {
			out[string(word)]++
		}
		word = word[:0]
	}

	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case isCJK(r):
			flushWord()
			start := i
			for i < len(rs) && isCJK(rs[i]) {
				i++
			}
			run := rs[start:i]
			if len(run) == 1 {
				out[string(run)]++
			} else {
				for j := 0; j+1 < len(run); j++ {
					out[string(run[j:j+2])]++
				}
			}
			i--
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			word = append(word, r)
		default:
			flushWord()
		}
	}
	flushWord()
	return out
}

// queryTerms 查询切词：中文 bigram / 英文单词，去重
func queryTerms(q string) []string {
	f := termFreq(q)
	out := make([]string, 0, len(f))
	for t := range f {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Search 全文检索
func (v *Vault) Search(q string, limit int) ([]SearchHit, error) {
	if limit <= 0 {
		limit = 20
	}
	terms := queryTerms(q)
	if len(terms) == 0 {
		return []SearchHit{}, nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	var total int
	if err := v.db.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&total); err != nil {
		return nil, err
	}
	if total == 0 {
		return []SearchHit{}, nil
	}

	ph := placeholders(len(terms))
	args := toAny(terms)

	// 每个词的文档频率（idf 用）
	df := map[string]int{}
	dfRows, err := v.db.Query(`SELECT term,COUNT(DISTINCT note_key) FROM note_terms WHERE term IN (`+ph+`) GROUP BY term`, args...)
	if err != nil {
		return nil, err
	}
	for dfRows.Next() {
		var t string
		var n int
		if err := dfRows.Scan(&t, &n); err == nil {
			df[t] = n
		}
	}
	dfRows.Close()
	if len(df) == 0 {
		return v.likeFallbackLocked(q, limit)
	}

	// 先按 AND（所有词都要命中）找；命中太少再退化为 OR
	hits, err := v.searchTermsLocked(terms, args, df, total, limit, true)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return v.searchTermsLocked(terms, args, df, total, limit, false)
	}
	return hits, nil
}

func (v *Vault) searchTermsLocked(terms []string, args []any, df map[string]int, total, limit int, and bool) ([]SearchHit, error) {
	ph := placeholders(len(terms))
	having := ""
	if and {
		having = ` HAVING COUNT(DISTINCT term)=` + itoa(len(terms))
	}
	rows, err := v.db.Query(`SELECT note_key,SUM(tf) FROM note_terms WHERE term IN (`+ph+`) GROUP BY note_key`+having, args...)
	if err != nil {
		return nil, err
	}
	type stat struct {
		key string
		tf  int
	}
	var stats []stat
	for rows.Next() {
		var s stat
		if err := rows.Scan(&s.key, &s.tf); err != nil {
			rows.Close()
			return nil, err
		}
		stats = append(stats, s)
	}
	rows.Close()
	if len(stats) == 0 {
		return []SearchHit{}, nil
	}

	out := make([]SearchHit, 0, len(stats))
	for _, s := range stats {
		var title, p, content, aliasesJSON string
		if err := v.db.QueryRow(`SELECT title,path,content,aliases FROM notes WHERE key=?`, s.key).
			Scan(&title, &p, &content, &aliasesJSON); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return nil, err
		}
		score := scoreOf(s.key, terms, df, total, s.tf, title, aliasesJSON)
		out = append(out, SearchHit{
			Key: s.key, Path: p, Title: title, Score: score,
			Excerpt: around(content, terms, 160), Terms: terms,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Title < out[j].Title
	})
	if len(out) > limit {
		out = out[:limit]
	}
	// 分数保留 3 位，免得 JSON 里全是长小数
	for i := range out {
		out[i].Score = math.Round(out[i].Score*1000) / 1000
	}
	return out, nil
}

// scoreOf 简化 BM25：词频 + idf，标题/别名整词命中额外加成。
func scoreOf(key string, terms []string, df map[string]int, total, tf int, title, aliasesJSON string) float64 {
	score := 0.0
	for _, t := range terms {
		weight := math.Log(1 + float64(total)/float64(df[t]+1))
		score += weight
	}
	score *= 1 + math.Log(1+float64(tf))
	lowTitle := strings.ToLower(title)
	lowAlias := strings.ToLower(aliasesJSON)
	for _, t := range terms {
		if strings.Contains(lowTitle, t) {
			score += 2
		}
		if strings.Contains(lowAlias, t) {
			score += 1
		}
	}
	return score
}

// around 从正文里截一段包含命中词的摘要
func around(content string, terms []string, maxRunes int) string {
	low := strings.ToLower(content)
	best := -1
	for _, t := range terms {
		if i := strings.Index(low, t); i >= 0 {
			if best < 0 || i < best {
				best = i
			}
		}
	}
	if best < 0 {
		return excerpt(content, maxRunes)
	}
	// 往前留一点上下文（按 rune 算）
	r := []rune(content)
	runeIdx := len([]rune(content[:best]))
	start := runeIdx - 30
	if start < 0 {
		start = 0
	}
	end := start + maxRunes
	if end > len(r) {
		end = len(r)
	}
	seg := strings.TrimSpace(string(r[start:end]))
	return strings.Join(strings.Fields(seg), " ")
}

// likeFallbackLocked 索引还没建好（或查询全是单字被过滤）时的兜底
func (v *Vault) likeFallbackLocked(q string, limit int) ([]SearchHit, error) {
	like := "%" + strings.TrimSpace(q) + "%"
	rows, err := v.db.Query(`SELECT key,path,title,content FROM notes
		WHERE title LIKE ? OR content LIKE ? ORDER BY updated_at DESC LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SearchHit{}
	for rows.Next() {
		var h SearchHit
		var content string
		if err := rows.Scan(&h.Key, &h.Path, &h.Title, &content); err != nil {
			return nil, err
		}
		h.Score = 1
		h.Excerpt = excerpt(content, 160)
		h.Terms = []string{}
		out = append(out, h)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return "''"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
