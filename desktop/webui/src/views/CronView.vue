<!-- 定时任务页（替换旧 apps2.js 的 renderCron + 桌面端「模块规划中」占位）。
     数据：GET  /api/agent/cron      → { jobs:[{ id, expr, exprText, goal, recipe, enabled,
                                            nextAt, parseError, running, runs, lastStatus, lastError }] }
     动作：POST /api/agent/cron      { action: add|update|toggle|remove, id, expr, goal, recipe, enabled }
           POST /api/agent/cron/run  { id }  → 202 { started, runId }（立刻试跑一次，不等排期）

     设计口径（与 TasksView 一致）：表格只读展示 + 每行三个动作；新增/编辑用右侧表单。
     「立刻跑一次」是这一页最要紧的按钮：表达式写错时用户本来要等到该跑的那一刻才发现。 -->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue';
import { NButton, NInput, NSwitch, NTag, NSelect } from 'naive-ui';
import { API, fmtTime } from '../api.js';

const jobs = ref([]);
const loading = ref(true);
const busy = ref(false);        // 表单提交中
const runningId = ref('');      // 正在「立刻跑」的任务 id
let timer = null;

/* 表单状态：editingId 为空 = 新增 */
const editingId = ref('');
const expr = ref('');
const goal = ref('');
const recipe = ref('');
const formMsg = ref('');
const formErr = ref(false);

const enabledCount = computed(() => jobs.value.filter(j => j.enabled).length);
const editing = computed(() => !!editingId.value);

/* 常用表达式：点一下填进去，省得用户背 cron 语法。
   值里不带星期限制的才给「每天/工作日」两种口径，别硬塞一个可能理解错的。 */
const PRESETS = [
  { label: '每天 08:00', value: '0 8 * * *' },
  { label: '每天 20:00', value: '0 20 * * *' },
  { label: '工作日 09:00', value: '0 9 * * 1-5' },
  { label: '每周一 09:00', value: '0 9 * * 1' },
  { label: '每小时', value: '0 * * * *' },
  { label: '每 30 分钟', value: '@every 30m' },
];
const presetOptions = PRESETS.map(p => ({ label: p.label + '（' + p.value + '）', value: p.value }));

function toast(text, tone) {
  const d = document.createElement('div');
  d.className = 'bz-toast ' + (tone || '');
  d.textContent = text;
  document.body.appendChild(d);
  setTimeout(() => d.remove(), 3800);
}

async function load() {
  const r = await API.get('/api/agent/cron');
  if (!r.ok) {
    loading.value = false;
    toast('读取定时任务失败：' + (r.error || '未知错误'), 'err');
    return;
  }
  jobs.value = (r.data && r.data.jobs) || [];
  loading.value = false;
}
onMounted(() => { load(); timer = setInterval(load, 12000); });
onBeforeUnmount(() => timer && clearInterval(timer));

function resetForm() {
  editingId.value = '';
  expr.value = '';
  goal.value = '';
  recipe.value = '';
  formMsg.value = '';
  formErr.value = false;
}

function startEdit(j) {
  editingId.value = j.id;
  expr.value = j.expr || '';
  goal.value = j.goal || '';
  recipe.value = j.recipe || '';
  formMsg.value = '';
  formErr.value = false;
}

async function save() {
  const e = expr.value.trim();
  const g = goal.value.trim();
  if (!e || !g) {
    formErr.value = true;
    formMsg.value = '表达式和目标都要填';
    return;
  }
  busy.value = true;
  formErr.value = false;
  formMsg.value = '提交中…';
  const body = { action: editing.value ? 'update' : 'add', expr: e, goal: g };
  if (editing.value) body.id = editingId.value;
  const rc = recipe.value.trim();
  if (rc) body.recipe = rc;
  const r = await API.post('/api/agent/cron', body);
  busy.value = false;
  if (!r.ok) {
    formErr.value = true;
    formMsg.value = '保存失败：' + (r.error || '未知错误');
    return;
  }
  if (r.data && r.data.jobs) jobs.value = r.data.jobs;
  toast(editing.value ? '定时任务已更新（最多 20 秒生效）' : '定时任务已添加（最多 20 秒生效）', 'ok');
  resetForm();
  await load();
}

