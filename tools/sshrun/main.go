// sshrun —— 部署用的小工具：Windows 自带的 ssh.exe 没法把密码喂进去（它只从控制台读），
// 所以这里用 Go 起一个能带密码执行命令 / 传文件的 SSH 客户端。
//
// 用法示例：
//
//	sshrun -host 192.168.1.100 -user user -pass xxx -cmd "uname -a"
//	sshrun -host 192.168.1.100 -pass xxx -probe "user,root,admin,fnos"
//	sshrun -host 192.168.1.100 -user user -pass xxx -put "D:\a\backend:/tmp/backend"
//	sshrun -host 192.168.1.100 -user user -pass xxx -get "/tmp/x.log:D:\x.log"
//
// 传文件走 `cat` 管道，不依赖 sftp/scp，省一个依赖；上传后会比一次大小，避免悄悄截断。
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func main() {
	host := flag.String("host", "", "主机")
	port := flag.Int("port", 22, "端口")
	user := flag.String("user", "", "用户名")
	pass := flag.String("pass", "", "密码")
	cmd := flag.String("cmd", "", "要执行的命令")
	put := flag.String("put", "", "上传 本地:远端")
	get := flag.String("get", "", "下载 远端:本地")
	probe := flag.String("probe", "", "逗号分隔的用户名，逐个试哪个能登录")
	timeout := flag.Duration("timeout", 25*time.Second, "连接超时")
	flag.Parse()

	var err error
	switch {
	case *probe != "":
		err = runProbe(*host, *port, *probe, *pass, *timeout)
	case *put != "":
		err = runPut(*host, *port, *user, *pass, *put, *timeout)
	case *get != "":
		err = runGet(*host, *port, *user, *pass, *get, *timeout)
	case *cmd != "":
		err = runCmd(*host, *port, *user, *pass, *cmd, *timeout)
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误："+err.Error())
		os.Exit(1)
	}
}

func dial(host string, port int, user, pass string, timeout time.Duration) (*ssh.Client, error) {
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         timeout,
	}
	return ssh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), cfg)
}

func runCmd(host string, port int, user, pass, cmd string, timeout time.Duration) error {
	c, err := dial(host, port, user, pass, timeout)
	if err != nil {
		return err
	}
	defer c.Close()
	s, err := c.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	s.Stdout = os.Stdout
	s.Stderr = os.Stderr
	err = s.Run(cmd)
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		fmt.Printf("\nEXIT=%d\n", ee.ExitStatus())
		os.Exit(3)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	fmt.Printf("\nEXIT=0\n")
	return nil
}

// runProbe 逐个试用户名（每个只试一次，避免触发锁定），命中后打印它
func runProbe(host string, port int, users, pass string, timeout time.Duration) error {
	var ok []string
	for _, u := range strings.Split(users, ",") {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		c, err := dial(host, port, u, pass, timeout)
		if err != nil {
			fmt.Printf("%-12s 登录失败: %s\n", u, shortErr(err))
			continue
		}
		s, err := c.NewSession()
		if err != nil {
			c.Close()
			fmt.Printf("%-12s 开会话失败: %s\n", u, shortErr(err))
			continue
		}
		out, err := s.CombinedOutput("id; hostname; uname -s; uname -m")
		s.Close()
		c.Close()
		if err != nil {
			fmt.Printf("%-12s 命令失败: %s\n", u, shortErr(err))
			continue
		}
		fmt.Printf("%-12s 登录成功\n%s\n", u, strings.TrimSpace(string(out)))
		ok = append(ok, u)
	}
	if len(ok) == 0 {
		return errors.New("没有一个用户名能登录")
	}
	fmt.Println("可用用户名: " + strings.Join(ok, ","))
	return nil
}

func runPut(host string, port int, user, pass, spec string, timeout time.Duration) error {
	local, remote, err := split2(spec, "上传")
	if err != nil {
		return err
	}
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}

	c, err := dial(host, port, user, pass, timeout)
	if err != nil {
		return err
	}
	defer c.Close()

	if i := strings.LastIndex(remote, "/"); i > 0 {
		if err := execOn(c, "mkdir -p "+sq(remote[:i])); err != nil {
			return err
		}
	}
	s, err := c.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	s.Stdin = f
	s.Stdout = os.Stdout
	s.Stderr = os.Stderr
	fmt.Printf("上传 %s (%d 字节) -> %s\n", local, st.Size(), remote)
	if err := s.Run("cat > " + sq(remote)); err != nil {
		return err
	}
	// 校验大小，别让传输悄悄截断
	out, err := c.NewSession()
	if err != nil {
		return err
	}
	b, err := out.Output("stat -c %s " + sq(remote))
	out.Close()
	if err != nil {
		return fmt.Errorf("远端 stat 失败：%w", err)
	}
	if got := strings.TrimSpace(string(b)); got != fmt.Sprint(st.Size()) {
		return fmt.Errorf("远端大小 %s != 本地 %d", got, st.Size())
	}
	fmt.Println("远端大小校验通过")
	return nil
}

func runGet(host string, port int, user, pass, spec string, timeout time.Duration) error {
	remote, local, err := split2(spec, "下载")
	if err != nil {
		return err
	}
	c, err := dial(host, port, user, pass, timeout)
	if err != nil {
		return err
	}
	defer c.Close()
	s, err := c.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	f, err := os.Create(local)
	if err != nil {
		return err
	}
	defer f.Close()
	s.Stdout = f
	s.Stderr = os.Stderr
	if err := s.Run("cat " + sq(remote)); err != nil {
		return err
	}
	st, _ := f.Stat()
	fmt.Printf("下载 %s -> %s (%d 字节)\n", remote, local, st.Size())
	return nil
}

func execOn(c *ssh.Client, cmd string) error {
	s, err := c.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	out, err := s.CombinedOutput(cmd)
	if err != nil {
		return fmt.Errorf("%s 失败：%v (%s)", cmd, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func split2(spec, what string) (string, string, error) {
	i := strings.LastIndex(spec, ":")
	if i <= 0 || i == len(spec)-1 {
		return "", "", fmt.Errorf("%s 参数要写成 本地:远端，收到的是 %q", what, spec)
	}
	return spec[:i], spec[i+1:], nil
}

// sq 单引号转义，防止路径里的空格/特殊字符把命令拆坏
func sq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func shortErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 160 {
		msg = msg[:160] + "…"
	}
	return msg
}
