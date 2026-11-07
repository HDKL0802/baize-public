<!-- 技能页：接 /api/agent/skills（清单 / 新建 / 改写 / 删除）。
     后端只返回去掉 front-matter 的正文（body），没有"读原始 SKILL.md"的接口，
     所以编辑时用 name / description / 正文重建整篇 —— 保存会整篇重写，界面上明说这一点。 -->
<script setup>
import { ref, computed, onMounted } from 'vue';
import { NButton, NInput } from 'naive-ui';
import { API } from '../api.js';

const skills = ref([]);
const loading = ref(true);
const keyword = ref('');
const expanded = ref('');
const editor = ref(null);
const formBusy = ref(false);
const toastMsg = ref('');
const toastTone = ref('');
let toastTimer = null;

function toast(text, tone) {
  toastMsg.value = text;
  toastTone.value = tone || '';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastMsg.value = ''; }, 2800);
}

const totalTokens = computed(() => skills.value.reduce((n, s) => n + (s.tokens || 0), 0));
const categories = computed(() => {
  const set = new Set();
  skills.value.forEach(s => set.add(s.category || '（顶层）'));
  return set.size;
});

const filtered = computed(() => {
  const kw = keyword.value.trim().toLowerCase();
  if (!kw) return skills.value;
  return skills.value.filter(s =>
    (s.name || '').toLowerCase().includes(kw) ||
    (s.slug || '').toLowerCase().includes(kw) ||
    (s.description || '').toLowerCase().includes(kw) ||
    (s.category || '').toLowerCase().includes(kw)
  );
});

async function load() {
  loading.value = true;
  const r = await API.get('/api/agent/skills');
  loading.value = false;
  if (!r.ok) { toast(r.error || '读取技能失败', 'err'); return; }
  skills.value = (r.data && r.data.skills) || [];
}

/* ---------- 编辑器 ---------- */
const TEMPLATE = `---
name: 技能名
description: 一句话说明什么时候用这个技能，以句号结尾。
---

# 技能名

## 步骤

1. …
`;

function openNew() {
  editor.value = {
    mode: 'create', name: '', category: '', slug: '', content: TEMPLATE,
    note: '新建技能：目录名（slug）用 ASCII，例如 note-sync。',
  };
}

function openEdit(s) {
  // 后端没给原始 front-matter，只能按这三个字段重建
  const content = `---\nname: ${s.name}\ndescription: ${s.description}\n---\n\n${s.body || ''}\n`;
  editor.value = {
    mode: 'edit', name: s.name, slug: s.slug, category: s.category || '', content,
    note: '此编辑器只保留 name / description / 正文；如果原 SKILL.md 还有其它 front-matter 字段，保存后会丢。',
  };
}

async function saveSkill() {
  const e = editor.value;
  if (!e.content.trim()) { toast('内容不能为空', 'warn'); return; }
  if (e.mode === 'create' && !e.slug.trim()) { toast('新建要填目录名（slug）', 'warn'); return; }
  formBusy.value = true;
  const r = await API.post('/api/agent/skills', {
    action: e.mode === 'create' ? 'create' : 'save',
    name: e.mode === 'create' ? e.slug.trim() : e.slug,
    category: e.mode === 'create' ? e.category.trim() : '',
    content: e.content,
  });
  formBusy.value = false;
  if (!r.ok) { toast(r.error || '保存失败', 'err'); return; }
  skills.value = (r.data && r.data.skills) || [];
  editor.value = null;
  toast('已保存技能', 'ok');
}

async function removeSkill(s) {
  if (!window.confirm(`删除技能「${s.name}」（${s.slug}）？白泽会把它归档，不会用错。`)) return;
  const r = await API.post('/api/agent/skills', { action: 'delete', name: s.slug });
  if (!r.ok) { toast(r.error || '删除失败', 'err'); return; }
  skills.value = (r.data && r.data.skills) || [];
  toast('已归档技能 ' + s.name, 'ok');
}

onMounted(load);
</script>

