<!-- 设备页：列出 NAS 上注册的所有设备（替换旧 renderDevices）。
     数据源 /api/devices；本机设备信息 /api/local/device。 -->
<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue';
import { NCard, NTag, NSpace, NGrid, NGi } from 'naive-ui';
import { API, localDeviceInfo, fmtTime } from '../api.js';

function toast(msg, kind) {
  const t = document.createElement('div');
  t.className = 'bz-toast ' + (kind || '');
  t.textContent = msg;
  document.body.appendChild(t);
  setTimeout(() => t.remove(), 3800);
}

const devices = ref([]);
const self = ref({});
const loading = ref(true);
let timer = null;

async function load() {
  const [r, me] = await Promise.all([API.get('/api/devices'), localDeviceInfo()]);
  self.value = me;
  if (r.ok) devices.value = (r.data && r.data.devices) || (Array.isArray(r.data) ? r.data : []);
  else {
    devices.value = [];
    if (r.error) toast('读取设备失败：' + r.error, 'err');
  }
  loading.value = false;
}

onMounted(() => {
  load();
  timer = setInterval(load, 12000);
});
onBeforeUnmount(() => timer && clearInterval(timer));

function deviceOnline(d) { return d.online === true || d.status === 'online'; }
function platformLabel(p) {
  return { desktop: '桌面', android: '手机', phone: '手机', server: '后端', device: '设备' }[p] || p || '—';
}
</script>

<template>
  <div class="dev">
    <n-grid :x-gap="12" :y-gap="12" :cols="{ 1: 1, 740: 2, 1200: 3 }">
      <!-- 本机设备卡（置顶，标「本机」） -->
      <n-gi v-if="Object.keys(self).length">
        <n-card size="small" title="本机设备">
          <n-space vertical size="4">
            <n-tag type="success" size="small">本机</n-tag>
            <div class="dev-line"><b>设备 ID</b><span class="dev-mono">{{ self.deviceId || '—' }}</span></div>
            <div class="dev-line"><b>平台</b><span>{{ platformLabel(self.platform) }}</span></div>
            <div class="dev-line"><b>在线</b><n-tag :type="self.online ? 'success' : 'default'" size="small">{{ self.online ? '在线' : '离线' }}</n-tag></div>
          </n-space>
        </n-card>
      </n-gi>

      <n-gi v-for="d in devices" :key="d.deviceId || d.id">
        <n-card size="small">
          <n-space vertical size="4">
            <n-space align="center" justify="space-between" style="width:100%">
              <b class="dev-name">{{ d.name || d.deviceId || d.id }}</b>
              <n-tag :type="deviceOnline(d) ? 'success' : 'default'" size="small">{{ deviceOnline(d) ? '在线' : '离线' }}</n-tag>
            </n-space>
            <div class="dev-line"><b>平台</b><span>{{ platformLabel(d.platform) }}</span></div>
            <div class="dev-line"><b>ID</b><span class="dev-mono">{{ d.deviceId || d.id }}</span></div>
            <div v-if="d.lastSeenAt" class="dev-line"><b>最近在线</b><span>{{ fmtTime(d.lastSeenAt) }}</span></div>
          </n-space>
        </n-card>
      </n-gi>

      <n-gi v-if="!loading && !devices.length && !Object.keys(self).length">
        <div class="dev-empty">还没有注册的设备</div>
      </n-gi>
    </n-grid>
  </div>
</template>

<style>
.dev-line { display: flex; gap: 8px; font-size: 13px; }
.dev-line b { color: var(--fg3); min-width: 56px; font-weight: 600; }
.dev-mono { font-family: var(--mono); font-size: 12px; word-break: break-all; }
.dev-name { font-size: 14px; }
.dev-empty { color: var(--fg3); font-size: 13px; text-align: center; padding: 20px; }
</style>
