// 白泽 API 层（从旧 apps.js 的 window.API 移植成模块）。
// 走本机服务 /api/be/*（Go 壳代理到 NAS，令牌不进网页）；
// 本机侧接口 /api/local/* 直接打 Go 壳。

/* 登录会话（多用户）：后端按它决定"以谁的身份访问数据"。
   存 localStorage，只在 /api/be 的请求头上带（配对令牌永远不进网页）。 */
const SESSION_KEY = 'bz_session';
export function sessionToken() {
  try { return localStorage.getItem(SESSION_KEY) || ''; } catch (e) { return ''; }
}
export function setSessionToken(t) {
  try { t ? localStorage.setItem(SESSION_KEY, t) : localStorage.removeItem(SESSION_KEY); } catch (e) { /* 忽略 */ }
}

export const API = {
  async call(path, method, body) {
    const headers = {};
    if (body) headers['Content-Type'] = 'application/json';
    const s = sessionToken();
    if (s) headers['X-Baize-Session'] = s;
    let r;
    try {
      r = await fetch('/api/be' + path, {
        method: method || 'GET',
        headers,
        body: body ? JSON.stringify(body) : undefined,
      });
    } catch (e) {
      return { ok: false, error: '调本机服务失败：' + (e && e.message) };
    }
    const text = await r.text();
    let data = {};
    try { data = text ? JSON.parse(text) : {}; } catch (e) { data = { raw: text }; }
    if (!r.ok) return { ok: false, error: (data && data.error) || ('HTTP ' + r.status), data };
    return { ok: true, data };
  },
  get(p) { return this.call(p, 'GET'); },
  post(p, b) { return this.call(p, 'POST', b); },

  /* ---- 多用户：登录 / 登出 / 当前身份 ---- */
  async login(name, password) {
    const r = await this.post('/api/agent/auth/login', { name, password });
    if (r.ok && r.data && r.data.token) setSessionToken(r.data.token);
    return r;
  },
  async logout() {
    try { await this.post('/api/agent/auth/logout', {}); } catch (e) { /* 登出失败也要清本地 */ }
    setSessionToken('');
  },
  whoami() { return this.get('/api/agent/auth/whoami'); },

  /* ---- 本机侧（Go 壳直接提供，不含 /api/be 代理） ---- */
  async localConfig() {
    try {
      const r = await (await fetch('/api/local/config')).json();
      if (r && r.config) return Object.assign({ ok: r.ok !== false }, r.config);
      return r && typeof r === 'object' ? r : { ok: false };
    } catch (e) { return { ok: false, error: String(e) }; }
  },
  async localGet(path) {
    try { return await (await fetch('/api/local' + path)).json(); }
    catch (e) { return { ok: false, error: String(e) }; }
  },
  async localPost(path, body) {
    try {
      return await (await fetch('/api/local' + path, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body || {}),
      })).json();
    } catch (e) { return { ok: false, error: String(e) }; }
  },
};

/* 通用小工具 */
export function fileToBase64(file) {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onerror = () => reject(new Error('读文件失败'));
    fr.onload = () => {
      const s = String(fr.result || '');
      const i = s.indexOf(',');
      resolve(i >= 0 ? s.slice(i + 1) : s);
    };
    fr.readAsDataURL(file);
  });
}
export function downloadBase64(name, b64) {
  const bin = atob(b64);
  const buf = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
  const url = URL.createObjectURL(new Blob([buf]));
  const a = document.createElement('a');
  a.href = url; a.download = name || 'download';
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 4000);
}
export function fmtTime(ts) {
  if (!ts) return '—';
  const d = new Date(ts), p = n => String(n).padStart(2, '0');
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
export async function localDeviceInfo() {
  try { const r = await (await fetch('/api/local/device')).json(); return (r && r.device) || {}; }
  catch (e) { return {}; }
}
export async function localDeviceId() { return (await localDeviceInfo()).deviceId || ''; }
