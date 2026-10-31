<!-- 白泽手机端外壳：底栏三 tab（对话 / 知识库 / 其他功能） -->
<script setup>
import { ref, computed, watchEffect } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { NAV } from './router.js';
import { useStore } from './store.js';
import { bzCoreStatus } from './api.js';

const route = useRoute();
const router = useRouter();
const store = useStore();

const currentTab = computed(() => {
  const name = route.name || '';
  if (name === 'chat') return 'chat';
  if (['todo', 'vault', 'kb'].includes(name)) return 'kb';
  if (['settings', 'more', 'plan'].includes(name)) return 'more';
  return 'chat';
});

const deviceStatus = ref(bzCoreStatus());

function switchTab(tab) {
  const map = { chat: '/chat', kb: '/kb', more: '/more' };
  router.push(map[tab] || '/chat');
}

const pendingCount = computed(() => store.pendingCount);
const badgeText = computed(() => (pendingCount.value > 99 ? '99+' : String(pendingCount.value)));

// 主题接线：进入页面即按 store 恢复深/浅色
watchEffect(() => {
  const t = store.theme === 'system' ? 'dark' : store.theme;
  document.documentElement.setAttribute('data-theme', t);
});

const ICONS = {
  chat: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3.5l1.6 3.9 3.9 1.6-3.9 1.6L12 14.5l-1.6-3.9L6.5 9l3.9-1.6L12 3.5z"/><path d="M18.5 15l.8 1.9 1.9.8-1.9.8-.8 1.9-.8-1.9-1.9-.8 1.9-.8.8-1.9z"/><path d="M4 15.5h3"/><path d="M4 18.5h5"/></svg>',
  book: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/><path d="M9 7h6M9 11h4"/></svg>',
  grid: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="3.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="3.5" y="13.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="13.5" width="7" height="7" rx="1.5"/></svg>',
};
</script>

<template>
  <div class="m-app">
    <!-- 主内容区 -->
    <main class="m-main">
      <router-view />
    </main>

    <!-- 底栏 -->
    <nav class="m-bottom-nav">
      <button
        v-for="item in NAV"
        :key="item.id"
        :class="['m-nav-item', { on: currentTab === item.id }]"
        @click="switchTab(item.id)"
      >
        <span class="m-nav-bar"></span>
        <span class="m-nav-icon-wrap">
          <span class="m-nav-icon" v-html="ICONS[item.icon] || ICONS.grid"></span>
          <span v-if="item.id === 'more' && pendingCount > 0" class="m-badge">{{ badgeText }}</span>
        </span>
        <span class="m-nav-label">{{ item.label }}</span>
      </button>
    </nav>

    <!-- 全局 toast -->
    <transition name="toast">
      <div v-if="store.toastMsg" :class="['m-toast', store.toastKind]">{{ store.toastMsg }}</div>
    </transition>
  </div>
</template>

<style>
/* ---------- 墨玉 · 东方蓝图 设计令牌 ---------- */
:root {
  --bg: #070807; --bg-elev: #0E100E; --surface: #0E100E;
  --surface-2: #131614; --surface-3: #181C18;
  --line: #1F231F; --line-strong: #2A2F28;
  --fg: #E6E1D6; --fg-2: #A6A296; --fg-3: #6F6C62;
  --accent: #3BE0C4; --accent-2: #7CEBD8;
  --accent-soft: rgba(59, 224, 196, .10); --accent-ink: #05221D;
  --ok: #52C07A; --warn: #D9A45B; --err: #E5765B;
  /* 兼容旧变量 */
  --line-2: var(--line-strong);
  --card-radius: 2px; --ctrl-radius: 2px; --nav-h: 56px;
  --ease-in: cubic-bezier(.2, 0, 0, 1);
  --ease-out: cubic-bezier(.4, 0, 1, 1);
}
[data-theme="light"] {
  --bg: #F2ECE0; --bg-elev: #FAF6EC; --surface: #FAF6EC;
  --surface-2: #EFE8DA; --surface-3: #E4DCCB;
  --line: #E0D7C5; --line-strong: #D1C7B3;
  --fg: #17150F; --fg-2: #55524A; --fg-3: #7B776B;
  --accent: #0B6E63; --accent-2: #0F8A7E;
  --accent-soft: rgba(11, 110, 99, .10); --accent-ink: #FAF6EC;
  --ok: #2F7A45; --warn: #96601F; --err: #B23B27;
}

* { box-sizing: border-box; }
html, body, #app { height: 100%; margin: 0; }
body {
  background: var(--bg);
  color: var(--fg);
  font: 14px/1.55 -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", system-ui, sans-serif;
  font-variant-numeric: tabular-nums;
  -webkit-font-smoothing: antialiased;
  overscroll-behavior-y: none;
}
.m-app {
  display: flex;
  flex-direction: column;
  height: 100vh;
  height: 100dvh;
}
.m-main {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
  padding: 14px 16px calc(var(--nav-h) + env(safe-area-inset-bottom) + 8px);
}

