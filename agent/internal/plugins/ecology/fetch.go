// Package ecology 第二部分：解析 DSH 市场索引、判定四类落地方式。
//
// 见 ecology.go 的包注释：这里转译的是内容，不是包；
// 纯运行时插件（cordis）只进目录、不进安装列表，不假装能装。
package ecology

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ---------- 拉索引 ----------

// FetchTimeout 拉一次索引的超时。
const FetchTimeout = 30 * time.Second

// maxIndexBytes 索引文档上限。DSH 的索引可能很大，给宽一点但仍有上限。
const maxIndexBytes = 32 << 20

// Fetch 拉一份 DSH 市场索引并解析成目录条目。
//
// indexURL 可以是 http(s) 地址，也可以是本机路径（离线用）。
// 返回的 Catalog 里每个条目都带 Kind 与 Installable —— 不能装的如实标出来。
func Fetch(indexURL string, probe Probe) (*Catalog, error) {
	cat := &Catalog{Source: indexURL}
	if probe == nil {
		probe = ProbeRepo
	}
	raw, err := fetchBytes(indexURL)
	if err != nil {
		return nil, err
	}
	var idx dshIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("索引不是合法 JSON：%w", err)
	}
	if idx.Format != 0 && idx.Format != 1 {
		return nil, fmt.Errorf("索引格式 %d 不认识（本包支持格式 1）", idx.Format)
	}
	cat.Title = idx.Title.String()
	cat.Updated = time.Now().UTC().Format(time.RFC3339)
	seen := map[string]bool{}
	for _, p := range idx.Plugins {
		e, err := convert(p, probe)
		if err != nil {
			if len(cat.Errors) < 50 {
				cat.Errors = append(cat.Errors, err.Error())
			}
			continue
		}
		if e == nil || e.ID == "" || seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		cat.Entries = append(cat.Entries, *e)
	}
	sort.Slice(cat.Entries, func(i, j int) bool {
		if cat.Entries[i].Kind != cat.Entries[j].Kind {
			return cat.Entries[i].Kind < cat.Entries[j].Kind
		}
		return cat.Entries[i].ID < cat.Entries[j].ID
	})
	cat.Total = len(cat.Entries)
	for _, e := range cat.Entries {
		if e.Installable {
			cat.Installable++
		} else {
			cat.Directory++
		}
	}
	return cat, nil
}

// fetchBytes 读一个地址（http(s) 走网络，本地路径直接读）。
func fetchBytes(ref string) ([]byte, error) {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		client := &http.Client{Timeout: FetchTimeout}
		resp, err := client.Get(ref)
		if err != nil {
			return nil, fmt.Errorf("下载索引失败（%s）：%w", ref, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("下载索引失败（%s）：HTTP %d", ref, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes+1))
		if err != nil {
			return nil, fmt.Errorf("读索引失败（%s）：%w", ref, err)
		}
		if int64(len(data)) > maxIndexBytes {
			return nil, fmt.Errorf("索引超过上限（%d 字节）", maxIndexBytes)
		}
		return data, nil
	}
	data, err := os.ReadFile(ref)
	if err != nil {
		return nil, fmt.Errorf("读不到索引（%s）：%w", ref, err)
	}
	return data, nil
}

// ---------- 一条 DSH 插件 → 一个生态条目 ----------

// convert 把一条 DSH 插件转成生态条目；判定内容形态（技能/工具/运行时/未知）。
func convert(p dshPlugin, probe Probe) (*Entry, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return nil, fmt.Errorf("有条插件没有 name")
	}
	if len(p.Versions) == 0 {
		return nil, fmt.Errorf("%s：没有可安装版本", name)
	}
	v := p.Versions[0]
	e := &Entry{
		ID:          EcosystemID(name),
		Name:        firstNonEmpty(p.Title.String(), name),
		Version:     strings.TrimSpace(v.Version),
		Description: p.Description.String(),
		Author:      strings.TrimSpace(p.Author),
		Homepage:    strings.TrimSpace(p.Homepage),
		Category:    strings.TrimSpace(p.Metadata.Category),
		Tags:        append(append([]string{}, p.Metadata.Keywords...), p.Metadata.Labels...),
	}
	if p.Repository != nil {
		e.Repository = strings.TrimSpace(p.Repository.URL)
	}
	if e.Repository == "" {
		e.Repository = strings.TrimSpace(p.Homepage)
	}
	// DSH 生态插件一律社区板块 —— 不替第三方宣称官方。
	e.Kind = KindUnknown
	if probe == nil {
		// 没开探测：只按索引元数据列目录，不猜形态。
		e.Reason = "没有探测内容形态（这次是纯目录模式）。开探测后会去仓库里看有没有 SKILL.md 或 MCP 配置，再判断能不能装。"
	}
	if probe != nil {
		info, err := probe(name, v, e.Repository)
		if err != nil {
			// 探测失败 ≠ 确认不能装。这两种情况在界面上必须说得清，
			// 否则用户会把「暂时读不到仓库」误当成「这插件白泽不支持」。
			e.Kind = KindUnknown
			e.Reason = "没能探测到内容形态（" + err.Error() + "）。这不代表它一定装不了 —— 只是现在还说不准；要确认请到它的仓库里看有没有 SKILL.md 或 MCP 配置。"
		} else if info != nil {
			e.Kind = info.Kind
			e.SkillPath = info.SkillPath
			e.MCP = info.MCP
			e.License = info.License
			if info.Kind == KindRuntime {
				e.Reason = "这是 cordis 运行时插件（界面/钩子），白泽没有 cordis 宿主，装了不会运行；这里只作目录展示。"
			} else if info.Kind == KindUnknown {
				e.Reason = "看不出这个插件的内容形态（既没有技能，也没有可注册的 MCP 工具），这里只作目录展示。"
			}
		}
	}
	e.Installable = e.Kind == KindSkill || e.Kind == KindTool
	if e.Installable {
		e.Reason = ""
	}
	e.InstallSpec = installSpec(name, v)
	return e, nil
}

// installSpec 生成生态侧的安装说明（仅供界面展示，白泽不执行）。
func installSpec(name string, v dshVersion) string {
	switch strings.ToLower(strings.TrimSpace(v.Source.Type)) {
	case "npm":
		if reg := strings.TrimSpace(v.Source.Registry); reg != "" {
			return fmt.Sprintf("pnpm add %s@%s --registry %s", name, v.Version, reg)
		}
		return fmt.Sprintf("pnpm add %s@%s", name, v.Version)
	case "git":
		return fmt.Sprintf("pnpm add git+%s#%s", v.Source.URL, v.Source.Commit)
	}
	return ""
}

// ---------- id 归一 ----------

// idRe 白泽插件 id 规则（与 plugins 包一致：只能用小写字母、数字、连字符、点和下划线）。
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// EcosystemID 把生态侧的包名归一成白泽合法的插件 id。
//
// 例：weibaohui/dsh-fireworks 变成 weibaohui.dsh-fireworks；dsh-meme 保持不变。
func EcosystemID(name string) string {
	s := strings.TrimSpace(name)
	s = strings.TrimPrefix(s, "@")
	s = strings.ReplaceAll(s, "/", ".")
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	outStr := strings.Trim(b.String(), ".-_")
	if outStr == "" {
		return ""
	}
	if len(outStr) > 64 {
		outStr = outStr[:64]
	}
	return outStr
}

// ---------- 小工具 ----------

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// hashHex 算十六进制摘要（打包时用）。
func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
