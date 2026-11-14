<!-- 插件市场页：接 /api/agent/plugins（源管理 / 逛市场 / 安装 / 更新 / 启停 / 卸载 / 直装 URL）。
     安装会真的落技能目录并改 MCP 配置，后端不提供"试运行"，界面也不假装能预演。
     生态板块：DSH 生态市场是内置源（npm/cordis 形态，白泽装不了），只能先同步成白泽插件源再装；
     Trae 适配器只做挂载开关，不逆向格式、不造假数据，写没写进后端都如实标注。 -->
<script setup>
import { ref, computed, onMounted } from 'vue';
import { NButton, NInput, NSwitch } from 'naive-ui';
import { API } from '../api.js';

const sources = ref([]);
const available = ref([]);
const installed = ref([]);
const dir = ref('');
const loading = ref(true);
const busy = ref('');
const channel = ref('all');
const toastMsg = ref('');
const toastTone = ref('');
let toastTimer = null;

function toast(text, tone) {
  toastMsg.value = text;
  toastTone.value = tone || '';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastMsg.value = ''; }, 3000);
}

const channels = computed(() => {
  const set = new Set();
  available.value.forEach(a => set.add(a.channel || 'community'));
  return [...set];
});

const shelf = computed(() => {
  if (channel.value === 'all') return available.value;
  return available.value.filter(a => (a.channel || 'community') === channel.value);
});

async function load() {
  loading.value = true;
  const r = await API.get('/api/agent/plugins');
  loading.value = false;
  if (!r.ok) { toast(r.error || '读取插件市场失败', 'err'); return false; }
  const d = r.data || {};
  sources.value = d.sources || [];
  available.value = d.available || [];
  installed.value = d.installed || [];
  dir.value = d.dir || '';
  return true;
}

/* ---------- 源管理 ---------- */
const showSource = ref(false);
const srcForm = ref({ name: '', url: '' });

async function addSource() {
  const f = srcForm.value;
  if (!f.url.trim()) { toast('要填索引地址', 'warn'); return; }
  busy.value = 'addSource';
  const r = await API.post('/api/agent/plugins', { action: 'addSource', name: f.name.trim(), url: f.url.trim(), enabled: true });
  busy.value = '';
  if (!r.ok) { toast(r.error || '添加源失败', 'err'); return; }
  srcForm.value = { name: '', url: '' };
  showSource.value = false;
  await load();
  toast('已添加源', 'ok');
}

async function removeSource(s) {
  if (!window.confirm(`移除源「${s.name}」？已安装的插件不会自动卸载。`)) return;
  busy.value = 'removeSource:' + s.url;
  const r = await API.post('/api/agent/plugins', { action: 'removeSource', url: s.url });
  busy.value = '';
  if (!r.ok) { toast(r.error || '移除失败', 'err'); return; }
  await load();
  toast('已移除源', 'ok');
}

/* ---------- 安装 / 启停 / 卸载 ---------- */
async function pluginAct(action, id) {
  busy.value = action + ':' + id;
  const r = await API.post('/api/agent/plugins', { action, id });
  busy.value = '';
  if (!r.ok) { toast(r.error || '操作失败', 'err'); return false; }
  if (r.data && r.data.installed) installed.value = r.data.installed;
  await load();
  return true;
}

async function install(a) {
  // 内置 DSH 生态源的条目装的是 npm/cordis 运行时插件，白泽没有宿主，后端会明确报错。
  // 界面先拦住：不给一个"点了会失败"的按钮，更不给假的成功提示。
  if (isDshEco(a)) {
    toast('「' + a.name + '」是 DSH 生态插件（npm/cordis 形态），白泽不能直接安装；请先在「生态」里同步为白泽插件源', 'err');
    return;
  }
  if (!window.confirm(`安装「${a.name}」${a.version ? ' v' + a.version : ''}？\n会往技能目录写文件，并可能注册 MCP 服务。`)) return;
  if (await pluginAct('install', a.id)) toast('已安装 ' + a.name, 'ok');
}

async function update(a) {
  if (isDshEco(a)) {
    toast('「' + a.name + '」是 DSH 生态插件（npm/cordis 形态），白泽不能直接升级；请先在「生态」里同步为白泽插件源', 'err');
    return;
  }
  if (!window.confirm(`把「${a.name}」升级到 ${a.version}？旧版本会先被拆掉。`)) return;
  if (await pluginAct('update', a.id)) toast('已更新 ' + a.name, 'ok');
}

async function toggleInstalled(p) {
  const action = p.enabled ? 'disable' : 'enable';
  if (await pluginAct(action, p.id)) toast((p.enabled ? '已停用 ' : '已启用 ') + p.name, 'ok');
}

