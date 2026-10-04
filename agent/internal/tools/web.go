package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// WebFetch 抓取网页/接口文本（原生工具里的"浏览器/网络"最小形态）
type WebFetch struct {
	client *http.Client
}

// NewWebFetch 创建 web_fetch
func NewWebFetch() *WebFetch {
	return &WebFetch{client: &http.Client{Timeout: 30 * time.Second}}
}

// Name 工具名
func (t *WebFetch) Name() string { return "web_fetch" }

// Description 说明
func (t *WebFetch) Description() string {
	return "抓取一个 http/https 地址的文本内容（自动去掉 HTML 标签与脚本样式），用于查资料"
}

// Schema 参数说明
func (t *WebFetch) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":      map[string]any{"type": "string", "description": "完整地址（http/https）"},
			"maxBytes": map[string]any{"type": "integer", "description": "最多读取字节数，默认 262144"},
		},
		"required": []string{"url"},
	}
}

// Run 执行
func (t *WebFetch) Run(ctx context.Context, args map[string]any) (any, error) {
	raw := ArgString(args, "url")
	if raw == "" {
		return nil, errors.New("url 不能为空")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("地址不合法：%w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("只支持 http/https，收到：%s", u.Scheme)
	}
	max := ArgInt(args, "maxBytes", 256*1024)
	if max <= 0 {
		max = 256 * 1024
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "baize-agent/0.1 (+local)")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(max)+1))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败：%w", err)
	}
	truncated := len(body) > max
	if truncated {
		body = body[:max]
	}
	text := string(body)
	ctype := resp.Header.Get("Content-Type")
	if strings.Contains(strings.ToLower(ctype), "html") || strings.HasPrefix(strings.TrimSpace(text), "<") {
		text = htmlToText(text)
	}
	return map[string]any{
		"url":         u.String(),
		"status":      resp.StatusCode,
		"contentType": ctype,
		"truncated":   truncated,
		"text":        text,
	}, nil
}

var (
	// Go 的 regexp（RE2）不支持反向引用，所以三种标签各写一条分支
	scriptStyleRe = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<noscript[^>]*>.*?</noscript>`)
	tagRe         = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRe       = regexp.MustCompile(`[ \t\x{00a0}]+`)
	blankLineRe   = regexp.MustCompile(`\n{3,}`)
)

// htmlToText 粗暴但够用的 HTML 转文本
func htmlToText(s string) string {
	s = scriptStyleRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, "\n")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	lines := strings.Split(s, "\n")
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(spaceRe.ReplaceAllString(line, " "))
		if line != "" {
			clean = append(clean, line)
		}
	}
	out := strings.Join(clean, "\n")
	out = blankLineRe.ReplaceAllString(out, "\n\n")
	if len(out) > 200000 {
		out = out[:200000] + "\n…（内容过长已截断）"
	}
	return strings.TrimSpace(out)
}
