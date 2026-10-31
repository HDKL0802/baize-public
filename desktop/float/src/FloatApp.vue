<!-- 浮窗主壳：顶栏 + 子组件容器 + 全局主题/轮询/事件总线 -->
<script setup>
import { ref, provide, onMounted, onBeforeUnmount, nextTick } from 'vue';
import { local, session, SESSION_KEY } from './api.js';
import ChatMode from './components/ChatMode.vue';
import TextMode from './components/TextMode.vue';
import DictateMode from './components/DictateMode.vue';
import SubtitleMode from './components/SubtitleMode.vue';
import CallMode from './components/CallMode.vue';

/* ---------- 全局 CSS 变量：墨玉 · 东方蓝图（四端统一） ---------- */
const globalCSS = `
:root {
  color-scheme: dark;
  --bg:#070807; --surface:#0E100E; --surface-2:#131614; --surface-3:#181C18;
  --line:#1F231F; --line-strong:#2A2F28;
  --text:#E6E1D6; --text-2:#A6A296; --text-3:#6F6C62;
  --accent:#3BE0C4; --accent-2:#7CEBD8; --accent-soft:rgba(59,224,196,.10); --accent-ink:#05221D;
  --ok:#52C07A; --warn:#D9A45B; --err:#E5765B;
  /* 旧变量名兼容（沿用 --border / --text-2 等写法） */
  --border:var(--line); --border-strong:var(--line-strong);
  --fg-2:var(--text-2); --fg-3:var(--text-3);
}
html[data-theme=light] {
  color-scheme: light;
  --bg:#F2ECE0; --surface:#FAF6EC; --surface-2:#EFE8DA; --surface-3:#E4DCCB;
  --line:#E0D7C5; --line-strong:#D1C7B3;
  --text:#17150F; --text-2:#55524A; --text-3:#7B776B;
  --accent:#0B6E63; --accent-2:#0F8A7E; --accent-soft:rgba(11,110,99,.10); --accent-ink:#FAF6EC;
  /* 浅色下语义色压深，保证白底可读 */
  --ok:#2F7A45; --warn:#96601F; --err:#B23B27;
}
* { box-sizing: border-box; }
[hidden] { display: none !important; }
html, body { height: 100%; }
body {
  margin: 0; background: var(--bg); color: var(--text);
  font: 13px/1.55 -apple-system,"Segoe UI","Microsoft YaHei",system-ui,sans-serif;
  overflow: hidden; -webkit-font-smoothing: antialiased;
}
#app { display: flex; flex-direction: column; height: 100vh; }
::selection { background: var(--accent-soft); }
::-webkit-scrollbar { width: 8px; height: 8px; }
::-webkit-scrollbar-thumb { background: var(--line-strong); border-radius: 4px; }
::-webkit-scrollbar-track { background: transparent; }
/* 通用：线性图标 / 等宽数字 / 空状态提示 */
.ic {
  display: block; width: 16px; height: 16px; flex: 0 0 auto;
  fill: none; stroke: currentColor; stroke-width: 1.6;
  stroke-linecap: round; stroke-linejoin: round;
}
.mono { font-family: ui-monospace,Consolas,monospace; font-variant-numeric: tabular-nums; }
.hint { font-size: 12px; color: var(--text-3); line-height: 1.55; }
`;

const mode = ref('chat');
const winTitle = ref('白泽浮窗');
const pinned = ref(false);
const hasImage = ref(false);
const rev = ref(0);
const curLang = ref('zh');         // translate 模式用，'zh' | 'en'
const showLangBtn = ref(false);     // 仅 translate 模式显示
const headHidden = ref(false);      // subtitle 模式隐藏顶栏

/* ---------- 提供给子组件的事件总线 ---------- */
function emit(type, payload) {
  window.dispatchEvent(new CustomEvent('float-event', { detail: { type, ...payload } }));
}
provide('bus', { emit, rev, lang: curLang, setLang });

