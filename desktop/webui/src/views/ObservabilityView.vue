<!-- 观测面：接 /api/agent/observability（运行汇总 / 时间桶 / 工具与通道统计 / 外部委托 / 日志环 / 健康检查 / 活动快照）。
     进程内计数重启清零，运行汇总来自 runs.db —— 界面上把两种口径都标清楚。 -->
<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue';
import { NButton } from 'naive-ui';
import { API, fmtTime } from '../api.js';

const windowKey = ref('24h');
const report = ref(null);
const loading = ref(false);
const errMsg = ref('');
const auto = ref(true);
let timer = null;

const WINDOWS = [
  ['1h', '1 小时'],
  ['24h', '24 小时'],
  ['7d', '7 天'],
  ['30d', '30 天'],
  ['all', '全部'],
];

const healthTone = s => (s === 'ok' ? 'ok' : s === 'warn' ? 'warn' : 'err');

const maxRuns = computed(() => {
  const b = (report.value && report.value.buckets) || [];
  return Math.max(1, ...b.map(x => x.runs));
});

const runs = computed(() => (report.value && report.value.runs) || {});
const logs = computed(() => (report.value && report.value.logs) || {});
const activity = computed(() => (report.value && report.value.activity) || {});

const STAGE_LABEL = {
  idle: '空闲', running: '运行中', thinking: '思考中', retrying: '重试中',
  tool: '调工具', tool_done: '工具返回', tool_error: '工具出错', blocked: '被审批拦下',
  compressing: '压缩上下文', checkpoint: '打快照', memory: '写记忆', done: '完成', error: '出错',
};
function stageName(s) { return STAGE_LABEL[s] || s || '—'; }

async function load(silent) {
  if (!silent) loading.value = true;
  const r = await API.get('/api/agent/observability?window=' + encodeURIComponent(windowKey.value));
  loading.value = false;
  if (!r.ok) { errMsg.value = r.error || '读取观测数据失败'; return; }
  errMsg.value = '';
  report.value = r.data || null;
}

function schedule() {
  stop();
  if (!auto.value) return;
  timer = setInterval(() => load(true), 15000);
}
function stop() { if (timer) { clearInterval(timer); timer = null; } }

watch(auto, schedule);
watch(windowKey, () => load(false));

function barHeight(b) {
  const h = Math.round(((b.runs || 0) / maxRuns.value) * 100);
  return (b.runs ? Math.max(h, 4) : 1) + '%';
}

function uptimeText(sec) {
  if (!sec) return '—';
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  if (d) return d + ' 天 ' + h + ' 小时';
  if (h) return h + ' 小时 ' + m + ' 分';
  return m + ' 分';
}

onMounted(() => { load(false); schedule(); });
onUnmounted(stop);
</script>