<template>
  <div class="bz-page sk-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">技能</h1>
        <p class="bz-sub">白泽按需加载的能力包。技能清单会进系统提示，正文只在用到时读 —— 写得短、触发描述清楚最省 token。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :loading="loading" @click="load">刷新</NButton>
        <NButton size="small" type="primary" @click="openNew">新建技能</NButton>
      </div>
    </header>

    <div class="sk-stats">
      <div class="sk-stat"><span class="sk-v">{{ skills.length }}</span><span class="sk-l">技能数</span></div>
      <div class="sk-stat"><span class="sk-v">{{ categories }}</span><span class="sk-l">分类</span></div>
      <div class="sk-stat"><span class="sk-v">{{ totalTokens }}</span><span class="sk-l">正文 token 合计</span></div>
    </div>

    <section v-if="editor" class="bz-card sk-form">
      <div class="sk-form-head">
        <h2 class="sk-form-title">{{ editor.mode === 'create' ? '新建技能' : '编辑：' + editor.name }}</h2>
        <button class="sk-x" @click="editor = null">×</button>
      </div>
      <div v-if="editor.mode === 'create'" class="sk-grid">
        <label class="sk-field"><span>目录名（slug，ASCII）</span><NInput v-model:value="editor.slug" size="small" placeholder="note-sync" /></label>
        <label class="sk-field"><span>分类（可空 = 顶层）</span><NInput v-model:value="editor.category" size="small" placeholder="writing" /></label>
      </div>
      <label class="sk-field"><span>SKILL.md 全文</span>
        <NInput v-model:value="editor.content" type="textarea" size="small" :autosize="{ minRows: 10, maxRows: 22 }" />
      </label>
      <div class="sk-form-foot">
        <span class="sk-note">{{ editor.note }}</span>
        <NButton size="small" type="primary" :loading="formBusy" @click="saveSkill">保存</NButton>
      </div>
    </section>

    <section class="bz-card sk-list">
      <div class="sk-toolbar">
        <NInput v-model:value="keyword" size="small" clearable placeholder="按名称 / 目录名 / 描述 / 分类筛选" style="max-width: 360px" />
        <span class="sk-note">显示 {{ filtered.length }} / {{ skills.length }}</span>
      </div>
      <div v-if="!loading && !skills.length" class="bz-empty">
        <div class="bz-empty-title">技能库是空的</div>
        <div class="bz-empty-desc">点「新建技能」写一份 SKILL.md，或让白泽在运行中自己沉淀。</div>
      </div>
      <div v-for="s in filtered" :key="s.slug" class="sk-item">
        <div class="sk-item-main">
          <div class="sk-item-head">
            <b class="sk-item-name">{{ s.name }}</b>
            <span class="sk-badge sk-mono">{{ s.slug }}</span>
            <span class="sk-badge">{{ s.category || '顶层' }}</span>
            <span class="sk-badge">{{ s.tokens }} tokens</span>
          </div>
          <div class="sk-item-desc">{{ s.description }}</div>
          <div v-if="(s.triggers || []).length" class="sk-item-meta">触发词：{{ s.triggers.join('、') }}</div>
          <button class="sk-expand" @click="expanded = expanded === s.slug ? '' : s.slug">
            {{ expanded === s.slug ? '收起正文' : '查看正文' }}
          </button>
          <pre v-if="expanded === s.slug" class="sk-body">{{ s.body }}</pre>
        </div>
        <div class="sk-item-actions">
          <NButton size="tiny" @click="openEdit(s)">编辑</NButton>
          <button class="sk-del" @click="removeSkill(s)">删除</button>
        </div>
      </div>
    </section>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.sk-page { display: flex; flex-direction: column; }
.sk-stats { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 1px; background: var(--line); border: 1px solid var(--line); margin-bottom: 14px; }
.sk-stat { background: var(--surface); padding: 10px 14px; display: flex; flex-direction: column; gap: 2px; }
.sk-v { font-family: var(--mono); font-size: 16px; color: var(--fg); }
.sk-l { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.sk-form { display: flex; flex-direction: column; gap: 10px; margin-bottom: 14px; }
.sk-form-head { display: flex; align-items: center; justify-content: space-between; }
.sk-form-title { margin: 0; font-size: 13px; font-weight: 600; }
.sk-x { border: 0; background: transparent; color: var(--fg-3); font-size: 18px; cursor: pointer; line-height: 1; }
.sk-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.sk-field { display: flex; flex-direction: column; gap: 5px; font-size: 11px; color: var(--fg-3); }
.sk-form-foot { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.sk-note { font-size: 11px; color: var(--fg-3); }
.sk-list { padding: 0; overflow: hidden; }
.sk-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 14px; border-bottom: 1px solid var(--line); }
.sk-item { display: flex; gap: 12px; justify-content: space-between; padding: 14px; border-bottom: 1px solid var(--line); }
.sk-item:last-child { border-bottom: 0; }
.sk-item-main { min-width: 0; flex: 1; }
.sk-item-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.sk-item-name { font-size: 13px; color: var(--fg); }
.sk-badge { padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px; font-family: var(--mono); font-size: 10px; color: var(--fg-3); }
.sk-mono { font-family: var(--mono); }
.sk-item-desc { margin-top: 6px; font-size: 12px; color: var(--fg-2); line-height: 1.6; }
.sk-item-meta { margin-top: 5px; font-size: 11px; color: var(--fg-3); }
.sk-expand { margin-top: 7px; border: 0; background: transparent; color: var(--accent); font-family: inherit; font-size: 11px; cursor: pointer; padding: 0; }
.sk-body { margin: 8px 0 0; padding: 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); color: var(--fg-2); white-space: pre-wrap; word-break: break-word; font-family: var(--mono); font-size: 12px; max-height: 360px; overflow: auto; }
.sk-item-actions { display: flex; flex-direction: column; gap: 6px; flex: none; }
.sk-del { height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 12px; cursor: pointer; }
.sk-del:hover { border-color: var(--err); color: var(--err); }

@media (max-width: 900px) {
  .sk-stats { grid-template-columns: 1fr 1fr 1fr; }
  .sk-grid { grid-template-columns: 1fr; }
  .sk-item { flex-direction: column; }
  .sk-item-actions { flex-direction: row; }
}
</style>