/* ---------- 底栏 ---------- */
.m-bottom-nav {
  position: fixed;
  bottom: 0; left: 0; right: 0;
  height: calc(var(--nav-h) + env(safe-area-inset-bottom));
  padding-bottom: env(safe-area-inset-bottom);
  display: flex;
  align-items: stretch;
  background: var(--surface);
  border-top: 1px solid var(--line);
  z-index: 100;
}
.m-nav-item {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 0;
  padding: 0 4px;
  background: none;
  border: none;
  color: var(--fg-3);
  font-size: 10.5px;
  font-weight: 400;
  cursor: pointer;
  position: relative;
}
.m-nav-item.on { color: var(--accent); font-weight: 600; }
.m-nav-bar {
  position: absolute;
  top: 0; left: 50%;
  width: 18px; height: 2px;
  border-radius: 1px;
  background: var(--accent);
  transform: translateX(-50%);
  opacity: 0;
  transition: opacity 120ms var(--ease-in);
}
.m-nav-item.on .m-nav-bar { opacity: 1; }
.m-nav-icon-wrap {
  width: 44px; height: 44px;
  display: flex;
  align-items: center;
  justify-content: center;
  position: relative;
  color: inherit;
}
.m-nav-icon { display: flex; align-items: center; justify-content: center; }
.m-nav-icon svg { width: 18px; height: 18px; display: block; }
.m-nav-label { line-height: 1.1; }
.m-badge {
  position: absolute;
  top: 3px; right: 3px;
  min-width: 16px; height: 16px;
  padding: 0 4px;
  border-radius: 999px;
  background: var(--err);
  color: #2B0A08;
  font-size: 10px;
  font-weight: 600;
  line-height: 16px;
  text-align: center;
}

/* ---------- 页头 ---------- */
.m-page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}
.m-head-text { min-width: 0; }
.m-page-title {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  line-height: 1.3;
  letter-spacing: -.01em;
}
.m-page-sub {
  margin: 2px 0 0;
  font-size: 12px;
  color: var(--fg-3);
  line-height: 1.4;
}
.m-icon-btn {
  flex: none;
  width: 36px; height: 36px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0;
  border-radius: 4px;
  border: 1px solid var(--line);
  background: var(--surface-2);
  color: var(--fg-2);
  cursor: pointer;
  position: relative;
  transition: background 120ms var(--ease-in), color 120ms var(--ease-in), border-color 120ms var(--ease-in);
}
.m-icon-btn::after { content: ''; position: absolute; inset: -4px; }
.m-icon-btn.accent { background: var(--accent-soft); border-color: transparent; color: var(--accent); }
.m-icon-btn:active { background: var(--surface-3); color: var(--fg); }
.m-icon-btn svg { width: 18px; height: 18px; display: block; }

/* ---------- 卡片 ---------- */
.m-card {
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--card-radius);
  padding: 14px;
  margin-bottom: 10px;
}
.m-card-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--fg-2);
  margin-bottom: 8px;
  line-height: 1.35;
}

/* ---------- 按钮 ---------- */
.m-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 44px;
  padding: 0 16px;
  border-radius: var(--ctrl-radius);
  border: 1px solid transparent;
  background: var(--accent);
  color: var(--accent-ink);
  font-size: 13px;
  font-weight: 600;
  line-height: 1;
  white-space: nowrap;
  cursor: pointer;
  position: relative;
  transition: background 120ms var(--ease-in), border-color 120ms var(--ease-in), color 120ms var(--ease-in);
}
.m-btn::after { content: ''; position: absolute; inset: -4px; }
.m-btn:active { background: var(--accent-2); }
.m-btn:disabled { opacity: .45; cursor: default; }
.m-btn.ghost {
  height: 36px;
  background: transparent;
  border-color: var(--line-strong);
  color: var(--fg-2);
  font-weight: 500;
}
.m-btn.ghost:active { background: var(--surface-2); }
.m-btn.danger {
  background: var(--err);
  color: #2B0A08;
}
.m-btn.danger:active { background: var(--err); opacity: .88; }
.m-btn.sm { height: 36px; padding: 0 14px; font-size: 12px; }
.m-btn.block { width: 100%; }

