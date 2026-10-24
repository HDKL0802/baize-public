/* 白泽手机端 API 层
   桥接原生 BzNative（Android WebView 注入）+ 本地 HTTP 服务（LocalServer）
   H5 模式下走 fetch；APK 模式下走 BzNative.bzCoreCall（绕开跨源限制） */
'use strict';

const nb = () => (window.BzNative && typeof window.BzNative.bzCoreCall === 'function')
  ? window.BzNative : null;

/** 调后端 API：APK 走原生桥，H5 走 fetch */
async function call(path, method = 'GET', body = null) {
  const b = nb();
  if (b) {
    let raw = '';
    try {
      raw = b.bzCoreCall(path, method, body == null ? null : (typeof body === 'string' ? body : JSON.stringify(body)));
    } catch (e) {
      return { ok: false, error: '调内核失败：' + (e.message || e) };
    }
    if (!raw) return { ok: false, error: '内核没有响应（可能还没起来）' };
    let out;
    try { out = JSON.parse(raw); } catch { out = { raw }; }
    if (out.ok === undefined && out.error) return { ok: false, error: out.error };
    return out;
  }
  // H5 模式：直接 fetch（LocalServer 同 origin）
  try {
    const opts = { method, headers: { 'Content-Type': 'application/json' } };
    if (body != null) opts.body = typeof body === 'string' ? body : JSON.stringify(body);
    const resp = await fetch(path, opts);
    const text = await resp.text();
    let data;
    try { data = JSON.parse(text); } catch { data = text; }
    if (resp.ok) return { ok: true, data };
    return { ok: false, error: data && data.error ? data.error : resp.status + ' ' + resp.statusText };
  } catch (e) {
    return { ok: false, error: '网络错误：' + (e.message || e) };
  }
}

/** 取后端知识库快照（待办 + 密码本） */
function remoteState() { return call('/api/state', 'GET'); }

/** 往后端知识库发一条操作 */
function remoteOp(op, args) { return call('/api/op', 'POST', JSON.stringify({ op, args: args || {} })); }

/** 设备连接状态 */
function deviceStatus() { return call('/api/device', 'GET'); }

/** 探测后端通不通 */
function probeRemote() { return call('/api/remote', 'GET'); }

/** Agent 运行 */
function agentRun(goal, wait, provider) {
  const body = { goal, wait: !!wait };
  if (provider) body.provider = provider;
  return call('/api/agent/run', 'POST', JSON.stringify(body));
}
function agentState() { return call('/api/agent/state', 'GET'); }
function agentProviders() { return call('/api/agent/providers', 'GET'); }
function agentRunDetail(id) { return call('/api/agent/runs/' + encodeURIComponent(id), 'GET'); }
function agentCommands() { return call('/api/agent/commands', 'GET'); }
function agentApprovals() { return call('/api/agent/approvals', 'GET'); }
function agentApprove(id) { return call('/api/agent/approvals/' + encodeURIComponent(id) + '/approve', 'POST', JSON.stringify({ by: '手机' })); }
function agentReject(id, reason) { return call('/api/agent/approvals/' + encodeURIComponent(id) + '/reject', 'POST', JSON.stringify({ by: '手机', reason: reason || '' })); }
function agentSetActiveProvider(name) { return call('/api/agent/providers', 'POST', JSON.stringify({ action: 'active', name })); }

/** 内核进程状态（APK 专属） */
function bzCoreStatus() {
  const b = nb();
  if (!b) return { native: false, supported: false };
  try { return Object.assign(JSON.parse(b.bzCoreStatus() || '{}'), { native: true }); }
  catch { return { native: true, error: '解析失败' }; }
}

/** 配置后端地址 + 令牌并重启内核 */
function bzCoreConfigure(server, pairToken, enabled) {
  const b = nb();
  if (!b) return { native: false, supported: false, error: '当前入口不是手机 App' };
  try { return JSON.parse(b.bzCoreConfigure(server || '', pairToken || '', !!enabled)); }
  catch { return { native: true, error: '解析失败' }; }
}

/** 原生附件相关 */
function pickAttachment(source) {
  const b = nb();
  if (b && b.pickAttachment) { try { b.pickAttachment(source); return true; } catch { return false; } }
  return false;
}
function pickAttachmentFor(source) {
  const b = nb();
  if (b && b.pickAttachmentFor) { try { b.pickAttachmentFor(source); return true; } catch { return false; } }
  return false;
}
function takeAttachmentFor(dest) {
  const b = nb();
  if (!b) return '';
  try {
    if (b.takeAttachmentFor) return b.takeAttachmentFor(dest) || '';
    if (b.takeAttachment) return b.takeAttachment() || '';
  } catch { }
  return '';
}
function attachPath(path) {
  const b = nb();
  if (b && b.attachPath) { try { b.attachPath(path); return true; } catch { return false; } }
  return false;
}
function hasAllFiles() {
  const b = nb();
  if (!b) return false;
  try { return !!b.hasAllFiles && b.hasAllFiles(); } catch { return false; }
}
function openAllFilesSetting() {
  const b = nb();
  if (b && b.openAllFilesSetting) { try { b.openAllFilesSetting(); } catch { } }
}

