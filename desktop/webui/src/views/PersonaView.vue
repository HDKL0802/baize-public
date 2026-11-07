<!-- 人设页：接 /api/agent/persona（总开关 / 文件读写 / 启停 / 排序 / 归档 / 恢复内置）。
     人设是拼进系统提示的 Markdown，改完下一次运行立即生效（后端每次现读）。 -->
<script setup>
import { ref, computed, onMounted } from 'vue';
import { NButton, NInput, NSwitch } from 'naive-ui';
import { API } from '../api.js';

const enabled = ref(true);
const files = ref([]);
const builtin = ref([]);
const dir = ref('');
const tokens = ref(0);
const promptPreview = ref('');
const loading = ref(true);
const busy = ref('');
const showPreview = ref(false);
const toastMsg = ref('');
const toastTone = ref('');
let toastTimer = null;

function toast(text, tone) {
  toastMsg.value = text;
  toastTone.value = tone || '';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastMsg.value = ''; }, 2800);
}

const enabledFiles = computed(() => files.value.filter(f => f.enabled));
const missing = computed(() => files.value.filter(f => f.enabled && !f.exists));

function apply(d) {
  enabled.value = !!d.enabled;
  files.value = d.files || [];
  builtin.value = d.builtin || [];
  dir.value = d.dir || '';
  tokens.value = d.tokens || 0;
  promptPreview.value = d.promptPreview || '';
}

async function load() {
  loading.value = true;
  const r = await API.get('/api/agent/persona');
  loading.value = false;
  if (!r.ok) { toast(r.error || '读取人设失败', 'err'); return; }
  apply(r.data || {});
}

async function post(body, okText) {
  busy.value = (body.action || '') + ':' + (body.name || '');
  const r = await API.post('/api/agent/persona', body);
  busy.value = '';
  if (!r.ok) { toast(r.error || '操作失败', 'err'); return false; }
  apply(r.data || {});
  if (okText) toast(okText, 'ok');
  return true;
}

/* ---------- 总开关 ---------- */
async function setMaster(on) {
  if (!(await post({ action: 'master', enabled: on }))) load();
  else toast(on ? '人设已开启' : '人设已关闭', 'ok');
}

/* ---------- 编辑 / 新建 ---------- */
const editor = ref(null);
const formBusy = ref(false);

async function openEdit(f) {
  const r = await API.get('/api/agent/persona/file?name=' + encodeURIComponent(f.name));
  if (!r.ok) { toast(r.error || '读文件失败', 'err'); return; }
  editor.value = { mode: 'edit', name: f.name, content: (r.data && r.data.content) || '', builtin: !!f.builtin };
}

function openNew() {
  editor.value = { mode: 'create', name: '', content: '# 人设补充\n\n', builtin: false };
}

async function saveFile() {
  const e = editor.value;
  if (!e.name.trim()) { toast('文件名不能为空（例如 NOTE.md）', 'warn'); return; }
  if (!e.content.trim()) { toast('内容不能为空', 'warn'); return; }
  formBusy.value = true;
  const ok = await post({ action: 'save', name: e.name.trim(), content: e.content }, '已保存 ' + e.name.trim());
  formBusy.value = false;
  if (ok) editor.value = null;
}

/* ---------- 启停 / 排序 / 归档 / 恢复 ---------- */
async function toggleFile(f, on) {
  await post({ action: on ? 'enable' : 'disable', name: f.name }, (on ? '已启用 ' : '已停用 ') + f.name);
}

function canMove(f, delta) {
  const list = enabledFiles.value.map(x => x.name);
  const i = list.indexOf(f.name);
  if (i < 0) return false;
  const j = i + delta;
  return j >= 0 && j < list.length;
}

async function move(f, delta) {
  const list = enabledFiles.value.map(x => x.name);
  const i = list.indexOf(f.name);
  const j = i + delta;
  if (i < 0 || j < 0 || j >= list.length) return;
  const tmp = list[i]; list[i] = list[j]; list[j] = tmp;
  await post({ action: 'order', files: list }, '顺序已更新');
}

