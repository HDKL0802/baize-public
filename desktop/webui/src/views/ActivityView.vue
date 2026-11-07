<!-- 活动追踪页：直接消费 /api/agent/activity（后端 AlwaysOn 追踪器已实现）。
     进程内状态、不落盘；页面每 3 秒轮询一次，显示"现在在干什么"和最近轨迹。 -->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue';
import { NButton } from 'naive-ui';
import { API, fmtTime } from '../api.js';

const snap = ref({});
const loading = ref(true);
const autoRefresh = ref(true);
const toastMsg = ref('');

const STAGE_TEXT = {
  idle: '空闲',
  running: '开始运行',
  thinking: '正在思考',
  retrying: '模型重试中',
  tool: '正在调工具',
  tool_done: '工具返回',
  tool_error: '工具报错',
  blocked: '被审批拦下',
  compressing: '压缩上下文',
  checkpoint: '打快照',
  memory: '写入记忆',
  done: '本轮完成',
  error: '出错收尾',
};

function stageText(s) { return STAGE_TEXT[s] || s || '未知'; }
function stageTone(s) {
  if (['done', 'tool_done', 'memory', 'checkpoint'].includes(s)) return 'ok';
  if (['error', 'tool_error', 'blocked'].includes(s)) return 'err';
  if (['thinking', 'tool', 'running', 'retrying', 'compressing'].includes(s)) return 'warn';
  return '';
}

async function load() {
  const r = await API.get('/api/agent/activity');
  loading.value = false;
  if (!r.ok) { toastMsg.value = '读取活动失败：' + (r.error || '未知'); return; }
  snap.value = r.data || {};
}

const state = computed(() => {
  const s = snap.value || {};
  if (!s.active) return { label: '空闲', tone: '', desc: '当前没有正在跑的任务。' };
  return { label: stageText(s.stage), tone: stageTone(s.stage), desc: s.detail || '正在工作…' };
});

const stats = computed(() => {
  const st = (snap.value && snap.value.stats) || {};
  return [
    { label: '累计运行', value: st.runs || 0 },
    { label: '工具调用', value: st.toolCalls || 0 },
    { label: '审批拦截', value: st.blocked || 0 },
    { label: '错误', value: st.errors || 0 },
  ];
});

const steps = computed(() => ((snap.value && snap.value.steps) || []).slice().reverse());

let timer = null;
function restart() {
  clearInterval(timer);
  if (autoRefresh.value) timer = setInterval(load, 3000);
}
onMounted(() => { load(); restart(); });
onBeforeUnmount(() => clearInterval(timer));
</script>

<template>
  <div class="bz-page act-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">活动追踪</h1>
        <p class="bz-sub">白泽始终记录自己在做什么；状态只留在后端进程内，重启后从空闲重新开始。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :class="{ on: autoRefresh }" @click="autoRefresh = !autoRefresh; restart()">
          {{ autoRefresh ? '自动刷新：开' : '自动刷新：关' }}
        </NButton>
        <NButton size="small" :loading="loading" @click="load">刷新</NButton>
      </div>
    </header>

    <section class="bz-card act-live">
      <div class="act-live-mark" :class="state.tone"></div>
      <div class="act-live-main">
        <div class="act-live-label">{{ state.label }}</div>
        <div class="act-live-desc">{{ state.desc }}</div>
        <div class="act-live-meta act-mono">
          档位 {{ snap.level || 'always_on' }}
          <span v-if="snap.runId"> · 运行 {{ snap.runId }}</span>
          <span v-if="snap.updatedAtMs"> · 更新于 {{ fmtTime(snap.updatedAtMs) }}</span>
          <span v-if="snap.sinceMs"> · 持续 {{ Math.max(0, Math.round((Date.now() - snap.sinceMs) / 1000)) }} 秒</span>
        </div>
      </div>
    </section>

    <div class="act-stats">
      <div v-for="s in stats" :key="s.label" class="act-stat">
        <span class="act-stat-v">{{ s.value }}</span>
        <span class="act-stat-l">{{ s.label }}</span>
      </div>
    </div>

    <section class="bz-card act-steps">
      <div class="act-steps-head">
        <h2 class="act-sec-title">最近轨迹</h2>
        <span class="act-count">{{ steps.length }} 条</span>
      </div>
      <div v-if="!steps.length" class="bz-empty">
        <div class="bz-empty-title">还没有活动记录</div>
        <div class="bz-empty-desc">去「对话」发一条任务，回来这里就能看到白泽的思考与工具调用轨迹。</div>
      </div>
      <div v-else class="act-timeline">
        <div v-for="(s, i) in steps" :key="i" class="act-step">
          <span class="act-dot" :class="stageTone(s.stage)"></span>
          <div class="act-step-main">
            <div class="act-step-head">
              <b class="act-step-stage" :class="stageTone(s.stage)">{{ stageText(s.stage) }}</b>
              <span class="act-step-time act-mono">{{ fmtTime(s.at) }}</span>
            </div>
            <div v-if="s.detail" class="act-step-detail">{{ s.detail }}</div>
            <div v-if="s.runId" class="act-step-run act-mono">run {{ s.runId }}</div>
          </div>
        </div>
      </div>
    </section>

    <div v-if="toastMsg" class="bz-toast err">{{ toastMsg }}</div>
  </div>