async function uninstall(p) {
  if (!window.confirm(`卸载「${p.name}」？它带来的技能与 MCP 注册会被一起清掉。`)) return;
  if (await pluginAct('uninstall', p.id)) toast('已卸载 ' + p.name, 'ok');
}

/* ---------- 直装 URL / 本地包 ---------- */
const direct = ref(null);
const directBusy = ref(false);
async function installDirect() {
  const d = direct.value;
  if (!d.url.trim()) { toast('要填插件包地址或本地路径', 'warn'); return; }
  directBusy.value = true;
  const r = await API.post('/api/agent/plugins', { action: 'install', url: d.url.trim() });
  directBusy.value = false;
  if (!r.ok) { toast(r.error || '安装失败', 'err'); return; }
  direct.value = null;
  await load();
  toast('已安装', 'ok');
}

/* ---------- 生态：DSH 生态市场（内置源） ---------- */
// 内置源由后端分发：Name = DSH 生态市场（内置），URL = builtin:dsh-market，
// 索引里每条 Meta 的 url 形如 builtin:dsh-market/<id>.zip、channel = community。
// 这些插件是 npm/cordis 形态，白泽不能直接装（后端会返回明确错误），
// 正确用法是先调 /api/agent/ecology/dsh/sync 转成白泽插件源再装。
const DSH_INDEX_URL = 'https://cdn.dsh.so/market/index.json';
const DSH_BUILTIN_NAME = 'DSH 生态市场（内置）';
const DSH_BUILTIN_URL = 'builtin:dsh-market';
const DSH_PKG_PREFIX = 'builtin:dsh-market/';

function isDshEco(a) {
  return typeof (a && a.url) === 'string' && a.url.trim().indexOf(DSH_PKG_PREFIX) === 0;
}

const dshSource = computed(() => sources.value.find(s => {
  const u = String(s.url || '').trim();
  return u === DSH_BUILTIN_URL || u.indexOf('builtin:dsh-market') === 0 ||
    (s.name && String(s.name).indexOf('DSH 生态市场') >= 0);
}) || null);
const dshEcoCount = computed(() => available.value.filter(isDshEco).length);
const dshHasData = computed(() => !!dshSource.value || dshEcoCount.value > 0);
const dshCount = computed(() => dshSource.value && typeof dshSource.value.count === 'number'
  ? dshSource.value.count : dshEcoCount.value);

// 同步表单：索引地址 / 源名 / 是否探测 / 上限。probe 默认关（不打 GitHub），
// 后端 sync 接口的 probe 是布尔：false = 只按索引元数据列目录，true = 逐条联网探测。
const showEcoSync = ref(false);
const ecoForm = ref({ index: DSH_INDEX_URL, name: 'dsh-eco', probe: false, maxEntries: '' });
const ecoBusy = ref(false);
const ecoSteps = ref([]);

function ecoJson(v) {
  let s;
  try { s = JSON.stringify(v); } catch (e) { s = String(v); }
  s = s || '（空）';
  return s.length > 600 ? s.slice(0, 600) + '…（已截断，原始共 ' + s.length + ' 字符）' : s;
}

function ecoStep(title, ok, lines, raw) {
  const arr = (lines || []).slice();
  if (raw !== undefined) arr.push('原始返回：' + ecoJson(raw));
  ecoSteps.value.push({ title: title, ok: ok, lines: arr });
}

// probe 字段：已与后端确认，sync handler 是 *bool（json:"probe"），布尔 false/true 正确
// （false → 纯目录、不联网；true → 逐条探测 GitHub）。默认按布尔发。
// 仍保留一层防御：万一将来 probe 被改成数值开关、后端在「解析请求体」阶段因类型不符拒绝，
// 才改用 0/1 重试一次；两次原始返回都写进步骤日志，绝不静默替换或伪装成功。
async function postSyncAdaptive(body, probeOn) {
  const notes = [];
  const b1 = Object.assign({}, body, { probe: !!probeOn });
  const r1 = await API.post('/api/agent/ecology/dsh/sync', b1);
  if (r1.ok) return { r: r1, body: b1, notes: notes };
  const err1 = String(r1.error || '');
  // 只认「请求体字段类型被拒」这类错误（Go 的 json: cannot unmarshal ... 是典型签名）；
  // 索引文件自身解析失败（如「解析 index.json 失败」绝不能误判成 probe 类型问题，
  // 否则会给出误导性的重试说明——宁可不重试，也不能说错原因。
  const looksLikeTypeReject = /cannot unmarshal|json: cannot|unmarshal.*into|解析请求体|请求体(?:解析|格式|类型)/i.test(err1);
  if (!looksLikeTypeReject) return { r: r1, body: b1, notes: notes };
  notes.push('布尔 probe 被拒：' + err1 + '；疑似后端把 probe 当数值开关，改用 0/1 重试一次（第一次未执行同步、无副作用）');
  const b2 = Object.assign({}, body, { probe: probeOn ? 1 : 0 });
  const r2 = await API.post('/api/agent/ecology/dsh/sync', b2);
  notes.push('0/1 probe 请求：' + (r2.ok ? '成功' : ('失败 · ' + String(r2.error || '未知'))));
  return { r: r2, body: b2, notes: notes };
}

