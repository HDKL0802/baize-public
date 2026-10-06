package plugins

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// extractZip 把 zip 内容解到 dest。安全性是这里的重点：
//   - 挡 zip slip（条目名带 .. 或绝对路径，解出去就跑到 dest 之外了）；
//   - 挡符号链接条目（顺着链接能写到任意位置）；
//   - 限文件数与总解包体积（防 zip 炸弹）。
func extractZip(data []byte, dest string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("插件包不是合法 zip：%w", err)
	}
	if len(zr.File) > maxFiles {
		return fmt.Errorf("插件包文件数 %d 超过上限 %d", len(zr.File), maxFiles)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	destClean := filepath.Clean(dest)
	var total int64
	for _, f := range zr.File {
		name := f.Name
		if strings.Contains(name, `\`) {
			// 有些打包工具用反斜杠当分隔符，统一成正斜杠再判
			name = strings.ReplaceAll(name, `\`, "/")
		}
		clean := filepath.Clean(filepath.FromSlash(name))
		if clean == "." {
			continue
		}
		target := filepath.Join(destClean, clean)
		// 归一后必须仍在 dest 之下（zip slip 的判据）
		if target != destClean && !strings.HasPrefix(target, destClean+string(os.PathSeparator)) {
			return fmt.Errorf("插件包含非法路径（试图写到包外）：%s", f.Name)
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("插件包含符号链接（%s），为安全起见拒绝安装", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("读取包内文件失败（%s）：%w", f.Name, err)
		}
		// 单个条目也限长，避免"一个巨型条目"绕过总量检查
		limited := io.LimitReader(rc, maxUnpackBytes-total+1)
		n, err := writeReader(target, limited)
		rc.Close()
		if err != nil {
			return fmt.Errorf("解包失败（%s）：%w", f.Name, err)
		}
		total += n
		if total > maxUnpackBytes {
			return fmt.Errorf("解包后体积超过上限（%d 字节）", maxUnpackBytes)
		}
	}
	return nil
}

func writeReader(path string, r io.Reader) (int64, error) {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	return io.Copy(out, r)
}

// copyDir 递归拷贝目录（用于把插件带的技能落进技能目录）。
// 只拷普通文件与目录，遇到符号链接直接跳过——插件没有理由用链接。
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // 符号链接/设备文件一律跳过
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		if _, err := writeReader(target, in); err != nil {
			return err
		}
		return nil
	})
}

// dirExists 目录是否存在（且真的是目录）
func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// fileExists 文件是否存在
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// moveDir 移动目录：先试 rename，跨盘/被占用失败就退化成"拷贝 + 删源"。
// Windows 上 rename 到已存在的目录会失败，所以调用方要保证 dst 不存在。
func moveDir(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyDir(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

// uniqueDir 返回一个还不存在的目录名（带序号后缀）
func uniqueDir(base string) string {
	if !dirExists(base) && !fileExists(base) {
		return base
	}
	for i := 1; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if !dirExists(cand) && !fileExists(cand) {
			return cand
		}
	}
	return base
}
