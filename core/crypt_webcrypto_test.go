package core

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// jsDecryptScript 用手机 App 同一套 WebCrypto 参数解开 Go 生成的密文库，
// 结果写到标准输出。BOX_FILE 环境变量指定输入文件。
const jsDecryptScript = `
const fs = require('fs');
const { webcrypto: crypto } = require('node:crypto');
(async () => {
  const v = JSON.parse(fs.readFileSync(process.env.BOX_FILE, 'utf8'));
  const enc = new TextEncoder();
  const base = await crypto.subtle.importKey('raw', enc.encode('baize-todo-vault:' + v.password), 'PBKDF2', false, ['deriveKey']);
  const key = await crypto.subtle.deriveKey(
    { name: 'PBKDF2', salt: Buffer.from(v.saltB64, 'base64'), iterations: 150000, hash: 'SHA-256' },
    base, { name: 'AES-GCM', length: 256 }, false, ['decrypt']);
  const pt = await crypto.subtle.decrypt(
    { name: 'AES-GCM', iv: Buffer.from(v.box.iv, 'base64') }, key, Buffer.from(v.box.data, 'base64'));
  process.stdout.write(new TextDecoder().decode(pt));
})().catch(e => { console.error(String(e)); process.exit(1); });
`

// 双向兼容的另一半：Go 加密 → JS 解密。
// 没有 node 时跳过（不影响其它用例）。
//
// 注意：文件名不能叫 *_js_test.go —— 那样 Go 会把它当 GOOS=js 的文件而永不编译。
func TestJSDecryptsGoBox(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("本机没有 node，跳过 Go→JS 解密验证")
	}
	v := loadVector(t)
	key, err := KeyFor(v.Password, PwdRecord{Salt: v.SaltB64, Hash: v.HashB64})
	if err != nil {
		t.Fatalf("派生密钥失败：%v", err)
	}
	plain := "白泽 待办中心 Go 核心 · 双向兼容验证"
	box, err := EncryptBox(key, []byte(plain))
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"password": v.Password,
		"saltB64":  v.SaltB64,
		"box":      box,
	})
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	file := filepath.Join(t.TempDir(), "box.json")
	if err := os.WriteFile(file, payload, 0o644); err != nil {
		t.Fatalf("写临时文件失败：%v", err)
	}

	cmd := exec.Command(node, "-e", jsDecryptScript)
	cmd.Env = append(os.Environ(), "BOX_FILE="+file)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node 解密失败：%v\n输出：%s", err, string(out))
	}
	if strings.TrimSpace(string(out)) != plain {
		t.Fatalf("JS 解出的内容不对：\n期望 %s\n实际 %s", plain, strings.TrimSpace(string(out)))
	}
}

// 确认 Go 生成的密文库结构与 JS 一致（base64 的 iv/data，可被 JSON 直接传递）
func TestEncBoxJSONShape(t *testing.T) {
	v := loadVector(t)
	key, err := KeyFor(v.Password, PwdRecord{Salt: v.SaltB64, Hash: v.HashB64})
	if err != nil {
		t.Fatalf("派生密钥失败：%v", err)
	}
	box, err := EncryptBox(key, []byte("x"))
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	raw, err := json.Marshal(box)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}
	if len(m) != 2 || m["iv"] == "" || m["data"] == "" {
		t.Fatalf("密文库 JSON 结构应为 {iv,data}：%s", string(raw))
	}
	if _, err := base64.StdEncoding.DecodeString(m["iv"]); err != nil {
		t.Fatalf("iv 应为 Base64：%v", err)
	}
}
