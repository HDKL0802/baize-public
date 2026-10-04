package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

func cmdPass(argv []string) error {
	if len(argv) == 0 {
		printPassUsage()
		return nil
	}
	sub, rest := strings.ToLower(argv[0]), argv[1:]
	switch sub {
	case "help", "-h", "-help", "--help":
		printPassUsage()
		return nil
	case "add", "new":
		return passAdd(rest)
	case "list", "ls":
		return passList(rest)
	case "get", "show":
		return passGet(rest)
	case "rm", "del", "remove", "delete":
		return passRm(rest)
	}
	return fmt.Errorf("未知的 pass 子命令：%s（可用：add | list | get | rm）", argv[0])
}

func printPassUsage() {
	outf(`密码本用法：
  bz pass add <平台> <账号> <密码> [--group 归属] [--url 网址] [--note 备注]
  bz pass list [--show]          默认密码打码，--show 显示明文
  bz pass get <序号|平台|平台/账号|id> [--show]
  bz pass rm <序号|平台|平台/账号|id>

说明：
  · 同一个「平台 + 账号」只算一条记录：再 add 一次就是改密码，
    旧密码会自动进历史（bz pass get 里能看到，新的在前）。
  · --group 是「归属」：同一服务的多个站点填同一个（例：Trae 国际站 + Trae 国内站 → 都填 Trae）。
`)
}

// passAdd bz pass add <平台> <账号> <密码> [--group g] [--url u] [--note n]
func passAdd(argv []string) error {
	p := parseArgs(argv)
	if len(p.pos) < 3 {
		return errors.New(`用法：bz pass add <平台> <账号> <密码> [--group 归属] [--url 网址] [--note 备注]`)
	}
	title := strings.TrimSpace(p.pos[0])
	account := strings.TrimSpace(p.pos[1])
	password := strings.TrimSpace(strings.Join(p.pos[2:], " "))
	if title == "" || account == "" {
		return errors.New("平台名称与账号不能为空")
	}
	d, err := loadData()
	if err != nil {
		return err
	}
	res, id := mergePass(d, Pass{
		Title:    title,
		Account:  account,
		Group:    strings.TrimSpace(p.str("group")),
		Password: password,
		URL:      strings.TrimSpace(p.str("url")),
		Note:     p.str("note"),
	})
	if err := saveData(d); err != nil {
		return err
	}
	switch res {
	case "updated":
		outf("已更新密码：%s / %s（旧密码已记入历史）  (id: %s)\n", title, account, id)
	case "same":
		outf("这个账号已有同样密码，未重复添加：%s / %s  (id: %s)\n", title, account, id)
	default:
		outf("已保存密码：%s / %s  (id: %s)\n", title, account, id)
	}
	return nil
}

// passList bz pass list [--show]
func passList(argv []string) error {
	p := parseArgs(argv)
	show := p.has("show")
	d, err := loadData()
	if err != nil {
		return err
	}
	if len(d.Vault) == 0 {
		outf("（密码本为空）\n")
		return nil
	}
	for i, v := range d.Vault {
		pw := v.Password
		if !show {
			pw = maskPassword(pw)
		}
		line := fmt.Sprintf("%d  %s", i+1, v.Title)
		if v.Group != "" {
			line += " [" + v.Group + "]"
		}
		line += fmt.Sprintf("  账号: %s  密码: %s", v.Account, pw)
		if len(v.History) > 0 {
			line += fmt.Sprintf("  历史密码 %d 个", len(v.History))
		}
		if v.URL != "" {
			line += "  URL: " + v.URL
		}
		outf("%s\n", line)
	}
	return nil
}

// passGet bz pass get <序号|id> [--show]
func passGet(argv []string) error {
	p := parseArgs(argv)
	ref, err := p.firstPos()
	if err != nil {
		return errors.New("用法：bz pass get <序号|id> [--show]")
	}
	d, err := loadData()
	if err != nil {
		return err
	}
	i, err := findPass(d, ref)
	if err != nil {
		return err
	}
	v := d.Vault[i]
	show := p.has("show")
	pw := v.Password
	if !show {
		pw = maskPassword(pw)
	}
	outf("平台: %s\n", v.Title)
	if v.Group != "" {
		outf("归属: %s\n", v.Group)
	}
	outf("账号: %s\n密码: %s\n", v.Account, pw)
	if len(v.History) > 0 {
		outf("历史密码（新的在前）:\n")
		for _, h := range v.History {
			hp := h.Pwd
			if !show {
				hp = maskPassword(hp)
			}
			outf("  - %s  ｜ %s\n", hp, time.UnixMilli(h.At).Format("2006-01-02 15:04"))
		}
	}
	if v.URL != "" {
		outf("URL: %s\n", v.URL)
	}
	if v.Note != "" {
		outf("备注: %s\n", v.Note)
	}
	outf("id: %s\n创建时间: %s\n", v.ID, time.UnixMilli(v.CreatedAt).Format("2006-01-02 15:04"))
	return nil
}

// passRm bz pass rm <序号|id>
func passRm(argv []string) error {
	p := parseArgs(argv)
	ref, err := p.firstPos()
	if err != nil {
		return errors.New("用法：bz pass rm <序号|id>")
	}
	d, err := loadData()
	if err != nil {
		return err
	}
	i, err := findPass(d, ref)
	if err != nil {
		return err
	}
	title, account := d.Vault[i].Title, d.Vault[i].Account
	d.Vault = append(d.Vault[:i], d.Vault[i+1:]...)
	if err := saveData(d); err != nil {
		return err
	}
	outf("已删除密码：%s / %s\n", title, account)
	return nil
}