<template>
  <div class="bz-page ob-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">观测面</h1>
        <p class="bz-sub">最近跑得怎么样：运行次数 / 延迟 / token、工具与通道的调用统计、外部委托、日志告警和健康检查。</p>
      </div>
      <div class="bz-head-actions">
        <button v-for="[k, label] in WINDOWS" :key="k" class="ob-tab" :class="{ on: windowKey === k }" @click="windowKey = k">{{ label }}</button>
        <NButton size="small" :loading="loading" @click="load(false)">刷新</NButton>
        <NButton size="small" :type="auto ? 'primary' : 'default'" @click="auto = !auto">{{ auto ? '自动刷新开' : '自动刷新关' }}</NButton>
      </div>
    </header>

    <div v-if="errMsg" class="bz-card ob-err">{{ errMsg }}</div>

    <template v-if="report">
      <div class="ob-stats">
        <div class="ob-stat"><span class="ob-v">{{ runs.total || 0 }}</span><span class="ob-l">运行次数</span></div>
        <div class="ob-stat"><span class="ob-v ok">{{ runs.done || 0 }}</span><span class="ob-l">完成</span></div>
        <div class="ob-stat"><span class="ob-v" :class="{ err: runs.failed }">{{ runs.failed || 0 }}</span><span class="ob-l">失败</span></div>
        <div class="ob-stat"><span class="ob-v">{{ runs.avgMs || 0 }} / {{ runs.p95Ms || 0 }} ms</span><span class="ob-l">平均 / P95 延迟</span></div>
        <div class="ob-stat"><span class="ob-v">{{ ((runs.promptTokens || 0) + (runs.outTokens || 0)).toLocaleString() }}</span><span class="ob-l">token 合计</span></div>
        <div class="ob-stat"><span class="ob-v">{{ uptimeText(report.uptimeSec) }}</span><span class="ob-l">进程运行时长</span></div>
      </div>

      <section class="bz-card ob-card">
        <div class="ob-card-head">
          <h2 class="ob-title">健康检查</h2>
          <span class="ob-note">{{ fmtTime(report.generatedAt) }} 生成</span>
        </div>
        <div class="ob-health">
          <div v-for="h in report.health || []" :key="h.name" class="ob-health-item" :class="healthTone(h.status)">
            <div class="ob-health-head"><b>{{ h.name }}</b><span class="ob-dot"></span></div>
            <div v-if="h.detail" class="ob-health-detail">{{ h.detail }}</div>
            <div v-if="h.hint" class="ob-health-hint">{{ h.hint }}</div>
          </div>
          <div v-if="!(report.health || []).length" class="ob-note">后端没有返回健康项。</div>
        </div>
      </section>

      <section class="bz-card ob-card">
        <div class="ob-card-head">
          <h2 class="ob-title">运行分布</h2>
          <span class="ob-note">每根柱子 {{ Math.round((report.bucketMs || 0) / 60000) }} 分钟；柱高 = 运行数，红色段 = 失败</span>
        </div>
        <div class="ob-bars">
          <div v-for="(b, i) in report.buckets || []" :key="i" class="ob-bar-wrap" :title="fmtTime(b.at) + ' · ' + b.runs + ' 次 · ' + b.tokens + ' token'">
            <div class="ob-bar" :style="{ height: barHeight(b) }">
              <div v-if="b.failed" class="ob-bar-fail" :style="{ height: Math.max(8, Math.round((b.failed / Math.max(b.runs, 1)) * 100)) + '%' }"></div>
            </div>
          </div>
          <div v-if="!(report.buckets || []).length" class="ob-note">这个时间窗里没有运行记录。</div>
        </div>
      </section>

      <section class="bz-card ob-card">
        <div class="ob-card-head">
          <h2 class="ob-title">当前活动</h2>
          <span class="ob-note">{{ activity.active ? '正在跑' : '空闲' }} · 阶段 {{ stageName(activity.stage) }}</span>
        </div>
        <div v-if="activity.detail" class="ob-note">{{ activity.detail }}</div>
        <div class="ob-timeline">
          <div v-for="(s, i) in (activity.steps || []).slice(0, 12)" :key="i" class="ob-step">
            <span class="ob-step-at">{{ fmtTime(s.at) }}</span>
            <span class="ob-step-stage">{{ stageName(s.stage) }}</span>
            <span class="ob-step-detail">{{ s.detail }}</span>
          </div>
          <div v-if="!(activity.steps || []).length" class="ob-note">进程启动后还没有活动。</div>
        </div>
      </section>

      <section class="bz-card ob-card">
        <div class="ob-card-head"><h2 class="ob-title">工具调用</h2><span class="ob-note">进程内计数，重启清零</span></div>
        <div v-if="!(report.tools || []).length" class="ob-note">这段时间还没有工具调用。</div>
        <table v-else class="ob-table">
          <thead><tr><th>工具</th><th>调用</th><th>失败</th><th>被拦</th><th>平均耗时</th><th>最近出错</th></tr></thead>
          <tbody>
            <tr v-for="t in report.tools" :key="t.tool">
              <td class="ob-mono">{{ t.tool }}</td>
              <td>{{ t.calls }}</td>
              <td :class="{ 'ob-red': t.errors }">{{ t.errors }}</td>
              <td :class="{ 'ob-warn': t.blocked }">{{ t.blocked }}</td>
              <td>{{ t.avgMs }} ms</td>
              <td class="ob-err-cell">{{ t.lastErr || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <section class="bz-card ob-card">
        <div class="ob-card-head"><h2 class="ob-title">模型通道</h2><span class="ob-note">进程内计数，重启清零</span></div>
        <div v-if="!(report.providers || []).length" class="ob-note">这段时间还没有模型调用。</div>
        <table v-else class="ob-table">
          <thead><tr><th>通道</th><th>模型</th><th>请求</th><th>回复</th><th>重试</th><th>token</th><th>平均等待</th><th>最近出错</th></tr></thead>
          <tbody>
            <tr v-for="p in report.providers" :key="p.provider">
              <td class="ob-mono">{{ p.provider }}</td>
              <td class="ob-mono">{{ p.model || '—' }}</td>
              <td>{{ p.requests }}</td>
              <td>{{ p.replies }}</td>
              <td :class="{ 'ob-warn': p.retries }">{{ p.retries }}</td>
              <td>{{ (p.tokens || 0).toLocaleString() }}</td>
              <td>{{ p.avgMs }} ms</td>
              <td class="ob-err-cell">{{ p.lastErr || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <section v-if="(report.external || []).length" class="bz-card ob-card">
        <div class="ob-card-head"><h2 class="ob-title">外部委托</h2><span class="ob-note">进程内计数，重启清零</span></div>
        <table class="ob-table">
          <thead><tr><th>目标</th><th>调用</th><th>失败</th><th>平均耗时</th><th>最近出错</th></tr></thead>
          <tbody>
            <tr v-for="e in report.external" :key="e.target">
              <td class="ob-mono">{{ e.target }}</td>
              <td>{{ e.calls }}</td>
              <td :class="{ 'ob-red': e.failed }">{{ e.failed }}</td>
              <td>{{ e.avgMs }} ms</td>
              <td class="ob-err-cell">{{ e.lastErr || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <section class="bz-card ob-card">
        <div class="ob-card-head">
          <h2 class="ob-title">日志告警</h2>
          <span class="ob-note">环里共 {{ logs.total || 0 }} 行 · warn {{ logs.warn || 0 }} · error {{ logs.error || 0 }}</span>
        </div>
        <div v-if="!(logs.recent || []).length" class="ob-note">没有 warn / error。</div>
        <div v-for="l in logs.recent || []" :key="l.seq" class="ob-log" :class="l.level">
          <span class="ob-log-at ob-mono">{{ fmtTime(l.at) }}</span>
          <span class="ob-log-level">{{ l.level }}</span>
          <span class="ob-log-text">{{ l.text }}</span>
        </div>
      </section>
    </template>

    <div v-if="!report && !errMsg && loading" class="bz-empty"><div class="bz-empty-title">正在读观测数据…</div></div>
  </div>
</template>

<style>
.ob-page { display: flex; flex-direction: column; gap: 14px; }
.ob-tab { padding: 4px 10px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 11px; cursor: pointer; }
.ob-tab.on { border-color: var(--accent); color: var(--accent); }
.ob-err { color: var(--err); font-size: 12px; }
.ob-stats { display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 1px; background: var(--line); border: 1px solid var(--line); }
.ob-stat { background: var(--surface); padding: 10px 12px; display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.ob-v { font-family: var(--mono); font-size: 16px; color: var(--fg); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ob-v.ok { color: var(--ok); }
.ob-v.err { color: var(--err); }
.ob-l { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.ob-card { display: flex; flex-direction: column; gap: 10px; }
.ob-card-head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.ob-title { margin: 0; font-size: 13px; font-weight: 600; }
.ob-note { font-size: 11px; color: var(--fg-3); }
.ob-mono { font-family: var(--mono); }
.ob-health { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 8px; }
.ob-health-item { padding: 8px 10px; border: 1px solid var(--line); border-left-width: 3px; border-radius: 2px; background: var(--surface-2); display: flex; flex-direction: column; gap: 3px; }
.ob-health-item.ok { border-left-color: var(--ok); }
.ob-health-item.warn { border-left-color: var(--warn); }
.ob-health-item.err { border-left-color: var(--err); }
.ob-health-head { display: flex; align-items: center; justify-content: space-between; font-size: 12px; color: var(--fg); }
.ob-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--fg-3); }
.ob-health-item.ok .ob-dot { background: var(--ok); }
.ob-health-item.warn .ob-dot { background: var(--warn); }
.ob-health-item.err .ob-dot { background: var(--err); }
.ob-health-detail { font-size: 11px; color: var(--fg-2); word-break: break-word; }
.ob-health-hint { font-size: 11px; color: var(--fg-3); }
.ob-bars { display: flex; align-items: flex-end; gap: 2px; height: 110px; padding: 8px 2px 0; border-bottom: 1px solid var(--line); overflow: hidden; }
.ob-bar-wrap { flex: 1 1 0; height: 100%; display: flex; align-items: flex-end; min-width: 2px; }
.ob-bar { width: 100%; background: color-mix(in srgb, var(--accent) 55%, transparent); min-height: 1px; position: relative; }
.ob-bar-fail { position: absolute; left: 0; right: 0; bottom: 0; background: var(--err); }
.ob-timeline { display: flex; flex-direction: column; gap: 4px; }
.ob-step { display: flex; gap: 8px; align-items: baseline; font-size: 11px; }
.ob-step-at { font-family: var(--mono); color: var(--fg-3); flex: none; width: 84px; }
.ob-step-stage { color: var(--accent); flex: none; width: 72px; }
.ob-step-detail { color: var(--fg-2); word-break: break-word; }
.ob-table { width: 100%; border-collapse: collapse; font-size: 11px; }
.ob-table th { text-align: left; font-weight: 500; color: var(--fg-3); padding: 5px 8px; border-bottom: 1px solid var(--line); white-space: nowrap; }
.ob-table td { padding: 5px 8px; border-bottom: 1px solid var(--line); color: var(--fg-2); }
.ob-table tr:last-child td { border-bottom: 0; }
.ob-red { color: var(--err); }
.ob-warn { color: var(--warn); }
.ob-err-cell { max-width: 340px; word-break: break-word; color: var(--fg-3); }
.ob-log { display: flex; gap: 8px; align-items: baseline; font-size: 11px; padding: 4px 0; border-bottom: 1px dashed var(--line); }
.ob-log-at { color: var(--fg-3); flex: none; }
.ob-log-level { font-family: var(--mono); flex: none; width: 44px; }
.ob-log.warn .ob-log-level { color: var(--warn); }
.ob-log.error .ob-log-level { color: var(--err); }
.ob-log-text { color: var(--fg-2); word-break: break-word; }

@media (max-width: 1000px) {
  .ob-stats { grid-template-columns: 1fr 1fr 1fr; }
  .ob-table { display: block; overflow-x: auto; }
}
</style>