async function runEcoSync() {
  const f = ecoForm.value;
  const index = String(f.index || '').trim();
  if (!index) { toast('要填索引地址', 'warn'); return; }
  const name = String(f.name || '').trim() || 'dsh-eco';
  const maxRaw = String(f.maxEntries == null ? '' : f.maxEntries).trim();
  let maxEntries = 0;
  if (maxRaw !== '') {
    const n = Number(maxRaw);
    if (!Number.isFinite(n) || !Number.isInteger(n) || n < 0) {
      toast('maxEntries 要填非负整数，或留空表示不限', 'warn'); return;
    }
    maxEntries = n;
  }

  ecoSteps.value = [];
  ecoBusy.value = true;
  try {
    // 第 1 步：把 DSH 格式 1 索引转译成白泽插件源（zip + index.json）
    const baseBody = { index: index, name: name, maxEntries: maxEntries };
    const syncResult = await postSyncAdaptive(baseBody, !!f.probe);
    const r1 = syncResult.r;
    const syncBody = syncResult.body;
    if (!r1.ok) {
      ecoStep('第 1 步 同步为白泽插件源（POST /api/agent/ecology/dsh/sync）', false,
        syncResult.notes.concat(['请求：' + ecoJson(syncBody), '错误：' + (r1.error || '未知错误')]), r1.data);
      toast('同步失败：' + (r1.error || '未知错误'), 'err');
      return;
    }
    const d1 = r1.data || {};
    const errs = Array.isArray(d1.errors) ? d1.errors : [];
    const lines1 = syncResult.notes.concat([
      '请求：' + ecoJson(syncBody),
      'ok：' + String(d1.ok),
      'indexPath：' + (d1.indexPath || '（没有返回）'),
      'outDir：' + (d1.outDir || '—'),
      'built：' + (d1.built == null ? '—' : d1.built) +
        ' · skipped：' + (d1.skipped == null ? '—' : d1.skipped) +
        ' · packages：' + (Array.isArray(d1.packages) ? d1.packages.length : '—'),
    ]);
    if (errs.length) {
      lines1.push('转换告警 ' + errs.length + ' 条（前 5 条，如实列出）：');
      errs.slice(0, 5).forEach(e => lines1.push('  · ' + String(e)));
      if (errs.length > 5) lines1.push('  …还有 ' + (errs.length - 5) + ' 条（见后端原始返回）');
    }
    if (d1.ok === false || !d1.indexPath) {
      lines1.push('判定：' + (d1.ok === false ? '后端返回 ok=false' : '返回里没有 indexPath，无法登记源'));
      ecoStep('第 1 步 同步为白泽插件源（POST /api/agent/ecology/dsh/sync）', false, lines1, r1.data);
      toast('同步未成功：' + (d1.ok === false ? '后端返回 ok=false' : '返回里没有 indexPath'), 'err');
      return;
    }
    ecoStep('第 1 步 同步为白泽插件源（POST /api/agent/ecology/dsh/sync）', true, lines1, r1.data);

    // 第 2 步：把 sync 返回的 indexPath（白泽格式 index.json）登记进插件源
    const regBody = { name: name, url: d1.indexPath };
    const r2 = await API.post('/api/agent/ecology/dsh/register', regBody);
    if (!r2.ok || (r2.data && r2.data.ok === false)) {
      ecoStep('第 2 步 登记插件源（POST /api/agent/ecology/dsh/register）', false,
        ['请求：' + ecoJson(regBody),
         '错误：' + (r2.error || (r2.data && r2.data.error) || '后端返回 ok=false'),
         '同步产物仍在：' + d1.indexPath + '（可用「添加源」手动填这个地址）'], r2.data);
      toast('登记失败：' + (r2.error || (r2.data && r2.data.error) || '后端返回 ok=false') + '（同步产物已生成，可手动添加源）', 'err');
      return;
    }
    const d2 = r2.data || {};
    const lines2 = [
      '请求：' + ecoJson(regBody),
      'ok：' + String(d2.ok),
      'name：' + (d2.name || '—') + ' · updated：' + String(d2.updated),
      'url：' + (d2.url || '—'),
      'sources：' + (Array.isArray(d2.sources) ? d2.sources.length + ' 条' : '—'),
    ];
    ecoStep('第 2 步 登记插件源（POST /api/agent/ecology/dsh/register）', true, lines2, r2.data);

    // 第 3 步：前两步都真的成功了才刷新列表；刷新失败也如实写出来
    const ok3 = await load();
    ecoStep('第 3 步 刷新插件市场列表', ok3, ok3
      ? ['插件源：' + sources.value.length + ' · 货架：' + available.value.length + ' · 已安装：' + installed.value.length]
      : ['刷新失败，见页面顶部错误提示；可点右上角「刷新」重试']);
    if (ok3) {
      toast('已同步并登记源「' + name + '」', 'ok');
      showEcoSync.value = false;
    } else {
      toast('已同步并登记源「' + name + '」，但刷新列表失败（可手动点「刷新」）', 'warn');
    }
  } catch (e) {
    const msg = (e && e.message) ? e.message : String(e);
    ecoStep('执行异常', false, ['错误：' + msg]);
    toast('同步异常：' + msg, 'err');
  } finally {
    ecoBusy.value = false;
  }
}

