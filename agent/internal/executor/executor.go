// Package executor 在桌面端就地执行后端下发的指令。
//
// MVP 支持的动作：ping / sys.info / window.now / fs.stat / fs.list / fs.delete。
// fs.delete 属于危险动作，除了后端审批闸门，这里还有一层本地路径护栏与 --dry-run 开关。
package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"baize/internal/sysinfo"
	"baize/shared/proto"
)

// ErrDangerDisabled 本端未开启危险能力（删除类动作被本地策略拒绝）
var ErrDangerDisabled = errors.New("本端未开启删除能力：需要 --dry-run（预演）、--allow-root（限定根目录）或 --allow-danger（显式放行）")

// Options 执行选项
type Options struct {
	DryRun      bool     // 危险动作只回执计划，不真正执行
	MaxPaths    int      // 单次最多处理多少个路径
	AllowRoots  []string // 允许删除的根目录白名单；非空时目标必须落在其内
	AllowDanger bool     // 显式承认危险动作（不带白名单时生效）
}

// Run 执行一条指令，返回结果（失败时同时返回错误，结果里仍带已完成的明细）
func Run(action string, args map[string]any, opt Options) (map[string]any, error) {
	if opt.MaxPaths <= 0 {
		opt.MaxPaths = 200
	}
	switch action {
	case proto.ActionPing:
		return map[string]any{"pong": true, "at": time.Now().UnixMilli()}, nil

	case proto.ActionSysInfo:
		return sysinfo.Local(), nil

	case proto.ActionWindowNow:
		w, err := sysinfo.ActiveWindow()
		if err != nil {
			return nil, err
		}
		return map[string]any{"window": w}, nil

	case proto.ActionFsStat:
		paths, err := pathsFrom(args, opt.MaxPaths)
		if err != nil {
			return nil, err
		}
		return statPaths(paths), nil

	case proto.ActionFsList:
		paths, err := pathsFrom(args, opt.MaxPaths)
		if err != nil {
			return nil, err
		}
		return listPaths(paths), nil

	case proto.ActionSysExec:
		// 执行命令：无界面端默认一律拒绝，只有显式 --allow-danger 或 --dry-run 才放行
		// （桌面端那条路由则由用户白名单 + 界面审批兜底）。
		if !opt.DryRun && !opt.AllowDanger {
			return nil, ErrDangerDisabled
		}
		return execCommand(args["cmd"], args["cwd"], args["timeoutSec"], opt.DryRun), nil

	case proto.ActionFsDelete:
		if !opt.DryRun && !opt.AllowDanger && len(opt.AllowRoots) == 0 {
			return nil, ErrDangerDisabled
		}
		paths, err := pathsFrom(args, opt.MaxPaths)
		if err != nil {
			return nil, err
		}
		return deletePaths(paths, opt)

	default:
		return nil, fmt.Errorf("不支持的动作：%s（本端支持：%s）", action, strings.Join(proto.KnownActions(), ", "))
	}
}

func pathsFrom(args map[string]any, max int) ([]string, error) {
	raw, ok := args["paths"]
	if !ok {
		return nil, errors.New("缺少参数 paths")
	}
	out := []string{}
	switch v := raw.(type) {
	case []any:
		for _, it := range v {
			if s := strings.TrimSpace(fmt.Sprint(it)); s != "" {
				out = append(out, s)
			}
		}
	case []string:
		for _, s := range v {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case string:
		out = append(out, splitList(v)...)
	default:
		return nil, fmt.Errorf("paths 参数类型不支持：%T", raw)
	}
	if len(out) == 0 {
		return nil, errors.New("paths 为空")
	}
	if len(out) > max {
		return nil, fmt.Errorf("paths 数量 %d 超过上限 %d", len(out), max)
	}
	return out, nil
}

func splitList(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ';'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func statPaths(paths []string) map[string]any {
	items := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		item := map[string]any{"path": p, "exists": false}
		info, err := os.Lstat(p)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				item["error"] = err.Error()
			}
			items = append(items, item)
			continue
		}
		item["exists"] = true
		item["isDir"] = info.IsDir()
		item["size"] = info.Size()
		item["mode"] = info.Mode().String()
		item["modTime"] = info.ModTime().UnixMilli()
		abs, _ := filepath.Abs(p)
		item["abs"] = abs
		items = append(items, item)
	}
	return map[string]any{"count": len(items), "items": items}
}

// listPaths 列目录条目（电脑控制第 1 档）：path 必须是个目录；单目录最多回 500 条。
// 返回 entries 按「目录在前、其余按名字」排序，带上大小与修改时间；子目录不递归。
func listPaths(paths []string) map[string]any {
	const maxEntries = 500
	out := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		item := map[string]any{"path": p, "ok": false}
		entries, err := os.ReadDir(p)
		if err != nil {
			item["error"] = err.Error()
			out = append(out, item)
			continue
		}
		list := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			sub := map[string]any{"name": e.Name(), "isDir": e.IsDir()}
			if info, err := e.Info(); err == nil {
				sub["size"] = info.Size()
				sub["modTime"] = info.ModTime().UnixMilli()
			}
			list = append(list, sub)
			if len(list) >= maxEntries {
				break
			}
		}
		sort.SliceStable(list, func(i, j int) bool {
			di, dj := list[i]["isDir"] == true, list[j]["isDir"] == true
			if di != dj {
				return di // 目录在前
			}
			return strings.ToLower(fmt.Sprint(list[i]["name"])) < strings.ToLower(fmt.Sprint(list[j]["name"]))
		})
		item["ok"] = true
		item["count"] = len(list)
		item["truncated"] = len(entries) > maxEntries
		item["entries"] = list
		out = append(out, item)
	}
	return map[string]any{"count": len(out), "dirs": out}
}

