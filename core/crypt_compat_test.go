package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

// vector 由 testdata/make_vector.mjs（Node + WebCrypto）生成，
// 用的是手机 App 那一套完全相同的算法参数。
type cryptoVector struct {
	Password  string `json:"password"`
	SaltB64   string `json:"saltB64"`
	HashB64   string `json:"hashB64"`
	IVB64     string `json:"ivB64"`
	Box       EncBox `json:"box"`
	Plaintext string `json:"plaintext"`
}

func loadVector(t *testing.T) cryptoVector {
	t.Helper()
	raw, err := os.ReadFile("testdata/vector.json")
	if err != nil {
		t.Fatalf("读取测试向量失败（先在 core/testdata 下跑 node make_vector.mjs）：%v", err)
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) // 容忍 BOM
	var v cryptoVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("测试向量解析失败：%v", err)
	}
	return v
}

// 与 JS hashOf 的兼容性：同一个密码 + 同一个盐必须算出同一个哈希
func TestPBKDF2MatchesJS(t *testing.T) {
	v := loadVector(t)
	salt, err := base64.StdEncoding.DecodeString(v.SaltB64)
	if err != nil {
		t.Fatalf("盐解码失败：%v", err)
	}
	if len(salt) != saltLen {
		t.Fatalf("盐长度应为 %d，实际 %d", saltLen, len(salt))
	}
	hash, err := HashPBKDF2(v.Password, salt)
	if err != nil {
		t.Fatalf("计算哈希失败：%v", err)
	}
	if hash != v.HashB64 {
		t.Fatalf("与 JS 的哈希不一致：\nJS  = %s\nGo  = %s", v.HashB64, hash)
	}

	// 密码错一位必须得出不同结果
	other, err := HashPBKDF2(v.Password+"x", salt)
	if err != nil {
		t.Fatalf("计算哈希失败：%v", err)
	}
	if other == v.HashB64 {
		t.Fatal("不同密码不应得到相同哈希")
	}
}

// 与 JS encryptBytes 的兼容性：Go 必须能解开 JS 加密的密文库
func TestDecryptBoxFromJS(t *testing.T) {
	v := loadVector(t)
	key, err := KeyFor(v.Password, PwdRecord{Salt: v.SaltB64, Hash: v.HashB64})
	if err != nil {
		t.Fatalf("派生密钥失败：%v", err)
	}
	pt, err := DecryptBox(key, v.Box)
	if err != nil {
		t.Fatalf("解不开 JS 生成的密文库：%v", err)
	}
	if string(pt) != v.Plaintext {
		t.Fatalf("解密结果不一致：\n期望 %s\n实际 %s", v.Plaintext, string(pt))
	}

	// 顺势验证 DecryptVault 能还原出密码本条目
	list, err := DecryptVault(key, v.Box)
	if err != nil {
		t.Fatalf("解密密码本失败：%v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应解出 1 条记录，实际 %d", len(list))
	}
	got := list[0]
	if got.Title != "Trae 国际站" || got.Account != "me@a.com" || got.Password != "newpass" || got.Group != "Trae" {
		t.Fatalf("解出的记录字段不对：%+v", got)
	}
	if len(got.History) != 1 || got.History[0].Pwd != "oldpass" || got.History[0].At != 1758800000000 {
		t.Fatalf("解出的历史密码不对：%+v", got.History)
	}
}

