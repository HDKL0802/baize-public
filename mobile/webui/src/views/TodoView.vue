<template>
  <div class="todo-view">
    <header class="m-page-head">
      <div>
        <h1 class="m-page-title">待办</h1>
        <p class="m-page-sub">共 {{ visibleList.length }} 条 · 已完成 {{ doneCount }}/{{ visibleList.length }}</p>
      </div>
      <button v-if="!showForm" class="m-btn" @click="showForm = true">＋ 新增</button>
      <button v-else class="m-btn ghost" @click="cancelForm">取消</button>
    </header>

    <!-- 快速添加 -->
    <div v-if="showForm" class="m-card">
      <div class="m-field">
        <label class="m-label">标题</label>
        <input v-model="form.title" class="m-input" type="text" placeholder="要做什么？" @keyup.enter="onAdd" />
      </div>
      <div class="m-field">
        <label class="m-label">归属</label>
        <div class="owner-row">
          <button :class="['owner-tag', form.owner === '我' ? 'on' : '']" @click="form.owner = '我'">我的待办</button>
          <button :class="['owner-tag', form.owner === 'agent' ? 'on' : '']" @click="form.owner = 'agent'">Agent 待办</button>
        </div>
      </div>
      <button class="m-btn" @click="onAdd">保存待办</button>
    </div>

    <!-- 分类 tab -->
    <div class="tab-row">
      <button :class="['tab', cat === 'today' ? 'on' : '']" @click="cat = 'today'">今日/日程排期</button>
      <button :class="['tab', cat === 'idle' ? 'on' : '']" @click="cat = 'idle'">闲暇待办</button>
    </div>
    <div class="tab-row">
      <button :class="['tab', owner === '我' ? 'on' : '']" @click="owner = '我'">我的待办</button>
      <button :class="['tab', owner === 'agent' ? 'on' : '']" @click="owner = 'agent'">Agent 待办</button>
    </div>

    <!-- 列表 -->
    <div v-if="!visibleList.length" class="m-empty">这个分类下还没有待办</div>
    <div v-for="t in visibleList" :key="t.id" class="todo-item" :class="{ done: t.done }">
      <label class="todo-check">
        <input type="checkbox" :checked="!!t.done" @change="onToggle(t)" />
      </label>
      <div class="todo-body">
        <div class="todo-title">{{ t.title }}</div>
        <div class="todo-meta">
          <span class="todo-owner">{{ t.owner === 'agent' ? 'Agent' : '我' }}</span>
          <span v-if="t.due">· {{ t.due }}</span>
          <span>· {{ fmtTime(t.ts) }}</span>
        </div>
      </div>
      <button class="todo-del" @click="onDel(t)" title="删除">✕</button>
    </div>

    <div v-if="outboxCount" class="sync-tip">{{ outboxCount }} 条变更待同步</div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue';
import { useStore } from '../store.js';
import { uid, fmtTime } from '../api.js';

const store = useStore();

const cat = ref('today');        // today | idle
const owner = ref('我');          // 我 | agent
const showForm = ref(false);
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

function onDel(t) {
  save(allTodos.value.filter(x => x.id !== t.id));
  store.toast('已删除', 'ok');
}
</script>

<style scoped>
.todo-view { display: flex; flex-direction: column; }

/* 快速表单 */
.owner-row { display: flex; gap: 8px; }
.owner-tag {
  padding: 6px 12px; border-radius: 8px; font-size: 12px;
  background: var(--surface-2); border: 1px solid var(--line);
  color: var(--fg-2); cursor: pointer;
}
.owner-tag.on { border-color: var(--accent); color: var(--accent); }

/* tab */
.tab-row {
  display: flex; gap: 8px; margin: 4px 0 12px;
  overflow-x: auto; scrollbar-width: none;
}
.tab-row::-webkit-scrollbar { display: none; }
.tab {
  flex: none; padding: 6px 14px; border-radius: 16px;
  font-size: 12px; background: var(--surface-2);
  border: 1px solid var(--line); color: var(--fg-2); cursor: pointer;
}
.tab.on { background: var(--accent); border-color: var(--accent); color: #fff; }

/* 列表项 */
.todo-item {
  display: flex; align-items: flex-start; gap: 10px;
  background: var(--surface); border: 1px solid var(--line);
  border-radius: var(--card-radius); padding: 10px 12px;
  margin-bottom: 8px;
}
.todo-item.done .todo-title { text-decoration: line-through; color: var(--fg-3); }
.todo-check { margin-top: 2px; }
.todo-check input { width: 18px; height: 18px; accent-color: var(--accent); cursor: pointer; }
.todo-body { flex: 1; min-width: 0; }
.todo-title { font-size: 14px; word-break: break-word; }
.todo-meta { font-size: 11px; color: var(--fg-3); margin-top: 2px; }
.todo-owner {
  display: inline-block; padding: 0 6px; border-radius: 4px;
  background: var(--surface-2); border: 1px solid var(--line-2);
}
.todo-del {
  flex: none; width: 28px; height: 28px;
  border-radius: 6px; border: none; background: transparent;
  color: var(--fg-3); cursor: pointer; font-size: 14px;
}
.todo-del:active { background: var(--surface-2); color: var(--err); }

.sync-tip {
  text-align: center; font-size: 12px; color: var(--warn);
  padding: 10px 0;
}
</style>