/** 通知相关 */
function notificationsEnabled() {
  const b = nb();
  if (!b) return false;
  try { return b.notificationsEnabled && b.notificationsEnabled(); } catch { return false; }
}
function requestNotificationPermission() {
  const b = nb();
  if (b && b.requestNotificationPermission) { try { b.requestNotificationPermission(); } catch { } }
}
function syncReminders(json) {
  const b = nb();
  if (b && b.syncReminders) { try { b.syncReminders(json); } catch { } }
}
function notifyNow(title, body) {
  const b = nb();
  if (b && b.notifyNow) { try { b.notifyNow(title, body); } catch { } }
}

/** 文件导入导出 */
function saveTextFile(name, content) {
  const b = nb();
  if (b && b.saveTextFile) { try { return b.saveTextFile(name, content) || ''; } catch { } }
  // H5 回退：用 <a download>
  const blob = new Blob([content], { type: 'text/markdown' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url; a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
  return '浏览器下载/' + name;
}
function pickTextFile() {
  const b = nb();
  if (b && b.pickTextFile) { try { b.pickTextFile(); return; } catch { } }
  // H5 回退
  const input = document.createElement('input');
  input.type = 'file';
  input.accept = '*/*';
  input.addEventListener('change', async () => {
    const f = input.files && input.files[0];
    if (!f) return;
    const text = await f.text();
    window.__bzFileContent && window.__bzFileContent(f.name, text);
  });
  input.click();
}
function takePickedText() {
  const b = nb();
  if (b && b.takePickedText) { try { return b.takePickedText() || ''; } catch { } }
  return '';
}

/** 附件操作（APK 专属） */
function openAttachment(path) {
  const b = nb();
  if (b && b.openAttachment) { try { b.openAttachment(path); } catch { } }
}
function saveAttachment(path) {
  const b = nb();
  if (b && b.saveAttachment) { try { b.saveAttachment(path); } catch { } }
}
function deleteAttachment(path) {
  const b = nb();
  if (b && b.deleteAttachment) { try { b.deleteAttachment(path); } catch { } }
}
function readAttachmentBase64(path) {
  const b = nb();
  if (b && b.readAttachmentBase64) { try { return b.readAttachmentBase64(path) || ''; } catch { } }
  return '';
}

/** KB 附件上传/下载/删除（走内核代理到 NAS） */
function kbUploadAttachment(path, name, kind, mime) {
  const b = nb();
  if (!b || !b.kbUploadAttachment) return { ok: false, error: 'APK 专属' };
  try { return JSON.parse(b.kbUploadAttachment(path, name, kind, mime) || '{}'); }
  catch { return { ok: false, error: '解析失败' }; }
}
function kbFetchAttachment(fileId, name) {
  const b = nb();
  if (!b || !b.kbFetchAttachment) return '';
  try { return b.kbFetchAttachment(fileId, name) || ''; } catch { return ''; }
}
function kbDeleteAttachment(fileId, localPath) {
  const b = nb();
  if (!b || !b.kbDeleteAttachment) return { ok: false, error: 'APK 专属' };
  try { return JSON.parse(b.kbDeleteAttachment(fileId, localPath) || '{}'); }
  catch { return { ok: false, error: '解析失败' }; }
}

/** 震动 */
function vibrate(ms) {
  const b = nb();
  if (b && b.vibrate) { try { b.vibrate(ms); } catch { } }
}

/** 格式化时间 */
function fmtTime(ts) {
  const d = new Date(ts);
  const p = (n) => String(n).padStart(2, '0');
  return p(d.getHours()) + ':' + p(d.getMinutes());
}

/** 格式化文件大小 */
function fmtSize(n) {
  if (!n) return '未知大小';
  return n > 1024 * 1024 ? (n / 1024 / 1024).toFixed(1) + 'MB' : Math.max(1, Math.round(n / 1024)) + 'KB';
}

/** uid */
function uid() {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

export {
  call, remoteState, remoteOp, deviceStatus, probeRemote,
  agentRun, agentState, agentProviders, agentRunDetail, agentCommands,
  agentApprovals, agentApprove, agentReject, agentSetActiveProvider,
  bzCoreStatus, bzCoreConfigure,
  pickAttachment, pickAttachmentFor, takeAttachmentFor,
  attachPath, hasAllFiles, openAllFilesSetting,
  notificationsEnabled, requestNotificationPermission, syncReminders, notifyNow,
  saveTextFile, pickTextFile, takePickedText,
  openAttachment, saveAttachment, deleteAttachment, readAttachmentBase64,
  kbUploadAttachment, kbFetchAttachment, kbDeleteAttachment,
  vibrate, fmtTime, fmtSize, uid,
};
