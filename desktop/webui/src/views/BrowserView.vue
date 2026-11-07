<!-- 内置浏览器：控制独立进程里的 WebView2 窗口（Evergreen Chromium 内核）。
     数据源是桌面端本机回环接口 /api/local/browser；不经过后端，也不碰设备连接。
     内核版本只读系统注册表，白泽不自带内核、不静默下载。 -->
<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue';
import { NButton, NInput, NSwitch } from 'naive-ui';
import { API } from '../api.js';

const state = ref(null);
const loading = ref(true);
const busy = ref(false);
const note = ref('');
const url = ref('https://www.bing.com');
const toastMsg = ref('');
const toastTone = ref('');
let toastTimer = null;
let pollTimer = null;

function toast(text, tone) {
  toastMsg.value = text;
  toastTone.value = tone || '';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastMsg.value = ''; }, 2800);
}

function apply(res) {
  if (!res || !res.state) return false;
  state.value = res.state;
  return true;
}

async function load(silent) {
  if (!silent) loading.value = true;
  const res = await API.localGet('/browser');
  loading.value = false;
  if (!apply(res)) {
    note.value = res && res.error ? res.error : '读不到本机浏览器状态（这个页面要在桌面端里打开）';
    return;
  }
  note.value = '';
}

async function act(action, extra) {
  busy.value = true;
  const res = await API.localPost('/browser', Object.assign({ action }, extra || {}));
  busy.value = false;
  apply(res);
  if (!res.ok) {
    toast(res.error || '操作失败', 'err');
    return null;
  }
  return res;
}

async function openUrl() {
  const v = url.value.trim();
  if (!v) { toast('先填一个网址', 'warn'); return; }
  const res = await act('open', { url: v });
  if (res) toast('内置浏览器已打开（首次冷启动可能要几秒）', 'ok');
}

async function handoff(on) {
  let sent = true;
  if (on) sent = window.confirm('把控制权交给人工？移交期间 AI 的浏览器动作会被拒绝，直到你收回。');
  if (!sent) return;
  const res = await act('handoff', { handoff: on });
  if (res) toast(on ? '已移交给人工' : '已收回控制权', 'ok');
}

async function closeBuiltin() {
  if (!window.confirm('关闭内置浏览器窗口？')) return;
  const res = await act('close');
  if (res) toast('已关闭', 'ok');
}

async function toggleControl(on) {
  let ack = true;
  if (on) {
    ack = window.confirm('开启后，白泽可以通过本机回环接口控制这个内置浏览器窗口（打开网页、点击、输入、截图）。只在本机生效，确认开启？');
    if (!ack) return;
  }
  const res = await act('setting', {
    on, allowChrome: on ? !!state.value.allowChrome : false, ack: on,
  });
  if (res) toast(on ? '已允许控制浏览器' : '已关闭浏览器控制', 'ok');
}

async function toggleChrome(on) {
  if (on && !state.value.control) { toast('先开启「允许控制浏览器」，再单独授权扩展接入', 'warn'); return; }
  const res = await act('setting', { on: true, allowChrome: on, ack: true });
  if (res) toast(on ? '已允许 Chrome 扩展接入' : '已关闭 Chrome 扩展接入', 'ok');
}

async function copyToken() {
  const t = state.value && state.value.extToken;
  if (!t) return;
  try {
    await navigator.clipboard.writeText(t);
    toast('扩展令牌已复制', 'ok');
  } catch (e) {
    toast('复制失败，请手动选中令牌复制', 'warn');
  }
}

async function rotateToken() {
  if (!window.confirm('重置扩展令牌？旧令牌立刻失效，扩展需要重新填写。')) return;
  const res = await act('rotate');
  if (res) toast('扩展令牌已重置', 'ok');
}

async function openExtDir() {
  const res = await act('opendir');
  if (res) toast('已在资源管理器打开扩展目录', 'ok');
}