async function archive(f) {
  if (!window.confirm(`归档人设文件「${f.name}」？文件会被移走，不会直接删掉。`)) return;
  await post({ action: 'archive', name: f.name }, '已归档 ' + f.name);
}

async function reset(f) {
  if (!window.confirm(`把「${f.name}」恢复成内置模板？当前内容会被覆盖。`)) return;
  await post({ action: 'reset', name: f.name }, '已恢复内置模板');
}

onMounted(load);
</script>

<template>
  <div class="bz-page pe-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">人设</h1>
        <p class="bz-sub">决定白泽"是谁、怎么说话、守什么规矩"。启用文件按顺序拼进系统提示，每次运行现读，改完立即生效。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :loading="loading" @click="load">刷新</NButton>
        <NButton size="small" @click="showPreview = !showPreview">{{ showPreview ? '收起预览' : '拼装预览' }}</NButton>
        <NButton size="small" type="primary" @click="openNew">新建文件</NButton>
      </div>
    </header>

    <div class="pe-stats">
      <div class="pe-stat"><span class="pe-v">{{ enabled ? '已开启' : '已关闭' }}</span><span class="pe-l">人设总开关</span></div>
      <div class="pe-stat"><span class="pe-v">{{ enabledFiles.length }} / {{ files.length }}</span><span class="pe-l">启用 / 全部文件</span></div>
      <div class="pe-stat"><span class="pe-v">{{ tokens }}</span><span class="pe-l">预估 token（启用部分）</span></div>
      <div class="pe-stat"><span class="pe-v" :class="{ err: missing.length }">{{ missing.length }}</span><span class="pe-l">启用但缺文件</span></div>
    </div>

    <section class="bz-card pe-master">
      <div class="pe-master-text">
        <b>人设总开关</b>
        <span class="pe-note">关掉后所有文件都不拼进系统提示，文件仍保留。{{ dir ? '目录：' + dir : '' }}</span>
      </div>
      <NSwitch :value="enabled" @update:value="setMaster" />
    </section>

    <section v-if="showPreview" class="bz-card pe-preview">
      <div class="pe-master-text">
        <b>拼装预览</b>
        <span class="pe-note">实际拼进系统提示的开头（截断），完整内容在文件里。</span>
      </div>
      <pre class="pe-pre">{{ promptPreview || '（人设关闭，或没有启用任何文件）' }}</pre>
    </section>

    <section v-if="editor" class="bz-card pe-form">
      <div class="pe-form-head">
        <h2 class="pe-form-title">{{ editor.mode === 'create' ? '新建人设文件' : '编辑：' + editor.name }}</h2>
        <button class="pe-x" @click="editor = null">×</button>
      </div>
      <label class="pe-field"><span>文件名（目录内 .md，不含路径）</span>
        <NInput v-model:value="editor.name" size="small" :disabled="editor.mode === 'edit'" placeholder="NOTE.md" />
      </label>
      <label class="pe-field"><span>内容</span>
        <NInput v-model:value="editor.content" type="textarea" size="small" :autosize="{ minRows: 10, maxRows: 24 }" />
      </label>
      <div class="pe-form-foot">
        <span class="pe-note">保存后要在这个文件上打开开关，才会拼进系统提示。</span>
        <NButton size="small" type="primary" :loading="formBusy" @click="saveFile">保存</NButton>
      </div>
    </section>

    <section class="bz-card pe-list">
      <div v-if="!loading && !files.length" class="bz-empty">
        <div class="bz-empty-title">人设目录是空的</div>
        <div class="bz-empty-desc">点「新建文件」写一份，或用「恢复内置」补齐内置模板。</div>
      </div>
      <div v-for="f in files" :key="f.name" class="pe-item">
        <div class="pe-item-main">
          <div class="pe-item-head">
            <b class="pe-item-name">{{ f.name }}</b>
            <span v-if="f.builtin" class="pe-badge">内置模板</span>
            <span v-if="!f.exists" class="pe-badge err">文件缺失</span>
            <span v-if="f.tokens" class="pe-badge">{{ f.tokens }} tokens</span>
            <span v-if="f.enabled" class="pe-badge ok">已启用</span>
          </div>
          <div v-if="f.preview" class="pe-item-preview">{{ f.preview }}</div>
        </div>
        <div class="pe-item-actions">
          <NSwitch :value="!!f.enabled" size="small" @update:value="v => toggleFile(f, v)" />
          <NButton size="tiny" :disabled="!canMove(f, -1)" @click="move(f, -1)">上移</NButton>
          <NButton size="tiny" :disabled="!canMove(f, 1)" @click="move(f, 1)">下移</NButton>
          <NButton size="tiny" @click="openEdit(f)">编辑</NButton>
          <NButton v-if="f.builtin" size="tiny" @click="reset(f)">恢复内置</NButton>
          <button class="pe-del" @click="archive(f)">归档</button>
        </div>
      </div>
      <div v-if="builtin.length" class="pe-builtin">
        内置模板：{{ builtin.join('、') }}（文件缺失时点「编辑」保存即可补上，或用「恢复内置」）
      </div>
    </section>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.pe-page { display: flex; flex-direction: column; gap: 14px; }
