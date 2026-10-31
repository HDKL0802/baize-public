<template>
  <div class="settings-view">
    <header class="m-page-head">
      <div class="m-head-text">
        <h1 class="m-page-title">设置</h1>
        <p class="m-page-sub">白泽智能体 · 手机端</p>
      </div>
      <button class="m-icon-btn" title="保存 AI 配置" @click="onSaveAi">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M5 4.5h10.5L20 9v10.5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1v-14a1 1 0 0 1 1-1z"/><path d="M8 4.5v5h7v-5M8 20v-5.5h8V20"/></svg>
      </button>
    </header>

    <!-- 外观 -->
    <div class="m-card">
      <div class="m-card-title">外观</div>
      <div class="setting-row">
        <span class="setting-label">主题</span>
        <div class="m-seg theme-seg">
          <button :class="['m-seg-btn', store.theme === 'dark' ? 'on' : '']" @click="store.setTheme('dark')">深色</button>
          <button :class="['m-seg-btn', store.theme === 'light' ? 'on' : '']" @click="store.setTheme('light')">浅色</button>
          <button :class="['m-seg-btn', store.theme === 'system' ? 'on' : '']" @click="store.setTheme('system')">跟随系统</button>
        </div>
      </div>
    </div>

    <!-- AI 模型配置 -->
    <div class="m-card">
      <div class="m-card-title">AI 模型</div>
      <div class="m-field">
        <label class="m-label">协议</label>
        <select v-model="ai.protocol" class="m-input">
          <option value="openai">OpenAI 兼容</option>
          <option value="anthropic">Anthropic</option>
        </select>
      </div>
      <div class="m-field">
        <label class="m-label">Base URL</label>
        <input v-model="ai.baseUrl" class="m-input" type="text" placeholder="https://…" />
      </div>
      <div class="m-field">
        <label class="m-label">API Key</label>
        <div class="m-input-wrap">
          <input v-model="ai.apiKey" class="m-input" :type="showAiKey ? 'text' : 'password'" placeholder="sk-…" />
          <button class="m-input-act" :title="showAiKey ? '隐藏' : '显示'" @click="showAiKey = !showAiKey">
            <svg v-if="showAiKey" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="2.5"/></svg>
            <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3l18 18"/><path d="M10.6 6.1A9.7 9.7 0 0 1 12 6c6 0 9.5 6 9.5 6a15 15 0 0 1-2.6 3.4"/><path d="M6.4 7.4C3.9 9 2.5 12 2.5 12S6 18 12 18a9.6 9.6 0 0 0 4.2-1"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>
          </button>
        </div>
      </div>
      <div class="m-field">
        <label class="m-label">模型</label>
        <input v-model="ai.model" class="m-input" type="text" placeholder="gpt-4o / claude-…" />
      </div>
      <button class="m-btn block" @click="onSaveAi">保存 AI 配置</button>
    </div>

    <!-- 设备 / 跨平台 -->
    <div class="m-card">
      <div class="m-card-title">设备 / 跨平台</div>
      <div v-if="core.native" class="setting-row">
        <span class="setting-label">内核</span>
        <span :class="['status-pill', core.running ? 'on' : '']">
          <span class="status-dot"></span>
          {{ core.running ? '运行中' : (core.supported ? '已停止' : '不可用') }}
        </span>
      </div>
      <div class="m-field">
        <label class="m-label">后端地址（Server）</label>
        <input v-model="device.server" class="m-input" type="text" placeholder="http://192.168.x.x:8000" />
      </div>
      <div class="m-field">
        <label class="m-label">配对令牌（Token）</label>
        <div class="m-input-wrap">
          <input v-model="device.token" class="m-input" :type="showDevToken ? 'text' : 'password'" placeholder="配对令牌" />
          <button class="m-input-act" :title="showDevToken ? '隐藏' : '显示'" @click="showDevToken = !showDevToken">
            <svg v-if="showDevToken" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="2.5"/></svg>
            <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3l18 18"/><path d="M10.6 6.1A9.7 9.7 0 0 1 12 6c6 0 9.5 6 9.5 6a15 15 0 0 1-2.6 3.4"/><path d="M6.4 7.4C3.9 9 2.5 12 2.5 12S6 18 12 18a9.6 9.6 0 0 0 4.2-1"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>
          </button>
        </div>
      </div>
      <div class="setting-row">
        <span class="setting-label">跨平台同步</span>
        <label class="row-check">
          <input type="checkbox" v-model="device.enabled" />
        </label>
      </div>
      <div class="setting-actions">
        <button v-if="core.native" class="m-btn block" @click="onSaveDevice">应用并重启内核</button>
        <button v-else class="m-btn ghost block" @click="store.toast('仅在 APK 入口可用', 'err')">仅 APK 可用</button>
      </div>
    </div>

    <!-- 数据管理 -->
    <div class="m-card">
      <div class="m-card-title">数据管理</div>
      <div class="data-actions">
        <button class="m-btn" @click="onExport">导出 Markdown</button>
        <button class="m-btn ghost" @click="onImport">导入</button>
        <button class="m-btn danger data-clear" @click="showClearConfirm = true">清除全部数据</button>
      </div>
      <div class="data-hint">导出为单个 .md 文件（待办 + 密码本 + 设置），导入前请先备份。</div>
    </div>

    <!-- 关于 -->
    <div class="m-card">
      <div class="m-card-title">关于</div>
      <div class="about-row"><span>应用</span><span>白泽 Baize 个人智能体</span></div>
      <div class="about-row"><span>版本</span><span>0.1.0</span></div>
      <div class="about-row"><span>模式</span><span>{{ store.kbMode ? '知识库（NAS）' : '本机' }}</span></div>
    </div>

    <!-- 清除数据二次确认（点遮罩不关闭） -->
    <transition name="sheet">
      <div v-if="showClearConfirm" class="m-sheet-mask">
        <div class="m-sheet-panel">
          <div class="m-sheet-handle"></div>
          <h2 class="m-sheet-title">清除全部数据</h2>
          <p class="m-sheet-text">将删除本机保存的待办、对话、密码本与设置，此操作不可恢复。</p>
          <div class="m-sheet-actions">
            <button class="m-btn ghost" @click="showClearConfirm = false">否</button>
            <button class="m-btn danger" @click="doClear">是，删除</button>
          </div>
        </div>
      </div>
    </transition>
  </div>