/* ---------- 主题跟随 ---------- */
let curThemePref = '';
function applyTheme(pref) {
  let t = pref;
  if (pref === 'system') {
    t = (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches) ? 'dark' : 'light';
  }
  document.documentElement.setAttribute('data-theme', t || 'dark');
  curThemePref = pref;
}
async function initTheme() {
  try {
    const c = await local('/config');
    applyTheme((c && (c.config?.theme || c.theme)) || 'system');
  } catch { applyTheme('system'); }
  // 4 秒对一次主题变了没（用户可能在设置页改了）
  setInterval(async () => {
    try {
      const c = await local('/config');
      const pref = (c && (c.config?.theme || c.theme)) || 'system';
      if (pref !== curThemePref) applyTheme(pref);
    } catch {}
  }, 4000);
}

/* ---------- 顶栏按钮动作 ---------- */
async function togglePin() {
  const r = await local('/float/pin', {});
  pinned.value = !!(r && r.pinned);
}
async function closeWin() {
  await local('/float/close', {});
}
async function toggleLang() {
  curLang.value = curLang.value === 'zh' ? 'en' : 'zh';
  emit('lang', { lang: curLang.value });
}
function setLang(l) {
  curLang.value = l;
  emit('lang', { lang: l });
}

/* ---------- 拖动（顶栏按下去每帧发 dx/dy 给原生） ---------- */
let dragging = false, lastX = 0, lastY = 0;
function onHeadDown(e) {
  if (e.target.closest('.ibtn')) return; // 按钮区不拖
  dragging = true;
  lastX = e.clientX; lastY = e.clientY;
  document.body.classList.add('dragging');
  e.preventDefault();
}
function onHeadMove(e) {
  if (!dragging) return;
  const dx = e.clientX - lastX, dy = e.clientY - lastY;
  if (dx !== 0 || dy !== 0) {
    local('/float/move', { dx, dy });
    lastX = e.clientX; lastY = e.clientY;
  }
}
function onHeadUp() {
  if (!dragging) return;
  dragging = false;
  document.body.classList.remove('dragging');
  local('/float/snap', {});
}

/* ---------- rev 轮询（1500ms） ---------- */
let revTimer = 0;
function startRevPoll() {
  revTimer = setInterval(async () => {
    try {
      const s = await local('/float/mode');
      if (!s || !s.ok) return;
      if ((s.rev | 0) !== rev.value) {
        rev.value = s.rev | 0;
        hasImage.value = !!s.hasImage;
        emit('rev', { rev: rev.value });
      }
    } catch {}
  }, 1500);
}

/* ---------- call 模式 ctx 轮询 ---------- */
let ctxRev = 0, ctxTimer = 0;
function startCtxPoll() {
  ctxTimer = setInterval(async () => {
    try {
      const c = await local('/float/ctx');
      if (!c || !c.ok) return;
      if ((c.rev | 0) !== ctxRev) {
        ctxRev = c.rev | 0;
        emit('ctx', { ctx: c.ctx, rev: ctxRev });
      }
    } catch {}
  }, 1500);
}

/* ---------- boot 流程 ---------- */
async function boot() {
  // 1. 读 pin 状态
  try {
    const st = await local('/float/state');
    pinned.value = !!(st && st.pinned);
  } catch {}

  // 2. 解析 hash 得 mode
  const hash = window.location.hash || '';
  const m = hash.match(/mode=(\w+)/);
  const parsed = (m && m[1]) || 'chat';
  // 校验白名单（防路径注入）
  const known = ['chat', 'extract', 'translate', 'dictate', 'subtitle', 'call'];
  mode.value = known.includes(parsed) ? parsed : 'chat';

  // 3. 读后端 mode 接口覆盖标题 / rev / hasImage
  try {
    const info = await local('/float/mode');
    if (info) {
      if (info.title) { winTitle.value = info.title; document.title = info.title; }
      rev.value = (info.rev | 0) || 0;
      hasImage.value = !!info.hasImage;
    }
  } catch {}

  // 4. 模式特异
  if (mode.value === 'subtitle') {
    headHidden.value = true;
  } else if (mode.value === 'translate') {
    showLangBtn.value = true;
  }

  // 5. 启动轮询
  startRevPoll();
  if (mode.value === 'call') startCtxPoll();

  await nextTick();
}

