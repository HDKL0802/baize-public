// 内置技能：随二进制一起分发（go:embed），首次启动时落盘到技能目录。
//
// 为什么要内置：白泽要开箱就能干几件正事（读文件、处理文档、造技能、配定时任务、记笔记），
// 而不是让用户先自己写几个技能。放 go:embed 而不是仓库里的散文件，是为了让 NAS 上的
// 容器（只拷二进制）也一定拿得到——不依赖镜像里有没有这些目录。
//
// 落盘策略（很重要）：
//   - **只在目标技能还不存在时写**，绝不覆盖用户的技能目录内容（用户改过就以用户的为准）；
//   - 用技能目录里的 `.builtin.json` 记「已落盘到哪个版本」，版本没变就一次都不扫；
//   - 用户删掉某个内置技能后，不会在下次启动时被"复活"（标记版本未变则跳过）。
package skills

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BuiltinVersion 内置技能集版本。**改动 builtin/ 里的内容时递增**，
// 递增后下次启动会把新技能补种进去（已存在的同名技能仍不覆盖）。
const BuiltinVersion = "1"

//go:embed builtin
var builtinFS embed.FS

// builtinMarker 记录已落盘的版本（放在技能目录里，跟技能一起被备份带走）
const builtinMarker = ".builtin.json"

type builtinMarkerFile struct {
	Version string   `json:"version"`
	Skills  []string `json:"skills"`
}

// BuiltinNames 内置技能名（目录 slug）清单，排序
func BuiltinNames() []string {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// SeedBuiltin 把内置技能落盘到技能目录，返回本次新写入的技能数。
//
// 幂等且不破坏用户内容：已存在的技能目录一律跳过；版本没变时直接返回 0（不扫盘）。
func SeedBuiltin(dir string) (int, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return 0, nil
	}
	if readBuiltinVersion(dir) == BuiltinVersion {
		return 0, nil // 已经是这一版，什么都不用做
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	seeded := 0
	written := []string{}
	for _, slug := range BuiltinNames() {
		target := filepath.Join(dir, slug)
		if fi, err := os.Stat(target); err == nil && fi.IsDir() {
			// 已经有了（可能是用户自己写的/改过的）→ 绝不覆盖
			written = append(written, slug)
			continue
		}
		n, err := copyBuiltinSkill(slug, target)
		if err != nil {
			return seeded, err
		}
		if n > 0 {
			seeded++
			written = append(written, slug)
		}
	}
	return seeded, writeBuiltinVersion(dir, written)
}

// copyBuiltinSkill 把 builtin/<slug>/ 下的文件原样落盘，返回写入的文件数
func copyBuiltinSkill(slug, target string) (int, error) {
	src := "builtin/" + slug
	count := 0
	err := fs.WalkDir(builtinFS, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		dst := filepath.Join(target, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, rerr := builtinFS.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

func readBuiltinVersion(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, builtinMarker))
	if err != nil {
		return ""
	}
	var m builtinMarkerFile
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	return strings.TrimSpace(m.Version)
}

func writeBuiltinVersion(dir string, names []string) error {
	sort.Strings(names)
	raw, err := json.MarshalIndent(builtinMarkerFile{Version: BuiltinVersion, Skills: names}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, builtinMarker)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return errors.New("写入内置技能标记失败：" + err.Error())
	}
	return nil
}
