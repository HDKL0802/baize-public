//go:build windows

// 电脑控制 · 第 1 档：本机文件浏览 + 一键整理进知识库。
//
// 这是**桌面端自己的界面**用的本机接口（不是后端派活那条路）：
// 用户在「电脑文件」页里浏览自己的盘/目录，挑几个文件「整理进知识库」。
//
// 安全口径与设备侧完全一致：**所有读写都先过 permGate** ——
// 权限档位 < 2（只读指定目录）时根本读不了文件，越界目录一律拒绝并说清原因。
// 这里的路由走本机回环，不经后端；配对令牌与会话令牌都到不了这一层。
package main

import (
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"baize/shared/proto"
)

// fileReadMax 单个文件读进内存的上限（与知识库附件上限同量级：16MB）
const fileReadMax = 16 << 20

// 目录内递归搜索的边界：只搜名字、不读内容；root 必须先过 permGate。
const (
	fileSearchMaxResults = 300
	fileSearchMaxVisited = 20000
	fileSearchMaxDepth   = 8
)

// fileMimeByExt 极简的「按扩展名猜 MIME」——只用于知识库展示，不追求完备。
func fileMimeByExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".log", ".md":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".zip":
		return "application/zip"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	}
	return ""
}

// fileKindOf 知识库附件分「image」与「file」两类；图片才进图库。
func fileKindOf(name, mime string) string {
	if strings.HasPrefix(mime, "image/") {
		return "image"
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return "image"
	}
	return "file"
}

// registerFileRoutes 本机文件浏览接口。全部要过 permGate。
func registerFileRoutes(mux *http.ServeMux) {
	// 列目录：path 为空时回「此电脑」（盘符清单）+ 当前权限/范围，供界面选起点。
	mux.HandleFunc("/api/local/files/list", func(w http.ResponseWriter, r *http.Request) {
		perm, scopes := currentPerm()
		path := strings.TrimSpace(r.URL.Query().Get("path"))
		if path == "" {
			roots := []map[string]any{}
			for _, v := range listVolumes() {
				roots = append(roots, map[string]any{"name": v, "path": v, "isDir": true})
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "path": "", "parent": "", "roots": roots,
				"perm": perm, "permTitle": PermTitle(perm), "scopes": scopes,
				"hint": "从「此电脑」挑一个目录进去；只能进你圈定的范围（设置 → 桌面控制）。",
			})
			return
		}
		// 权限闸门：越界/档位不够一律拒绝，错误信息直接给界面看
		if err := permGate(proto.ActionFsList, []string{path}); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": false, "error": err.Error(), "perm": perm, "permTitle": PermTitle(perm), "scopes": scopes,
			})
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "路径不合法"})
			return
		}
		res := listPaths([]string{abs})
		dirs, _ := res["dirs"].([]map[string]any)
		if len(dirs) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "读目录失败"})
			return
		}
		d := dirs[0]
		if ok, _ := d["ok"].(bool); !ok {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": false, "error": d["error"], "path": abs, "parent": parentDir(abs),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "path": abs, "parent": parentDir(abs),
			"entries": d["entries"], "truncated": d["truncated"],
			"perm": perm, "permTitle": PermTitle(perm), "scopes": scopes,
		})
	})

	// 读一个文件（整理进知识库用）：回 base64 + 猜出来的 MIME/Kind。
	mux.HandleFunc("/api/local/files/read", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSpace(r.URL.Query().Get("path"))
		if path == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "path 不能为空"})
			return
		}
		if err := permGate(proto.ActionFsStat, []string{path}); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "路径不合法"})
			return
		}
		info, err := os.Stat(abs)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "读不到：" + err.Error()})
			return
		}
		if info.IsDir() {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "这是个目录，不能整理进知识库"})
			return
		}
		if info.Size() > fileReadMax {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": false, "error": "文件太大（超过 16MB），先压缩或换个小的"})
			return
		}
		f, err := os.Open(abs)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "打不开：" + err.Error()})
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, fileReadMax))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "读取失败：" + err.Error()})
			return
		}
		name := filepath.Base(abs)
		mime := fileMimeByExt(name)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "name": name, "size": len(data), "mime": mime,
			"kind":       fileKindOf(name, mime),
			"dataBase64": base64.StdEncoding.EncodeToString(data),
		})
	})

	// 目录内递归搜索（只匹配文件名，不读文件内容）：给「电脑文件」页的搜索框用。
	// 边界：root 必须先过 permGate；最多 8 层、最多看 2 万个条目、最多回 300 条结果。
	// 不跟随符号链接 / 目录联接（WalkDir 的 DirEntry.IsDir 只认真实目录）。
	mux.HandleFunc("/api/local/files/search", func(w http.ResponseWriter, r *http.Request) {
		perm, scopes := currentPerm()
		root := strings.TrimSpace(r.URL.Query().Get("path"))
		kw := strings.TrimSpace(r.URL.Query().Get("q"))
		if kw == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "搜索关键词不能为空"})
			return
		}
		if root == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "先进入一个目录再搜索（「此电脑」不递归）"})
			return
		}
		if err := permGate(proto.ActionFsList, []string{root}); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": false, "error": err.Error(), "perm": perm, "permTitle": PermTitle(perm), "scopes": scopes,
			})
			return
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "路径不合法"})
			return
		}
		lower := strings.ToLower(kw)
		results := []map[string]any{}
		visited := 0
		truncated := false
		walkErr := filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() && p != abs {
					return filepath.SkipDir
				}
				return nil
			}
			visited++
			if visited > fileSearchMaxVisited {
				truncated = true
				return filepath.SkipAll
			}
			rel, relErr := filepath.Rel(abs, p)
			if relErr != nil {
				rel = p
			}
			if d.IsDir() {
				if p != abs && strings.Count(rel, string(os.PathSeparator)) >= fileSearchMaxDepth {
					return filepath.SkipDir
				}
				if p != abs && strings.Contains(strings.ToLower(d.Name()), lower) {
					results = append(results, fileSearchItem(p, rel, d, true))
				}
			} else if strings.Contains(strings.ToLower(d.Name()), lower) {
				results = append(results, fileSearchItem(p, rel, d, false))
			}
			if len(results) >= fileSearchMaxResults {
				truncated = true
				return filepath.SkipAll
			}
			return nil
		})
		if walkErr != nil && len(results) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "搜索失败：" + walkErr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "path": abs, "q": kw, "results": results,
			"truncated": truncated, "visited": visited,
			"perm": perm, "permTitle": PermTitle(perm), "scopes": scopes,
		})
	})
}

// fileSearchItem 把搜索命中的一条转成界面用的形状（不带文件内容）。
func fileSearchItem(path, rel string, d os.DirEntry, isDir bool) map[string]any {
	item := map[string]any{"name": d.Name(), "path": path, "rel": rel, "isDir": isDir}
	if info, err := d.Info(); err == nil {
		if !isDir {
			item["size"] = info.Size()
		}
		item["modTime"] = info.ModTime().UnixMilli()
	}
	return item
}

// parentDir 上一层目录；本身已是盘根（D:\）就回空串（界面据此回「此电脑」）。
func parentDir(abs string) string {
	if isDriveRoot(abs) {
		return ""
	}
	p := filepath.Dir(abs)
	if p == abs || p == "" {
		return ""
	}
	return p
}

// isDriveRoot 是不是盘根（D: / D:\ 都算）
func isDriveRoot(p string) bool {
	p = strings.TrimRight(p, `\/`)
	return len(p) == 2 && p[1] == ':'
}
