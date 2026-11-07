<!-- 备份页：接 /api/agent/backups（打包 / 列表 / 校验 / 恢复 / 删除）。
     恢复先走 dryRun（只校验、报告会动什么），再让用户二次确认真正恢复；恢复前后端还会自动打安全点。 -->
<script setup>
import { ref, computed, onMounted } from 'vue';
import { NButton, NInput } from 'naive-ui';
import { API, fmtTime } from '../api.js';

const backups = ref([]);
const dir = ref('');
const hint = ref('');
const loading = ref(true);
const busy = ref('');
const toastMsg = ref('');
const toastTone = ref('');
let toastTimer = null;

function toast(text, tone) {
  toastMsg.value = text;
  toastTone.value = tone || '';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastMsg.value = ''; }, 3000);
}

const sel = ref({ config: true, hub: true, memory: true, runs: true, skills: true, checkpoints: false, workspace: false, vault: false });
const note = ref('');
const creating = ref(false);

const SEL_LABELS = [
  ['config', '配置与令牌', 'config.json + 配对令牌'],
  ['hub', '设备任务库', 'agent.db：设备 / 任务 / 记录 / 记忆索引'],
  ['memory', '记忆树', 'memory.db（Memory Tree）'],
  ['runs', '运行记录', 'runs.db'],
  ['skills', '技能库', 'skills/ 目录'],
  ['checkpoints', '变更快照', 'checkpoints/（通常很大）'],
  ['workspace', '工作目录', 'workspace/（Agent 产出的文件）'],
  ['vault', '知识库原文', 'vault.*（若存在）'],
];

const selectedLabel = computed(() => SEL_LABELS.filter(([k]) => sel.value[k]).map(([, l]) => l).join('、') || '（未选择）');

async function load() {
  loading.value = true;
  const r = await API.get('/api/agent/backups');
  loading.value = false;
  if (!r.ok) { toast(r.error || '读取备份失败', 'err'); return; }
  const d = r.data || {};
  backups.value = d.backups || [];
  dir.value = d.dir || '';
  hint.value = d.hint || '';
}

async function createBackup() {
  if (!SEL_LABELS.some(([k]) => sel.value[k])) { toast('至少要选一项', 'warn'); return; }
  creating.value = true;
  const r = await API.post('/api/agent/backups', { selection: { ...sel.value }, note: note.value.trim() });
  creating.value = false;
  if (!r.ok) { toast(r.error || '打包失败', 'err'); return; }
  note.value = '';
  await load();
  toast('备份完成：' + ((r.data && r.data.manifest && r.data.manifest.totalSize) || 0) + ' 字节', 'ok');
}

async function verify(b) {
  busy.value = 'verify:' + b.path;
  const r = await API.post('/api/agent/backups/verify', { path: b.path });
  busy.value = '';
  if (!r.ok) { toast(r.error || '校验失败', 'err'); return; }
  const m = r.data && r.data.manifest;
  toast('校验通过：' + ((m && m.entries) || []).length + ' 个文件', 'ok');
  await load();
}

/* ---------- 恢复：先 dryRun ---------- */
const restore = ref(null);
const restoreBusy = ref(false);
const restoreResult = ref(null);

function openRestore(b) {
  restore.value = b;
  restoreResult.value = null;
}

async function dryRun() {
  if (!restore.value) return;
  restoreBusy.value = true;
  const r = await API.post('/api/agent/backups/restore', { path: restore.value.path, dryRun: true });
  restoreBusy.value = false;
  const d = r.data || {};
  restoreResult.value = { dryRun: true, result: d.result, error: d.error || (!r.ok ? r.error : '') };
  if (d.error) toast('预演失败：' + d.error, 'err');
  else toast('预演完成：会恢复 ' + (((d.result || {}).restored || []).length) + ' 个文件', 'ok');
}

async function realRestore() {
  if (!restore.value) return;
  if (!window.confirm('真的恢复？当前数据会被备份包覆盖，后端会先打一个安全点。')) return;
  restoreBusy.value = true;
  const r = await API.post('/api/agent/backups/restore', { path: restore.value.path, dryRun: false });
  restoreBusy.value = false;
  const d = r.data || {};
  restoreResult.value = { dryRun: false, result: d.result, error: d.error || (!r.ok ? r.error : '') };
  if (d.error) toast('恢复失败：' + d.error, 'err');
  else toast('恢复完成，安全点：' + ((d.result || {}).safePoint || '无'), 'ok');
  await load();
}