/* ---------- 输入 ---------- */
.m-input {
  width: 100%;
  height: 44px;
  padding: 0 12px;
  background: var(--surface-2);
  border: 1px solid var(--line);
  border-radius: var(--ctrl-radius);
  color: var(--fg);
  font: inherit;
  font-size: 14px;
  outline: none;
  transition: border-color 120ms var(--ease-in), box-shadow 120ms var(--ease-in);
}
.m-input::placeholder { color: var(--fg-3); }
.m-input:focus { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
textarea.m-input { height: auto; min-height: 76px; padding: 10px 12px; resize: none; line-height: 1.5; }
.m-input-wrap { position: relative; }
.m-input-wrap .m-input { padding-right: 48px; }
.m-input-act {
  position: absolute;
  top: 0; right: 0;
  width: 44px; height: 44px;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: none;
  background: none;
  color: var(--fg-3);
  cursor: pointer;
  border-radius: 0 var(--ctrl-radius) var(--ctrl-radius) 0;
}
.m-input-act:active { color: var(--fg); }
.m-input-act svg { width: 18px; height: 18px; }
.m-label {
  display: block;
  font-size: 11px;
  letter-spacing: .08em;
  color: var(--fg-3);
  margin-bottom: 6px;
}
.m-field { margin-bottom: 12px; }

/* ---------- 空状态 ---------- */
.m-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  text-align: center;
  color: var(--fg-3);
  font-size: 12px;
  padding: 40px 0;
}
.m-empty svg { width: 40px; height: 40px; opacity: .7; }
.m-empty p { margin: 0; }

/* ---------- 底部弹层 ---------- */
.m-sheet-mask {
  position: fixed;
  inset: 0;
  z-index: 300;
  display: flex;
  align-items: flex-end;
  background: rgba(0, 0, 0, .5);
}
.m-sheet-panel {
  width: 100%;
  max-height: 90vh;
  overflow-y: auto;
  background: var(--surface);
  border-top: 1px solid var(--line-strong);
  border-radius: 12px 12px 0 0;
  padding: 10px 16px calc(16px + env(safe-area-inset-bottom));
}
.m-sheet-handle {
  width: 36px; height: 4px;
  margin: 4px auto 12px;
  border-radius: 2px;
  background: var(--line-strong);
}
.m-sheet-title {
  margin: 0 0 14px;
  font-size: 16px;
  font-weight: 600;
  line-height: 1.35;
}
.m-sheet-text {
  margin: 0 0 4px;
  font-size: 13px;
  color: var(--fg-2);
  line-height: 1.55;
  word-break: break-word;
}
.m-sheet-actions {
  display: flex;
  gap: 8px;
  margin-top: 16px;
}
.m-sheet-actions .m-btn { flex: 1; height: 44px; }
.sheet-enter-active, .sheet-leave-active { transition: opacity 250ms var(--ease-in); }
.sheet-enter-active .m-sheet-panel { transition: transform 250ms var(--ease-in); }
.sheet-leave-active .m-sheet-panel { transition: transform 200ms var(--ease-out); }
.sheet-enter-from, .sheet-leave-to { opacity: 0; }
.sheet-enter-from .m-sheet-panel, .sheet-leave-to .m-sheet-panel { transform: translateY(100%); }

/* ---------- 分段控件 ---------- */
.m-seg {
  display: flex;
  padding: 0;
  background: var(--surface-2);
  border: 1px solid var(--line);
  border-radius: var(--ctrl-radius);
  overflow: hidden;
}
.m-seg-btn {
  flex: 1;
  min-width: 0;
  height: 44px;
  padding: 0 8px;
  border: none;
  background: transparent;
  color: var(--fg-2);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  position: relative;
  transition: background 120ms var(--ease-in), color 120ms var(--ease-in);
}
.m-seg-btn.on {
  background: var(--accent-soft);
  color: var(--accent);
  font-weight: 600;
}
.m-seg-btn + .m-seg-btn::before {
  content: '';
  position: absolute;
  left: 0; top: 10px; bottom: 10px;
  width: 1px;
  background: var(--line);
}

/* ---------- 通用小件 ---------- */
.mono { font-family: ui-monospace, "SF Mono", Consolas, monospace; }
.m-divider { height: 1px; background: var(--line); }

/* ---------- toast ---------- */
.m-toast {
  position: fixed;
  top: calc(env(safe-area-inset-top) + 12px);
  left: 50%;
  transform: translateX(-50%);
  z-index: 500;
  max-width: calc(100vw - 32px);
  padding: 10px 14px;
  border-radius: 4px;
  background: var(--surface-2);
  border: 1px solid var(--line-strong);
  color: var(--fg);
  font-size: 13px;
  line-height: 1.4;
  text-align: center;
  box-shadow: 0 8px 24px rgba(0, 0, 0, .35);
}
.m-toast.ok { color: var(--ok); border-color: rgba(79, 209, 139, .45); }
.m-toast.err { color: var(--err); border-color: rgba(240, 115, 106, .45); }
.m-toast.warn { color: var(--warn); border-color: rgba(227, 169, 60, .45); }
.toast-enter-active, .toast-leave-active {
  transition: opacity 200ms var(--ease-in), transform 200ms var(--ease-in);
}
.toast-enter-from, .toast-leave-to {
  opacity: 0;
  transform: translateX(-50%) translateY(-8px);
}
</style>
