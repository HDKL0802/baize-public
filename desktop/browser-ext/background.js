// 白泽浏览器控制 · background service worker
//
// 职责：连本机白泽桌面端（ws://127.0.0.1:47121/ext/ws），收到 browser.* 动作后，
// 用 chrome.debugger 在当前活动标签页执行，再把结果回传。
//
// 安全：只连 127.0.0.1；地址与令牌由用户在扩展弹窗里填（令牌在白泽设置页生成）。
// 调试权限是 Chrome 明确弹条提示的（"白泽浏览器控制 started debugging this browser"）。

let ws = null;
let retryTimer = null;
let lastStatus = '';

function setStatus(s) {
  if (s === lastStatus) return;
  lastStatus = s;
  chrome.storage.local.set({ status: s });
}

function scheduleRetry() {
  if (retryTimer) clearTimeout(retryTimer);
  retryTimer = setTimeout(() => { retryTimer = null; ensureConnected(false); }, 3000);
}

async function ensureConnected(force) {
  const st = await chrome.storage.local.get(['wsUrl', 'token']);
  if (!st.wsUrl || !st.token) return;
  if (!force && ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;
  if (ws) { try { ws.close(); } catch (e) {} ws = null; }
  const url = st.wsUrl + (st.wsUrl.indexOf('?') >= 0 ? '&' : '?') + 'token=' + encodeURIComponent(st.token);
  try {
    ws = new WebSocket(url);
  } catch (e) {
    setStatus('地址不合法：' + String(e && e.message || e));
    scheduleRetry();
    return;
  }
  ws.onopen = () => setStatus('ok');
  ws.onclose = () => { setStatus(''); scheduleRetry(); };
  ws.onerror = () => setStatus('连不上本机服务（白泽桌面端开着吗？端口/令牌对吗？）');
  ws.onmessage = (ev) => { handle(ev.data); };
}

function reply(obj) {
  try { if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(obj)); } catch (e) {}
}

async function handle(raw) {
  let msg;
  try { msg = JSON.parse(raw); } catch (e) { return; }
  if (!msg || !msg.id || !msg.action) return;
  try {
    const result = await run(msg.action, msg.args || {});
    reply({ id: msg.id, ok: true, result: result });
  } catch (e) {
    reply({ id: msg.id, ok: false, error: String((e && e.message) || e) });
  }
}

/* ---------------- 动作 ---------------- */

async function run(action, args) {
  const tab = await activeTab();
  switch (action) {
    case 'status':
      return { url: tab.url || '', title: tab.title || '', tabId: tab.id };
    case 'open': {
      const url = normalizeUrl(args.url);
      await chrome.tabs.update(tab.id, { url: url });
      await sleep(900);
      const t2 = await chrome.tabs.get(tab.id);
      return { url: t2.url || url, title: t2.title || '' };
    }
    case 'read': {
      const info = await evalJS(tab.id,
        `JSON.stringify({url: location.href, title: document.title,
          text: document.body ? (document.body.innerText || '') : ''})`);
      let obj = {};
      try { obj = JSON.parse(info || '{}'); } catch (e) {}
      return obj;
    }
    case 'click':
      return await doClick(tab.id, args);
    case 'type':
      return await doType(tab.id, args);
    case 'key':
      return await doKey(tab.id, args);
    case 'screenshot': {
      const r = await cdp(tab.id, 'Page.captureScreenshot', { format: 'png' });
      if (!r || !r.data) throw new Error('截图失败（浏览器没给数据）');
      return { pngBase64: r.data };
    }
    default:
      throw new Error('扩展不支持的动作：' + action);
  }
}

async function activeTab() {
  const tabs = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  if (!tabs || !tabs.length) throw new Error('没有活动标签页');
  return tabs[0];
}