/* ---------- 生态适配器设置：Trae ---------- */
// 只做挂载开关：读写后端 config 的 traeAdapterEnabled / traeAdapterIndexURL。
// 后端 POST /api/agent/config 是宽松解码（未知字段会被静默丢掉但仍回 ok:true），
// 所以写完必须 GET 回读复核；没回显就明确标注「仅本机界面设置，未写入后端配置」。
// 这里不逆向 Trae 索引格式、不生成任何测试数据。
const TRAE_LS_KEY = 'bz_trae_adapter';
const traeEnabled = ref(false);
const traeUrl = ref('');
const traeBusy = ref(false);
const traeBackendOk = ref(false);
const traeDetail = ref('');

function traeHasKeys(d) {
  return d && Object.prototype.hasOwnProperty.call(d, 'traeAdapterEnabled') &&
    Object.prototype.hasOwnProperty.call(d, 'traeAdapterIndexURL');
}
function traeLocalSave() {
  try { localStorage.setItem(TRAE_LS_KEY, JSON.stringify({ enabled: traeEnabled.value, url: traeUrl.value })); }
  catch (e) { /* 本机存储不可用也由 traeDetail 如实说明，不假装成功 */ }
}
function traeLocalRead() {
  try {
    const raw = localStorage.getItem(TRAE_LS_KEY);
    if (!raw) return null;
    const o = JSON.parse(raw);
    return o && typeof o === 'object' ? o : null;
  } catch (e) { return null; }
}

async function loadTrae() {
  let r;
  try { r = await API.get('/api/agent/config'); }
  catch (e) { r = { ok: false, error: String(e) }; }
  if (r.ok && traeHasKeys(r.data)) {
    traeEnabled.value = !!r.data.traeAdapterEnabled;
    traeUrl.value = String(r.data.traeAdapterIndexURL || '');
    traeBackendOk.value = true;
    traeDetail.value = '已从后端配置读取（GET /api/agent/config 回显了这两个字段）';
    return;
  }
  traeBackendOk.value = false;
  const local = traeLocalRead();
  if (local) {
    traeEnabled.value = !!local.enabled;
    traeUrl.value = typeof local.url === 'string' ? local.url : '';
  }
  traeDetail.value = r.ok
    ? '后端 GET /api/agent/config 未回显 traeAdapterEnabled / traeAdapterIndexURL（后端可能尚未支持），以下仅为上次的本机界面设置'
    : ('读取后端配置失败：' + (r.error || '未知错误') + '；以下仅为上次的本机界面设置');
}

async function saveTrae() {
  const body = {
    traeAdapterEnabled: !!traeEnabled.value,
    traeAdapterIndexURL: String(traeUrl.value || '').trim(),
  };
  traeBusy.value = true;
  traeLocalSave(); // 先留本机镜像；后端没接受时界面状态也不会丢，但会如实标注
  try {
    const w = await API.post('/api/agent/config', body);
    if (!w.ok) {
      traeBackendOk.value = false;
      traeDetail.value = '写入后端失败：' + (w.error || '未知错误') + '；当前值仅保存在本机界面（localStorage）';
      toast('保存失败：' + (w.error || '未知错误') + '（仅本机界面设置，未写入后端配置）', 'err');
      return;
    }
    // POST 返回 ok 不等于写进去了：宽松解码会吞掉未知字段。GET 回读复核才作数。
    let g;
    try { g = await API.get('/api/agent/config'); }
    catch (e) { g = { ok: false, error: String(e) }; }
    if (!g.ok) {
      traeBackendOk.value = false;
      traeDetail.value = '已 POST，但回读后端配置失败：' + (g.error || '未知错误') + '；无法确认是否落盘，当前仅本机界面设置';
      toast('无法确认是否保存（回读失败），仅本机界面设置', 'err');
      return;
    }
    if (!traeHasKeys(g.data)) {
      traeBackendOk.value = false;
      traeDetail.value = '后端 POST 返回 ok，但 GET 回读里没有 traeAdapterEnabled / traeAdapterIndexURL（字段很可能被后端忽略）；当前仅本机界面设置，未写入后端配置';
      toast('后端未接受这两个字段，仅本机界面设置，未写入后端配置', 'err');
      return;
    }
    const gotOn = !!g.data.traeAdapterEnabled;
    const gotUrl = String(g.data.traeAdapterIndexURL || '');
    if (gotOn !== body.traeAdapterEnabled || gotUrl !== body.traeAdapterIndexURL) {
      traeBackendOk.value = false;
      traeDetail.value = '回读值与提交不一致：提交 enabled=' + body.traeAdapterEnabled +
        ' / url=' + (body.traeAdapterIndexURL || '（空）') +
        '，回读 enabled=' + gotOn + ' / url=' + (gotUrl || '（空）') +
        '；当前仅本机界面设置，未写入后端配置';
      toast('后端回读值不一致，未确认保存', 'err');
      return;
    }
    traeEnabled.value = gotOn;
    traeUrl.value = gotUrl;
    traeBackendOk.value = true;
    traeDetail.value = '已写入后端配置（POST /api/agent/config，且 GET 回读一致）';
    toast('已保存 Trae 适配器设置到后端配置', 'ok');
  } catch (e) {
    const msg = (e && e.message) ? e.message : String(e);
    traeBackendOk.value = false;
    traeDetail.value = '保存异常：' + msg + '；当前仅本机界面设置，未写入后端配置';
    toast('保存异常：' + msg, 'err');
  } finally {
    traeBusy.value = false;
  }
}

