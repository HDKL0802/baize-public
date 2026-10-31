<!-- 设备页：列出 NAS 上注册的所有设备（替换旧 renderDevices）。
     数据源 /api/devices；本机设备信息 /api/local/device。 -->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue';
import { NButton } from 'naive-ui';
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
const hasSelf = computed(() => Object.keys(self.value).length > 0);
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
function capsOf(d) {
  const caps = Array.isArray(d.caps) ? d.caps : [];
  return caps.slice(0, 6);
}
function lastSeenOf(d) { return d.lastSeen || d.lastSeenAt; }
</script>

<template>
  <div class="bz-page dev-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">设备</h1>
        <p class="bz-sub">NAS 上注册过的客户端，在线状态每 12 秒刷新一次。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" @click="load">刷新</NButton>
      </div>
    </header>

    <!-- 本机设备置顶 -->
    <section v-if="hasSelf" class="dev-row self">
      <div class="dev-main">
        <div class="dev-name-line">
          <i class="bz-dot" :class="{ on: self.online }"></i>
          <span class="dev-name">本机设备</span>
          <span class="dev-kind">本机</span>
        </div>
        <div class="dev-id">{{ self.deviceId || '—' }}</div>
      </div>
      <div class="dev-caps">
        <span class="dev-cap">{{ platformLabel(self.platform) }}</span>
      </div>
      <div class="dev-side">
        <span class="dev-status" :class="self.online ? 'on' : 'off'">{{ self.online ? '在线' : '离线' }}</span>
      </div>
    </section>

    <div class="dev-list">
      <div v-for="d in devices" :key="d.deviceId || d.id" class="dev-row">
        <div class="dev-main">
          <div class="dev-name-line">
            <i class="bz-dot" :class="{ on: deviceOnline(d) }"></i>
            <span class="dev-name">{{ d.name || d.deviceId || d.id }}</span>
            <span class="dev-kind">{{ platformLabel(d.os || d.platform) }}</span>
          </div>
          <div class="dev-id">{{ d.deviceId || d.id }}</div>
        </div>
        <div class="dev-caps">
          <span v-for="c in capsOf(d)" :key="c" class="dev-cap">{{ c }}</span>
        </div>
        <div class="dev-side">
          <span class="dev-status" :class="deviceOnline(d) ? 'on' : 'off'">{{ deviceOnline(d) ? '在线' : '离线' }}</span>
          <span v-if="lastSeenOf(d)" class="dev-seen">{{ fmtTime(lastSeenOf(d)) }}</span>
        </div>
      </div>
    </div>

    <section v-if="!loading && !devices.length && !hasSelf" class="bz-card">
      <div class="bz-empty">
        <svg class="bz-empty-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor"
             stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M3.5 5.5h17v10h-17z" />
          <path d="M8.5 19.5h7" />
          <path d="M12 15.5v4" />
        </svg>
        <div class="bz-empty-title">还没有注册的设备</div>
        <div class="bz-empty-desc">在手机或另一台电脑上登录白泽，设备会自动出现在这里。</div>
      </div>
    </section>
  </div>
</template>

<style>
.dev-page { display: flex; flex-direction: column; }
.dev-row {
  display: flex; align-items: center; gap: 16px;
  min-height: 64px; padding: 12px 16px; margin-bottom: 8px;
  background: var(--surface); border: 1px solid var(--line); border-radius: 6px;
  transition: background 120ms cubic-bezier(.2,0,0,1);
}
.dev-row:hover { background: var(--surface-2); }
.dev-row.self { border-color: var(--line-strong); }
.dev-main { min-width: 0; flex: 1; }
.dev-name-line { display: flex; align-items: center; gap: 8px; min-width: 0; }
.dev-name { font-size: 14px; font-weight: 600; color: var(--fg); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.dev-kind {
  flex: none; padding: 1px 6px; border-radius: 4px;
  background: var(--surface-3); color: var(--fg-3); font-size: 11px; letter-spacing: .04em;
}
.dev-id { margin-top: 4px; font-family: var(--mono); font-size: 11px; color: var(--fg-3); word-break: break-all; }
.dev-caps { display: flex; flex-wrap: wrap; gap: 6px; justify-content: flex-end; max-width: 46%; }
.dev-cap {
  padding: 2px 7px; border-radius: 4px;
  background: var(--surface-2); color: var(--fg-2); font-size: 11px; line-height: 1.5;
}
.dev-side { display: flex; flex-direction: column; align-items: flex-end; gap: 3px; flex: none; min-width: 78px; }
.dev-status { font-size: 12px; }
.dev-status.on { color: var(--ok); }
.dev-status.off { color: var(--fg-3); }
.dev-seen { font-family: var(--mono); font-size: 11px; color: var(--fg-3); font-variant-numeric: tabular-nums; }
.dev-list { margin-top: 6px; }
</style>