func deletePaths(paths []string, opt Options) (map[string]any, error) {
	items := make([]map[string]any, 0, len(paths))
	okCount, failCount, missing := 0, 0, 0
	var firstErr error

	for _, p := range paths {
		abs, err := guardPath(p, opt)
		if err != nil {
			failCount++
			if firstErr == nil {
				firstErr = err
			}
			items = append(items, map[string]any{"path": p, "ok": false, "error": err.Error(), "blocked": true})
			continue
		}
		info, err := os.Lstat(abs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				missing++
				items = append(items, map[string]any{"path": abs, "ok": false, "exists": false, "note": "目标不存在，已跳过"})
				continue
			}
			failCount++
			if firstErr == nil {
				firstErr = err
			}
			items = append(items, map[string]any{"path": abs, "ok": false, "error": err.Error()})
			continue
		}

		item := map[string]any{
			"path": abs, "ok": true, "isDir": info.IsDir(), "size": info.Size(),
			"dryRun": opt.DryRun,
		}
		if opt.DryRun {
			item["removed"] = false
			item["note"] = "dry-run：仅预演，未删除"
			okCount++
			items = append(items, item)
			continue
		}
		if info.IsDir() {
			err = os.RemoveAll(abs)
		} else {
			err = os.Remove(abs)
		}
		if err != nil {
			failCount++
			item["ok"] = false
			item["error"] = err.Error()
			if firstErr == nil {
				firstErr = err
			}
		} else {
			okCount++
			item["removed"] = true
		}
		items = append(items, item)
	}

	result := map[string]any{
		"dryRun":  opt.DryRun,
		"count":   len(items),
		"ok":      okCount,
		"failed":  failCount,
		"missing": missing,
		"items":   items,
	}
	if failCount > 0 {
		return result, fmt.Errorf("%d 个路径处理失败，首个错误：%v", failCount, firstErr)
	}
	return result, nil
}

// 系统目录护栏：这些位置一律拒绝删除
var deniedPrefixes = []string{
	`c:\windows`, `c:\program files`, `c:\program files (x86)`, `c:\programdata`,
	`c:\$recycle.bin`, `c:\system volume information`,
	`/etc`, `/usr`, `/bin`, `/sbin`, `/boot`, `/sys`, `/proc`, `/dev`, `/lib`, `/opt`,
}

// normPath 统一比较口径：清理 + 统一正斜杠 + 小写（Windows 路径分隔符/大小写不敏感）
func normPath(p string) string {
	return strings.ToLower(strings.ReplaceAll(filepath.Clean(p), "\\", "/"))
}

// withinOrSame 判断 target 是否等于 root 或落在 root 之内
func withinOrSame(root, target string) bool {
	if target == root {
		return true
	}
	if root == "/" {
		return strings.HasPrefix(target, "/")
	}
	return strings.HasPrefix(target, root+"/")
}

// guardPath 校验待删除路径，返回规范化后的绝对路径。
// 注意：所有比较必须走 normPath，否则 Windows 上会出现“分隔符/大小写不一致导致护栏失效”。
func guardPath(p string, opt Options) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("路径为空")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("路径非法：%v", err)
	}
	abs = filepath.Clean(abs)
	low := normPath(abs)

	// 磁盘根目录（Windows 的 C:\ 或 Linux 的 /）
	if abs == filepath.VolumeName(abs)+string(filepath.Separator) || abs == string(filepath.Separator) {
		return "", fmt.Errorf("拒绝删除磁盘根目录：%s", abs)
	}

	for _, pre := range deniedPrefixes {
		if withinOrSame(normPath(pre), low) {
			return "", fmt.Errorf("拒绝删除系统目录：%s", abs)
		}
	}

	// 用户主目录、以及包含主目录的上级目录
	if home := homeDir(); home != "" {
		if low == normPath(home) {
			return "", fmt.Errorf("拒绝删除用户主目录本身：%s", abs)
		}
		if withinOrSame(low, normPath(home)) {
			return "", fmt.Errorf("拒绝删除包含用户主目录的上级目录：%s", abs)
		}
	}

	// 当前工作目录、以及包含工作目录的上级目录（避免把运行现场删掉）
	if wd, err := os.Getwd(); err == nil {
		if withinOrSame(low, normPath(wd)) {
			return "", fmt.Errorf("拒绝删除当前工作目录或其上级目录：%s（工作目录 %s）", abs, wd)
		}
	}

	// 白名单：给了 --allow-root 就只允许删白名单内的路径
	if len(opt.AllowRoots) > 0 {
		allowed := false
		for _, r := range opt.AllowRoots {
			ra, err := filepath.Abs(strings.TrimSpace(r))
			if err != nil {
				continue
			}
			if withinOrSame(normPath(ra), low) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("拒绝删除：%s 不在允许的根目录白名单内（--allow-root）", abs)
		}
	}

	if runtime.GOOS == "windows" && strings.HasPrefix(low, "/") {
		return "", fmt.Errorf("路径格式不合法：%s", abs)
	}
	return abs, nil
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}