onMounted(async () => {
  // 注入全局 CSS
  const sty = document.createElement('style');
  sty.textContent = globalCSS;
  document.head.appendChild(sty);

  await initTheme();
  await boot();
});

onBeforeUnmount(() => {
  if (revTimer) clearInterval(revTimer);
  if (ctxTimer) clearInterval(ctxTimer);
});
</script>

<template>
  <!-- 顶栏（subtitle 模式隐藏） -->
  <header v-show="!headHidden" class="fhead"
          @pointerdown="onHeadDown"
          @pointermove="onHeadMove"
          @pointerup="onHeadUp"
          @pointercancel="onHeadUp"
          @pointerleave="onHeadUp">
    <span class="ftitle">{{ winTitle }}</span>
    <div class="fbtns">
      <!-- translate 模式的语言切换 -->
      <button v-if="showLangBtn" class="ibtn lang" :title="curLang === 'zh' ? '切换到英文' : '切回中文'"
              @click.stop="toggleLang">{{ curLang === 'zh' ? '中' : 'EN' }}</button>
      <!-- 图钉 -->
      <button :class="['ibtn', { on: pinned }]" :title="pinned ? '取消钉住' : '钉住窗口（锁死不可拖）'"
              @click.stop="togglePin">
        <svg class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M12 17v5" />
          <path d="M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z" />
        </svg>
      </button>
      <!-- 关闭 -->
      <button class="ibtn" title="关闭浮窗" @click.stop="closeWin">
        <svg class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M18 6 6 18" /><path d="m6 6 12 12" />
        </svg>
      </button>
    </div>
  </header>

  <!-- 内容区：按 mode 切子组件 -->
  <main class="fbody">
    <ChatMode    v-if="mode === 'chat'" />
    <TextMode    v-else-if="mode === 'extract'" :mode="'extract'" />
    <TextMode    v-else-if="mode === 'translate'" :mode="'translate'" />
    <DictateMode v-else-if="mode === 'dictate'" />
    <SubtitleMode v-else-if="mode === 'subtitle'" />
    <CallMode    v-else-if="mode === 'call'" />
  </main>
</template>

<style>
body.dragging { cursor: move; }
/* 顶栏：surface 底 + 底部 1px hairline */
.fhead {
  height: 40px; flex: 0 0 40px; display: flex; align-items: center; gap: 8px;
  padding: 0 8px 0 12px; background: var(--surface);
  border-bottom: 1px solid var(--line);
  user-select: none; cursor: default;
}
.ftitle {
  flex: 1; min-width: 0; font-size: 12.5px; font-weight: 600; color: var(--text-2);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.fbtns { flex: 0 0 auto; display: flex; align-items: center; gap: 8px; }
.ibtn {
  width: 30px; height: 30px; display: grid; place-items: center;
  padding: 0; border: 0; border-radius: 4px;
  background: transparent; color: var(--text-3); cursor: pointer;
  transition: background 120ms cubic-bezier(.2,0,0,1), color 120ms cubic-bezier(.2,0,0,1);
}
.ibtn:hover { background: var(--surface-2); color: var(--text); }
.ibtn:active { background: var(--surface-3); }
/* 图钉激活：accent 图标 + accent-soft 底 */
.ibtn.on { color: var(--accent); background: var(--accent-soft); }
.ibtn.on:hover { color: var(--accent); background: var(--accent-soft); }
/* 中 / EN 语言切换：文字按钮，等宽数字排版 */
.ibtn.lang {
  width: auto; min-width: 30px; padding: 0 7px;
  font-size: 11px; font-weight: 600; letter-spacing: .04em;
  font-family: ui-monospace,Consolas,monospace; color: var(--text-2);
}
.ibtn.lang:hover { color: var(--text); }
.fbody { flex: 1; min-height: 0; display: flex; flex-direction: column; }
</style>
