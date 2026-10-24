<!-- 任务与审批页：列出后端任务 + 待审批（替换旧 renderTasks）。
     数据源 /api/state（tasks 列表）。批量审批走全局 ApprovalModal，这里只做查看。 -->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue';
import { NCard, NTable, NTag, NSpace } from 'naive-ui';
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
  pending_approval: { label: '待审批', type: 'warning' },
  running: { label: '执行中', type: 'info' },
  done: { label: '已完成', type: 'success' },
  failed: { label: '失败', type: 'error' },
  rejected: { label: '已驳回', type: 'default' },
};
function statusOf(t) {
  return STATUS_META[t.status] || { label: t.status || '未知', type: 'default' };
}

const columns = [
  { title: 'ID', key: 'id', width: 90 },
  { title: '动作', key: 'action', width: 140 },
  { title: '目标设备', key: 'deviceId', width: 130 },
  { title: '状态', key: 'status', width: 100, render: (t) => statusOf(t).label },
  { title: '创建时间', key: 'createdAt', render: (t) => fmtTime(t.createdAt) },
];
</script>

<template>
  <n-card :title="'任务与审批' + (pendingCount ? '（' + pendingCount + ' 待审批）' : '')" size="small">
    <n-table :columns="columns" :rows="tasks" :loading="loading" :bordered="true" :single-line="false" />
  </n-card>
</template>