</template>

<style>
.act-page { display: flex; flex-direction: column; }
.act-live { display: flex; gap: 14px; align-items: flex-start; }
.act-live-mark { width: 8px; height: 8px; flex: none; border-radius: 50%; margin-top: 7px; background: var(--fg-3); }
.act-live-mark.ok { background: var(--ok); }
.act-live-mark.warn { background: var(--warn); box-shadow: 0 0 0 4px color-mix(in srgb, var(--warn) 14%, transparent); }
.act-live-mark.err { background: var(--err); }
.act-live-label { font-size: 15px; font-weight: 600; color: var(--fg); }
.act-live-desc { margin-top: 3px; font-size: 12px; color: var(--fg-2); word-break: break-word; }
.act-live-meta { margin-top: 6px; font-size: 10px; color: var(--fg-3); }
.act-stats { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1px; background: var(--line); border: 1px solid var(--line); margin: 14px 0; }
.act-stat { background: var(--surface); padding: 10px 14px; display: flex; flex-direction: column; gap: 2px; }
.act-stat-v { font-family: var(--mono); font-size: 16px; color: var(--fg); font-variant-numeric: tabular-nums; }
.act-stat-l { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.act-steps { padding: 0; overflow: hidden; }
.act-steps-head { display: flex; align-items: center; justify-content: space-between; padding: 12px 14px; border-bottom: 1px solid var(--line); }
.act-sec-title { margin: 0; font-size: 13px; font-weight: 600; }
.act-count { font-family: var(--mono); font-size: 11px; color: var(--fg-3); }
.act-timeline { padding: 8px 14px; }
.act-step { display: flex; gap: 10px; padding: 7px 0; }
.act-dot { width: 7px; height: 7px; flex: none; border-radius: 50%; margin-top: 7px; background: var(--fg-3); }
.act-dot.ok { background: var(--ok); }
.act-dot.warn { background: var(--warn); }
.act-dot.err { background: var(--err); }
.act-step-main { min-width: 0; flex: 1; }
.act-step-head { display: flex; align-items: baseline; gap: 10px; }
.act-step-stage { font-size: 12px; color: var(--fg); font-weight: 600; }
.act-step-stage.ok { color: var(--ok); }
.act-step-stage.warn { color: var(--warn); }
.act-step-stage.err { color: var(--err); }
.act-step-time { font-size: 10px; color: var(--fg-3); }
.act-step-detail { margin-top: 2px; font-size: 11px; color: var(--fg-2); word-break: break-word; }
.act-step-run { margin-top: 2px; font-size: 10px; color: var(--fg-3); }
.act-mono { font-family: var(--mono); font-variant-numeric: tabular-nums; }
@media (max-width: 900px) { .act-stats { grid-template-columns: 1fr 1fr; } }
</style>
