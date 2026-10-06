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
// 内嵌目录结构与线上插件仓（`baize-plugins`）**同一套规矩**：
//
//	official/<阶段 stable|beta>/<分类>/<插件id>/plugin.json
//
// 内置源整份都是官方，所以不需要 community 那一层。与远端源的**唯一差别只在"从哪儿取字节"**：
// 索引与插件包都在内存里现场生成，之后走的还是同一条路（下载 → 校验 sha256 → 解包 →
// 落技能目录 + 登记 MCP）。换句话说：内置源是一份"格式完全合规的示例源"。
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

// builtinEntry 内嵌源里找到的一个插件（阶段/分类由目录决定）
type builtinEntry struct {
	stage    string
	category string
	id       string
	root     string // "official/<阶段>/<分类>/<id>"
}

// loadBuiltin 扫一遍内嵌目录：目录结构决定"阶段/分类"，清单决定其余元数据。
//
// 任何一处不对（层级不对、阶段目录不认识、清单缺字段、目录名与 id 对不上、JSON 坏了）都
// **整源报错**，而不是悄悄少一个插件 —— 内置内容是随二进制发的，出错就是发布事故，必须吵出来。
func loadBuiltin() {
	builtinPkgs = map[string][]byte{}

	found := []builtinEntry{}
	err := fs.WalkDir(officialFS, "official", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(d.Name(), "plugin.json") {
			return nil
		}
		parts := strings.Split(strings.TrimPrefix(p, "official/"), "/")
		if len(parts) != 4 {
			return fmt.Errorf("内置插件的目录必须是 official/<阶段>/<分类>/<插件id>/plugin.json，收到：%s", p)
		}
		stage, category, id := parts[0], parts[1], parts[2]
		if stage != StageStable && stage != StageBeta {
			return fmt.Errorf("%s：阶段目录只能是 %s 或 %s，收到 %q", p, StageStable, StageBeta, stage)
		}
		if strings.TrimSpace(category) == "" || strings.HasPrefix(category, ".") {
			return fmt.Errorf("%s：分类目录名不合法", p)
		}
		if !ValidID(id) {
			return fmt.Errorf("%s：插件目录名 %q 不合法（%s）", p, id, idRule)
		}
		found = append(found, builtinEntry{
			stage: stage, category: category, id: id,
			root: "official/" + stage + "/" + category + "/" + id,
		})
		return nil
	})
	if err != nil {
		builtinErr = fmt.Errorf("内置官方源读不到：%w", err)
		return
	}
	if len(found) == 0 {
		builtinErr = fmt.Errorf("内置官方源里一个插件都没有")
		return
	}
	// 顺序写死：正式版在前 → 分类 → id（索引与界面顺序都稳定，也方便 diff）
	sort.Slice(found, func(i, j int) bool {
		if found[i].stage != found[j].stage {
			return found[i].stage == StageStable
		}
		if found[i].category != found[j].category {
			return found[i].category < found[j].category
		}
		return found[i].id < found[j].id
	})

	seen := map[string]bool{}
	idx := Index{Schema: SchemaVersion, Name: BuiltinSourceName, Plugins: []Meta{}}
	for _, e := range found {
		if seen[e.id] {
			builtinErr = fmt.Errorf("内置官方源里插件 id 重名：%s", e.id)
			return
		}
		seen[e.id] = true

		raw, err := fs.ReadFile(officialFS, e.root+"/plugin.json")
		if err != nil {
			builtinErr = fmt.Errorf("内置插件 %s 的 plugin.json 读不到：%w", e.id, err)
			return
		}
		var m Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			builtinErr = fmt.Errorf("内置插件 %s 的 plugin.json 不是合法 JSON：%w", e.id, err)
			return
		}
		if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Version) == "" || strings.TrimSpace(m.Description) == "" {
			builtinErr = fmt.Errorf("内置插件 %s 的清单缺少 name / version / description（内置内容必须写全）", e.id)
			return
		}
		norm, err := normalizeManifest(m)
		if err != nil {
			builtinErr = fmt.Errorf("内置插件 %s 的清单不合法：%w", e.id, err)
			return
		}
		if norm.ID != e.id {
			builtinErr = fmt.Errorf("内置插件目录名 %s 与清单里的 id %q 对不上", e.id, norm.ID)
			return
		}
		pkg, err := zipEmbedDir(e.root)
		if err != nil {
			builtinErr = fmt.Errorf("打包内置插件 %s 失败：%w", e.id, err)
			return
		}
		sum := sha256.Sum256(pkg)
		builtinPkgs[e.id] = pkg
		idx.Plugins = append(idx.Plugins, Meta{
			ID: norm.ID, Name: norm.Name, Version: norm.Version,
			Description: norm.Description, Author: norm.Author, Homepage: norm.Homepage,
			License: norm.License, Tags: norm.Tags, MinBaize: norm.MinBaize,
			Channel: ChannelOfficial, Stage: e.stage, Category: e.category,
			URL: builtinPkgPrefix + e.id + ".zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(pkg)),
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

// OfficialFS 内置官方源的只读文件系统（给测试与将来的"发布脚本"复用）
func OfficialFS() fs.FS { return officialFS }
