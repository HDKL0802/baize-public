package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// defaultHTTPTimeout 拉索引/插件包的单次超时。插件包可能有点大，给宽一点；但不能没有上限
// （没有超时的 http.Client 会一直挂着，界面就永远停在"加载中"）。
const defaultHTTPTimeout = 60 * time.Second

func newHTTPClient() *http.Client { return &http.Client{Timeout: defaultHTTPTimeout} }

// resolveRef 把"相对引用"解析成绝对地址/路径：
//   - base 是 http(s) 且 ref 是相对 → 用 URL 语义解析（保留 host 与目录）；
//   - 否则当成文件系统：ref 相对 base 所在目录解析（base 一般就是那个 index.json 的路径）。
//
// 相对引用的意义：整套插件源（index.json + 一堆 zip）能整体搬家，换域名/换目录都不用改内容。
func resolveRef(base, ref string) string {
	ref = strings.TrimSpace(ref)
	base = strings.TrimSpace(base)
	if ref == "" {
		return ""
	}
	// ref 本身就是绝对 http(s) 地址 → 原样用。**必须先判这一步**：
	// 否则会走到"当成本地路径"的分支，把 http:// 里的 / 换成分隔符（Windows 上变反斜杠），
	// 请求 URL 直接烂掉。
	if u, err := url.Parse(ref); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return ref
	}
	if bu, err := url.Parse(base); err == nil && (bu.Scheme == "http" || bu.Scheme == "https") {
		if ru, err := url.Parse(ref); err == nil {
			return bu.ResolveReference(ru).String()
		}
		return ref
	}
	p := filepath.FromSlash(ref)
	if filepath.IsAbs(p) {
		return p
	}
	if base == "" {
		return p
	}
	return filepath.Join(filepath.Dir(base), p)
}

// isRemote 是不是 http(s) 地址
func isRemote(ref string) bool {
	u, err := url.Parse(strings.TrimSpace(ref))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}

// readRef 读一个地址：http(s) 走网络，其余当本地文件。返回内容和"最终解析到的地址"。
func readRef(ctx context.Context, hc *http.Client, ref, base string, limit int64) ([]byte, string, error) {
	target := resolveRef(base, ref)
	if target == "" {
		return nil, "", fmt.Errorf("地址为空")
	}
	if isRemote(target) {
		if hc == nil {
			hc = newHTTPClient()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, target, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, target, fmt.Errorf("下载失败（%s）：%w", target, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, target, fmt.Errorf("下载失败（%s）：HTTP %d", target, resp.StatusCode)
		}
		if limit <= 0 {
			limit = maxPackageBytes
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, target, fmt.Errorf("读取响应失败（%s）：%w", target, err)
		}
		if int64(len(data)) > limit {
			return nil, target, fmt.Errorf("内容超过上限（%d 字节）：%s", limit, target)
		}
		return data, target, nil
	}
	local := strings.TrimPrefix(target, "file://")
	fi, err := os.Stat(local)
	if err != nil {
		return nil, local, fmt.Errorf("读不到本地文件（%s）：%w", local, err)
	}
	if fi.IsDir() {
		return nil, local, fmt.Errorf("这是个目录，不是文件：%s", local)
	}
	data, err := os.ReadFile(local)
	if err != nil {
		return nil, local, fmt.Errorf("读取失败（%s）：%w", local, err)
	}
	return data, local, nil
}

// fetchIndex 拉一个源的索引。第二个返回值是"索引真实的地址"，用于解析包里的相对 url。
func fetchIndex(ctx context.Context, hc *http.Client, src Source) (Index, string, error) {
	raw, at, err := readRef(ctx, hc, src.URL, "", maxPackageBytes)
	if err != nil {
		return Index{}, at, err
	}
	var idx Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return Index{}, at, fmt.Errorf("索引不是合法 JSON（%s）：%w", at, err)
	}
	if idx.Schema != 0 && idx.Schema != SchemaVersion {
		return Index{}, at, fmt.Errorf("索引格式版本 %d 不认识（本机支持 %d）：%s", idx.Schema, SchemaVersion, at)
	}
	return normalizeIndex(idx), at, nil
}

// sha256Hex 算十六进制摘要
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// verifySHA256 校验摘要。want 为空 = 源没提供，返回 (false, nil)：调用方据此如实写"未校验"。
func verifySHA256(data []byte, want string) (bool, error) {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" {
		return false, nil
	}
	got := sha256Hex(data)
	if got != want {
		return true, fmt.Errorf("sha256 校验不通过：期望 %s，实际 %s（包可能在传输中损坏或被改过，已拒绝安装）", want, got)
	}
	return true, nil
}
