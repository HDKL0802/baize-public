<template>
  <div class="vault-view" :class="{ embedded }">
    <header class="m-page-head">
      <div class="m-head-text">
        <h1 v-if="!embedded" class="m-page-title">密码本</h1>
        <p class="m-page-sub">共 {{ filtered.length }} 条 · {{ store.kbMode ? '知识库（NAS）' : '本机加密' }}</p>
      </div>
      <button class="m-icon-btn accent" title="新增密码" @click="openForm()">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v14M5 12h14"/></svg>
      </button>
    </header>

    <!-- 搜索 -->
    <div class="vault-search">
      <svg class="search-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="10.5" cy="10.5" r="6.5"/><path d="M15.5 15.5L21 21"/></svg>
      <input v-model="kw" class="m-input" type="search" placeholder="搜索标题 / 账号…" />
    </div>

    <!-- 列表 -->
    <div v-if="loading" class="m-empty">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="8.5"/><path d="M12 7v5l3 2"/></svg>
      <p>加载中…</p>
    </div>
    <div v-else-if="!filtered.length" class="m-empty">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="14" r="4"/><path d="M11 11l8-8M16 4l3 3M14 6l2.5 2.5"/></svg>
      <p>{{ kw ? '没有匹配的记录' : '还没有保存密码' }}</p>
      <button v-if="kw" class="m-btn ghost" @click="kw = ''">清空搜索</button>
      <button v-else class="m-btn ghost" @click="openForm()">新增一条</button>
    </div>

    <div v-for="v in filtered" :key="v.id" class="vault-item">
      <div class="vault-item-head">
        <span class="vault-icon">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="14" r="4"/><path d="M11 11l8-8M16 4l3 3M14 6l2.5 2.5"/></svg>
        </span>
        <div class="vault-body">
          <div class="vault-title">{{ v.title || '未命名' }}</div>
          <div class="vault-meta">
            <span v-if="v.account">@ {{ v.account }}</span>
            <span v-if="v.note">· {{ v.note }}</span>
          </div>
        </div>
        <div class="vault-ops">
          <button class="vault-act" title="复制密码" @click="copyPwd(v)">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="8.5" y="8.5" width="11" height="11" rx="2"/><path d="M15.5 8.5v-2a2 2 0 0 0-2-2h-7a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h2"/></svg>
          </button>
          <button class="vault-act" title="编辑" @click="openForm(v)">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M16.2 4.3l3.5 3.5L12.5 15l-3.8.6.6-3.8 6.9-7.5z"/><path d="M12 20H5.5A1.5 1.5 0 0 1 4 18.5V6a2 2 0 0 1 2-2h5"/></svg>
          </button>
          <button class="vault-act danger" title="删除" @click="askDel(v)">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M9 7V5.5A1.5 1.5 0 0 1 10.5 4h3A1.5 1.5 0 0 1 15 5.5V7M6.5 7l.8 12.1a2 2 0 0 0 2 1.9h5.4a2 2 0 0 0 2-1.9L17.5 7"/></svg>
          </button>
        </div>
      </div>
      <button class="vault-secret" :class="{ show: revealed[v.id] }" @click="toggleReveal(v)">
        <span class="mono">{{ revealed[v.id] ? v.password : '••••••••' }}</span>
        <svg v-if="revealed[v.id]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="2.5"/></svg>
        <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3l18 18"/><path d="M10.6 6.1A9.7 9.7 0 0 1 12 6c6 0 9.5 6 9.5 6a15 15 0 0 1-2.6 3.4"/><path d="M6.4 7.4C3.9 9 2.5 12 2.5 12S6 18 12 18a9.6 9.6 0 0 0 4.2-1"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>
      </button>
    </div>

    <!-- 新建 / 编辑（底部弹层） -->
    <transition name="sheet">
      <div v-if="showForm" class="m-sheet-mask" @click.self="closeForm">
        <div class="m-sheet-panel">
          <div class="m-sheet-handle"></div>
          <h2 class="m-sheet-title">{{ editingId ? '编辑密码' : '新增密码' }}</h2>
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
            <div class="m-input-wrap">
              <input v-model="form.password" class="m-input" :type="showPwd ? 'text' : 'password'" placeholder="密码" @keyup.enter="onSave" />
              <button class="m-input-act" :title="showPwd ? '隐藏密码' : '显示密码'" @click="showPwd = !showPwd">
                <svg v-if="showPwd" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="2.5"/></svg>
                <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3l18 18"/><path d="M10.6 6.1A9.7 9.7 0 0 1 12 6c6 0 9.5 6 9.5 6a15 15 0 0 1-2.6 3.4"/><path d="M6.4 7.4C3.9 9 2.5 12 2.5 12S6 18 12 18a9.6 9.6 0 0 0 4.2-1"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>
              </button>
            </div>
          </div>
          <div class="m-field">
            <label class="m-label">备注（可选）</label>
            <input v-model="form.note" class="m-input" type="text" placeholder="备注信息" @keyup.enter="onSave" />
          </div>
          <div class="m-sheet-actions">
            <button class="m-btn ghost" @click="closeForm">取消</button>
            <button class="m-btn" @click="onSave">保存</button>
          </div>
        </div>
      </div>
    </transition>

    <!-- 删除二次确认（点遮罩不关闭） -->
    <transition name="sheet">
      <div v-if="pendingDel" class="m-sheet-mask">
        <div class="m-sheet-panel">
          <div class="m-sheet-handle"></div>
          <h2 class="m-sheet-title">删除密码</h2>
          <p class="m-sheet-text">「{{ pendingDel.title || '未命名' }}」删除后无法恢复。</p>
          <div class="m-sheet-actions">
            <button class="m-btn ghost" @click="pendingDel = null">否</button>
            <button class="m-btn danger" @click="confirmDel">是，删除</button>
          </div>
        </div>
      </div>
    </transition>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue';