const engine = () => (state.value && state.value.engine) || {};
const builtin = () => (state.value && state.value.builtin) || { open: false };

onMounted(() => {
  load();
  pollTimer = setInterval(() => load(true), 8000);
});
onBeforeUnmount(() => clearInterval(pollTimer));
</script>

<template>
  <div class="bz-page br-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">内置浏览器</h1>
        <p class="bz-sub">
          独立进程里的 WebView2 窗口（Evergreen Chromium 内核），AI 用 browser.* 动作操作它；
          控制口只监听 127.0.0.1，且每个请求都带随机令牌。
        </p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :loading="loading" @click="load()">刷新状态</NButton>
        <NButton size="small" :disabled="!builtin().open" @click="closeBuiltin">关闭浏览器</NButton>
      </div>
    </header>

    <div v-if="note" class="bz-card br-warn">
      {{ note }}
    </div>

    <!-- 内核 -->
    <section class="bz-card br-engine">
      <div class="br-engine-head">
        <div>
          <div class="br-engine-name">{{ engine().kind || 'WebView2' }}</div>
          <div class="br-engine-sub">
            选型：不打包内核，直接用系统上的 Microsoft Edge WebView2（Chromium），
            安全更新跟着 Microsoft Edge Update 走。
          </div>
        </div>
        <span :class="['br-chip', engine().found ? 'ok' : 'err']">
          {{ engine().found ? '运行时已安装' : '未检测到运行时' }}
        </span>
      </div>

      <div class="br-kv">
        <div class="br-kv-item">
          <span class="br-kv-l">内核版本</span>
          <span class="br-kv-v br-mono">{{ engine().version || '—' }}</span>
        </div>
        <div class="br-kv-item">
          <span class="br-kv-l">更新渠道</span>
          <span class="br-kv-v">
            {{ engine().channel === 'fixed' ? '固定版本（不自动更新）' : 'Evergreen（随 Edge Update 自动更新）' }}
          </span>
        </div>
        <div class="br-kv-item">
          <span class="br-kv-l">Edge 稳定版</span>
          <span class="br-kv-v br-mono">{{ engine().edgeVersion || '—' }}</span>
        </div>
        <div class="br-kv-item br-kv-wide">
          <span class="br-kv-l">安装位置</span>
          <span class="br-kv-v br-mono">{{ engine().location || '—' }}</span>
        </div>
      </div>

      <div v-if="engine().behind" class="br-behind">
        当前 WebView2 内核落后于本机 Edge 稳定版一大截（版本号第一段不同）。通常是 Edge Update
        被策略、代理或离线环境挡住了；界面显示的是实际版本，不谎称“最新”。
      </div>
      <div v-else-if="engine().found && engine().channel !== 'fixed'" class="br-policy-ok">
        与 Edge 稳定版同代（第一段版本号一致），说明系统更新通道是通的。
      </div>

      <div class="br-policy">
        <div class="br-policy-title">版本策略与离线退化（如实说明）</div>
        <ul>
          <li>
            <b>“最新”怎么保证：</b>选 Evergreen，内核由 Microsoft Edge Update 维护；白泽每次打开/刷新
            都从注册表读出实际版本并显示，不自带内核、不冻结版本。
          </li>
          <li>
            <b>不承诺的部分：</b>“永远最新”取决于系统更新是否在跑。被组策略、代理或断网挡住时，
            这里显示的版本就是实际版本，可能落后。
          </li>
          <li>
            <b>没装运行时的退化：</b>内置浏览器会启动失败并报错，不会偷偷下载安装。离线装机需随
            安装包预置 WebView2 Evergreen 独立安装器，或部署固定版本运行时目录
            （<span class="br-mono">WEBVIEW2_BROWSER_EXECUTABLE_FOLDER</span>）。
          </li>
          <li>
            <b>未验证：</b>“无网络环境下运行时能否自动更新”未做实测，不做承诺；固定版本目录
            一定不会自动更新，需要人工升级。
          </li>
        </ul>
      </div>
    </section>

    <!-- 控制 -->
    <section class="bz-card br-control">
      <div class="br-control-head">
        <div class="br-control-title">控制开关</div>
        <span class="br-mono br-sub">本机回环 · 不经过后端</span>
      </div>
      <div class="br-switch-row">
        <div>
          <div class="br-switch-name">允许控制浏览器</div>
          <div class="br-switch-desc">总开关：AI 才能用 browser.* 操作内置浏览器；关闭后动作一律拒绝。</div>
        </div>
        <NSwitch :value="!!(state && state.control)" :disabled="busy || !state" @update:value="toggleControl" />
      </div>
      <div class="br-switch-row">
        <div>
          <div class="br-switch-name">允许 Chrome 扩展接入</div>
          <div class="br-switch-desc">与内置浏览器分开授权：允许你用自己装的 Chrome/Edge 参与浏览器控制。</div>
        </div>
        <NSwitch :value="!!(state && state.allowChrome)" :disabled="busy || !state || !state.control" @update:value="toggleChrome" />
      </div>

      <div class="br-open-row">
        <NInput v-model:value="url" size="small" placeholder="https://…（只支持 http/https）" @keyup.enter="openUrl" />
        <NButton size="small" type="primary" :loading="busy" @click="openUrl">打开</NButton>
        <NButton size="small" :disabled="busy || !builtin().open" @click="handoff(true)">移交给人工</NButton>
        <NButton size="small" :disabled="busy || !builtin().open" @click="handoff(false)">收回控制</NButton>
      </div>

      <div class="br-status">
        <span :class="['br-chip', builtin().open ? 'ok' : '']">
          {{ builtin().open ? '窗口已打开' : '窗口未打开' }}
        </span>
        <span v-if="builtin().handoff" class="br-chip warn">控制权在人工手里</span>
        <span v-if="builtin().url" class="br-status-url br-mono">{{ builtin().url }}</span>
        <span v-if="builtin().title" class="br-status-title">{{ builtin().title }}</span>
        <span v-if="builtin().readyState" class="br-status-ready br-mono">{{ builtin().readyState }}</span>
      </div>
      <div v-if="builtin().note" class="br-status-note">{{ builtin().note }}</div>
      <div class="br-hint">
        打开后是一个独立的窗口；AI 的 read/click/type/key/screenshot 都在那个进程里执行。
        截图走窗口抓取（WebView2 是合成器渲染，已用 PrintWindow 的 PW_RENDERFULLCONTENT）。
      </div>
    </section>

    <!-- Chrome 扩展 -->
    <section class="bz-card br-ext">
      <div class="br-control-head">
        <div class="br-control-title">Chrome / Edge 扩展接入</div>
        <span :class="['br-chip', state && state.extConnected ? 'ok' : '']">
          {{ state && state.extConnected ? '扩展已连接' : '扩展未连接' }}
        </span>
      </div>
      <div class="br-ext-grid">
        <div class="br-kv-item">
          <span class="br-kv-l">接入端口</span>
          <span class="br-kv-v br-mono">{{ state ? state.extPort : '—' }}</span>
        </div>
        <div class="br-kv-item br-kv-wide">
          <span class="br-kv-l">扩展令牌</span>
          <div class="br-token-row">
            <NInput :value="state ? state.extToken : ''" size="small" readonly class="br-mono" />
            <NButton size="tiny" :disabled="!state || !state.extToken" @click="copyToken">复制</NButton>
            <NButton size="tiny" :disabled="busy" @click="rotateToken">重置</NButton>
          </div>
        </div>
      </div>
      <div class="br-ext-foot">
        <span class="br-hint">扩展目录：<span class="br-mono">{{ state ? state.extDir : '—' }}</span></span>
        <NButton size="small" :disabled="busy" @click="openExtDir">打开扩展目录</NButton>
      </div>
    </section>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.br-page { display: flex; flex-direction: column; gap: 12px; }