async function toggle(j) {
  const r = await API.post('/api/agent/cron', { action: 'toggle', id: j.id, enabled: !j.enabled });
  if (!r.ok) { toast('操作失败：' + (r.error || '未知错误'), 'err'); return; }
  if (r.data && r.data.jobs) jobs.value = r.data.jobs;
  toast(j.enabled ? '已停用' : '已启用', 'ok');
  await load();
}

async function remove(j) {
  if (!window.confirm('删除这条定时任务？\n\n' + (j.goal || j.id) + '\n\n删掉后就不再按点自动跑。')) return;
  const r = await API.post('/api/agent/cron', { action: 'remove', id: j.id });
  if (!r.ok) { toast('删除失败：' + (r.error || '未知错误'), 'err'); return; }
  if (editingId.value === j.id) resetForm();
  toast('已删除', 'ok');
  await load();
}

/* 立刻跑一次：表达式写错时，用户本来要等到"该跑的那一刻"才发现；
   这个按钮用同一套语义（目标/配方/是否免审批）当场验证这条任务能不能跑通。 */
async function runNow(j) {
  if (runningId.value) return;
  runningId.value = j.id;
  const r = await API.post('/api/agent/cron/run', { id: j.id });
  runningId.value = '';
  if (!r.ok) {
    toast('执行失败：' + (r.error || '未知错误'), 'err');
    await load();
    return;
  }
  toast('已开始执行（运行 ' + ((r.data && r.data.runId) || '?') + '），结果在「任务与审批」里看', 'ok');
  await load();
}

function statusOf(j) {
  if (j.parseError) return { label: '表达式不合法', tone: 'err' };
  if (!j.enabled) return { label: '已停用', tone: 'mute' };
  if (j.running) return { label: '执行中', tone: 'accent' };
  if (j.lastStatus === 'failed') return { label: '上次失败', tone: 'err' };
  if (j.lastStatus === 'done') return { label: '上次成功', tone: 'ok' };
  return { label: '启用中', tone: 'ok' };
}

function nextText(j) {
  if (j.parseError) return '—';
  if (!j.enabled) return '已停用';
  if (!j.nextAt) return '等待排期';
  return fmtTime(j.nextAt);
}
</script>