async function removeBackup(b) {
  if (!window.confirm(`删除备份「${b.name}」？文件和清单都会删掉。`)) return;
  busy.value = 'delete:' + b.path;
  const r = await API.post('/api/agent/backups/delete', { path: b.path });
  busy.value = '';
  if (!r.ok) { toast(r.error || '删除失败', 'err'); return; }
  await load();
  toast('已删除 ' + b.name, 'ok');
}

function selLabel(s) {
  if (!s) return '';
  return SEL_LABELS.filter(([k]) => s[k]).map(([, l]) => l).join('、');
}

function sizeText(n) {
  if (!n) return '0 B';
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB';
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB';
}

onMounted(load);
</script>

<template>
  <div class="bz-page bk-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">备份与恢复</h1>
        <p class="bz-sub">把白泽的数据打成一个 zip。恢复前会自动打安全点；界面强制先预演，再让你二次确认。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :loading="loading" @click="load">刷新</NButton>
      </div>
    </header>

    <div class="bk-stats">
      <div class="bk-stat"><span class="bk-v">{{ backups.length }}</span><span class="bk-l">备份包</span></div>
      <div class="bk-stat"><span class="bk-v">{{ sizeText(backups.reduce((n, b) => n + (b.size || 0), 0)) }}</span><span class="bk-l">占用空间</span></div>
      <div class="bk-stat"><span class="bk-v" :class="{ err: backups.filter(b => b.error).length }">{{ backups.filter(b => b.error).length }}</span><span class="bk-l">读不出清单</span></div>
      <div class="bk-stat bk-dir"><span class="bk-v bk-mono">{{ dir || '—' }}</span><span class="bk-l">备份目录</span></div>
    </div>

    <section class="bz-card bk-form">
      <h2 class="bk-title">打新备份</h2>
      <div class="bk-sel-grid">
        <label v-for="[k, label, desc] in SEL_LABELS" :key="k" class="bk-sel">
          <input v-model="sel[k]" type="checkbox" />
          <span class="bk-sel-text"><b>{{ label }}</b><span>{{ desc }}</span></span>
        </label>
      </div>
      <label class="bk-field"><span>备注（可空）</span><NInput v-model:value="note" size="small" placeholder="例如：升级前" /></label>
      <div class="bk-form-foot">
        <span class="bk-note">已选：{{ selectedLabel }}</span>
        <NButton size="small" type="primary" :loading="creating" @click="createBackup">开始备份</NButton>
      </div>
      <div v-if="hint" class="bk-note">{{ hint }}</div>
    </section>

    <section v-if="restore" class="bz-card bk-restore">
      <div class="bk-form-head">
        <h2 class="bk-title">恢复 · {{ restore.name }}</h2>
        <button class="bk-x" @click="restore = null">×</button>
      </div>
      <div class="bk-restore-actions">
        <NButton size="small" :loading="restoreBusy" @click="dryRun">预演（只校验，不动数据）</NButton>
        <NButton size="small" type="warning" :loading="restoreBusy" @click="realRestore">真正恢复</NButton>
      </div>
      <div v-if="restoreResult" class="bk-result">
        <div v-if="restoreResult.error" class="bk-err">{{ restoreResult.error }}</div>
        <template v-else>
          <div class="bk-note">{{ restoreResult.dryRun ? '预演结果' : '恢复结果' }}：恢复 {{ (restoreResult.result.restored || []).length }} 项，跳过 {{ (restoreResult.result.skipped || []).length }} 项，共 {{ sizeText(restoreResult.result.bytes) }}</div>
          <div v-if="restoreResult.result.safePoint" class="bk-note bk-mono">安全点：{{ restoreResult.result.safePoint }}</div>
          <pre v-if="(restoreResult.result.restored || []).length" class="bk-pre">{{ (restoreResult.result.restored || []).join('\n') }}</pre>
          <pre v-if="(restoreResult.result.skipped || []).length" class="bk-pre">跳过：\n{{ (restoreResult.result.skipped || []).join('\n') }}</pre>
        </template>
      </div>
    </section>

    <section class="bz-card bk-list">
      <div v-if="!loading && !backups.length" class="bz-empty">
        <div class="bz-empty-title">还没有备份</div>
        <div class="bz-empty-desc">在上面勾选内容，点「开始备份」。</div>
      </div>
      <div v-for="b in backups" :key="b.path" class="bk-item">
        <div class="bk-item-main">
          <div class="bk-item-head">
            <b class="bk-item-name">{{ b.name }}</b>
            <span class="bk-badge">{{ sizeText(b.size) }}</span>
            <span v-if="b.error" class="bk-badge err">清单读不出</span>
          </div>
          <div v-if="b.manifest" class="bk-item-meta">
            {{ fmtTime(b.manifest.createdAt) }} · 版本 {{ b.manifest.version }} · {{ selLabel(b.manifest.selection) }}
          </div>
          <div v-if="b.manifest && b.manifest.note" class="bk-item-note">备注：{{ b.manifest.note }}</div>
          <div v-if="b.error" class="bk-err">{{ b.error }}</div>
        </div>
        <div class="bk-item-actions">
          <NButton size="tiny" :loading="busy === 'verify:' + b.path" @click="verify(b)">校验</NButton>
          <NButton size="tiny" @click="openRestore(b)">恢复</NButton>
          <button class="bk-del" @click="removeBackup(b)">删除</button>
        </div>
      </div>
    </section>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.bk-page { display: flex; flex-direction: column; gap: 14px; }
