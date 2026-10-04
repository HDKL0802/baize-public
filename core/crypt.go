package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// 密码本加密参数：必须与手机 App（WebCrypto 版）逐字节兼容，
// 否则同一份 bz_vault_enc / bz_vault_pwd 在两端解不开。
//
//	KDF : PBKDF2-HMAC-SHA256，口令前缀 "baize-todo-vault:"，迭代 150000，输出 32 字节
//	加密: AES-GCM 256，IV 12 字节随机，密文 = 密文体||16 字节 tag（WebCrypto 默认布局）
//	组织: {"iv": base64, "data": base64}
const (
	kdfPrefix        = "baize-todo-vault:"
	PBKDF2Iterations = 150000
	saltLen          = 16
	keyLen           = 32
	gcmIVLen         = 12
)

// EncBox 加密后的密码本（与 JS 的 {iv,data} 结构一致）
type EncBox struct {
	IV   string `json:"iv"`
	Data string `json:"data"`
}

// PwdRecord 主密码校验记录（App 侧存在 bz_vault_pwd）
type PwdRecord struct {
	Salt string `json:"salt"`
	Hash string `json:"hash,omitempty"`
}

// DeriveKeyPBKDF2 由主密码派生加密密钥（兼容 JS 的 derive）
func DeriveKeyPBKDF2(pwd string, salt []byte) ([]byte, error) {
	key, err := pbkdf2.Key(sha256.New, kdfPrefix+pwd, salt, PBKDF2Iterations, keyLen)
	if err != nil {
		return nil, fmt.Errorf("派生密钥失败：%w", err)
	}
	return key, nil
}

// HashPBKDF2 主密码的确定性校验值（兼容 JS 的 hashOf）
func HashPBKDF2(pwd string, salt []byte) (string, error) {
	key, err := DeriveKeyPBKDF2(pwd, salt)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// NewPwdRecord 设置主密码：生成随机盐、校验哈希，并返回已派生的密钥
func NewPwdRecord(pwd string) (PwdRecord, []byte, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return PwdRecord{}, nil, fmt.Errorf("生成随机盐失败：%w", err)
	}
	hash, err := HashPBKDF2(pwd, salt)
	if err != nil {
		return PwdRecord{}, nil, err
	}
	key, err := DeriveKeyPBKDF2(pwd, salt)
	if err != nil {
		return PwdRecord{}, nil, err
	}
	return PwdRecord{Salt: base64.StdEncoding.EncodeToString(salt), Hash: hash}, key, nil
}

// KeyFor 由主密码与记录里的盐派生出密钥（解锁用）
func KeyFor(pwd string, rec PwdRecord) ([]byte, error) {
	salt, err := decodeSalt(rec)
	if err != nil {
		return nil, err
	}
	return DeriveKeyPBKDF2(pwd, salt)
}

func decodeSalt(rec PwdRecord) ([]byte, error) {
	salt, err := base64.StdEncoding.DecodeString(rec.Salt)
	if err != nil || len(salt) == 0 {
		return nil, errors.New("主密码记录损坏：盐不是合法的 Base64")
	}
	return salt, nil
}

// VerifyPwd 校验主密码。带 hash 的记录做哈希比对；旧版只存了密文库的记录，
// 用“能不能解开密文库”来判断（与 JS 兼容路径一致）。
func VerifyPwd(pwd string, rec PwdRecord, box *EncBox) (bool, error) {
	salt, err := decodeSalt(rec)
	if err != nil {
		return false, err
	}
	if rec.Hash != "" {
		hash, err := HashPBKDF2(pwd, salt)
		if err != nil {
			return false, err
		}
		return hash == rec.Hash, nil
	}
	if box == nil {
		return false, nil
	}
	key, err := DeriveKeyPBKDF2(pwd, salt)
	if err != nil {
		return false, err
	}
	if _, err := DecryptBox(key, *box); err != nil {
		return false, nil
	}
	return true, nil
}

// EncryptBox 用密钥加密字节串
func EncryptBox(key, plaintext []byte) (EncBox, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return EncBox{}, err
	}
	iv := make([]byte, gcmIVLen)
	if _, err := rand.Read(iv); err != nil {
		return EncBox{}, fmt.Errorf("生成 IV 失败：%w", err)
	}
	ct := gcm.Seal(nil, iv, plaintext, nil)
	return EncBox{
		IV:   base64.StdEncoding.EncodeToString(iv),
		Data: base64.StdEncoding.EncodeToString(ct),
	}, nil
}

// DecryptBox 用密钥解密
func DecryptBox(key []byte, box EncBox) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	iv, err := base64.StdEncoding.DecodeString(box.IV)
	if err != nil {
		return nil, errors.New("密文库损坏：iv 不是合法的 Base64")
	}
	ct, err := base64.StdEncoding.DecodeString(box.Data)
	if err != nil {
		return nil, errors.New("密文库损坏：data 不是合法的 Base64")
	}
	pt, err := gcm.Open(nil, iv, ct, nil)
	if err != nil {
		return nil, errors.New("解密失败（主密码不对，或密文库已损坏）")
	}
	return pt, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("密钥长度必须是 %d 字节，实际 %d", keyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES 失败：%w", err)
	}
	gcm, err := cipher.NewGCM(block) // 默认 tag 16 字节，与 WebCrypto AES-GCM 一致
	if err != nil {
		return nil, fmt.Errorf("初始化 AES-GCM 失败：%w", err)
	}
	return gcm, nil
}

// EncryptVault 加密整个密码本（与 JS saveVault 一致：JSON 化后加密）
func EncryptVault(key []byte, list []Pass) (EncBox, error) {
	if list == nil {
		list = []Pass{}
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return EncBox{}, err
	}
	return EncryptBox(key, raw)
}

// DecryptVault 解密整个密码本
func DecryptVault(key []byte, box EncBox) ([]Pass, error) {
	raw, err := DecryptBox(key, box)
	if err != nil {
		return nil, err
	}
	list := []Pass{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, errors.New("密文库内容不是合法的密码本 JSON")
		}
	}
	for i := range list {
		normalizePass(&list[i])
	}
	return list, nil
}
