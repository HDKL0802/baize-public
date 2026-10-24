<!-- 设置页：连接 + 本机设备 + 外观（替换旧 renderSettings 的核心部分）。 -->
<script setup>
import { ref, onMounted } from 'vue';
import {
  NCard, NInput, NButton, NSpace, NTag, NRadioGroup, NRadio,
} from 'naive-ui';
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

onMounted(async () => {
  const c = await API.localConfig();
  server.value = c.server || '';
  info.value = c;
  device.value = await localDeviceInfo();
  themePref.value = localStorage.getItem('bz_theme') || 'system';
});

function applyTheme() {
  if (window.__bzTheme) window.__bzTheme.setPref(themePref.value);
  document.documentElement.setAttribute('data-theme', themePref.value === 'system' ? 'light' : themePref.value);
  toast('已应用主题：' + themePref.value, 'ok');
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
</script>

<template>
  <div class="set" style="max-width: 680px">
    <n-card title="连接" size="small">
      <n-space vertical size="10">
        <div>
          <label class="set-label">后端地址</label>
          <n-input v-model:value="server" placeholder="http://192.168.1.100:8787" />
        </div>
        <div>
          <label class="set-label">配对令牌（留空 = 不改）</label>
          <n-input v-model:value="token" type="password" placeholder="留空则保留已保存的" show-eye-on-hover />
        </div>
        <n-space>
          <n-button type="primary" @click="saveConfig">保存</n-button>
          <n-button @click="testConn">连接测试</n-button>
        </n-space>
      </n-space>
    </n-card>

    <n-card title="本机设备" size="small" style="margin-top:14px">
      <n-space vertical size="4">
        <div class="set-line"><b>设备 ID</b><span class="set-mono">{{ device.deviceId || '—' }}</span></div>
        <div class="set-line"><b>平台</b><span>{{ device.platform || '—' }}</span></div>
        <div class="set-line"><b>在线</b>
          <n-tag :type="device.online ? 'success' : 'default'" size="small">{{ device.online ? '在线' : '离线' }}</n-tag>
        </div>
      </n-space>
    </n-card>

    <n-card title="外观" size="small" style="margin-top:14px">
      <n-space vertical size="10">
        <n-radio-group v-model:value="themePref" @update:value="applyTheme">
          <n-space>
            <n-radio value="light">浅色</n-radio>
            <n-radio value="dark">深色</n-radio>
            <n-radio value="system">跟随系统</n-radio>
          </n-space>
        </n-radio-group>
        <div class="set-sub">主题作用于整个控制台与临时浮窗。「跟随系统」跟 Windows 的浅色/深色设置走。</div>
      </n-space>
    </n-card>
  </div>
</template>

<style>
.set-label { display: block; font-size: 12px; color: var(--fg3); margin-bottom: 4px; }
.set-line { display: flex; gap: 8px; font-size: 13px; }
.set-line b { color: var(--fg3); min-width: 56px; font-weight: 600; }
.set-mono { font-family: var(--mono); font-size: 12px; word-break: break-all; }
.set-sub { color: var(--fg3); font-size: 12px; }
</style>
