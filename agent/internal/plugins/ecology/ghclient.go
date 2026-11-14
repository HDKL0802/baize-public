// Package ecology 第四部分：GitHub / GitLab 公开仓库的只读探测客户端。
//
// 只读公开内容（不 clone、不执行第三方代码）；访问不到就如实返回错误。
package ecology

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const ghTimeout = 20 * time.Second

// ghRepoInfo 拆出来的一个仓库。
type ghRepoInfo struct {
	owner string
	repo string
	ref  string
	api  string
}

// githubRepo 从仓库网页地址里解析出 owner/repo/ref。
func githubRepo(repo string) (ghRepoInfo, bool) {
	s := strings.TrimSpace(repo)
	if s == "" {
		return ghRepoInfo{}, false
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return ghRepoInfo{}, false
	}
	u, err := url.Parse(s)
	if err != nil {
		return ghRepoInfo{}, false
	}
	host := strings.ToLower(u.Host)
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ghRepoInfo{}, false
	}
	info := ghRepoInfo{owner: parts[0], repo: parts[1], ref: "HEAD"}
	switch host {
	case "github.com", "www.github.com":
		info.api = "https://api.github.com/repos/"
	case "gitlab.com", "www.gitlab.com":
		info.api = "https://gitlab.com/api/v4/projects/"
	default:
		return ghRepoInfo{}, false
	}
	return info, true
}

// ghClient 一个极简的公开仓库读取器。
type ghClient struct {
	info ghRepoInfo
	http *http.Client
}

// newGH 造一个读取器。
func newGH(info ghRepoInfo) *ghClient {
	return &ghClient{info: info, http: &http.Client{Timeout: ghTimeout}}
}

// ghFile 仓库里的一个文件。
type ghFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// listFiles 列一个目录（浅一层）。
func (c *ghClient) listFiles(owner, repo, ref, dir string) ([]ghFile, error) {
	u := c.info.api + owner + "/" + repo + "/contents/"
	if dir != "" {
		u += dir
	}
	if ref != "" && ref != "HEAD" {
		u += "?ref=" + url.QueryEscape(ref)
	}
	body, err := c.get(u)
	if err != nil {
		return nil, err
	}
	var files []ghFile
	if err := json.Unmarshal(body, &files); err != nil {
		return nil, fmt.Errorf("仓库内容不是合法 JSON：%w", err)
	}
	return files, nil
}

// findFile 在一个目录里找一个文件名（不递归 —— 生态插件的 SKILL.md 通常就在包根或一层子目录）。
func (c *ghClient) findFile(owner, repo, ref, dir, name string) (string, error) {
	files, err := c.listFiles(owner, repo, ref, dir)
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if f.Type == "file" && strings.EqualFold(f.Name, name) {
			return f.Path, nil
		}
	}
	return "", fmt.Errorf("目录里没有 %s", name)
}

// readFile 读一个文件的内容。
func (c *ghClient) readFile(owner, repo, ref, p string) ([]byte, error) {
	u := c.info.api + owner + "/" + repo + "/contents/" + p
	if ref != "" && ref != "HEAD" {
		u += "?ref=" + url.QueryEscape(ref)
	}
	body, err := c.get(u)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("文件内容不是合法 JSON：%w", err)
	}
	if doc.Encoding != "base64" {
		return nil, fmt.Errorf("文件编码 %q 不认识", doc.Encoding)
	}
	return base64Decode(doc.Content)
}

// get 走一次 HTTP GET。
func (c *ghClient) get(u string) ([]byte, error) {
	resp, err := c.http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// base64Decode 解 base64（忽略换行）。
func base64Decode(s string) ([]byte, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", "")
	return base64.StdEncoding.DecodeString(s)
}

// pathJoin 拼仓库内路径。
func pathJoin(dir, name string) string {
	if dir == "" {
		return name
	}
	return path.Join(dir, name)
}

// ---------- 打包用的内容读取器 ----------

// GitHubSkillReader 从生态仓里读 SKILL.md 原文，供打包塞进白泽技能目录。
//
// 读不到就如实返回错误 —— 打包那一步会把它记进 errors，不会假装装成功。
func GitHubSkillReader(e Entry) (string, error) {
	info, ok := githubRepo(e.Repository)
	if !ok {
		return "", fmt.Errorf("仓库地址不是 GitHub/GitLab 形态：%s", e.Repository)
	}
	c := newGH(info)
	dir := ""
	name := strings.TrimSpace(e.SkillPath)
	if name == "" {
		name = "SKILL.md"
	}
	p, err := c.findFile(info.owner, info.repo, info.ref, dir, name)
	if err != nil {
		return "", fmt.Errorf("找不到 %s：%w", name, err)
	}
	raw, err := c.readFile(info.owner, info.repo, info.ref, p)
	if err != nil {
		return "", fmt.Errorf("读 %s 失败：%w", name, err)
	}
	return string(raw), nil
}

// GitHubMCPReader 从生态仓里读 MCP 配置原文（给「再读一份原文核对」用）。
//
// 读不到如实报错。
func GitHubMCPReader(e Entry) ([]byte, error) {
	info, ok := githubRepo(e.Repository)
	if !ok {
		return nil, fmt.Errorf("仓库地址不是 GitHub/GitLab 形态：%s", e.Repository)
	}
	c := newGH(info)
	for _, n := range []string{"mcp.json", ".mcp.json"} {
		p, err := c.findFile(info.owner, info.repo, info.ref, "", n)
		if err != nil || p == "" {
			continue
		}
		raw, err := c.readFile(info.owner, info.repo, info.ref, p)
		if err != nil {
			continue
		}
		return raw, nil
	}
	return nil, fmt.Errorf("这个插件里没有 MCP 配置")
}
