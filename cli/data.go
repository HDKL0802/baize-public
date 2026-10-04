package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"baize/core"
)

// 电脑侧命令行只是核心（baize/core）的一层薄壳：
// 领域逻辑、数据格式、密码本合并规则、Markdown 导入导出全部来自 core，
// 保证与手机端内核、后端 Agent 用的是同一套语义。
type (
	Todo        = core.Task
	Pass        = core.Pass
	PassHistory = core.PassHistory
	Data        = core.Doc
)

// dataDir 数据目录：Windows 用 %USERPROFILE%\.baize-todo，其它系统用 $HOME/.baize-todo
func dataDir() string {
	if runtime.GOOS == "windows" {
		if p := os.Getenv("USERPROFILE"); p != "" {
			return filepath.Join(p, ".baize-todo")
		}
	}
	if h := os.Getenv("HOME"); h != "" {
		return filepath.Join(h, ".baize-todo")
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, ".baize-todo")
}

func dataPath() string { return filepath.Join(dataDir(), "data.json") }

func dataStore() *core.Store { return core.OpenFile(dataPath()) }

func loadData() (*Data, error) { return dataStore().Load() }

func saveData(d *Data) error { return dataStore().Save(d) }

func newID() string { return core.NewID() }

/* ---------- 归一化 / 定位（转发到 core） ---------- */

func normalizePriority(v string) (string, error) { return core.NormalizePriority(v) }

func normalizeDue(v string) (string, error) { return core.NormalizeDue(v) }

func priLabel(p string) string { return core.PriorityLabel(p) }

func visibleOrder(todos []Todo, includeClosed bool) []int {
	return core.VisibleOrder(todos, includeClosed)
}

func findTodo(d *Data, ref string) (int, error) { return d.FindTask(ref) }

func findPass(d *Data, ref string) (int, error) { return d.FindPass(ref) }

// mergePass 合并一条密码，返回（new|updated|same, 记录 id）
func mergePass(d *Data, v Pass) (string, string) {
	res, idx := d.MergePass(v)
	if idx >= 0 && idx < len(d.Vault) {
		return res, d.Vault[idx].ID
	}
	return res, ""
}

// maskPassword 打码显示（点数与 App 一致：至少 6 个、最多 14 个）
func maskPassword(p string) string {
	if p == "" {
		return "(空)"
	}
	n := len([]rune(p))
	if n < 6 {
		n = 6
	}
	if n > 14 {
		n = 14
	}
	return strings.Repeat("•", n)
}