onMounted(() => { load(); loadTrae(); });
</script>

<template>
  <div class="bz-page pl-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">插件市场</h1>
        <p class="bz-sub">插件包是静态 zip：装进来会落技能目录、注册 MCP 服务。没有沙箱隔离，只装信得过的源。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :loading="loading" @click="load">刷新</NButton>
        <NButton size="small" @click="showSource = !showSource">添加源</NButton>
        <NButton size="small" type="primary" @click="direct = { url: '' }">从地址直装</NButton>
      </div>
    </header>

    <div class="pl-stats">
      <div class="pl-stat"><span class="pl-v">{{ sources.length }}</span><span class="pl-l">插件源</span></div>
      <div class="pl-stat"><span class="pl-v">{{ available.length }}</span><span class="pl-l">货架条目</span></div>
      <div class="pl-stat"><span class="pl-v">{{ installed.length }}</span><span class="pl-l">已安装</span></div>
      <div class="pl-stat"><span class="pl-v">{{ installed.filter(p => p.enabled).length }}</span><span class="pl-l">已启用</span></div>
    </div>

    <section v-if="showSource" class="bz-card pl-form">
      <div class="pl-form-head"><h2 class="pl-form-title">添加插件源</h2><button class="pl-x" @click="showSource = false">×</button></div>
      <div class="pl-grid">
        <label class="pl-field"><span>源名称（可空）</span><NInput v-model:value="srcForm.name" size="small" placeholder="团队源" /></label>
        <label class="pl-field"><span>index.json 地址</span><NInput v-model:value="srcForm.url" size="small" placeholder="https://.../index.json" /></label>
      </div>
      <div class="pl-form-foot">
        <span class="pl-note">支持 http(s) 与本地文件路径。</span>
        <NButton size="small" type="primary" :loading="busy === 'addSource'" @click="addSource">添加</NButton>
      </div>
    </section>

    <section v-if="direct" class="bz-card pl-form">
      <div class="pl-form-head"><h2 class="pl-form-title">从地址直装</h2><button class="pl-x" @click="direct = null">×</button></div>
      <label class="pl-field"><span>插件包（.zip 地址或本地路径）</span><NInput v-model:value="direct.url" size="small" placeholder="https://.../plugin.zip 或 D:\pkg\plugin.zip" /></label>
      <div class="pl-form-foot">
        <span class="pl-note">直装不校验索引里的 sha256；能用源装就尽量用源。</span>
        <NButton size="small" type="primary" :loading="directBusy" @click="installDirect">安装</NButton>
      </div>
    </section>

    <section class="bz-card pl-card">
      <div class="pl-card-head"><h2 class="pl-form-title">插件源</h2><span v-if="dir" class="pl-note pl-mono">安装目录 {{ dir }}</span></div>
      <div v-for="s in sources" :key="s.url" class="pl-src">
        <div class="pl-src-main">
          <div class="pl-src-head">
            <b class="pl-src-name">{{ s.name || s.url }}</b>
            <span v-if="s.builtin" class="pl-badge ok">内置官方源</span>
            <span class="pl-badge" :class="s.enabled ? '' : 'warn'">{{ s.enabled ? '启用' : '停用' }}</span>
            <span class="pl-badge">{{ s.count }} 条</span>
          </div>
          <div class="pl-src-meta pl-mono">{{ s.url }}</div>
          <div v-if="s.error" class="pl-err">{{ s.error }}</div>
        </div>
        <button v-if="!s.builtin" class="pl-del" @click="removeSource(s)">移除</button>
      </div>
      <div v-if="!sources.length" class="pl-note">没有配置任何源，可以添加一个 index.json 地址。</div>
    </section>

    <!-- 生态：DSH 生态市场（内置源）是 npm/cordis 形态，白泽装不了，先同步成白泽插件源。 -->
    <section class="bz-card pl-card">
      <div class="pl-card-head">
        <h2 class="pl-form-title">生态</h2>
        <span class="pl-note">别的 Agent 生态的插件市场，转译成白泽插件源后才能装</span>
      </div>

      <div class="pl-src">
        <div class="pl-src-main">
          <div class="pl-src-head">
            <b class="pl-src-name">{{ DSH_BUILTIN_NAME }}</b>
            <span class="pl-badge ok">内置源</span>
            <span class="pl-badge" :class="dshSource && dshSource.enabled === false ? 'warn' : ''">{{ dshSource && dshSource.enabled === false ? '停用' : '启用' }}</span>
            <span class="pl-badge">{{ dshHasData ? dshCount + ' 条' : '— 条' }}</span>
          </div>
          <div class="pl-src-meta pl-mono">{{ dshSource ? dshSource.url : DSH_BUILTIN_URL }}</div>
          <div class="pl-note">索引来源 {{ DSH_INDEX_URL }}（DSH 格式 1）。DSH 生态插件是 npm/cordis 形态，白泽不能直接安装：需先「同步为可安装源」转成白泽插件源。</div>
          <div v-if="dshSource && dshSource.error" class="pl-err">{{ dshSource.error }}</div>
          <div v-if="!dshHasData" class="pl-note">没从后端读到这个内置源的数据（后端可能尚未更新，或源拉取失败）；仍可用下面的按钮手动同步公网索引。</div>
        </div>
        <NButton size="small" type="primary" @click="showEcoSync = !showEcoSync">{{ showEcoSync ? '收起表单' : '同步为可安装源' }}</NButton>
      </div>

      <div v-if="showEcoSync" class="pl-eco-form">
        <div class="pl-grid">
          <label class="pl-field"><span>索引地址</span><NInput v-model:value="ecoForm.index" size="small" placeholder="https://cdn.dsh.so/market/index.json" /></label>
          <label class="pl-field"><span>源名（转成白泽源后登记用）</span><NInput v-model:value="ecoForm.name" size="small" placeholder="dsh-eco" /></label>
        </div>
        <div class="pl-grid">
          <label class="pl-field"><span>maxEntries（可填上限，留空 = 不限）</span><NInput v-model:value="ecoForm.maxEntries" size="small" placeholder="留空不限，例如 200" /></label>
          <div class="pl-field pl-eco-probe">
            <span>是否逐条探测（默认不探测，不打 GitHub）</span>
            <span class="pl-eco-switch"><NSwitch v-model:value="ecoForm.probe" size="small" /> {{ ecoForm.probe ? '探测（慢但准）' : '不探测（快）' }}</span>
          </div>
        </div>
        <div class="pl-form-foot">
          <span class="pl-note">提交后依次：同步转译 → 登记返回的 indexPath → 刷新列表；每一步都显示真实返回，失败就停下并报错。</span>
          <NButton size="small" type="primary" :loading="ecoBusy" :disabled="ecoBusy" @click="runEcoSync">开始同步</NButton>
        </div>
      </div>

      <div v-if="ecoSteps.length" class="pl-eco-steps">
        <div v-for="(st, i) in ecoSteps" :key="i" class="pl-eco-step" :class="st.ok ? 'ok' : 'err'">
          <div class="pl-eco-step-head"><b>{{ st.title }}</b><span class="pl-badge" :class="st.ok ? 'ok' : 'warn'">{{ st.ok ? '成功' : '失败/中断' }}</span></div>
          <div v-for="(l, j) in st.lines" :key="j" class="pl-eco-line pl-mono">{{ l }}</div>
        </div>
      </div>
    </section>

    <!-- 生态适配器设置：Trae 只做挂载开关，不逆向格式、不给假数据。 -->
    <section class="bz-card pl-card">
      <div class="pl-card-head"><h2 class="pl-form-title">生态适配器设置</h2></div>
      <div class="pl-trae-row">
        <div class="pl-trae-main">
          <div class="pl-trae-head">
            <span class="pl-trae-switch"><NSwitch v-model:value="traeEnabled" size="small" /> 启用 Trae 生态适配器（需自备 Trae 索引地址）</span>
          </div>
          <div class="pl-note">Trae 索引格式待逆向确认，本适配器只接受白泽格式 index.json；填错市场会明确报错。</div>
        </div>
      </div>
      <label class="pl-field"><span>Trae 索引地址（白泽格式 index.json）</span>
        <NInput v-model:value="traeUrl" size="small" :disabled="!traeEnabled" placeholder="关闭时不可编辑；填白泽格式 index.json 的地址或本机路径" />
      </label>
      <div class="pl-form-foot">
        <span class="pl-note" :class="traeBackendOk ? 'pl-ok' : 'pl-warn'">
          {{ traeBackendOk ? '已写入后端配置' : '仅本机界面设置，未写入后端配置' }}<template v-if="traeDetail"> · {{ traeDetail }}</template>
        </span>
        <NButton size="small" :loading="traeBusy" :disabled="traeBusy" @click="saveTrae">保存</NButton>
      </div>
      <div class="pl-note">改完点「保存」才会写入后端 config（traeAdapterEnabled / traeAdapterIndexURL）；仅当两者都设置了后端才挂载 Trae 适配器源。本页不逆向 Trae 格式、不生成任何测试数据。</div>
    </section>

    <section class="bz-card pl-card">
      <div class="pl-card-head">
        <h2 class="pl-form-title">货架</h2>
        <div class="pl-tabs">
          <button class="pl-tab" :class="{ on: channel === 'all' }" @click="channel = 'all'">全部</button>
          <button v-for="c in channels" :key="c" class="pl-tab" :class="{ on: channel === c }" @click="channel = c">{{ c === 'official' ? '官方' : '社区' }}</button>
        </div>
      </div>
      <div v-if="!loading && !shelf.length" class="pl-note">货架是空的。源拉取失败会在上面标出来，不会是"假装没有插件"。</div>
      <div class="pl-grid-cards">
        <div v-for="a in shelf" :key="a.source + ':' + a.id" class="pl-card-item">
          <div class="pl-card-item-head">
            <b class="pl-item-name">{{ a.name }}</b>
            <span class="pl-badge">{{ a.version }}</span>
            <span class="pl-badge" :class="a.channel === 'official' ? 'ok' : ''">{{ a.channel === 'official' ? '官方' : '社区' }}</span>
            <span v-if="a.stage === 'beta'" class="pl-badge warn">beta</span>
          </div>
          <div class="pl-item-desc">{{ a.description || '（没有描述）' }}</div>
          <div class="pl-item-meta">
            {{ a.id }}
            <template v-if="a.author"> · {{ a.author }}</template>
            <template v-if="a.category"> · {{ a.category }}</template>
            <template v-if="a.license"> · {{ a.license }}</template>
          </div>
          <div class="pl-item-foot">
            <template v-if="isDshEco(a)">
              <span class="pl-eco-tip">DSH 生态插件（npm/cordis 形态），需先同步为白泽插件源</span>
              <NButton size="tiny" disabled title="DSH 生态插件（npm/cordis 形态），白泽不能直接安装；请先在「生态」里同步为白泽插件源">安装</NButton>
            </template>
            <template v-else>
              <span v-if="a.hasUpdate" class="pl-badge warn">可升级 {{ a.instVersion }} → {{ a.version }}</span>
              <span v-else-if="a.installed" class="pl-badge ok">已装 {{ a.instVersion || a.version }}</span>
              <NButton v-if="a.hasUpdate" size="tiny" :loading="busy === 'update:' + a.id" @click="update(a)">升级</NButton>
              <NButton v-else-if="!a.installed" size="tiny" :loading="busy === 'install:' + a.id" @click="install(a)">安装</NButton>
              <NButton v-else size="tiny" disabled>已安装</NButton>
            </template>
          </div>
        </div>
      </div>
    </section>

    <section class="bz-card pl-card">
      <div class="pl-card-head"><h2 class="pl-form-title">已安装</h2></div>
      <div v-if="!installed.length" class="pl-note">还没装任何插件。</div>
      <div v-for="p in installed" :key="p.id" class="pl-inst">
        <div class="pl-inst-main">
          <div class="pl-inst-head">
            <b class="pl-item-name">{{ p.name }}</b>
            <span class="pl-badge">{{ p.version }}</span>
            <span class="pl-badge" :class="p.enabled ? 'ok' : ''">{{ p.enabled ? '已启用' : '已停用' }}</span>
          </div>
          <div class="pl-item-meta">
            来源 {{ p.source || '—' }}
            <template v-if="(p.skills || []).length"> · 技能 {{ p.skills.join(', ') }}</template>
            <template v-if="(p.mcp || []).length"> · MCP {{ p.mcp.join(', ') }}</template>
          </div>
          <div v-if="p.note" class="pl-note">{{ p.note }}</div>
        </div>
        <div class="pl-inst-actions">
          <NButton size="tiny" :loading="busy === 'enable:' + p.id || busy === 'disable:' + p.id" @click="toggleInstalled(p)">
            {{ p.enabled ? '停用' : '启用' }}
          </NButton>
          <button class="pl-del" @click="uninstall(p)">卸载</button>
        </div>
      </div>
    </section>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.pl-page { display: flex; flex-direction: column; gap: 14px; }