function normalizeUrl(raw) {
  let u = String(raw || '').trim();
  if (!u) throw new Error('缺少 url');
  if (u.indexOf('://') < 0) u = 'https://' + u;
  if (!/^https?:\/\//i.test(u)) throw new Error('只支持 http/https 地址：' + u);
  return u;
}

async function attach(tabId) {
  try {
    await chrome.debugger.attach({ tabId: tabId }, '1.3');
  } catch (e) {
    const s = String((e && e.message) || e);
    if (!/already attached/i.test(s)) throw new Error('拿不到标签页调试权限：' + s);
  }
}

async function cdp(tabId, method, params) {
  await attach(tabId);
  return await chrome.debugger.sendCommand({ tabId: tabId }, method, params || {});
}

async function evalJS(tabId, expression) {
  const r = await cdp(tabId, 'Runtime.evaluate', {
    expression: expression, returnByValue: true, awaitPromise: true,
  });
  if (r && r.exceptionDetails) {
    const d = r.exceptionDetails;
    throw new Error((d.exception && d.exception.description) || d.text || '页面执行出错');
  }
  return r && r.result ? r.result.value : undefined;
}

async function doClick(tabId, args) {
  const sel = String(args.selector || '');
  const txt = String(args.text || '');
  const js = `(() => {
    const sel = ${JSON.stringify(sel)};
    const txt = ${JSON.stringify(txt)};
    let el = sel ? document.querySelector(sel) : null;
    if (!el && txt) {
      const all = document.querySelectorAll('a,button,input,textarea,[role=button],[onclick]');
      for (let i = 0; i < all.length; i++) {
        const s = (all[i].innerText || all[i].value || '').trim();
        if (s === txt) { el = all[i]; break; }
      }
      if (!el) {
        for (let i = 0; i < all.length; i++) {
          const s = (all[i].innerText || all[i].value || '').trim();
          if (s && s.indexOf(txt) >= 0) { el = all[i]; break; }
        }
      }
    }
    if (!el) return 'NONE';
    el.scrollIntoView({block:'center', inline:'center'});
    el.click();
    return 'OK';
  })()`;
  const v = await evalJS(tabId, js);
  if (v !== 'OK') throw new Error('页面上没找到要点的元素（selector/text）');
  return { clicked: sel || txt };
}

async function doType(tabId, args) {
  const sel = String(args.selector || '');
  const text = args.text == null ? '' : String(args.text);
  const submit = !!args.submit;
  const js = `(() => {
    const sel = ${JSON.stringify(sel)};
    const text = ${JSON.stringify(text)};
    const el = sel ? document.querySelector(sel) : document.activeElement;
    if (!el) return 'NONE';
    el.focus();
    const setter = Object.getOwnPropertyDescriptor(el.constructor.prototype, 'value');
    if (setter && setter.set) setter.set.call(el, text); else el.value = text;
    el.dispatchEvent(new Event('input', {bubbles:true}));
    el.dispatchEvent(new Event('change', {bubbles:true}));
    return 'OK';
  })()`;
  const v = await evalJS(tabId, js);
  if (v !== 'OK') throw new Error('页面上没找到输入框（selector）');
  if (submit) {
    await pressCombo(tabId, 'enter');
  }
  return { typed: sel || '(active)', chars: text.length, submitted: submit };
}

const MOD = {
  ctrl: { vk: 17, key: 'Control', mod: 2 },
  control: { vk: 17, key: 'Control', mod: 2 },
  shift: { vk: 16, key: 'Shift', mod: 8 },
  alt: { vk: 18, key: 'Alt', mod: 1 },
  meta: { vk: 91, key: 'Meta', mod: 4 },
  win: { vk: 91, key: 'Meta', mod: 4 },
};

const KEYS = {
  enter: { vk: 13, key: 'Enter', text: '\r' },
  esc: { vk: 27, key: 'Escape' },
  escape: { vk: 27, key: 'Escape' },
  tab: { vk: 9, key: 'Tab' },
  space: { vk: 32, key: ' ', text: ' ' },
  backspace: { vk: 8, key: 'Backspace' },
  delete: { vk: 46, key: 'Delete' },
  del: { vk: 46, key: 'Delete' },
  up: { vk: 38, key: 'ArrowUp' },
  down: { vk: 40, key: 'ArrowDown' },
  left: { vk: 37, key: 'ArrowLeft' },
  right: { vk: 39, key: 'ArrowRight' },
  home: { vk: 36, key: 'Home' },
  end: { vk: 35, key: 'End' },
  pageup: { vk: 33, key: 'PageUp' },
  pagedown: { vk: 34, key: 'PageDown' },
};
for (let i = 1; i <= 12; i++) KEYS['f' + i] = { vk: 111 + i, key: 'F' + i };

async function doKey(tabId, args) {
  const raw = String(args.keys || '').trim();
  if (!raw) throw new Error('缺少参数 keys（如 enter / ctrl+s；多组用逗号串）');
  const combos = raw.split(',').map(s => s.trim()).filter(Boolean);
  for (const c of combos) await pressCombo(tabId, c);
  return { keys: raw, combos: combos };
}

async function pressCombo(tabId, combo) {
  const parts = combo.split('+').map(s => s.trim().toLowerCase()).filter(Boolean);
  if (!parts.length) return;
  const keyName = parts.pop();
  const mods = [];
  let modifiers = 0;
  for (const p of parts) {
    const m = MOD[p];
    if (!m) throw new Error('不认识的修饰键：' + p);
    mods.push(m);
    modifiers |= m.mod;
  }
  const main = KEYS[keyName] || (keyName.length === 1
    ? { vk: keyName.toUpperCase().charCodeAt(0), key: keyName, text: keyName }
    : null);
  if (!main) throw new Error('不认识的键：' + keyName);

  for (const m of mods) {
    await cdp(tabId, 'Input.dispatchKeyEvent', {
      type: 'keyDown', key: m.key, windowsVirtualKeyCode: m.vk, nativeVirtualKeyCode: m.vk, modifiers: modifiers,
    });
  }
  await cdp(tabId, 'Input.dispatchKeyEvent', {
    type: 'keyDown', key: main.key, text: main.text || undefined,
    windowsVirtualKeyCode: main.vk, nativeVirtualKeyCode: main.vk, modifiers: modifiers,
  });
  await cdp(tabId, 'Input.dispatchKeyEvent', {
    type: 'keyUp', key: main.key,
    windowsVirtualKeyCode: main.vk, nativeVirtualKeyCode: main.vk, modifiers: modifiers,
  });
  for (let i = mods.length - 1; i >= 0; i--) {
    const m = mods[i];
    await cdp(tabId, 'Input.dispatchKeyEvent', {
      type: 'keyUp', key: m.key, windowsVirtualKeyCode: m.vk, nativeVirtualKeyCode: m.vk,
      modifiers: modifiers & ~m.mod,
    });
  }
  await sleep(60);
}

function sleep(ms) { return new Promise(resolve => setTimeout(resolve, ms)); }

/* ---------------- 生命周期 ---------------- */

chrome.alarms.create('baize-keepalive', { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener(() => ensureConnected(false));
chrome.runtime.onStartup.addListener(() => ensureConnected(true));
chrome.runtime.onInstalled.addListener(() => ensureConnected(true));
chrome.storage.onChanged.addListener((changes) => {
  if (changes.wsUrl || changes.token) ensureConnected(true);
});
chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (msg && msg.type === 'reconnect') {
    ensureConnected(true).then(() => sendResponse({ ok: true })).catch(() => sendResponse({ ok: false }));
    return true;
  }
});

ensureConnected(true);