// 反向兼容：Go 加密出来的密文库，应当能被 JS 解开。
// 这里只断言布局与 JS 一致（iv 12 字节、密文 = 明文 + 16 字节 tag），
// 真正的跨语言解密验证见 TestJSDecryptsGoBox。
func TestEncryptBoxLayout(t *testing.T) {
	v := loadVector(t)
	key, err := KeyFor(v.Password, PwdRecord{Salt: v.SaltB64, Hash: v.HashB64})
	if err != nil {
		t.Fatalf("派生密钥失败：%v", err)
	}
	plain := []byte("hello 白泽")
	box, err := EncryptBox(key, plain)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	iv, err := base64.StdEncoding.DecodeString(box.IV)
	if err != nil {
		t.Fatalf("iv 解码失败：%v", err)
	}
	if len(iv) != gcmIVLen {
		t.Fatalf("iv 应为 %d 字节，实际 %d", gcmIVLen, len(iv))
	}
	ct, err := base64.StdEncoding.DecodeString(box.Data)
	if err != nil {
		t.Fatalf("data 解码失败：%v", err)
	}
	if len(ct) != len(plain)+16 {
		t.Fatalf("密文长度应为 明文+16(tag)，实际 %d（明文 %d）", len(ct), len(plain))
	}
	back, err := DecryptBox(key, box)
	if err != nil {
		t.Fatalf("自解失败：%v", err)
	}
	if string(back) != string(plain) {
		t.Fatalf("往返不一致：%s != %s", string(back), string(plain))
	}
	// 换把钥匙必须解不开（不是“看起来加密了”）
	wrongKey, err := DeriveKeyPBKDF2("wrong", []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("派生密钥失败：%v", err)
	}
	if _, err := DecryptBox(wrongKey, box); err == nil {
		t.Fatal("用错误密钥竟然解开了，说明加密没生效")
	}
}

func TestVerifyPwdBothPaths(t *testing.T) {
	v := loadVector(t)
	rec := PwdRecord{Salt: v.SaltB64, Hash: v.HashB64}
	ok, err := VerifyPwd(v.Password, rec, &v.Box)
	if err != nil || !ok {
		t.Fatalf("正确密码应校验通过：ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPwd("wrong", rec, &v.Box)
	if err != nil || ok {
		t.Fatalf("错误密码不应通过：ok=%v err=%v", ok, err)
	}

	// 旧版记录（无 hash）：靠能否解密密文库判断
	legacy := PwdRecord{Salt: v.SaltB64}
	ok, err = VerifyPwd(v.Password, legacy, &v.Box)
	if err != nil || !ok {
		t.Fatalf("旧记录用正确密码应通过：ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPwd("wrong", legacy, &v.Box)
	if err != nil || ok {
		t.Fatalf("旧记录用错误密码不应通过：ok=%v err=%v", ok, err)
	}
	if _, err := VerifyPwd("x", PwdRecord{Salt: "不是base64"}, nil); err == nil {
		t.Fatal("损坏的主密码记录应明确报错")
	}
}

func TestNewPwdRecordAndUnlock(t *testing.T) {
	rec, key, err := NewPwdRecord("my-master-pwd")
	if err != nil {
		t.Fatalf("设置主密码失败：%v", err)
	}
	if rec.Hash == "" || rec.Salt == "" {
		t.Fatalf("记录不完整：%+v", rec)
	}
	list := []Pass{{ID: "p1", Title: "GitHub", Account: "a@b.com", Password: "s3cret"}}
	box, err := EncryptVault(key, list)
	if err != nil {
		t.Fatalf("加密密码本失败：%v", err)
	}

	// 用错误密码解锁失败
	if ok, _ := VerifyPwd("bad", rec, &box); ok {
		t.Fatal("错误主密码不应解锁")
	}
	// 正确密码解锁后能拿到密钥并解密
	ok, err := VerifyPwd("my-master-pwd", rec, &box)
	if err != nil || !ok {
		t.Fatalf("正确主密码应解锁：ok=%v err=%v", ok, err)
	}
	k2, err := KeyFor("my-master-pwd", rec)
	if err != nil {
		t.Fatalf("派生密钥失败：%v", err)
	}
	got, err := DecryptVault(k2, box)
	if err != nil {
		t.Fatalf("解密失败：%v", err)
	}
	if len(got) != 1 || got[0].Password != "s3cret" {
		t.Fatalf("解出的密码本不对：%+v", got)
	}
}
