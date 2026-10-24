<!-- 白泽手机端外壳：底栏三 tab（对话 / 知识库 / 其他功能） -->
<script setup>
import { ref, computed } from 'vue';
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
</script>

<template>
  <div class="m-app" :class="{ theme: store.theme }">
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
        <span class="m-nav-icon" v-html="item.icon === 'chat' ? chatIcon : item.icon === 'book' ? bookIcon : gridIcon"></span>
        <span>{{ item.label }}</span>
        <span v-if="item.id === 'more' && pendingCount > 0" class="m-badge">{{ pendingCount }}</span>
      </button>
    </nav>
  </div>
</template>

<script>
const chatIcon = '<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l1.9 4.6L18.5 9.5l-4.6 1.9L12 16l-1.9-4.6L5.5 9.5l4.6-1.9L12 3z"/><path d="M18 16l.9 2.1L21 19l-2.1.9L18 22l-.9-2.1L15 19l2.1-.9L18 16z"/></svg>';
const bookIcon = '<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/><path d="M9 7h6M9 11h4"/></svg>';
const gridIcon = '<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="7" rx="2"/><rect x="14" y="3" width="7" height="7" rx="2"/><rect x="3" y="14" width="7" height="7" rx="2"/><rect x="14" y="14" width="7" height="7" rx="2"/></svg>';
</script>

<style>
:root {
  --bg: #0b0e13; --surface: #12161f; --surface-2: #1a1f2e;
  --line: #232a3a; --line-2: #2d3548;
  --fg: #e8ecf4; --fg-2: #8b94a8; --fg-3: #5a6377;
  --accent: #4c7dff; --accent-2: #3561d4;
  --ok: #34b882; --warn: #e5a53a; --err: #e55656;
  --card-radius: 10px; --nav-h: 56px;
}
[data-theme="light"] {
  --bg: #f5f6f9; --surface: #ffffff; --surface-2: #f0f2f7;
  --line: #e4e7ef; --line-2: #d4d8e4;
  --fg: #1b1f2a; --fg-2: #5b6472; --fg-3: #98a1b0;
  --accent: #3b6ef6; --accent-2: #2a5ad4;
}
* { box-sizing: border-box; }
html, body, #app { height: 100%; margin: 0; }
body {
  background: var(--bg); color: var(--fg);
  font: 14px/1.5 -apple-system, "PingFang SC", "Microsoft YaHei", sans-serif;
  -webkit-font-smoothing: antialiased;
}
.m-app {
  display: flex; flex-direction: column; height: 100vh;
  padding-bottom: env(safe-area-inset-bottom);
}
.m-main {
  flex: 1; overflow-y: auto;
  padding: 12px 14px calc(var(--nav-h) + 8px);
}
/* 底栏 */
.m-bottom-nav {
  position: fixed; bottom: 0; left: 0; right: 0;
  height: var(--nav-h);
  display: flex; align-items: center; justify-content: space-around;
  background: var(--surface);
  border-top: 1px solid var(--line);
  padding-bottom: env(safe-area-inset-bottom);
  z-index: 100;
}
.m-nav-item {
  display: flex; flex-direction: column; align-items: center; gap: 2px;
  font-size: 10px; color: var(--fg-3);
  background: none; border: none; cursor: pointer;
  padding: 4px 16px; border-radius: 8px;
  position: relative;
}
.m-nav-item.on { color: var(--accent); }
.m-nav-item.on .m-nav-icon { color: var(--accent); }
.m-badge {
  position: absolute; top: 0; right: 4px;
  min-width: 16px; height: 16px; padding: 0 4px;
  border-radius: 8px; background: var(--err); color: #fff;
  font-size: 10px; font-weight: 600;
  display: flex; align-items: center; justify-content: center;
}
/* 通用 */
.m-page-head {
  display: flex; align-items: flex-start; justify-content: space-between;
  margin-bottom: 14px;
}
.m-page-title { font-size: 18px; font-weight: 700; margin: 0; }
.m-page-sub { font-size: 12px; color: var(--fg-2); margin: 2px 0 0; }
.m-card {
  background: var(--surface); border: 1px solid var(--line);
  border-radius: var(--card-radius); padding: 12px;
  margin-bottom: 10px;
}
.m-card-title { font-size: 13px; font-weight: 600; color: var(--fg-2); margin-bottom: 8px; }
.m-empty {
  text-align: center; color: var(--fg-3); font-size: 13px;
  padding: 30px 0;
}
.m-btn {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 8px 14px; border-radius: 8px;
  font-size: 13px; font-weight: 500;
  background: var(--accent); color: #fff;
  border: none; cursor: pointer;
}
.m-btn.ghost {
  background: transparent; color: var(--fg-2);
  border: 1px solid var(--line-2);
}
.m-btn.danger { background: var(--err); }
.m-input {
  width: 100%; padding: 10px 12px;
  background: var(--surface-2); border: 1px solid var(--line);
  border-radius: 8px; color: var(--fg); font-size: 14px;
  outline: none;
}
.m-input:focus { border-color: var(--accent); }
.m-label { display: block; font-size: 12px; color: var(--fg-3); margin-bottom: 4px; }
.m-field { margin-bottom: 10px; }
/* toast */
.m-toast {
  position: fixed; top: 16px; left: 50%; transform: translateX(-50%);
  background: var(--surface-2); border: 1px solid var(--line-2);
  border-radius: 8px; padding: 8px 16px;
  font-size: 13px; color: var(--fg);
  z-index: 999; max-width: 90vw;
  animation: m-toast-in .2s ease-out;
}
.m-toast.err { border-color: var(--err); color: var(--err); }
.m-toast.ok { border-color: var(--ok); color: var(--ok); }
@keyframes m-toast-in { from { opacity: 0; transform: translateX(-50%) translateY(-8px); } to { opacity: 1; transform: translateX(-50%); } }
</style>
