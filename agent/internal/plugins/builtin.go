package plugins

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"
)

// 内置官方源：随后端二进制一起分发（go:embed），所以**不需要任何外部托管**就能开箱用，
// 也天生离线可用。它落在数据目录之外，因此不写进 config —— 换机器、换数据目录都不会失效。
//
// 与远端源的**唯一差别只在"从哪儿取字节"**：索引与插件包都在内存里现场生成，
// 之后走的还是同一条路（下载 → 校验 sha256 → 解包 → 落技能目录 + 登记 MCP）。
// 换句话说：内置源是一份"格式完全合规的示例源"，模板仓里要发布的那种源与它同构。
const (
	// BuiltinSourceURL 内置官方源的地址（索引）
	BuiltinSourceURL = "builtin:official"
	// BuiltinSourceName 内置官方源在界面上的显示名
	BuiltinSourceName = "官方插件源（内置）"
	builtinPkgPrefix  = "builtin:official/"
)

//go:embed official
var officialFS embed.FS

// IsBuiltinSource 这个地址是不是内置源（或它的插件包）
func IsBuiltinSource(ref string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), "builtin:")
}

// builtinZipTime 打包时写死的修改时间。同一份内容每次打出来的字节因此完全一致，
// sha256 也就稳定 —— 内置源才能走上"真校验"这条路径，而不是"自己人所以免检"。
var builtinZipTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var (
	builtinOnce sync.Once
	builtinIdx  Index
	builtinPkgs map[string][]byte
	builtinErr  error
)

// loadBuiltin 扫一遍内嵌目录：每个 <id>/ 一份插件，读清单、打 zip、算摘要。
// 任何一处不对（清单缺字段、目录名与 id 对不上、JSON 坏了）都**整源报错**，
// 而不是悄悄少一个插件 —— 内置内容是随二进制发的，出错就是发布事故，必须吵出来。
func loadBuiltin() {
	builtinPkgs = map[string][]byte{}
	entries, err := fs.ReadDir(officialFS, "official")
	if err != nil {
		builtinErr = fmt.Errorf("内置官方源读不到（二进制里没有 official/）：%w", err)
		return
	}
	ids := []string{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		id := e.Name()
		if !ValidID(id) {
			builtinErr = fmt.Errorf("内置官方源里有个目录名不合法：%q（%s）", id, idRule)
			return
		}
		if !fileExistsInFS(officialFS, "official/"+id+"/plugin.json") {
			builtinErr = fmt.Errorf("内置插件 %s 没有 plugin.json（每个插件目录都得有清单）", id)
			return
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	idx := Index{Schema: SchemaVersion, Name: BuiltinSourceName, Plugins: []Meta{}}
	for _, id := range ids {
		raw, err := fs.ReadFile(officialFS, "official/"+id+"/plugin.json")
		if err != nil {
			builtinErr = fmt.Errorf("内置插件 %s 的 plugin.json 读不到：%w", id, err)
			return
		}
		var m Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			builtinErr = fmt.Errorf("内置插件 %s 的 plugin.json 不是合法 JSON：%w", id, err)
			return
		}
		if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Version) == "" {
			builtinErr = fmt.Errorf("内置插件 %s 的清单缺少 name 或 version（内置内容必须写全）", id)
			return
		}
		norm, err := normalizeManifest(m)
		if err != nil {
			builtinErr = fmt.Errorf("内置插件 %s 的清单不合法：%w", id, err)
			return
		}
		if norm.ID != id {
			builtinErr = fmt.Errorf("内置插件目录名 %s 与清单里的 id %q 对不上", id, norm.ID)
			return
		}
		pkg, err := zipEmbedDir("official/" + id)
		if err != nil {
			builtinErr = fmt.Errorf("打包内置插件 %s 失败：%w", id, err)
			return
		}
		sum := sha256.Sum256(pkg)
		builtinPkgs[id] = pkg
		idx.Plugins = append(idx.Plugins, Meta{
			ID: norm.ID, Name: norm.Name, Version: norm.Version,
			Description: norm.Description, Author: norm.Author, Homepage: norm.Homepage,
			License: norm.License, Tags: norm.Tags, MinBaize: norm.MinBaize,
			URL: builtinPkgPrefix + id + ".zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(pkg)),
		})
	}
	builtinIdx = idx
}

// readBuiltin 读内置源的内容：索引，或某个插件的 zip 包。
// 第二个返回值是"解析到的地址"，与 readRef 的约定一致（内置地址原样回给调用方）。
func readBuiltin(ref string) ([]byte, string, error) {
	builtinOnce.Do(loadBuiltin)
	if builtinErr != nil {
		return nil, ref, builtinErr
	}
	ref = strings.TrimSpace(ref)
	if ref == strings.TrimSuffix(BuiltinSourceURL, "/") {
		raw, err := json.MarshalIndent(builtinIdx, "", "  ")
		return raw, ref, err
	}
	if rest, ok := strings.CutPrefix(ref, builtinPkgPrefix); ok {
		id := strings.TrimSuffix(rest, ".zip")
		pkg, ok := builtinPkgs[id]
		if !ok {
			return nil, ref, fmt.Errorf("内置官方源里没有这个插件包：%s", ref)
		}
		return pkg, ref, nil
	}
	return nil, ref, fmt.Errorf("不认识的内置地址：%s（可用的是 %s）", ref, BuiltinSourceURL)
}

// builtinIDs 内置源的插件 id（排好序；出错返回 nil）
func builtinIDs() []string {
	builtinOnce.Do(loadBuiltin)
	if builtinErr != nil {
		return nil
	}
	out := make([]string, 0, len(builtinIdx.Plugins))
	for _, m := range builtinIdx.Plugins {
		out = append(out, m.ID)
	}
	return out
}

// builtinIndex 内置源的索引（出错返回零值 + 错误）
func builtinIndex() (Index, error) {
	builtinOnce.Do(loadBuiltin)
	if builtinErr != nil {
		return Index{}, builtinErr
	}
	return builtinIdx, nil
}

// zipEmbedDir 把一个内嵌目录打成 zip。条目名用正斜杠、时间戳写死，
// 因此结果与平台无关、且可复现。
func zipEmbedDir(root string) ([]byte, error) {
	names := []string{}
	if err := fs.WalkDir(officialFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		names = append(names, p)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range names {
		rel := strings.TrimPrefix(p, root+"/")
		if rel == "" || rel == p {
			return nil, fmt.Errorf("内置包里的路径不对：%s", p)
		}
		data, err := fs.ReadFile(officialFS, p)
		if err != nil {
			return nil, err
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: rel, Method: zip.Deflate, Modified: builtinZipTime})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fileExistsInFS 内嵌文件系统里这个文件在不在
func fileExistsInFS(fsys fs.FS, p string) bool {
	fi, err := fs.Stat(fsys, p)
	return err == nil && !fi.IsDir()
}

// OfficialFS 内置官方源的只读文件系统（给测试与将来的"发布脚本"复用）
func OfficialFS() fs.FS { return officialFS }
