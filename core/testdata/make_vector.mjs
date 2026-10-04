// 用手机 App（WebCrypto 版）的同一套算法生成兼容性测试向量，
// 供 Go 侧断言「同一份密码本在两端都能解开」。
//
// 用法：node make_vector.mjs   （直接把 testdata/vector.json 写出来，避免 shell 重定向带 BOM）
import { webcrypto as crypto } from 'node:crypto';
import { writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const enc = new TextEncoder();
const b64 = (buf) => Buffer.from(new Uint8Array(buf)).toString('base64');

const password = 'p@ss-测试-123';
const salt = Buffer.from('0123456789abcdef', 'utf8'); // 固定 16 字节盐
const iv = Buffer.from('abcdefghijkl', 'utf8'); // 固定 12 字节 IV

// 与 todo-app/js/store.js 的 derive / hashOf / encryptBytes 完全一致
const base = await crypto.subtle.importKey('raw', enc.encode('baize-todo-vault:' + password), 'PBKDF2', false, ['deriveBits', 'deriveKey']);
const bits = await crypto.subtle.deriveBits({ name: 'PBKDF2', salt, iterations: 150000, hash: 'SHA-256' }, base, 256);
const key = await crypto.subtle.deriveKey(
  { name: 'PBKDF2', salt, iterations: 150000, hash: 'SHA-256' },
  base, { name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt']
);

const plaintext = JSON.stringify([{
  title: 'Trae 国际站', account: 'me@a.com', password: 'newpass',
  history: [{ pwd: 'oldpass', at: 1758800000000 }], group: 'Trae',
}]);
const ct = await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, key, enc.encode(plaintext));

const out = {
  note: '由 make_vector.mjs 用 WebCrypto 生成，勿手改',
  password,
  saltB64: b64(salt),
  hashB64: b64(bits),
  ivB64: b64(iv),
  box: { iv: b64(iv), data: b64(ct) },
  plaintext,
};
const here = dirname(fileURLToPath(import.meta.url));
writeFileSync(join(here, 'vector.json'), JSON.stringify(out, null, 2) + '\n', 'utf8');
console.log('vector.json 已生成');
