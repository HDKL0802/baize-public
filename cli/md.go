package main

import (
	"errors"
	"os"
	"strings"
	"time"

	"baize/core"
)

// cmdExport bz export [--out 文件名.md]
func cmdExport(argv []string) error {
	p := parseArgs(argv)
	d, err := loadData()
	if err != nil {
		return err
	}
	md := core.ExportMarkdown(d, time.Now())
	out := strings.TrimSpace(p.str("out"))
	if out == "" {
		os.Stdout.WriteString(md)
		return nil
	}
	if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
		return err
	}
	outf("已导出 %d 条待办、%d 条密码到 %s\n", len(d.Todos), len(d.Vault), out)
	return nil
}

// cmdImport bz import <文件.md>
func cmdImport(argv []string) error {
	p := parseArgs(argv)
	path, err := p.firstPos()
	if err != nil {
		return errors.New("用法：bz import <文件.md>")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d, err := loadData()
	if err != nil {
		return err
	}
	res := d.ImportMarkdown(string(raw))
	if res.TodoAdded+res.TodoSkipped+res.PassAdded+res.PassMerged == 0 {
		return errors.New("未从文件中解析出任何待办或密码，请确认文件为本工具导出的 Markdown 格式")
	}
	if err := saveData(d); err != nil {
		return err
	}
	outf("导入完成：待办新增 %d 条（跳过重复 %d 条），密码新增 %d 条（合并 %d 条）\n",
		res.TodoAdded, res.TodoSkipped, res.PassAdded, res.PassMerged)
	return nil
}