.br-page .bz-card { padding: 14px 16px; }
.br-warn { color: var(--warn); border-color: var(--warn); font-size: 12px; line-height: 1.7; }
.br-engine-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.br-engine-name { font-family: Georgia, "Songti SC", serif; font-size: 16px; color: var(--fg); }
.br-engine-sub { margin-top: 4px; font-size: 11px; color: var(--fg-3); line-height: 1.7; max-width: 760px; }
.br-kv { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 1px; background: var(--line); border: 1px solid var(--line); margin-top: 12px; }
.br-kv-item { background: var(--surface); padding: 8px 10px; display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.br-kv-wide { grid-column: span 3; }
.br-kv-l { font-size: 10px; color: var(--fg-3); letter-spacing: .06em; }
.br-kv-v { font-size: 12px; color: var(--fg-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.br-behind { margin-top: 10px; padding: 8px 10px; border: 1px solid var(--warn); color: var(--warn); font-size: 11px; line-height: 1.7; }
.br-policy-ok { margin-top: 10px; font-size: 11px; color: var(--ok); }
.br-policy { margin-top: 12px; border-top: 1px solid var(--line); padding-top: 10px; }
.br-policy-title { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; margin-bottom: 6px; }
.br-policy ul { margin: 0; padding-left: 18px; }
.br-policy li { font-size: 11px; color: var(--fg-2); line-height: 1.85; margin-bottom: 2px; }
.br-policy b { color: var(--fg); font-weight: 600; }
.br-mono { font-family: var(--mono); font-variant-numeric: tabular-nums; }
.br-sub { font-size: 10px; color: var(--fg-3); }
.br-chip {
  display: inline-flex; align-items: center; height: 20px; padding: 0 8px; flex: 0 0 auto;
  border: 1px solid var(--line); border-radius: 2px; color: var(--fg-3); font-size: 10px;
}
.br-chip.ok { border-color: var(--accent); color: var(--accent); }
.br-chip.warn { border-color: var(--warn); color: var(--warn); }
.br-chip.err { border-color: var(--err); color: var(--err); }

.br-control-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 8px; }
.br-control-title { font-size: 12px; color: var(--fg); letter-spacing: .04em; }
.br-switch-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 10px 0; border-top: 1px solid var(--line); }
.br-switch-name { font-size: 13px; color: var(--fg); }
.br-switch-desc { margin-top: 3px; font-size: 11px; color: var(--fg-3); line-height: 1.6; }
.br-open-row { display: flex; align-items: center; gap: 8px; margin-top: 10px; flex-wrap: wrap; }
.br-open-row .n-input { flex: 1; min-width: 240px; }
.br-status { display: flex; align-items: center; gap: 8px; margin-top: 10px; flex-wrap: wrap; min-height: 20px; }
.br-status-url, .br-status-ready { font-size: 10px; color: var(--fg-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 100%; }
.br-status-title { font-size: 12px; color: var(--fg-2); }
.br-status-note { margin-top: 6px; font-size: 11px; color: var(--warn); }
.br-hint { margin-top: 10px; font-size: 11px; color: var(--fg-3); line-height: 1.7; }

.br-ext-grid { display: grid; grid-template-columns: 120px minmax(0, 1fr); gap: 1px; background: var(--line); border: 1px solid var(--line); margin-top: 8px; }
.br-token-row { display: flex; align-items: center; gap: 8px; }
.br-token-row .n-input { flex: 1; }
.br-ext-foot { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-top: 10px; }
.br-ext-foot .br-hint { margin-top: 0; }

@media (max-width: 900px) {
  .br-kv { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .br-kv-wide { grid-column: span 2; }
  .br-ext-grid { grid-template-columns: 1fr; }
}
</style>
