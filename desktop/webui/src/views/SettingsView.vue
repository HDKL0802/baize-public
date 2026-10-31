<!-- 设置页：连接 + 本机设备 + 外观（替换旧 renderSettings 的核心部分）。 -->
<script setup>
import { ref, onMounted } from 'vue';
import { NButton, NInput } from 'naive-ui';
import { API, localDeviceInfo } from '../api.js';

function toast(msg, kind) {
  const t = document.createElement('div');
  t.className = 'bz-toast ' + (kind || '');
  t.textContent = msg;
  document.body.appendChild(t);
  setTimeout(() => t.remove(), 3800);
}

const server = ref('');
const token = ref('');
const info = ref({});
const device = ref({});
const themePref = ref('system');
const THEMES = [
  { v: 'light', label: '浅色' },
  { v: 'dark', label: '深色' },
  { v: 'system', label: '跟随系统' },
];

onMounted(async () => {
  const c = await API.localConfig();
  server.value = c.server || '';
  info.value = c;
  device.value = await localDeviceInfo();
  themePref.value = localStorage.getItem('bz_theme') || 'system';
});

// 主题交由 main.js 的 __bzTheme 统一处理，data-theme 变化会同步到 Naive 主题
function applyTheme() {
  if (window.__bzTheme) window.__bzTheme.setPref(themePref.value);
  toast('已应用主题：' + themePref.value, 'ok');
}

function pickTheme(v) {
  themePref.value = v;
  applyTheme();
}

async function saveConfig() {
  const r = await API.localPost('/config', { server: server.value, token: token.value });
  if (r.ok !== false) {
    token.value = '';
    toast('已保存连接配置', 'ok');
  } else {
    toast('保存失败：' + (r.error || '未知'), 'err');
  }
}

async function testConn() {
  toast('正在测试连接…', 'warn');
  try {
    const r = await API.localPost('/config/test', { server: server.value, token: token.value });
    toast(r.ok ? '连接正常' : ('连接失败：' + (r.error || '未知')), r.ok ? 'ok' : 'err');
  } catch (e) {
    toast('连接失败：' + e.message, 'err');
  }
}

/* ---- 后端更新：由后端自己从 Gitee / GitHub 拉新二进制，重启容器后生效 ---- */
const upd = ref({ current: '', latest: '', mirror: '', hasUpdate: false, inDocker: false });
const updBusy = ref(false);
const updMsg = ref('');

async function checkBackendUpdate(silent) {
  updBusy.value = true;
  if (!silent) updMsg.value = '正在检查后端版本…';
  try {
    const r = await API.get('/api/agent/update');
    if (r.ok && r.data && r.data.current) {
      upd.value = r.data;
      updMsg.value = `当前 ${r.data.current} · 最新 ${r.data.latest || '—'}${r.data.mirror ? '（' + r.data.mirror + '）' : ''}`;
      if (!silent) toast(r.data.hasUpdate ? '发现新版本：' + r.data.latest : '后端已是最新版本', r.data.hasUpdate ? 'warn' : 'ok');
    } else {
      updMsg.value = '检查失败：' + (r.error || '未知');
      if (!silent) toast(updMsg.value, 'err');
    }
  } catch (e) {
    updMsg.value = '检查失败：' + e.message;
    if (!silent) toast(updMsg.value, 'err');
  } finally {
    updBusy.value = false;
  }
}

async function applyBackendUpdate() {
  updBusy.value = true;
  updMsg.value = '正在下载新后端并校验 sha256…';
  try {
    const r = await API.post('/api/agent/update', { mirror: upd.value.mirror || '' });
    if (r.ok && r.data && r.data.ok) {
      updMsg.value = r.data.inDocker
        ? `已装好 ${r.data.to}，容器会在几秒内自动重启，稍后刷新即可`
        : `已装好 ${r.data.to}，重启后端进程后生效`;
      toast('后端更新完成', 'ok');
      setTimeout(() => checkBackendUpdate(true), 8000);
    } else {
      updMsg.value = '更新失败：' + ((r.data && r.data.error) || r.error || '未知');
      toast(updMsg.value, 'err');
    }
  } catch (e) {
    updMsg.value = '更新失败：' + e.message;
    toast(updMsg.value, 'err');
  } finally {
    updBusy.value = false;
  }
}

onMounted(() => checkBackendUpdate(true));
</script>

