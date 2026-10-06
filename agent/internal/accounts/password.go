package accounts

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 口令哈希用 PBKDF2-HMAC-SHA256（Go 1.24+ 标准库自带，不引入第三方依赖）。
//
// 为什么不用 bcrypt/argon2：本机没有 C 编译器，白泽整体走"纯 Go"路线；
// PBKDF2 是 NIST 认可的方案，配上足够大的迭代次数足以对抗离线爆破。
const (
	passwordAlgo    = "pbkdf2_sha256"
	passwordIter    = 210000 // 单次约几十毫秒，登录可接受、爆破代价高
	passwordSaltLen = 16
	passwordKeyLen  = 32
)

// ErrBadPassword 口令不正确（登录失败统一报这个，不区分"用户不存在"，
// 免得被人拿它枚举用户名）。
var ErrBadPassword = errors.New("用户名或口令不正确")

// hashPassword 生成 `pbkdf2_sha256$迭代$盐$密钥` 形式的可存储串。
func hashPassword(password string) (string, error) {
	if strings.TrimSpace(password) == "" {
		return "", errors.New("口令不能为空")
	}
	salt := make([]byte, passwordSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐失败：%w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIter, passwordKeyLen)
	if err != nil {
		return "", fmt.Errorf("计算口令哈希失败：%w", err)
	}
	return fmt.Sprintf("%s$%d$%s$%s", passwordAlgo, passwordIter,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifyPassword 校验口令。任何格式问题都当"不匹配"处理（不 panic、不泄露细节）。
func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordAlgo {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	// 定长比较，避免时序侧信道
	return subtle.ConstantTimeCompare(got, want) == 1
}