</template>

<script setup>
import { ref, reactive, onMounted } from 'vue';
import { useStore } from '../store.js';
import { bzCoreStatus, bzCoreConfigure, saveTextFile, pickTextFile, takePickedText } from '../api.js';

const store = useStore();

const ai = reactive({ ...store.getSettings().ai });
const device = reactive({
  server: '',
  token: '',
  enabled: false,
});
const showAiKey = ref(false);
const showDevToken = ref(false);
const showClearConfirm = ref(false);
const core = ref(bzCoreStatus());

onMounted(async () => {
  core.value = bzCoreStatus();
  await store.refreshRemote();
  // H5 导入回调用
  window.__bzFileContent = async (name, text) => {
    try {
      JSON.parse(text);
      store.toast('已选择文件：' + name, 'ok');
    } catch {
      store.toast('导入失败：不是有效的 JSON/Markdown 备份', 'err');
    }
  };
});

function onSaveAi() {
  store.saveSettings({ ai: { ...ai } });
  store.toast('AI 配置已保存', 'ok');
}

function onSaveDevice() {
  store.saveSettings({ kbServer: device.server, kbToken: device.token });
  const r = bzCoreConfigure(device.server, device.token, device.enabled);
  if (r.error) store.toast(r.error, 'err');
  else store.toast('内核配置已应用', 'ok');
  setTimeout(() => { core.value = bzCoreStatus(); }, 800);
}

function buildExport() {
  const todos = store.getTodos();
  const lines = ['# 白泽数据导出', '', '## 待办', ''];
  todos.forEach(t => {
    lines.push('- [ ] ' + (t.title || '') + (t.owner === 'agent' ? '（Agent）' : ''));
  });
  return lines.join('\n');
}

function onExport() {
  const name = '白泽备份-' + new Date().toISOString().slice(0, 10) + '.md';
  saveTextFile(name, buildExport());
  store.toast('已导出', 'ok');
}

function onImport() {
  pickTextFile();
}

function doClear() {
  showClearConfirm.value = false;
  localStorage.removeItem('bz_todos');
  localStorage.removeItem('bz_chats');
  localStorage.removeItem('bz_chat');
  localStorage.removeItem('bz_vault');
  localStorage.removeItem('bz_settings');
  location.reload();
}
</script>

<style scoped>
.settings-view { display: flex; flex-direction: column; }

.setting-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 52px;
  font-size: 13px;
}
.setting-row + .setting-row { border-top: 1px solid var(--line); }
.setting-label { flex: none; color: var(--fg); }
.theme-seg { flex: 0 1 216px; }

.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--fg-2);
}
.status-dot {
  width: 6px; height: 6px;
  border-radius: 999px;
  background: var(--fg-3);
}
.status-pill.on { color: var(--ok); }
.status-pill.on .status-dot { background: var(--ok); }

.row-check {
  width: 44px; height: 44px;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  cursor: pointer;
}
.row-check input {
  width: 22px; height: 22px;
  accent-color: var(--accent);
}

.setting-actions { margin-top: 8px; }
.data-actions {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
}
.data-actions .m-btn { height: 44px; }
.data-actions .m-btn.ghost { height: 44px; }
.data-clear { grid-column: 1 / -1; }
.data-hint {
  margin-top: 10px;
  font-size: 11px;
  line-height: 1.5;
  color: var(--fg-3);
}

.about-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 52px;
  font-size: 13px;
}
.about-row + .about-row { border-top: 1px solid var(--line); }
.about-row span:first-child { color: var(--fg-2); }
</style>