<template>
  <div class="bz-page set-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">设置</h1>
        <p class="bz-sub">本机配置只保存在这台电脑上，配对令牌不会进入网页。</p>
      </div>
    </header>

    <section class="bz-card set-sec">
      <div class="set-sec-head">
        <h2 class="set-sec-title">连接</h2>
        <p class="set-sec-desc">填写 NAS 后端地址与配对令牌；留空令牌表示沿用已保存的那一个。</p>
      </div>
      <div class="set-field">
        <label class="set-label">后端地址</label>
        <NInput v-model:value="server" placeholder="http://192.168.1.100:8787" />
      </div>
      <div class="set-field">
        <label class="set-label">配对令牌（留空 = 不改）</label>
        <NInput v-model:value="token" type="password" placeholder="留空则保留已保存的" show-eye-on-hover />
      </div>
      <div class="set-actions">
        <NButton type="primary" @click="saveConfig">保存配置</NButton>
        <NButton @click="testConn">连接测试</NButton>
      </div>
    </section>

    <section class="bz-card set-sec">
      <div class="set-sec-head">
        <h2 class="set-sec-title">本机设备</h2>
        <p class="set-sec-desc">这台电脑在 NAS 上的设备身份，用于把任务派回来执行。</p>
      </div>
      <div class="set-rows">
        <div class="set-row">
          <span class="set-row-label">设备 ID</span>
          <span class="set-row-value set-mono">{{ device.deviceId || '—' }}</span>
        </div>
        <div class="set-row">
          <span class="set-row-label">平台</span>
          <span class="set-row-value">{{ device.platform || '—' }}</span>
        </div>
        <div class="set-row">
          <span class="set-row-label">在线状态</span>
          <span class="set-row-value set-status" :class="device.online ? 'on' : 'off'">
            <i class="bz-dot" :class="{ on: device.online }"></i>{{ device.online ? '在线' : '离线' }}
          </span>
        </div>
      </div>
    </section>

    <section class="bz-card set-sec">
      <div class="set-sec-head">
        <h2 class="set-sec-title">后端更新</h2>
        <p class="set-sec-desc">从 Gitee（国内）/ GitHub（国外）拉最新后端二进制，校验 sha256 后切换。NAS 容器部署会自己重启，桌面端只需重启后台程序。</p>
      </div>
      <div class="set-rows">
        <div class="set-row">
          <span class="set-row-label">当前版本</span>
          <span class="set-row-value set-mono">{{ upd.current || '—' }}</span>
        </div>
        <div class="set-row">
          <span class="set-row-label">最新版本</span>
          <span class="set-row-value set-mono">{{ upd.latest || '—' }}</span>
        </div>
        <div class="set-row">
          <span class="set-row-label">下载源</span>
          <span class="set-row-value">{{ upd.mirror || '自动' }}{{ upd.inDocker ? ' · 容器部署' : '' }}</span>
        </div>
      </div>
      <p v-if="updMsg" class="set-sec-desc">{{ updMsg }}</p>
      <div class="set-actions">
        <NButton :loading="updBusy" @click="checkBackendUpdate(false)">检查更新</NButton>
        <NButton type="primary" :disabled="!upd.hasUpdate || updBusy" @click="applyBackendUpdate">更新后端</NButton>
      </div>
    </section>

    <section class="bz-card set-sec">
      <div class="set-sec-head">
        <h2 class="set-sec-title">外观</h2>
        <p class="set-sec-desc">主题作用于整个控制台与临时浮窗。「跟随系统」跟 Windows 的浅色 / 深色设置走。</p>
      </div>
      <div class="set-theme">
        <button
          v-for="opt in THEMES"
          :key="opt.v"
          class="set-theme-btn"
          :class="{ on: themePref === opt.v }"
          @click="pickTheme(opt.v)"
        >{{ opt.label }}</button>
      </div>
    </section>
  </div>
</template>

<style>
.set-page { display: flex; flex-direction: column; max-width: 720px; }
.set-sec { display: flex; flex-direction: column; gap: 14px; }
.set-sec-head { display: flex; flex-direction: column; gap: 3px; }
.set-sec-title { margin: 0; font-size: 13px; font-weight: 600; color: var(--fg); }
.set-sec-desc { margin: 0; font-size: 12px; color: var(--fg-3); line-height: 1.55; }
.set-field { display: flex; flex-direction: column; gap: 5px; }
.set-label { font-size: 11px; color: var(--fg-3); letter-spacing: .04em; }
.set-actions { display: flex; gap: 8px; padding-top: 2px; }
.set-rows { display: flex; flex-direction: column; }
.set-row {
  display: flex; align-items: center; gap: 12px;
  min-height: 44px; border-top: 1px solid var(--line);
}
.set-row:first-child { border-top: 0; }
.set-row-label { flex: none; width: 76px; font-size: 12px; color: var(--fg-3); }
.set-row-value { min-width: 0; font-size: 13px; color: var(--fg); word-break: break-all; }
.set-mono { font-family: var(--mono); font-size: 12px; color: var(--fg-2); }
.set-status { display: inline-flex; align-items: center; gap: 6px; }
.set-status.on { color: var(--ok); }
.set-status.off { color: var(--fg-3); }
.set-theme { display: flex; gap: 8px; flex-wrap: wrap; }
.set-theme-btn {
  height: 32px; min-width: 88px; padding: 0 14px; border-radius: 4px;
  border: 1px solid var(--line); background: var(--surface-2); color: var(--fg-2);
  font-family: inherit; font-size: 13px; cursor: pointer;
  transition: background 120ms cubic-bezier(.2,0,0,1), color 120ms cubic-bezier(.2,0,0,1), border-color 120ms cubic-bezier(.2,0,0,1);
}
.set-theme-btn:hover { border-color: var(--line-strong); color: var(--fg); }
.set-theme-btn.on { background: var(--accent-soft); border-color: var(--accent-soft); color: var(--accent); font-weight: 500; }
</style>
