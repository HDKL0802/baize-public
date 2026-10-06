package accounts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 存储分区布局（对应用户「每个用户单独一个数据库」的诉求）：
//
//	<数据目录>/accounts.db          账号与用户组注册表（本包）
//	<数据目录>/users/<用户id>/      某用户的私人分区：memory.db + kb/ + workspace/
//	<数据目录>/groups/<组id>/       某用户组的共享分区：memory.db + kb/ + shared/
//
// 根数据目录本身仍是「默认主体」的数据 —— 没登录时一切照旧，老用户无感。

// uidOK / gidOK 只认本包生成的 id 形状，杜绝 ../ 之类的路径穿越
func uidOK(id string) bool { return idOK(id, "u_") }
func gidOK(id string) bool { return idOK(id, "g_") }

func idOK(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+12 {
		return false
	}
	for _, c := range id[len(prefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// UserDir 用户的私人数据分区目录
func (r *Registry) UserDir(id string) string {
	return filepath.Join(r.dir, "users", id)
}

// GroupDir 用户组的共享数据分区目录
func (r *Registry) GroupDir(id string) string {
	return filepath.Join(r.dir, "groups", id)
}

// ensureUserDirs 确保用户分区存在（记忆库/知识库/工作目录各自建好）
func (r *Registry) ensureUserDirs(id string) error {
	if !uidOK(id) {
		return fmt.Errorf("用户 id 不合法：%s", id)
	}
	return ensurePartition(r.UserDir(id))
}

// ensureGroupDirs 确保用户组分区存在（多一个 shared/ 放组内共享文档）
func (r *Registry) ensureGroupDirs(id string) error {
	if !gidOK(id) {
		return fmt.Errorf("用户组 id 不合法：%s", id)
	}
	dir := r.GroupDir(id)
	if err := ensurePartition(dir); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(dir, "shared"), 0o755)
}

// ensurePartition 建一个分区的基础目录
func ensurePartition(dir string) error {
	for _, sub := range []string{"", "kb", "kb/files", "workspace"} {
		p := dir
		if sub != "" {
			p = filepath.Join(dir, sub)
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("创建分区目录失败（%s）：%w", p, err)
		}
	}
	return nil
}

// Principal 表示"以谁的身份访问数据"。
//
// 空串 / "default" = 默认主体（根数据目录，单人用法）；
// "user:<id>"  = 某个用户的私人分区；
// "group:<id>" = 某个用户组的共享分区。
type Principal string

// 主体常量
const (
	PrincipalDefault Principal = ""
	principalUser              = "user:"
	principalGroup             = "group:"
)

// UserPrincipal 构造用户主体
func UserPrincipal(id string) Principal { return Principal(principalUser + id) }

// GroupPrincipal 构造用户组主体
func GroupPrincipal(id string) Principal { return Principal(principalGroup + id) }

// ParsePrincipal 解析主体字符串（不合法一律回落到默认主体）
func ParsePrincipal(s string) Principal {
	s = strings.TrimSpace(s)
	if s == "" || s == "default" {
		return PrincipalDefault
	}
	if strings.HasPrefix(s, principalUser) {
		id := strings.TrimPrefix(s, principalUser)
		if uidOK(id) {
			return Principal(principalUser + id)
		}
		return PrincipalDefault
	}
	if strings.HasPrefix(s, principalGroup) {
		id := strings.TrimPrefix(s, principalGroup)
		if gidOK(id) {
			return Principal(principalGroup + id)
		}
		return PrincipalDefault
	}
	return PrincipalDefault
}

// Key 主体的字符串形式（给日志/接口用）
func (p Principal) Key() string { return string(p) }

// IsDefault 是否默认主体
func (p Principal) IsDefault() bool { return p == PrincipalDefault }

// PartitionDir 主体对应的数据目录（账号库自身在根目录，这里给的是"数据分区"）
func (r *Registry) PartitionDir(principal Principal) string {
	key := principal.Key()
	switch {
	case strings.HasPrefix(key, principalUser):
		return r.UserDir(strings.TrimPrefix(key, principalUser))
	case strings.HasPrefix(key, principalGroup):
		return r.GroupDir(strings.TrimPrefix(key, principalGroup))
	default:
		return r.dir
	}
}

// UserID / GroupID 从主体里取 id（不是对应类型时返回空串）
func (p Principal) UserID() string {
	if strings.HasPrefix(p.Key(), principalUser) {
		return strings.TrimPrefix(p.Key(), principalUser)
	}
	return ""
}

// GroupID 取组 id
func (p Principal) GroupID() string {
	if strings.HasPrefix(p.Key(), principalGroup) {
		return strings.TrimPrefix(p.Key(), principalGroup)
	}
	return ""
}
