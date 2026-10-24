<template>
  <div class="kb-view">
    <header class="m-page-head">
      <div>
        <h1 class="m-page-title">知识库</h1>
        <p class="m-page-sub">
          {{ store.kbMode ? (store.kbOnline ? '已连接白泽后端' : '后端离线') : '本机模式' }}
        </p>
      </div>
      <button class="m-btn ghost" @click="onSync">同步</button>
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
      <div v-else class="m-card kb-placeholder">
        <div class="m-card-title">{{ activeLabel }}</div>
        <div class="kb-placeholder-text">该模块开发中，敬请期待</div>
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
.kb-tabs {
  display: flex; gap: 6px; margin-bottom: 12px;
  overflow-x: auto; scrollbar-width: none;
}
.kb-tabs::-webkit-scrollbar { display: none; }
.kb-tab {
  flex: none; padding: 6px 14px; border-radius: 16px;
  font-size: 12px; background: var(--surface);
  border: 1px solid var(--line); color: var(--fg-2); cursor: pointer;
}
.kb-tab.on { background: var(--accent); border-color: var(--accent); color: #fff; }

.kb-placeholder-text { font-size: 13px; color: var(--fg-3); padding: 10px 0; }

/* 嵌入模式下去掉子组件自带的大标题区，避免重复 */
:deep(.embedded > header) { margin-bottom: 8px; }
</style>