<template>
  <div class="bz-page cron-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">定时任务</h1>
        <p class="bz-sub">
          到点让白泽自动干一件事。表达式是标准 5 段 cron（分 时 日 月 周）或 @every 30m；
          改完最多 20 秒生效。
        </p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" @click="load">刷新</NButton>
      </div>
    </header>

    <section class="bz-card cron-card">
      <div class="cron-card-head">
        <span class="cron-card-title">任务</span>
        <span class="cron-summary">
          共 {{ jobs.length }} 条 · 启用 {{ enabledCount }} 条
        </span>
      </div>

      <div class="cron-cols">
        <span class="cron-col-expr">表达式</span>
        <span class="cron-col-goal">要干什么</span>
        <span class="cron-col-next">下次触发</span>
        <span class="cron-col-status">状态</span>
        <span class="cron-col-actions">操作</span>
      </div>

      <div class="cron-list">
        <div v-for="j in jobs" :key="j.id" class="cron-row" :class="{ off: !j.enabled }">
          <span class="cron-col-expr cron-mono">
            {{ j.expr }}
            <em v-if="j.exprText" class="cron-expr-text">{{ j.exprText }}</em>
          </span>
          <span class="cron-col-goal">
            {{ j.goal || '—' }}
            <em v-if="j.parseError" class="cron-err" :title="j.parseError">表达式解析失败：{{ j.parseError }}</em>
            <em v-else-if="j.lastError" class="cron-err" :title="j.lastError">上次错误：{{ j.lastError }}</em>
            <em v-if="j.runs" class="cron-meta">已跑 {{ j.runs }} 次<template v-if="j.lastRunAt"> · 上次 {{ fmtTime(j.lastRunAt) }}</template></em>
          </span>
          <span class="cron-col-next cron-mono">{{ nextText(j) }}</span>
          <span class="cron-col-status" :class="'tone-' + statusOf(j).tone">
            <i class="bz-dot"></i>{{ statusOf(j).label }}
          </span>
          <span class="cron-col-actions">
            <NButton size="tiny" :loading="runningId === j.id" :disabled="!!runningId && runningId !== j.id"
                     @click="runNow(j)">立刻跑</NButton>
            <NButton size="tiny" @click="toggle(j)">{{ j.enabled ? '停用' : '启用' }}</NButton>
            <NButton size="tiny" @click="startEdit(j)">编辑</NButton>
            <NButton size="tiny" quaternary type="error" @click="remove(j)">删除</NButton>
          </span>
        </div>
      </div>

      <div v-if="loading && !jobs.length" class="cron-hint">正在读取定时任务…</div>
      <div v-if="!loading && !jobs.length" class="bz-empty">
        <svg class="bz-empty-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor"
             stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17Z" />
          <path d="M12 7.5V12l3.2 2" />
        </svg>
        <div class="bz-empty-title">还没有定时任务</div>
        <div class="bz-empty-desc">
          在下面填一条：什么时候跑（cron 表达式）、跑起来要干什么（一句目标）。
          加好后可以点「立刻跑」当场验证能不能跑通。
        </div>
      </div>
    </section>

    <section class="bz-card cron-form">
      <div class="cron-form-head">
        <h2 class="cron-form-title">{{ editing ? '编辑定时任务' : '加一条' }}</h2>
        <span v-if="editing" class="cron-form-id cron-mono">{{ editingId }}</span>
      </div>

      <div class="cron-fields">
        <label class="cron-field">
          <span class="cron-label">cron 表达式</span>
          <NInput v-model:value="expr" class="cron-mono-input" placeholder="0 8 * * *"
                  :status="formErr && !expr.trim() ? 'error' : undefined" />
        </label>
        <label class="cron-field cron-field-preset">
          <span class="cron-label">常用</span>
          <NSelect v-model:value="expr" :options="presetOptions" placeholder="点一下填入" clearable />
        </label>
        <label class="cron-field cron-field-recipe">
          <span class="cron-label">配方</span>
          <NInput v-model:value="recipe" placeholder="chat（一般不用填）" />
        </label>
      </div>

      <label class="cron-field cron-field-goal">
        <span class="cron-label">要干什么</span>
        <NInput v-model:value="goal" type="textarea" :autosize="{ minRows: 1, maxRows: 4 }"
                placeholder="总结昨天的工作记录并写成一条记忆"
                :status="formErr && !goal.trim() ? 'error' : undefined" />
      </label>

      <div class="cron-form-actions">
        <NButton type="primary" size="small" :loading="busy" @click="save">
          {{ editing ? '保存修改' : '添加' }}
        </NButton>
        <NButton v-if="editing" size="small" @click="resetForm">取消编辑</NButton>
        <span class="cron-form-msg" :class="{ err: formErr }">{{ formMsg }}</span>
      </div>
      <p class="cron-form-note">
        表达式写错会当场报错，不会留一条「看着加上了其实永远不跑」的任务；
        保存后调度器最多 20 秒重读一次配置。
      </p>
    </section>
  </div>
</template>

