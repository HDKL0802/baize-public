<!-- 白泽桌面主控台外壳：左侧导航 + 顶栏 + 内容区（Naive UI 实现，替换旧 shell.html） -->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { NAV, VIEW_META } from './router';
import { useConnStore } from './store/conn.js';
import ApprovalModal from './components/ApprovalModal.vue';

const route = useRoute();
const router = useRouter();
const conn = useConnStore();
const currentTitle = computed(() => (route.meta && route.meta.title) || '—');
const currentId = computed(() => route.name ? String(route.name) : '');

function openApp(id) { router.push('/' + id); }

// 待审批角标：跟随 conn 轮询的 pendingCount
const pendingCount = computed(() => conn.pendingCount);

onMounted(() => { conn.start(); });
onBeforeUnmount(() => { conn.stop(); });
</script>

<template>
  <div class="bz-layout" :class="{ mini: conn.mini }">
    <aside class="bz-sidebar">
      <div class="bz-brand">
        <svg class="bz-mark" viewBox="0 0 32 32" xmlns="http://www.w3.org/2000/svg">
          <rect width="32" height="32" rx="7" fill="var(--accent)"/>
          <text x="16" y="21" text-anchor="middle" font-size="14" font-weight="700" fill="#fff">白</text>
        </svg>
        <div class="bz-brand-text"><b>白泽</b><span>个人智能体</span></div>
      </div>

      <nav class="bz-nav">
        <template v-for="g in NAV" :key="g.title">
          <div class="bz-nav-grp">{{ g.title }}</div>
          <a
            v-for="id in g.ids"
            :key="id"
            :class="['bz-nav-item', { on: currentId === id }]"
            @click.prevent="openApp(id)"
          >
            <span class="bz-nav-n">{{ VIEW_META[id] || id }}</span>
            <span v-if="id === 'tasks' && pendingCount > 0" class="bz-badge">{{ pendingCount }}</span>
          </a>
        </template>
      </nav>

      <div class="bz-sidefoot">
        <span class="bz-pill" :class="{ err: !conn.connected }">
          <i class="bz-dot" :class="{ on: conn.connected, err: !conn.connected }"></i>
          <span>{{ conn.connected ? conn.statusText : '连不上后端' }}</span>
        </span>
        <span class="bz-mono bz-server">{{ conn.serverText }}</span>
      </div>
    </aside>

    <main class="bz-main">
      <header class="bz-topbar">
        <div class="bz-crumb"><b>{{ currentTitle }}</b></div>
        <div class="bz-topbar-right">
          <span
            v-if="pendingCount > 0"
            class="bz-pill bz-approval"
            @click="openApp('tasks')"
            title="待人工放行的跨端任务"
          >{{ pendingCount }} 待审批</span>
          <span class="bz-mono bz-clock">{{ conn.clock }}</span>
        </div>
      </header>
      <div class="bz-view">
        <router-view />
      </div>
    </main>

    <!-- 危险操作审批弹窗（置顶，全局可用） -->
    <ApprovalModal />
  </div>
</template>

<style>
:root, [data-theme="light"] {
  --bg: #f7f8fb; --panel: #ffffff; --line: #e4e7ef;
  --fg: #1b1f2a; --fg2: #5b6472; --fg3: #98a1b0;
  --accent: #3b6ef6; --warn: #d98b1f; --err: #d64545; --ok: #2f9e6f;
  --mono: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, monospace;
}
[data-theme="dark"] {
  --bg: #0e1015; --panel: #151924; --line: #232a38;
  --fg: #e6e9f0; --fg2: #9aa3b2; --fg3: #5d6675;
  --accent: #5b86ff; --warn: #e0a33c; --err: #e06b6b; --ok: #4cc08c;
}
* { box-sizing: border-box; }
html, body, #app { height: 100%; margin: 0; }
body { background: var(--bg); color: var(--fg); font: 14px/1.5 system-ui, "PingFang SC", "Microsoft YaHei", sans-serif; }

.bz-layout { display: flex; height: 100vh; }
.bz-sidebar {
  width: 232px; min-width: 232px; display: flex; flex-direction: column;
  background: var(--panel); border-right: 1px solid var(--line);
}
.bz-main { flex: 1; display: flex; flex-direction: column; min-width: 0; }

/* 品牌区 */
.bz-brand { display: flex; align-items: center; gap: 10px; padding: 14px 16px; }
.bz-mark { width: 30px; height: 30px; border-radius: 8px; }
.bz-brand-text b { display: block; font-size: 15px; }
.bz-brand-text span { font-size: 11px; color: var(--fg3); }

/* 导航 */
.bz-nav { flex: 1; overflow-y: auto; padding: 4px 10px 10px; }
.bz-nav-grp { padding: 12px 8px 4px; font-size: 11px; color: var(--fg3); letter-spacing: .05em; }
.bz-nav-item {
  display: flex; align-items: center; justify-content: space-between;
  padding: 7px 10px; border-radius: 8px; color: var(--fg2); cursor: pointer;
  text-decoration: none; margin-bottom: 1px;
}
.bz-nav-item:hover { background: color-mix(in srgb, var(--fg) 5%, transparent); color: var(--fg); }
.bz-nav-item.on { background: color-mix(in srgb, var(--accent) 14%, transparent); color: var(--accent); font-weight: 600; }
.bz-badge {
  min-width: 18px; height: 18px; padding: 0 5px; border-radius: 9px;
  background: var(--err); color: #fff; font-size: 11px; font-weight: 600;
  display: inline-flex; align-items: center; justify-content: center;
}

/* 侧栏底部 */
.bz-sidefoot { padding: 10px 14px; border-top: 1px solid var(--line); display: flex; flex-direction: column; gap: 6px; }
.bz-pill {
  display: inline-flex; align-items: center; gap: 6px; padding: 4px 8px;
  border-radius: 999px; background: color-mix(in srgb, var(--fg) 4%, transparent);
  font-size: 12px; color: var(--fg2);
}
.bz-pill.err { color: var(--err); }
.bz-pill.bz-approval { background: color-mix(in srgb, var(--warn) 14%, transparent); color: var(--warn); cursor: pointer; }
.bz-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--fg3); }
.bz-dot.on { background: var(--ok); }
.bz-dot.err { background: var(--err); }
.bz-mono { font-family: var(--mono); font-size: 11px; color: var(--fg3); }
.bz-server { word-break: break-all; }

/* 顶栏 */
.bz-topbar {
  display: flex; align-items: center; justify-content: space-between;
  height: 48px; padding: 0 18px; border-bottom: 1px solid var(--line);
  background: var(--panel);
}
.bz-crumb b { font-size: 15px; }
.bz-topbar-right { display: flex; align-items: center; gap: 10px; }
.bz-clock { font-family: var(--mono); font-size: 13px; color: var(--fg2); }

/* 内容区 */
.bz-view { flex: 1; overflow: auto; padding: 18px; }

/* 迷你小窗（原生侧把主窗口切成置顶小窗时，藏掉侧栏与顶栏） */
.bz-layout.mini .bz-sidebar, .bz-layout.mini .bz-topbar { display: none; }
</style>
