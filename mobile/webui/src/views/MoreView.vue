<template>
  <div class="more-view">
    <header class="m-page-head">
      <div class="m-head-text">
        <h1 class="m-page-title">其他功能</h1>
        <p class="m-page-sub">AI 排期 · 运行记录 · 审批 · 更多</p>
      </div>
      <button class="m-icon-btn" title="刷新审批" @click="onRefreshApprovals">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M20 12a8 8 0 1 1-2.4-5.7"/><path d="M20 4v4h-4"/></svg>
      </button>
    </header>

    <!-- 待审批入口 -->
    <div v-if="pendingCount > 0" class="m-card pending-card">
      <div class="pending-head">
        <span class="pending-dot"></span>
        <span class="pending-title">模型审批</span>
        <span class="pending-count">{{ pendingCount }}</span>
      </div>
      <div class="pending-text">有 {{ pendingCount }} 条模型调用等待你批准</div>
      <div class="pending-actions">
        <button class="m-btn sm" @click="router.push('/more?tab=approve')">处理审批</button>
        <button class="m-btn ghost" @click="onRefreshApprovals">刷新</button>
      </div>
    </div>

    <!-- 功能卡片网格 -->
    <div class="feature-grid">
      <button
        v-for="f in features"
        :key="f.key"
        class="feature-card"
        @click="onOpen(f)"
      >
        <div class="feature-icon" v-html="f.icon"></div>
        <div class="feature-label">{{ f.label }}</div>
      </button>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue';
import { useRouter, useRoute } from 'vue-router';
import { useStore } from '../store.js';
import { agentApprovals } from '../api.js';

const store = useStore();
const router = useRouter();
const route = useRoute();

const features = [
  {
    key: 'plan', label: 'AI 排期', sub: '按策略生成今日/本周执行顺序',
    icon: iconOf('plan'),
  },
  {
    key: 'runs', label: '运行记录', sub: '查看白泽 agent 历史运行',
    icon: iconOf('runs'),
  },
  {
    key: 'approve', label: '模型审批', sub: '批准 / 拒绝 agent 的模型调用',
    icon: iconOf('approve'),
  },
  {
    key: 'voice', label: '语音设置', sub: '通知、震动、语音播报',
    icon: iconOf('voice'),
  },
  {
    key: 'export', label: '数据导出', sub: '导出 / 导入 Markdown 备份',
    icon: iconOf('export'),
  },
];

const pendingCount = computed(() => store.pendingCount);

function onOpen(f) {
  switch (f.key) {
    case 'plan': router.push('/plan'); break;
    case 'runs':
      store.toast('运行记录开发中，敬请期待', '');
      break;
    case 'approve':
      store.toast('请在设置页查看待审批项', '');
      onRefreshApprovals();
      break;
    case 'voice':
      router.push('/settings');
      break;
    case 'export':
      router.push('/settings');
      break;
  }
}

async function onRefreshApprovals() {
  const r = await agentApprovals();
  if (r.ok) {
    const pending = (r.data && r.data.list) || r.data || [];
    const n = Array.isArray(pending) ? pending.length : 0;
    store.pendingCount = n;
    store.toast(n ? '待审批 ' + n + ' 条' : '没有待审批', n ? 'warn' : 'ok');
  } else {
    store.toast('取审批列表失败：' + (r.error || ''), 'err');
  }
}

onMounted(onRefreshApprovals);

function iconOf(kind) {
  const base = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">';
  const ends = '</svg>';
  switch (kind) {
    case 'plan':
      return base + '<rect x="3.5" y="4" width="17" height="16.5" rx="2"/><path d="M3.5 9h17M8 2v4M16 2v4"/><path d="M8 13h3M8 16.5h6"/>' + ends;
    case 'runs':
      return base + '<circle cx="12" cy="12" r="8.5"/><path d="M12 7v5l3 2"/>' + ends;
    case 'approve':
      return base + '<circle cx="12" cy="12" r="8.5"/><path d="M8 12l2.7 2.7L16 9.5"/>' + ends;
    case 'voice':
      return base + '<path d="M12 4v16M7.5 8.5v7M16.5 8.5v7M3.5 11v2M20.5 11v2"/>' + ends;
    case 'export':
      return base + '<path d="M12 4v11M7.5 8.5L12 4l4.5 4.5M4 19.5h16"/>' + ends;
    default:
      return base + '<circle cx="12" cy="12" r="8.5"/>' + ends;
  }
}
</script>

<style scoped>
.more-view { display: flex; flex-direction: column; }

.pending-card { border-color: var(--line); }
.pending-head { display: flex; align-items: center; gap: 8px; }
.pending-dot {
  width: 6px; height: 6px;
  border-radius: 999px;
  background: var(--warn);
}
.pending-title { font-size: 12px; font-weight: 600; color: var(--fg-2); }
.pending-count {
  margin-left: auto;
  min-width: 20px;
  height: 18px;
  padding: 0 6px;
  border-radius: 999px;
  background: var(--accent-soft);
  color: var(--accent);
  font-size: 11px;
  font-weight: 600;
  line-height: 18px;
  text-align: center;
}
.pending-text { margin: 8px 0 12px; font-size: 13px; color: var(--fg-2); }
.pending-actions { display: flex; gap: 8px; }

.feature-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 10px;
}
.feature-card {
  height: 88px;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  justify-content: center;
  gap: 3px;
  padding: 12px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 6px;
  color: var(--fg);
  text-align: left;
  cursor: pointer;
  transition: border-color 120ms var(--ease-in), background 120ms var(--ease-in);
}
.feature-card:active { border-color: var(--accent); background: var(--accent-soft); }
.feature-icon { display: flex; color: var(--accent); }
.feature-icon svg { width: 20px; height: 20px; display: block; }
.feature-label { font-size: 13px; font-weight: 600; line-height: 1.3; }
</style>
