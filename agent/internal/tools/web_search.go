package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// WebSearch 联网搜索（只用真实搜索通道，绝不自造结果）。
//
// 为什么不做成"默认能用"：白泽是私有部署，跑在用户自己的 NAS 上，没有一个通用且合法的
// 免密钥搜索接口能被无条件依赖。所以这里**只认显式配置的通道**：
//
//	searxng    —— 自建/公共 SearXNG 实例的 JSON 接口（推荐，不依赖第三方账号）
//	duckduckgo —— 抓 DuckDuckGo 的 HTML 结果页（无需密钥，但可能被限流/被墙）
//
// 没配 provider 时工具会明确告诉用户「去哪儿配」，而不是返回空结果或伪造结果。
type WebSearch struct {
	cfg    WebSearchConfig
	client *http.Client
}

// WebSearchConfig 搜索通道配置
type WebSearchConfig struct {
	Provider   string // searxng | duckduckgo；空 = 没配（工具会明确报错）
	BaseURL    string // searxng 实例地址，如 http://192.168.1.10:8080
	APIKey     string // searxng 可选（实例开了鉴权时用）
	TimeoutSec int
	MaxResults int
}

// NewWebSearch 创建 web_search
func NewWebSearch(cfg WebSearchConfig) *WebSearch {
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 20
	}
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = 8
	}
	return &WebSearch{
		cfg:    cfg,
		client: &http.Client{Timeout: time.Duration(cfg.TimeoutSec) * time.Second},
	}
}

// Name 工具名
func (t *WebSearch) Name() string { return "web_search" }

// Description 说明
func (t *WebSearch) Description() string {
	return "联网搜索：给关键词，返回标题 + 链接 + 摘要（需要先在配置里指定搜索通道；没配会明确报错）。" +
		"要读某个结果的正文再用 web_fetch / browser。"
}

// Schema 参数说明
func (t *WebSearch) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "搜索关键词"},
			"limit": map[string]any{"type": "integer", "description": "返回条数，默认用配置里的值"},
			"lang":  map[string]any{"type": "string", "description": "语言偏好，如 zh-CN / en；留空不指定"},
		},
		"required": []string{"query"},
	}
}

// WebSearchResult 一条搜索结果
type WebSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Source  string `json:"source,omitempty"`
}

// Run 执行
func (t *WebSearch) Run(ctx context.Context, args map[string]any) (any, error) {
	query := ArgString(args, "query")
	if query == "" {
		return nil, errors.New("query 不能为空")
	}
	provider := strings.ToLower(strings.TrimSpace(t.cfg.Provider))
	if provider == "" {
		return nil, errors.New("还没配搜索通道：在 config.json 的 search 里指定 provider（searxng 或 duckduckgo），" +
			"searxng 还要填 baseUrl（如 http://192.168.1.10:8080）；配好之前不能联网搜索")
	}
	limit := ArgInt(args, "limit", t.cfg.MaxResults)
	if limit <= 0 {
		limit = t.cfg.MaxResults
	}
	lang := ArgString(args, "lang")

	var results []WebSearchResult
	var err error
	switch provider {
	case "searxng":
		results, err = t.searxng(ctx, query, lang, limit)
	case "duckduckgo", "ddg":
		results, err = t.duckduckgo(ctx, query, limit)
	default:
		return nil, fmt.Errorf("不支持的搜索通道 %q（可用：searxng、duckduckgo）", provider)
	}
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"provider": provider, "query": query, "count": len(results), "results": results,
	}
	if len(results) == 0 {
		out["note"] = "通道通了但没返回结果：换个关键词，或检查实例本身能不能搜"
	}
	return out, nil
}

// searxng 走 SearXNG 的 JSON 接口（format=json，需要实例允许 JSON 输出）
func (t *WebSearch) searxng(ctx context.Context, query, lang string, limit int) ([]WebSearchResult, error) {
	base := strings.TrimRight(strings.TrimSpace(t.cfg.BaseURL), "/")
	if base == "" {
		return nil, errors.New("searxng 通道需要 baseUrl（实例地址，如 http://192.168.1.10:8080）")
	}
	u, err := url.Parse(base + "/search")
	if err != nil {
		return nil, fmt.Errorf("searxng 地址不合法：%w", err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	if lang != "" {
		q.Set("language", lang)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "baize-agent/0.1 (+local)")
	if t.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.cfg.APIKey)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 SearXNG（%s）：%w", base, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		hint := ""
		if resp.StatusCode == http.StatusForbidden {
			hint = "；SearXNG 默认不开 JSON 输出，需要在 settings.yml 的 search.formats 里加 json"
		}
		return nil, fmt.Errorf("SearXNG 返回 HTTP %d%s", resp.StatusCode, hint)
	}
	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
			Engine  string `json:"engine"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("SearXNG 返回的不是 JSON（%v）；确认实例开启了 json 格式输出", err)
	}
	out := []WebSearchResult{}
	for _, r := range parsed.Results {
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		out = append(out, WebSearchResult{
			Title: strings.TrimSpace(r.Title), URL: strings.TrimSpace(r.URL),
			Snippet: strings.TrimSpace(r.Content), Source: strings.TrimSpace(r.Engine),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

var (
	ddgResultRe  = regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRe = regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
)

// duckduckgo 抓 HTML 结果页（无需密钥；可能被限流或网络不可达，失败就如实报错）
func (t *WebSearch) duckduckgo(ctx context.Context, query string, limit int) ([]WebSearchResult, error) {
	form := url.Values{}
	form.Set("q", query)
	form.Set("kl", "wt-wt")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://html.duckduckgo.com/html/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) baize-agent/0.1")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 DuckDuckGo（本机可能无法访问外网）：%w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DuckDuckGo 返回 HTTP %d（可能被限流）", resp.StatusCode)
	}
	html := string(body)
	links := ddgResultRe.FindAllStringSubmatch(html, -1)
	snips := ddgSnippetRe.FindAllStringSubmatch(html, -1)
	titleRe := regexp.MustCompile(`(?s)<[^>]+>`)
	out := []WebSearchResult{}
	for i, m := range links {
		href := decodeDDGURL(m[1])
		title := strings.TrimSpace(titleRe.ReplaceAllString(m[2], ""))
		if href == "" || title == "" {
			continue
		}
		r := WebSearchResult{Title: decodeHTMLEntities(title), URL: href, Source: "duckduckgo"}
		if i < len(snips) {
			r.Snippet = decodeHTMLEntities(strings.TrimSpace(titleRe.ReplaceAllString(snips[i][1], "")))
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// decodeDDGURL DDG 的跳转链接是 //duckduckgo.com/l/?uddg=<编码后的真实地址>，要还原
func decodeDDGURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if real := u.Query().Get("uddg"); real != "" {
		return real
	}
	return raw
}

func decodeHTMLEntities(s string) string {
	rep := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ",
		"&#x27;", "'", "&#x2F;", "/",
	)
	return strings.TrimSpace(rep.Replace(s))
}

// RegisterWebSearch 注册 web_search（没配通道也注册：调用时会明确告诉用户去哪儿配）
func RegisterWebSearch(r *Registry, cfg WebSearchConfig) {
	r.Register(NewWebSearch(cfg))
}
