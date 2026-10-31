<template>
  <div class="kb-view">
    <header class="m-page-head">
      <div class="m-head-text">
        <h1 class="m-page-title">知识库</h1>
        <p class="m-page-sub">
          <span :class="['kb-status-dot', (store.kbMode && store.kbOnline) ? 'ok' : '']"></span>
          {{ store.kbMode ? (store.kbOnline ? '已连接白泽后端' : '后端离线') : '本机模式' }}
        </p>
      </div>
      <button class="m-icon-btn" title="同步" @click="onSync">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M20 12a8 8 0 1 1-2.4-5.7"/><path d="M20 4v4h-4"/></svg>
      </button>
    </header>

    <!-- 子 tab -->
    <div class="kb-tabs">
      <button
        v-for="t in tabs"
        :key="t.key"
        :class="['kb-tab', tab === t.key ? 'on' : '']"
        @click="tab = t.key"
      >{{ t.label }}</button>
    </div>

    <div class="kb-body">
      <!-- 待办 / 密码本：复用子组件 -->
      <TodoView v-if="tab === 'todo'" embedded />
      <VaultView v-else-if="tab === 'vault'" embedded />

      <!-- 占位 -->
      <div v-else class="m-empty kb-placeholder">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3.5l8.5 4.5L12 12.5 3.5 8 12 3.5z"/><path d="M3.5 12l8.5 4.5 8.5-4.5"/><path d="M3.5 16l8.5 4.5 8.5-4.5"/></svg>
        <p>{{ activeLabel }}模块开发中，敬请期待</p>
        <button class="m-btn ghost" @click="tab = 'todo'">返回待办</button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue';
import TodoView from './TodoView.vue';
import VaultView from './VaultView.vue';
import { useStore } from '../store.js';

const store = useStore();

const tabs = [
  { key: 'todo', label: '待办' },
  { key: 'vault', label: '密码本' },
  { key: 'attach', label: '附件' },
  { key: 'memory', label: '记忆' },
  { key: 'api', label: 'API 服务' },
  { key: 'approve', label: '模型审批' },
];
const tab = ref('todo');

const activeLabel = computed(() => (tabs.find(t => t.key === tab.value) || {}).label || '');

async function onSync() {
  if (!store.kbMode) { store.toast('未连接后端，无法同步', 'err'); return; }
  const r = await store.syncFromRemote();
  if (r.ok) store.toast('同步完成：待办 ' + r.todos + ' 条', 'ok');
  else store.toast(r.error || '同步失败', 'err');
}

onMounted(async () => {
  await store.refreshRemote();
});
</script>

<style scoped>
.kb-status-dot {
  display: inline-block;
  width: 6px; height: 6px;
  margin-right: 6px;
  border-radius: 999px;
  background: var(--fg-3);
  vertical-align: 1px;
}
.kb-status-dot.ok { background: var(--ok); }

.kb-tabs {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
  overflow-x: auto;
  scrollbar-width: none;
}
.kb-tabs::-webkit-scrollbar { display: none; }
.kb-tab {
  flex: none;
  height: 36px;
  padding: 0 14px;
  border-radius: 4px;
  border: 1px solid var(--line);
  background: var(--surface-2);
  color: var(--fg-2);
  font: inherit;
  font-size: 12px;
  white-space: nowrap;
  cursor: pointer;
  position: relative;
  transition: background 120ms var(--ease-in), color 120ms var(--ease-in);
}
.kb-tab::after { content: ''; position: absolute; inset: -4px; }
.kb-tab.on {
  background: var(--accent-soft);
  border-color: transparent;
  color: var(--accent);
  font-weight: 600;
}
.kb-placeholder { padding-bottom: 56px; }
</style>