.pl-stats { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1px; background: var(--line); border: 1px solid var(--line); margin-bottom: 0; }
.pl-stat { background: var(--surface); padding: 10px 14px; display: flex; flex-direction: column; gap: 2px; }
.pl-v { font-family: var(--mono); font-size: 16px; color: var(--fg); }
.pl-l { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.pl-form { display: flex; flex-direction: column; gap: 10px; }
.pl-form-head, .pl-card-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.pl-form-title { margin: 0; font-size: 13px; font-weight: 600; }
.pl-x { border: 0; background: transparent; color: var(--fg-3); font-size: 18px; cursor: pointer; line-height: 1; }
.pl-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.pl-field { display: flex; flex-direction: column; gap: 5px; font-size: 11px; color: var(--fg-3); }
.pl-form-foot { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.pl-note { font-size: 11px; color: var(--fg-3); }
.pl-mono { font-family: var(--mono); }
.pl-card { display: flex; flex-direction: column; gap: 10px; }
.pl-src { display: flex; gap: 12px; justify-content: space-between; padding: 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); }
.pl-src-main { min-width: 0; flex: 1; }
.pl-src-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.pl-src-name { font-size: 12px; color: var(--fg); }
.pl-src-meta { margin-top: 4px; font-size: 11px; color: var(--fg-3); word-break: break-all; }
.pl-badge { padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px; font-family: var(--mono); font-size: 10px; color: var(--fg-3); }
.pl-badge.ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 40%, transparent); }
.pl-badge.warn { color: var(--warn); border-color: color-mix(in srgb, var(--warn) 40%, transparent); }
.pl-err { margin-top: 5px; font-size: 11px; color: var(--err); word-break: break-word; }
.pl-del { height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 12px; cursor: pointer; }
.pl-del:hover { border-color: var(--err); color: var(--err); }
.pl-tabs { display: flex; gap: 4px; }
.pl-tab { padding: 3px 10px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 11px; cursor: pointer; }
.pl-tab.on { border-color: var(--accent); color: var(--accent); }
.pl-grid-cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 10px; }
.pl-card-item { display: flex; flex-direction: column; gap: 6px; padding: 12px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); }
.pl-card-item-head { display: flex; align-items: baseline; gap: 6px; flex-wrap: wrap; }
.pl-item-name { font-size: 13px; color: var(--fg); }
.pl-item-desc { font-size: 12px; color: var(--fg-2); line-height: 1.55; }
.pl-item-meta { font-size: 11px; color: var(--fg-3); word-break: break-all; }
.pl-item-foot { margin-top: auto; display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.pl-inst { display: flex; gap: 12px; justify-content: space-between; padding: 10px; border: 1px solid var(--line); border-radius: 2px; }
.pl-inst-main { min-width: 0; flex: 1; }
.pl-inst-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.pl-inst-actions { display: flex; gap: 6px; align-items: center; }

/* 生态板块：同步表单 / 分步真实返回 / DSH 生态条目标注 */
.pl-eco-form { display: flex; flex-direction: column; gap: 10px; padding: 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); }
.pl-eco-probe { justify-content: center; }
.pl-eco-switch { display: inline-flex; align-items: center; gap: 8px; font-size: 11px; color: var(--fg-3); }
.pl-eco-steps { display: flex; flex-direction: column; gap: 8px; }
.pl-eco-step { padding: 8px 10px; border: 1px solid var(--line); border-left: 2px solid var(--warn); border-radius: 2px; background: var(--surface-2); }
.pl-eco-step.ok { border-left-color: var(--ok); }
.pl-eco-step-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 5px; font-size: 11px; color: var(--fg-2); }
.pl-eco-line { font-size: 11px; color: var(--fg-3); line-height: 1.6; word-break: break-all; white-space: pre-wrap; }
.pl-eco-tip { min-width: 0; flex: 1; font-size: 11px; color: var(--warn); line-height: 1.45; }

/* 生态适配器设置（Trae） */
.pl-trae-row { display: flex; gap: 12px; justify-content: space-between; }
.pl-trae-main { min-width: 0; flex: 1; display: flex; flex-direction: column; gap: 6px; }
.pl-trae-switch { display: inline-flex; align-items: center; gap: 8px; font-size: 12px; color: var(--fg); }
.pl-ok { color: var(--ok); }
.pl-warn { color: var(--warn); }

@media (max-width: 900px) {
  .pl-stats { grid-template-columns: 1fr 1fr; }
  .pl-grid { grid-template-columns: 1fr; }
  .pl-inst { flex-direction: column; }
  .pl-trae-row { flex-direction: column; }
}
</style>
