package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const usage = `白泽·待办中心 命令行版 (bz) v0.10.0（Go 全量重写版：共用 baize/core 核心）

用法：
  bz add "标题" [--due 2026-09-26T10:00] [--cat 工作开发] [--pri high|mid|low]
               [--form schedule|leisure] [--minutes 45] [--weekly] [--note "备注"]
  bz list [--all] [--cat 领域] [--leisure] [--json]
  bz done <序号|id>          标记完成
  bz undo <序号|id>          恢复为未完成
  bz rm <序号|id>            删除待办
  bz pass add <平台> <账号> <密码> [--group 归属] [--url 网址] [--note 备注]
  bz pass list [--show]      列出密码（默认打码，--show 显示明文）
  bz pass get <序号|平台|平台/账号|id> [--show]
  bz pass rm <序号|平台|平台/账号|id>
  bz export [--out 文件名.md]  不传 --out 则输出到标准输出
  bz import <文件.md>          按标题/账号去重合并
  bz where                   打印数据文件路径
  bz help                    显示本帮助

说明：
  · 序号指 bz list 输出里的第 N 项（从 1 开始）；也可直接用完整 id 或 id 前缀。
  · list 默认只显示未完成项，按截止时间升序，无截止的排最后。
  · 优先级默认 mid，形态 form 默认 schedule；默认类别为空。
  · 密码本里同一个「平台 + 账号」只算一条记录：再 add 一次就是改密码，
    旧密码自动进历史（用 bz pass get 看）；--group 是「归属」，
    同一服务的多个站点填同一个（例：Trae 国际站 + Trae 国内站 都填 Trae）。
  · 定位一条密码可以用：序号、「平台」（同名多条时会提示）、「平台/账号」、id 或 id 前缀。
  · 数据文件：%USERPROFILE%\.baize-todo\data.json（Windows），
              $HOME/.baize-todo/data.json（其它系统）。

示例：
  bz add "整理本周工作计划" --due 2026-09-26T14:00 --cat 工作开发 --pri high --minutes 45
  bz add "看两章《Go 语言实战》" --form leisure --pri low
  bz list --all --json
  bz done 1
  bz pass add GitHub user@example.com abc123 --url https://github.com
  bz pass add "Trae 国际站" me@a.com newpass --group Trae
  bz export --out d:\todo.md
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		printUsage()
		return
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "help", "-h", "-help", "--help", "?":
		printUsage()
		return
	case "add", "new":
		err = cmdAdd(rest)
	case "list", "ls":
		err = cmdList(rest)
	case "done", "finish":
		err = cmdMark(rest, "done")
	case "undo", "reopen":
		err = cmdMark(rest, "todo")
	case "rm", "del", "remove", "delete":
		err = cmdRemove(rest)
	case "pass", "pw", "vault":
		err = cmdPass(rest)
	case "export":
		err = cmdExport(rest)
	case "import":
		err = cmdImport(rest)
	case "where":
		err = cmdWhere()
	default:
		err = fmt.Errorf("未知命令：%s（运行 bz help 查看用法）", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误："+err.Error())
		os.Exit(1)
	}
}

func printUsage() { os.Stdout.WriteString(usage) }

func outf(format string, a ...any) { fmt.Fprintf(os.Stdout, format, a...) }

/* ---------- 参数解析 ---------- */

type parsed struct {
	pos   []string
	flags map[string]string
	bools map[string]bool
}

var valueFlags = map[string]bool{
	"due": true, "cat": true, "category": true, "pri": true, "priority": true,
	"form": true, "minutes": true, "estimate": true, "note": true,
	"url": true, "out": true, "title": true, "account": true, "password": true,
	"group": true, "vault": true,
}

func parseArgs(list []string) *parsed {
	p := &parsed{flags: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(list); i++ {
		s := list[i]
		if !strings.HasPrefix(s, "--") || len(s) == 2 {
			p.pos = append(p.pos, s)
			continue
		}
		key := strings.TrimPrefix(s, "--")
		if eq := strings.Index(key, "="); eq >= 0 {
			p.flags[strings.ToLower(key[:eq])] = key[eq+1:]
			continue
		}
		key = strings.ToLower(key)
		if valueFlags[key] && i+1 < len(list) && !strings.HasPrefix(list[i+1], "--") {
			p.flags[key] = list[i+1]
			i++
			continue
		}
		p.bools[key] = true
	}
	return p
}

func (p *parsed) str(keys ...string) string {
	for _, k := range keys {
		if v, ok := p.flags[k]; ok {
			return v
		}
	}
	return ""
}

func (p *parsed) has(keys ...string) bool {
	for _, k := range keys {
		if p.bools[k] {
			return true
		}
	}
	return false
}

func (p *parsed) needPos(n int) error {
	if len(p.pos) < n {
		return fmt.Errorf("参数不足：需要 %d 个位置参数，实际 %d 个", n, len(p.pos))
	}
	return nil
}

func (p *parsed) firstPos() (string, error) {
	if len(p.pos) == 0 {
		return "", fmt.Errorf("缺少参数")
	}
	return p.pos[0], nil
}

func atoiDefault(s string, def int) (int, error) {
	if strings.TrimSpace(s) == "" {
		return def, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("数字格式错误：%s", s)
	}
	return n, nil
}
