/* 白泽浮窗 API 层（本机回环，直接 fetch） */
'use strict';

const SESSION_KEY = 'bz_session';
const session = () => { try { return localStorage.getItem(SESSION_KEY) || ''; } catch { return ''; }; }

/** 调后端 API（经 Go 壳代理 /api/be/*） */
async function api(path, method, body) {
  const headers = {};
  if (body) headers['Content-Type'] = 'application/json';
  const s = session();
  if (s) headers['X-Baize-Session'] = s;
  try {
    const r = await fetch('/api/be' + path, {
      method: method || 'GET',
      headers,
      body: body ? JSON.stringify(body) : undefined,
    });
    const t = await r.text();
    let d = {};
    try { d = t ? JSON.parse(t) : {}; } catch { d = { raw: t }; }
    if (!r.ok) return { ok: false, error: (d && d.error) || ('HTTP ' + r.status) };
    return { ok: true, data: d };
  } catch (e) {
    return { ok: false, error: String((e && e.message) || e) };
  }
}

/** 调本机浮窗接口（/api/local/float/*） */
async function local(path, body) {
  try {
    const r = await fetch('/api/local' + path, body
      ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
      : {});
    return await r.json();
  } catch (e) {
    return { ok: false, error: String(e) };
  }
}

/** 读当前截图（base64） */
async function imageBase64() {
  try {
    const r = await fetch('/api/local/float/image', { cache: 'no-store' });
    if (!r.ok) return '';
    const buf = new Uint8Array(await r.arrayBuffer());
    let s = '';
    const CH = 0x8000;
    for (let i = 0; i < buf.length; i += CH) s += String.fromCharCode.apply(null, buf.subarray(i, i + CH));
    return btoa(s);
  } catch { return ''; }
}

/** 调视觉接口（提取 / 翻译） */
async function visionText(action, lang) {
  const b64 = await imageBase64();
  if (!b64) return null;
  const body = { action, imageBase64: b64 };
  if (action === 'translate' && lang) body.lang = lang;
  const r = await api('/api/agent/vision', 'POST', body);
  if (!r.ok) return { error: r.error || '未知错误' };
  const d = r.data || {};
  const inner = d.data || d;
  return { text: inner.text || '', model: inner.model || '', ms: inner.latencyMs || 0 };
}

export { api, local, session, imageBase64, visionText, SESSION_KEY };
