<template>
  <div class="settings-view">
    <header class="m-page-head">
      <div>
        <h1 class="m-page-title">设置</h1>
        <p class="m-page-sub">白泽智能体 · 手机端</p>
      </div>
    </header>

    <!-- 主题 -->
    <div class="m-card">
      <div class="m-card-title">外观</div>
      <div class="setting-row">
        <span>主题</span>
        <div class="theme-row">
          <button :class="['theme-btn', store.theme === 'dark' ? 'on' : '']" @click="store.setTheme('dark')">深色</button>
          <button :class="['theme-btn', store.theme === 'light' ? 'on' : '']" @click="store.setTheme('light')">浅色</button>
          <button :class="['theme-btn', store.theme === 'system' ? 'on' : '']" @click="store.setTheme('system')">跟随系统</button>
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
        <input v-model="ai.apiKey" class="m-input" :type="showAiKey ? 'text' : 'password'" placeholder="sk-…" />
        <button class="mini-act" @click="showAiKey = !showAiKey">{{ showAiKey ? '隐藏' : '显示' }}</button>
      </div>
      <div class="m-field">
        <label class="m-label">模型</label>
        <input v-model="ai.model" class="m-input" type="text" placeholder="gpt-4o / claude-… " />
      </div>
      <button class="m-btn" @click="onSaveAi">保存 AI 配置</button>
    </div>

    <!-- 设备 / 跨平台 -->
    <div class="m-card">
      <div class="m-card-title">设备 / 跨平台</div>
      <div v-if="core.native" class="setting-row">
        <span>内核</span>
        <span :class="['dot', core.running ? 'ok' : '']">{{ core.running ? '运行中' : (core.supported ? '已停止' : '不可用') }}</span>
      </div>
      <div class="m-field">
        <label class="m-label">后端地址（Server）</label>
        <input v-model="device.server" class="m-input" type="text" placeholder="http://192.168.x.x:8000" />
      </div>
      <div class="m-field">
        <label class="m-label">配对令牌（Token）</label>
        <input v-model="device.token" class="m-input" :type="showDevToken ? 'text' : 'password'" placeholder="配对令牌" />
      </div>
      <div class="setting-row">
        <span>跨平台同步</span>
        <input type="checkbox" v-model="device.enabled" class="big-check" />
      </div>
      <div class="device-actions">
        <button v-if="core.native" class="m-btn" @click="onSaveDevice">应用并重启内核</button>
        <button v-else class="m-btn ghost" @click="store.toast('仅在 APK 入口可用', 'err')">仅 APK 可用</button>
      </div>
    </div>

    <!-- 数据管理 -->
    <div class="m-card">
      <div class="m-card-title">数据管理</div>
      <div class="data-actions">
        <button class="m-btn" @click="onExport">导出 Markdown</button>
        <button class="m-btn ghost" @click="onImport">导入</button>
        <button class="m-btn danger" @click="onClear">清除全部数据</button>
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
  </div>
</template>

<script setup>
import { ref, reactive, onMounted } from 'vue';
import { useStore } from '../store.js';
import { bzCoreStatus, saveTextFile, pickTextFile, takePickedText } from '../api.js';

const store = useStore();

const ai = reactive({ ...store.getSettings().ai });
const device = reactive({
  server: '',
  token: '',
  enabled: false,
});
const showAiKey = ref(false);
const showDevToken = ref(false);
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

function onClear() {
  if (!confirm('确定清除本机所有数据？此操作不可恢复。')) return;
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
  display: flex; align-items: center; justify-content: space-between;
  padding: 6px 0; font-size: 13px;
}
.dot { font-size: 12px; color: var(--fg-2); }
.dot.ok { color: var(--ok); }

.theme-row { display: flex; gap: 6px; }
.theme-btn {
  padding: 4px 10px; border-radius: 6px; font-size: 12px;
  background: var(--surface-2); border: 1px solid var(--line);
  color: var(--fg-2); cursor: pointer;
}
.theme-btn.on { background: var(--accent); border-color: var(--accent); color: #fff; }

.mini-act {
  margin-top: 4px; padding: 2px 8px; font-size: 11px;
  background: none; border: 1px solid var(--line-2);
  border-radius: 4px; color: var(--fg-3); cursor: pointer;
}

.big-check { width: 20px; height: 20px; accent-color: var(--accent); }

.device-actions { margin-top: 10px; }
.data-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.data-hint { font-size: 11px; color: var(--fg-3); margin-top: 8px; }

.about-row {
  display: flex; justify-content: space-between;
  padding: 5px 0; font-size: 13px;
}
.about-row span:first-child { color: var(--fg-2); }
</style>