<style>
.cron-page { display: flex; flex-direction: column; }
.cron-card { padding: 0; overflow: hidden; }
.cron-card-head {
  display: flex; align-items: center; justify-content: space-between;
  padding: 14px 16px; border-bottom: 1px solid var(--line);
}
.cron-card-title { font-size: 13px; font-weight: 600; color: var(--fg); }
.cron-summary { font-size: 11px; color: var(--fg-3); font-family: var(--mono); }

.cron-cols, .cron-row {
  display: grid;
  grid-template-columns: minmax(140px, 0.8fr) minmax(240px, 2fr) 112px 100px 268px;
  align-items: center; gap: 12px;
}
.cron-cols { padding: 10px 16px; border-bottom: 1px solid var(--line); }
.cron-cols span { font-size: 11px; color: var(--fg-3); letter-spacing: .08em; }
.cron-row {
  min-height: 52px; padding: 8px 16px; border-bottom: 1px solid var(--line);
  transition: background 120ms cubic-bezier(.2,0,0,1);
}
.cron-row:last-child { border-bottom: 0; }
.cron-row:hover { background: var(--surface-2); }
.cron-row.off { opacity: .62; }
.cron-row > span { font-size: 13px; color: var(--fg); min-width: 0; }

.cron-col-expr { display: flex; flex-direction: column; gap: 2px; }
.cron-expr-text { font-style: normal; font-size: 11px; color: var(--fg-3); }
.cron-col-goal { display: flex; flex-direction: column; gap: 2px; overflow: hidden; }
.cron-col-goal { white-space: normal; word-break: break-word; }
/* 上次错误可能是整段模型报错（"没有可用的模型通道…请编辑 C:\…\config.json"），
   整段铺开会把这一行撑成五六行，把别的任务挤到屏幕外。这里收成两行，全文放 title 里。 */
.cron-err {
  font-style: normal; font-size: 11px; color: var(--err);
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
  overflow: hidden; line-height: 1.5;
}
.cron-meta { font-style: normal; font-size: 11px; color: var(--fg-3); font-family: var(--mono); }
.cron-mono { font-family: var(--mono); font-size: 12px; color: var(--fg-2); font-variant-numeric: tabular-nums; }

.cron-col-status { display: inline-flex; align-items: center; gap: 6px; }
.cron-col-status .bz-dot { width: 6px; height: 6px; }
.cron-col-actions { display: flex; align-items: center; gap: 6px; justify-content: flex-end; flex-wrap: wrap; }

.tone-warn { color: var(--warn); } .tone-warn .bz-dot { background: var(--warn); }
.tone-accent { color: var(--accent); } .tone-accent .bz-dot { background: var(--accent); }
.tone-ok { color: var(--ok); } .tone-ok .bz-dot { background: var(--ok); }
.tone-err { color: var(--err); } .tone-err .bz-dot { background: var(--err); }
.tone-mute { color: var(--fg-3); } .tone-mute .bz-dot { background: var(--fg-3); }

.cron-hint { padding: 20px 16px; font-size: 12px; color: var(--fg-3); }

.cron-form-head { display: flex; align-items: baseline; gap: 10px; margin-bottom: 12px; }
.cron-form-title { margin: 0; font-size: 13px; font-weight: 600; color: var(--fg); }
.cron-form-id { font-size: 11px; color: var(--fg-3); }
.cron-fields { display: grid; grid-template-columns: minmax(160px, 1fr) minmax(180px, 1fr) 160px; gap: 12px; }
.cron-field { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.cron-field-goal { margin-top: 12px; }
.cron-label { font-size: 11px; color: var(--fg-3); letter-spacing: .04em; }
.cron-mono-input .n-input__input-el { font-family: var(--mono); }
.cron-form-actions { display: flex; align-items: center; gap: 10px; margin-top: 14px; }
.cron-form-msg { font-size: 12px; color: var(--fg-3); }
.cron-form-msg.err { color: var(--err); }
.cron-form-note { margin: 10px 0 0; font-size: 11px; color: var(--fg-3); line-height: 1.6; }
</style>
