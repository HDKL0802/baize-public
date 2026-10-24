<template>
  <div class="vault-view">
    <header class="m-page-head">
      <div>
        <h1 class="m-page-title">密码本</h1>
        <p class="m-page-sub">
          共 {{ filtered.length }} 条 · {{ store.kbMode ? '知识库（NAS）' : '本机加密' }}
        </p>
      </div>
      <button v-if="!showForm" class="m-btn" @click="openForm()">＋ 新增</button>
      <button v-else class="m-btn ghost" @click="showForm = false">取消</button>
    </header>

    <!-- 编辑/新增表单 -->
    <div v-if="showForm" class="m-card">
      <div class="m-field">
        <label class="m-label">标题</label>
        <input v-model="form.title" class="m-input" type="text" placeholder="如：GitHub / 邮箱 / 银行" @keyup.enter="onSave" />
      </div>
      <div class="m-field">
        <label class="m-label">账号</label>
        <input v-model="form.account" class="m-input" type="text" placeholder="用户名 / 邮箱 / 手机号" />
      </div>
      <div class="m-field">
        <label class="m-label">密码</label>
        <input v-model="form.password" class="m-input" :type="showPwd ? 'text' : 'password'" placeholder="密码" @keyup.enter="onSave" />
      </div>
      <div class="m-field">
        <label class="m-label">备注（可选）</label>
        <input v-model="form.note" class="m-input" type="text" placeholder="备注信息" @keyup.enter="onSave" />
      </div>
      <div class="vault-form-actions">
        <button class="m-btn" @click="onSave">保存</button>
        <button class="m-btn ghost" @click="showPwd = !showPwd">{{ showPwd ? '隐藏' : '显示密码' }}</button>
      </div>
    </div>

    <!-- 搜索 -->
    <div class="vault-search">
      <input v-model="kw" class="m-input" type="search" placeholder="搜索标题 / 账号…" />
    </div>

    <!-- 列表 -->
    <div v-if="loading" class="m-empty">加载中…</div>
    <div v-else-if="!filtered.length" class="m-empty">没有匹配的记录</div>
    <div v-for="v in filtered" :key="v.id" class="vault-item">
      <div class="vault-item-head">
        <span class="vault-title">{{ v.title || '未命名' }}</span>
        <div class="vault-item-ops">
          <button class="vault-act" @click="copyPwd(v)" title="复制密码">复制</button>
          <button class="vault-act" @click="openForm(v)">编辑</button>
          <button class="vault-act danger" @click="onDel(v)">删除</button>
        </div>
      </div>
      <div class="vault-item-body">
        <span v-if="v.account" class="vault-account">@ {{ v.account }}</span>
        <span class="vault-pwd" :class="{ show: revealed[v.id] }" @click="toggleReveal(v)">
          {{ revealed[v.id] ? v.password : '••••••••' }}
        </span>
        <span v-if="v.note" class="vault-note">{{ v.note }}</span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue';
import { useStore } from '../store.js';
import { uid } from '../api.js';

const store = useStore();

const list = ref([]);
const loading = ref(false);
const kw = ref('');
const showForm = ref(false);
const showPwd = ref(false);
const revealed = ref({});
const editingId = ref(null);
const form = ref(emptyForm());

function emptyForm() {
  return { title: '', account: '', password: '', note: '' };
}

const filtered = computed(() => {
  const k = kw.value.trim().toLowerCase();
  if (!k) return list.value;
  return list.value.filter(v =>
    (v.title || '').toLowerCase().includes(k) ||
    (v.account || '').toLowerCase().includes(k)
  );
});

async function load() {
  loading.value = true;
  try {
    const r = await store.getVault();
    if (r.ok) list.value = r.list || [];
  } finally {
    loading.value = false;
  }
}

async function persist(next) {
  list.value = next;
  await store.saveVault(next);
}

function openForm(v) {
  editingId.value = v ? v.id : null;
  form.value = v
    ? { title: v.title || '', account: v.account || '', password: v.password || '', note: v.note || '' }
    : emptyForm();
  showPwd.value = false;
  showForm.value = true;
}

async function onSave() {
  const f = form.value;
  if (!f.title.trim() && !f.account.trim() && !f.password.trim()) return;
  let next;
  if (editingId.value) {
    next = list.value.map(x => x.id === editingId.value ? { ...x, ...f } : x);
    store.toast('已更新', 'ok');
  } else {
    next = list.value.concat({ id: uid(), ts: Date.now(), ...f });
    store.toast('已添加', 'ok');
  }
  await persist(next);
  showForm.value = false;
  editingId.value = null;
}

async function onDel(v) {
  await persist(list.value.filter(x => x.id !== v.id));
  store.toast('已删除', 'ok');
}

async function copyPwd(v) {
  try {
    await navigator.clipboard.writeText(v.password || '');
    store.toast('密码已复制', 'ok');
  } catch {
    store.toast('复制失败', 'err');
  }
}

function toggleReveal(v) {
  revealed.value[v.id] = !revealed.value[v.id];
}

onMounted(() => {
  load();
  if (window.__bzOpenVaultForm) window.__bzOpenVaultForm = () => openForm(null);
});
</script>

<style scoped>
.vault-view { display: flex; flex-direction: column; }

.vault-form-actions { display: flex; gap: 8px; }
.vault-search { margin-bottom: 12px; }

.vault-item {
  background: var(--surface); border: 1px solid var(--line);
  border-radius: var(--card-radius); padding: 10px 12px;
  margin-bottom: 8px;
}
.vault-item-head {
  display: flex; align-items: center; justify-content: space-between;
  margin-bottom: 6px;
}
.vault-title { font-size: 14px; font-weight: 600; }
.vault-item-ops { display: flex; gap: 4px; }
.vault-act {
  padding: 3px 8px; border-radius: 6px; font-size: 11px;
  background: var(--surface-2); border: 1px solid var(--line-2);
  color: var(--fg-2); cursor: pointer;
}
.vault-act.danger { color: var(--err); }
.vault-item-body {
  display: flex; flex-wrap: wrap; align-items: center; gap: 8px;
  font-size: 13px;
}
.vault-account { color: var(--fg-2); }
.vault-pwd {
  font-family: ui-monospace, Consolas, monospace;
  color: var(--accent); cursor: pointer; user-select: none;
}
.vault-note { color: var(--fg-3); font-size: 12px; }
</style>