import { useStore } from '../store.js';
import { uid } from '../api.js';

defineProps({ embedded: Boolean });

const store = useStore();

const list = ref([]);
const loading = ref(false);
const kw = ref('');
const showForm = ref(false);
const showPwd = ref(false);
const revealed = ref({});
const editingId = ref(null);
const pendingDel = ref(null);
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

function closeForm() {
  showForm.value = false;
  editingId.value = null;
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

function askDel(v) {
  pendingDel.value = v;
}

async function confirmDel() {
  const v = pendingDel.value;
  if (!v) return;
  await persist(list.value.filter(x => x.id !== v.id));
  pendingDel.value = null;
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

/* 搜索 */
.vault-search { position: relative; margin-bottom: 12px; }
.vault-search .search-icon {
  position: absolute;
  left: 12px; top: 50%;
  width: 16px; height: 16px;
  transform: translateY(-50%);
  color: var(--fg-3);
  pointer-events: none;
}
.vault-search .m-input { padding-left: 34px; }

/* 列表卡片 */
.vault-item {
  padding: 10px 12px;
  margin-bottom: 10px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 6px;
}
.vault-item-head {
  display: flex;
  align-items: center;
  gap: 10px;
}
.vault-icon {
  flex: none;
  width: 28px; height: 28px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--fg-2);
}
.vault-icon svg { width: 28px; height: 28px; }
.vault-body { flex: 1; min-width: 0; }
.vault-title {
  font-size: 14px;
  font-weight: 500;
  line-height: 1.4;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.vault-meta {
  display: flex;
  gap: 6px;
  margin-top: 2px;
  font-size: 11px;
  color: var(--fg-3);
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.vault-meta span { overflow: hidden; text-overflow: ellipsis; }
.vault-ops { flex: none; display: flex; gap: 8px; }
.vault-act {
  width: 44px; height: 44px;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0;
  background: none;
  border: none;
  border-radius: 4px;
  color: var(--fg-3);
  cursor: pointer;
}
.vault-act svg { width: 18px; height: 18px; }
.vault-act:active { background: var(--surface-2); color: var(--fg); }
.vault-act.danger:active { color: var(--err); }

/* 密码行 */
.vault-secret {
  width: 100%;
  height: 44px;
  margin-top: 8px;
  padding: 0 12px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  background: var(--surface-2);
  border: 1px solid var(--line);
  border-radius: 4px;
  color: var(--fg-2);
  font: inherit;
  font-size: 13px;
  text-align: left;
  cursor: pointer;
}
.vault-secret span {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.vault-secret svg { flex: none; width: 18px; height: 18px; color: var(--fg-3); }
.vault-secret.show { color: var(--accent); border-color: rgba(62, 217, 198, .3); }
[data-theme="light"] .vault-secret.show { border-color: rgba(15, 138, 126, .3); }
</style>
