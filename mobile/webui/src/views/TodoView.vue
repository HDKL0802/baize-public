<template>
  <div class="todo-view" :class="{ embedded }">
    <header class="m-page-head">
      <div class="m-head-text">
        <h1 v-if="!embedded" class="m-page-title">待办</h1>
        <p class="m-page-sub">共 {{ visibleList.length }} 条 · 已完成 {{ doneCount }}/{{ visibleList.length }}</p>
      </div>
      <button class="m-icon-btn accent" title="新增待办" @click="showForm = true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v14M5 12h14"/></svg>
      </button>
    </header>

    <!-- 分类筛选 -->
    <div class="todo-filters">
      <div class="m-seg">
        <button :class="['m-seg-btn', cat === 'today' ? 'on' : '']" @click="cat = 'today'">今日/日程排期</button>
        <button :class="['m-seg-btn', cat === 'idle' ? 'on' : '']" @click="cat = 'idle'">闲暇待办</button>
      </div>
      <div class="m-seg">
        <button :class="['m-seg-btn', owner === '我' ? 'on' : '']" @click="owner = '我'">我的待办</button>
        <button :class="['m-seg-btn', owner === 'agent' ? 'on' : '']" @click="owner = 'agent'">Agent 待办</button>
      </div>
    </div>

    <!-- 列表 -->
    <div v-if="!visibleList.length" class="m-empty">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="3.5" y="3.5" width="17" height="17" rx="3"/><path d="M8 12l2.8 2.8L16.5 9"/></svg>
      <p>这个分类下还没有待办</p>
      <button class="m-btn ghost" @click="showForm = true">新增一条</button>
    </div>
    <div v-for="t in visibleList" :key="t.id" class="todo-item" :class="{ done: t.done }">
      <button class="todo-check" :class="{ on: t.done }" :aria-label="t.done ? '标记为未完成' : '标记为完成'" @click="onToggle(t)">
        <svg v-if="t.done" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M8 12.3l2.6 2.6L16 9.5"/></svg>
        <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/></svg>
      </button>
      <div class="todo-body">
        <div class="todo-title">{{ t.title }}</div>
        <div class="todo-meta">
          <span class="todo-owner">{{ t.owner === 'agent' ? 'Agent' : '我' }}</span>
          <span v-if="t.due" class="todo-date">{{ t.due }}</span>
          <span class="todo-date">{{ fmtTime(t.ts) }}</span>
        </div>
      </div>
      <button class="todo-del" title="删除" @click="askDel(t)">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M9 7V5.5A1.5 1.5 0 0 1 10.5 4h3A1.5 1.5 0 0 1 15 5.5V7M6.5 7l.8 12.1a2 2 0 0 0 2 1.9h5.4a2 2 0 0 0 2-1.9L17.5 7"/></svg>
      </button>
    </div>

    <div v-if="outboxCount" class="sync-tip">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M20 12a8 8 0 1 1-2.4-5.7"/><path d="M20 4v4h-4"/></svg>
      {{ outboxCount }} 条变更待同步
    </div>

    <!-- 新建待办（底部弹层） -->
    <transition name="sheet">
      <div v-if="showForm" class="m-sheet-mask" @click.self="cancelForm">
        <div class="m-sheet-panel">
          <div class="m-sheet-handle"></div>
          <h2 class="m-sheet-title">新建待办</h2>
          <div class="m-field">
            <label class="m-label">标题</label>
            <input v-model="form.title" class="m-input" type="text" placeholder="要做什么？" @keyup.enter="onAdd" />
          </div>
          <div class="m-field">
            <label class="m-label">归属</label>
            <div class="m-seg">
              <button :class="['m-seg-btn', form.owner === '我' ? 'on' : '']" @click="form.owner = '我'">我的待办</button>
              <button :class="['m-seg-btn', form.owner === 'agent' ? 'on' : '']" @click="form.owner = 'agent'">Agent 待办</button>
            </div>
          </div>
          <div class="m-sheet-actions">
            <button class="m-btn ghost" @click="cancelForm">取消</button>
            <button class="m-btn" @click="onAdd">保存待办</button>
          </div>
        </div>
      </div>
    </transition>

    <!-- 删除二次确认（点遮罩不关闭） -->
    <transition name="sheet">
      <div v-if="pendingDel" class="m-sheet-mask">
        <div class="m-sheet-panel">
          <div class="m-sheet-handle"></div>
          <h2 class="m-sheet-title">删除待办</h2>
          <p class="m-sheet-text">「{{ pendingDel.title }}」删除后无法恢复。</p>
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
import { uid, fmtTime } from '../api.js';

