<template>
  <div class="more-view">
    <header class="m-page-head">
      <div>
        <h1 class="m-page-title">其他功能</h1>
        <p class="m-page-sub">AI 排期 · 运行记录 · 审批 · 更多</p>
      </div>
    </header>

    <!-- 待审批入口 -->
    <div v-if="pendingCount > 0" class="m-card pending-card">
      <div class="m-card-title">模型审批（{{ pendingCount }} 条待处理）</div>
      <div class="pending-actions">
        <button class="m-btn" @click="onRefreshApprovals">刷新</button>
        <button class="m-btn ghost" @click="router.push('/more?tab=approve')">处理审批</button>
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
        <div class="feature-sub">{{ f.sub }}</div>
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
  const base = '<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">';
  const ends = '</svg>';
  switch (kind) {
    case 'plan':
      return base + '<rect x="3" y="4" width="18" height="17" rx="2"/><path d="M3 9h18M8 2v4M16 2v4"/>' + ends;
    case 'runs':
      return base + '<path d="M22 12a10 10 0 1 1-10-10"/><path d="M12 6v6l4 2"/>' + ends;
    case 'approve':
      return base + '<path d="M20 6L9 17l-5-5"/>' + ends;
    case 'voice':
      return base + '<path d="M12 3v18M7 8v8M17 8v8M3 11v2M21 11v2"/>' + ends;
    case 'export':
      return base + '<path d="M12 3v12M7 8l5-5 5 5M4 21h16"/>' + ends;
    default:
      return base + '<circle cx="12" cy="12" r="9"/>' + ends;
  }
}
</script>

<style scoped>
.more-view { display: flex; flex-direction: column; }

.pending-card { border-color: var(--warn); }
.pending-actions { display: flex; gap: 8px; }

.feature-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 10px;
}
.feature-card {
  display: flex; flex-direction: column; align-items: flex-start; gap: 6px;
  padding: 14px 12px;
  background: var(--surface); border: 1px solid var(--line);
  border-radius: var(--card-radius);
  color: var(--fg); text-align: left; cursor: pointer;
}
.feature-card:active { border-color: var(--accent); }
.feature-icon { color: var(--accent); display: flex; }
.feature-label { font-size: 14px; font-weight: 600; }
.feature-sub { font-size: 11px; color: var(--fg-3); line-height: 1.4; }
</style>
