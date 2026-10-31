<!-- 任务与审批页：列出后端任务 + 待审批（替换旧 renderTasks）。
     数据源 /api/state（tasks 列表）。批量审批走全局 ApprovalModal，这里只做查看。 -->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue';
import { NButton } from 'naive-ui';
import { API, fmtTime } from '../api.js';

const tasks = ref([]);
const loading = ref(true);
let timer = null;

const pendingCount = computed(() => tasks.value.filter(t => t.status === 'pending_approval').length);

async function load() {
  const r = await API.get('/api/state');
  if (r.ok) tasks.value = (r.data && r.data.tasks) || [];
  loading.value = false;
}
onMounted(() => { load(); timer = setInterval(load, 12000); });
onBeforeUnmount(() => timer && clearInterval(timer));

const STATUS_META = {
  pending_approval: { label: '待审批', tone: 'warn' },
  running: { label: '执行中', tone: 'accent' },
  done: { label: '已完成', tone: 'ok' },
  failed: { label: '失败', tone: 'err' },
  rejected: { label: '已驳回', tone: 'mute' },
};
function statusOf(t) {
  return STATUS_META[t.status] || { label: t.status || '未知', tone: 'mute' };
}
</script>

<template>
  <div class="bz-page task-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">任务与审批</h1>
        <p class="bz-sub">跨端任务会先停在审批队列，确认后才真正下发到设备。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" @click="load">刷新</NButton>
      </div>
    </header>

    <section class="bz-card task-card">
      <div class="task-card-head">
        <span class="task-card-title">任务</span>
        <span v-if="pendingCount > 0" class="task-pending"><i class="bz-dot"></i>{{ pendingCount }} 条待审批</span>
      </div>
      <div class="task-cols">
        <span class="task-col-id">ID</span>
        <span class="task-col-action">动作</span>
        <span class="task-col-device">目标设备</span>
        <span class="task-col-status">状态</span>
        <span class="task-col-time">创建时间</span>
      </div>

      <div class="task-list">
        <div v-for="t in tasks" :key="t.id" class="task-row">
          <span class="task-col-id task-mono">{{ t.id }}</span>
          <span class="task-col-action">{{ t.action || '—' }}</span>
          <span class="task-col-device task-mono">{{ t.deviceId || '—' }}</span>
          <span class="task-col-status" :class="'tone-' + statusOf(t).tone">
            <i class="bz-dot"></i>{{ statusOf(t).label }}
          </span>
          <span class="task-col-time task-mono">{{ fmtTime(t.createdAt) }}</span>
        </div>
      </div>

      <div v-if="loading && !tasks.length" class="task-hint">正在读取任务…</div>
      <div v-if="!loading && !tasks.length" class="bz-empty">
        <svg class="bz-empty-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor"
             stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17Z" />
          <path d="M8.5 12l2.4 2.4 4.6-4.8" />
        </svg>
        <div class="bz-empty-title">还没有任务</div>
        <div class="bz-empty-desc">在对话里让白泽操作设备，任务会出现在这里；需要确认的会标为待审批。</div>
        <NButton size="small" @click="$router.push('/chat')">去对话</NButton>
      </div>
    </section>
  </div>
</template>

<style>
.task-page { display: flex; flex-direction: column; }
.task-card { padding: 0; overflow: hidden; }
.task-card-head {
  display: flex; align-items: center; justify-content: space-between;
  padding: 14px 16px; border-bottom: 1px solid var(--line);
}
.task-card-title { font-size: 13px; font-weight: 600; color: var(--fg); }
.task-pending { display: inline-flex; align-items: center; gap: 6px; font-size: 11px; color: var(--warn); letter-spacing: .04em; }
.task-pending .bz-dot { background: var(--warn); }
.task-cols,
.task-row {
  display: grid;
  grid-template-columns: 96px minmax(120px, 1.1fr) minmax(130px, 1fr) 116px 132px;
  align-items: center; gap: 12px;
}
.task-cols { padding: 10px 16px; border-bottom: 1px solid var(--line); }
.task-cols span { font-size: 11px; color: var(--fg-3); letter-spacing: .08em; }
.task-row {
  min-height: 44px; padding: 0 16px; border-bottom: 1px solid var(--line);
  transition: background 120ms cubic-bezier(.2,0,0,1);
}
.task-row:last-child { border-bottom: 0; }
.task-row:hover { background: var(--surface-2); }
.task-row > span { font-size: 13px; color: var(--fg); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.task-mono { font-family: var(--mono); font-size: 12px; color: var(--fg-2); font-variant-numeric: tabular-nums; }
.task-col-status { display: inline-flex; align-items: center; gap: 6px; }
.task-col-status .bz-dot { width: 6px; height: 6px; }
.tone-warn { color: var(--warn); }
.tone-warn .bz-dot { background: var(--warn); }
.tone-accent { color: var(--accent); }
.tone-accent .bz-dot { background: var(--accent); }
.tone-ok { color: var(--ok); }
.tone-ok .bz-dot { background: var(--ok); }
.tone-err { color: var(--err); }
.tone-err .bz-dot { background: var(--err); }
.tone-mute { color: var(--fg-3); }
.tone-mute .bz-dot { background: var(--fg-3); }
.task-hint { padding: 20px 16px; font-size: 12px; color: var(--fg-3); }
</style>