defineProps({ embedded: Boolean });

const store = useStore();

const cat = ref('today');        // today | idle
const owner = ref('我');          // 我 | agent
const showForm = ref(false);
const pendingDel = ref(null);
const form = ref({ title: '', owner: '我' });

// main.js 里预留的快捷入口
onMounted(() => {
  if (window.__bzOpenTodoForm) window.__bzOpenTodoForm = () => { showForm.value = true; };
});

const allTodos = computed(() => store.getTodos());
const doneCount = computed(() => visibleList.value.filter(t => t.done).length);
const outboxCount = computed(() => store.outbox.length);

const visibleList = computed(() => {
  return allTodos.value.filter(t => {
    if (owner.value === '我' && t.owner === 'agent') return false;
    if (owner.value === 'agent' && t.owner !== 'agent') return false;
    // today：有 due 或标为今日；idle：没有排期
    if (cat.value === 'today') return !!(t.due || t.today);
    if (cat.value === 'idle') return !(t.due || t.today);
    return true;
  });
});

function save(list) { store.saveTodos(list); }

function onAdd() {
  const title = form.value.title.trim();
  if (!title) return;
  const t = { id: uid(), title, done: false, ts: Date.now(), owner: form.value.owner, cat: cat.value };
  save(allTodos.value.concat(t));
  form.value = { title: '', owner: '我' };
  showForm.value = false;
}

function cancelForm() {
  form.value = { title: '', owner: '我' };
  showForm.value = false;
}

function onToggle(t) {
  const list = allTodos.value.slice();
  const item = list.find(x => x.id === t.id);
  if (!item) return;
  item.done = !item.done;
  save(list);
}

function askDel(t) {
  pendingDel.value = t;
}

function confirmDel() {
  const t = pendingDel.value;
  if (!t) return;
  save(allTodos.value.filter(x => x.id !== t.id));
  pendingDel.value = null;
  store.toast('已删除', 'ok');
}
</script>

<style scoped>
.todo-view { display: flex; flex-direction: column; }
.todo-filters {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 12px;
}

/* 列表项 */
.todo-item {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  min-height: 56px;
  padding: 6px 8px 6px 4px;
  margin-bottom: 10px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 6px;
}
.todo-check {
  flex: none;
  width: 44px; height: 44px;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0;
  background: none;
  border: none;
  color: var(--fg-3);
  cursor: pointer;
}
.todo-check svg { width: 22px; height: 22px; }
.todo-check.on { color: var(--accent); }
.todo-body { flex: 1; min-width: 0; padding-top: 9px; }
.todo-title {
  font-size: 14px;
  line-height: 1.45;
  word-break: break-word;
}
.todo-item.done .todo-title {
  color: var(--fg-3);
  text-decoration: line-through;
}
.todo-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-top: 4px;
  font-size: 11px;
  color: var(--fg-3);
}
.todo-owner {
  padding: 0 5px;
  border: 1px solid var(--line);
  border-radius: 4px;
  background: var(--surface-2);
  font-family: ui-monospace, "SF Mono", Consolas, monospace;
  font-size: 10px;
  line-height: 16px;
}
.todo-date { font-family: ui-monospace, "SF Mono", Consolas, monospace; }
.todo-del {
  flex: none;
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
.todo-del svg { width: 18px; height: 18px; }
.todo-del:active { color: var(--err); background: var(--surface-2); }

.sync-tip {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 10px 0;
  font-size: 11px;
  color: var(--fg-3);
}
.sync-tip svg { width: 14px; height: 14px; }
</style>