.bk-stats { display: grid; grid-template-columns: 1fr 1fr 1fr 1.6fr; gap: 1px; background: var(--line); border: 1px solid var(--line); }
.bk-stat { background: var(--surface); padding: 10px 14px; display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.bk-v { font-family: var(--mono); font-size: 16px; color: var(--fg); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bk-v.err { color: var(--err); }
.bk-l { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.bk-form, .bk-restore { display: flex; flex-direction: column; gap: 10px; }
.bk-title { margin: 0; font-size: 13px; font-weight: 600; }
.bk-form-head { display: flex; align-items: center; justify-content: space-between; }
.bk-x { border: 0; background: transparent; color: var(--fg-3); font-size: 18px; cursor: pointer; line-height: 1; }
.bk-sel-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 8px; }
.bk-sel { display: flex; align-items: flex-start; gap: 8px; padding: 8px 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); font-size: 12px; color: var(--fg); cursor: pointer; }
.bk-sel input { margin-top: 2px; accent-color: var(--accent); }
.bk-sel-text { display: flex; flex-direction: column; gap: 2px; }
.bk-sel-text span { font-size: 11px; color: var(--fg-3); }
.bk-field { display: flex; flex-direction: column; gap: 5px; font-size: 11px; color: var(--fg-3); }
.bk-form-foot { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.bk-note { font-size: 11px; color: var(--fg-3); }
.bk-mono { font-family: var(--mono); }
.bk-restore-actions { display: flex; gap: 8px; flex-wrap: wrap; }
.bk-result { display: flex; flex-direction: column; gap: 6px; }
.bk-err { font-size: 11px; color: var(--err); word-break: break-word; }
.bk-pre { margin: 0; padding: 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); color: var(--fg-2); white-space: pre-wrap; word-break: break-all; font-family: var(--mono); font-size: 11px; max-height: 260px; overflow: auto; }
.bk-list { padding: 0; overflow: hidden; }
.bk-item { display: flex; gap: 12px; justify-content: space-between; padding: 14px; border-bottom: 1px solid var(--line); }
.bk-item:last-child { border-bottom: 0; }
.bk-item-main { min-width: 0; flex: 1; }
.bk-item-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.bk-item-name { font-size: 13px; color: var(--fg); word-break: break-all; }
.bk-badge { padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px; font-family: var(--mono); font-size: 10px; color: var(--fg-3); }
.bk-badge.err { color: var(--err); border-color: color-mix(in srgb, var(--err) 40%, transparent); }
.bk-item-meta { margin-top: 5px; font-size: 11px; color: var(--fg-3); }
.bk-item-note { margin-top: 5px; font-size: 11px; color: var(--fg-2); }
.bk-item-actions { display: flex; gap: 6px; flex: none; align-items: flex-start; }
.bk-del { height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 12px; cursor: pointer; }
.bk-del:hover { border-color: var(--err); color: var(--err); }

@media (max-width: 900px) {
  .bk-stats { grid-template-columns: 1fr 1fr; }
  .bk-item { flex-direction: column; }
}
</style>