.pe-stats { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1px; background: var(--line); border: 1px solid var(--line); }
.pe-stat { background: var(--surface); padding: 10px 14px; display: flex; flex-direction: column; gap: 2px; }
.pe-v { font-family: var(--mono); font-size: 16px; color: var(--fg); }
.pe-v.err { color: var(--err); }
.pe-l { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.pe-master, .pe-preview { display: flex; flex-direction: column; gap: 8px; }
.pe-master { flex-direction: row; align-items: center; justify-content: space-between; }
.pe-master-text { display: flex; flex-direction: column; gap: 4px; font-size: 13px; color: var(--fg); min-width: 0; }
.pe-note { font-size: 11px; color: var(--fg-3); word-break: break-all; }
.pe-pre { margin: 0; padding: 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); color: var(--fg-2); white-space: pre-wrap; word-break: break-word; font-family: var(--mono); font-size: 12px; max-height: 360px; overflow: auto; }
.pe-form { display: flex; flex-direction: column; gap: 10px; }
.pe-form-head { display: flex; align-items: center; justify-content: space-between; }
.pe-form-title { margin: 0; font-size: 13px; font-weight: 600; }
.pe-x { border: 0; background: transparent; color: var(--fg-3); font-size: 18px; cursor: pointer; line-height: 1; }
.pe-field { display: flex; flex-direction: column; gap: 5px; font-size: 11px; color: var(--fg-3); }
.pe-form-foot { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.pe-list { padding: 0; overflow: hidden; }
.pe-item { display: flex; gap: 12px; justify-content: space-between; padding: 14px; border-bottom: 1px solid var(--line); }
.pe-item:last-child { border-bottom: 0; }
.pe-item-main { min-width: 0; flex: 1; }
.pe-item-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.pe-item-name { font-size: 13px; color: var(--fg); }
.pe-badge { padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px; font-family: var(--mono); font-size: 10px; color: var(--fg-3); }
.pe-badge.ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 40%, transparent); }
.pe-badge.err { color: var(--err); border-color: color-mix(in srgb, var(--err) 40%, transparent); }
.pe-item-preview { margin-top: 6px; font-size: 12px; color: var(--fg-2); line-height: 1.6; white-space: pre-wrap; }
.pe-item-actions { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; justify-content: flex-end; flex: none; max-width: 380px; }
.pe-del { height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 12px; cursor: pointer; }
.pe-del:hover { border-color: var(--err); color: var(--err); }
.pe-builtin { padding: 10px 14px; font-size: 11px; color: var(--fg-3); border-top: 1px dashed var(--line); }

@media (max-width: 900px) {
  .pe-stats { grid-template-columns: 1fr 1fr; }
  .pe-item { flex-direction: column; }
  .pe-item-actions { justify-content: flex-start; max-width: none; }
}
</style>
