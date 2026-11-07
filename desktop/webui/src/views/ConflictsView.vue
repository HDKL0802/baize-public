<!-- 冲突协商页：接 /api/agent/conflicts + /api/agent/groups/{id}/conflicts/{sid}。
     两版左右对照、只看不改；会话内聊天轮询；定稿保留某一版，或一方放弃。 -->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue';
import { NButton, NInput } from 'naive-ui';
import { API, fmtTime, downloadBase64 } from '../api.js';

const me = ref({ loggedIn: false });
const groups = ref([]);
const groupId = ref('');
const sessions = ref([]);
const showAll = ref(false);
const selected = ref(null);
const detail = ref({ versions: [], messages: [] });
const preview = ref(null); // { docId, name, mime, text, dataUrl }
const reply = ref('');
const loading = ref(false);
const busy = ref(false);
const toastMsg = ref('');
const toastTone = ref('');
let toastTimer = null;

function toast(text, tone) {
  toastMsg.value = text;
  toastTone.value = tone || '';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastMsg.value = ''; }, 2800);
}

const statusText = { open: '协商中', resolved: '已定稿', abandoned: '已放弃' };
function stText(s) { return statusText[s] || s || '未知'; }

async function boot() {
  const who = await API.whoami();
  me.value = (who.ok && who.data) || { loggedIn: false };
  if (!me.value.loggedIn) return;
  const r = await API.get('/api/agent/groups');
  if (r.ok) groups.value = (r.data && r.data.groups) || [];
  if (groups.value.length) {
    groupId.value = groups.value[0].id;
    await loadSessions();
  }
}

async function loadSessions() {
  if (!groupId.value) return;
  loading.value = true;
  const r = await API.get('/api/agent/groups/' + encodeURIComponent(groupId.value) + '/conflicts' + (showAll.value ? '?all=1' : ''));
  loading.value = false;
  if (!r.ok) { toast(r.error || '读取协商列表失败', 'err'); return; }
  sessions.value = (r.data && r.data.conflicts) || [];
}

async function openSession(s) {
  selected.value = s;
  preview.value = null;
  detail.value = { versions: [], messages: [] };
  await loadDetail();
}

const meId = computed(() => me.value.user?.id || '');

async function loadDetail() {
  if (!selected.value) return;
  const r = await API.get('/api/agent/groups/' + encodeURIComponent(groupId.value) + '/conflicts/' + encodeURIComponent(selected.value.id));
  if (!r.ok) { toast(r.error || '读取协商详情失败', 'err'); return; }
  if (r.ok) detail.value = r.data || { versions: [], messages: [] };
  if (r.data && r.data.conflict) selected.value = r.data.conflict;
}

async function previewVersion(v) {
  const r = await API.get('/api/agent/groups/' + encodeURIComponent(groupId.value) + '/docs/' + encodeURIComponent(v.docId));
  if (!r.ok) { toast(r.error || '读取版本内容失败', 'err'); return; }
  const info = (r.data && r.data.doc) || {};
  const isImage = String(info.mime || '').startsWith('image/');
  const isText = /text|json|javascript|xml|csv|markdown/.test(String(info.mime || '')) || /\.(txt|md|json|js|ts|css|html|vue|go|py|yaml|yml|csv|log)$/i.test(info.name || '');
  let text = '';
  if (isText) {
    try {
      text = new TextDecoder('utf-8').decode(Uint8Array.from(atob(r.data.dataBase64), c => c.charCodeAt(0)));
    } catch (e) { text = '（无法按文本解码，请下载查看）'; }
  }
  preview.value = {
    docId: v.docId,
    name: info.name || selected.value.name,
    mime: info.mime || '',
    ownerName: v.ownerName,
    text: text.slice(0, 4000),
    dataUrl: isImage ? 'data:' + info.mime + ';base64,' + r.data.dataBase64 : '',
    truncated: text.length > 4000,
  };
}

function downloadVersion(v) {
  const d = preview.value && preview.value.docId === v.docId ? preview.value : null;
  if (!d) { toast('先点「查看内容」再下载', 'warn'); return; }
  API.get('/api/agent/groups/' + encodeURIComponent(groupId.value) + '/docs/' + encodeURIComponent(v.docId)).then(r => {
    if (!r.ok) { toast(r.error || '下载失败', 'err'); return; }
    downloadBase64((r.data.doc && r.data.doc.name) || selected.value.name, r.data.dataBase64);
  });
}

async function send() {
  const text = reply.value.trim();
  if (!text || !selected.value) return;
  busy.value = true;
  const r = await API.post('/api/agent/groups/' + encodeURIComponent(groupId.value) + '/conflicts/' + encodeURIComponent(selected.value.id) + '/messages', { text });
  busy.value = false;
  if (!r.ok) { toast(r.error || '发送失败', 'err'); return; }
  reply.value = '';
  await loadDetail();
}

async function resolve(v) {
  if (!window.confirm(`定稿：保留「${v.ownerName || v.ownerId}」这一版？其余同名版本会被删除。`)) return;
  const r = await API.post('/api/agent/groups/' + encodeURIComponent(groupId.value) + '/conflicts/' + encodeURIComponent(selected.value.id) + '/resolve', { docId: v.docId });
  if (!r.ok) { toast(r.error || '定稿失败', 'err'); return; }
  toast('已定稿，其余版本已清理', 'ok');
  await loadSessions();
  await loadDetail();
}

async function giveup() {
  if (!window.confirm('放弃自己这一版？对方的版本会自动成为正本。')) return;
  const r = await API.post('/api/agent/groups/' + encodeURIComponent(groupId.value) + '/conflicts/' + encodeURIComponent(selected.value.id) + '/giveup', {});
  if (!r.ok) { toast(r.error || '操作失败', 'err'); return; }
  toast('已放弃自己这一版', 'ok');
  await loadSessions();
  await loadDetail();
}

function mine(v) { return meId.value && v.ownerId === meId.value; }

let timer = null;
onMounted(() => {
  boot();
  timer = setInterval(() => {
    if (selected.value && selected.value.status === 'open') loadDetail();
  }, 4000);
});
onBeforeUnmount(() => clearInterval(timer));
</script>

<template>
  <div class="bz-page cf-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">冲突协商</h1>
        <p class="bz-sub">同一份文档被多人各改一版时，在这里左右对照、把话说清，最后定稿保留一版。</p>
      </div>
      <div class="bz-head-actions">
        <NButton v-if="me.loggedIn" size="small" @click="loadSessions">刷新</NButton>
      </div>
    </header>

    <section v-if="!me.loggedIn" class="bz-card bz-empty">
      <div class="bz-empty-title">需要先登录</div>
      <div class="bz-empty-desc">冲突协商按用户组隔离，请先到「账号与共享」登录并加入用户组。</div>
    </section>

    <template v-else>
      <section class="bz-card cf-toolbar">
        <select v-model="groupId" class="cf-select" @change="selected = null; loadSessions()">
          <option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}（{{ g.memberCount || 0 }} 人）</option>
        </select>
        <label class="cf-check"><input v-model="showAll" type="checkbox" @change="loadSessions" /> 显示已结束</label>
        <span class="cf-count">{{ sessions.length }} 个协商</span>
      </section>

      <div class="cf-cols">
        <section class="bz-card cf-list">
          <div v-if="!sessions.length" class="bz-empty">
            <div class="bz-empty-title">没有待处理的冲突</div>
            <div class="bz-empty-desc">当组内出现同名多版文档时会自动生成协商，并出现在这里。</div>
          </div>
          <button v-for="s in sessions" :key="s.id"
                  :class="['cf-row', { on: selected && selected.id === s.id }]" @click="openSession(s)">
            <div class="cf-row-main">
              <b class="cf-row-name">{{ s.name }}</b>
              <span class="cf-badge" :class="s.status">{{ stText(s.status) }}</span>
            </div>
            <div class="cf-row-meta cf-mono">
              {{ s.versionCount }} 版 · {{ s.messageCount || 0 }} 条消息 · {{ fmtTime(s.updatedAt) }}
            </div>
          </button>
        </section>

        <section v-if="selected" class="bz-card cf-detail">
          <div class="cf-detail-head">
            <div>
              <h2 class="cf-detail-title">{{ selected.name }}</h2>
              <div class="cf-detail-meta cf-mono">会话 {{ selected.id }} · {{ stText(selected.status) }} · 创建于 {{ fmtTime(selected.createdAt) }}</div>
            </div>
            <div class="cf-detail-actions">
              <NButton v-if="selected.status === 'open'" size="small" @click="giveup">放弃自己那版</NButton>
            </div>
          </div>

          <div class="cf-versions">
            <div v-for="v in detail.versions" :key="v.docId" class="cf-version" :class="{ mine: mine(v), picked: selected.resolvedDoc === v.docId }">
              <div class="cf-version-head">
                <b>{{ v.ownerName || v.ownerId || '未知用户' }}</b>
                <span v-if="mine(v)" class="cf-badge mine">我</span>
                <span v-if="selected.resolvedDoc === v.docId" class="cf-badge resolved">已保留</span>
              </div>
              <div class="cf-version-meta cf-mono">{{ v.size }} 字节 · {{ fmtTime(v.at) }}</div>
              <div class="cf-version-actions">
                <NButton size="tiny" @click="previewVersion(v)">查看内容</NButton>
                <NButton size="tiny" @click="downloadVersion(v)">下载</NButton>
                <NButton v-if="selected.status === 'open'" size="tiny" type="primary" @click="resolve(v)">保留这版</NButton>
              </div>
            </div>
          </div>

          <div v-if="preview" class="cf-preview">
            <div class="cf-preview-head">
              <b>{{ preview.name }}</b>
              <span class="cf-preview-meta cf-mono">{{ preview.ownerName }} · {{ preview.mime || '未知类型' }}（只读）</span>
              <button class="cf-x" @click="preview = null">×</button>
            </div>
            <img v-if="preview.dataUrl" :src="preview.dataUrl" class="cf-preview-img" alt="文档预览" />
            <pre v-else-if="preview.text" class="cf-preview-text">{{ preview.text }}{{ preview.truncated ? '\n\n…（已截断，完整内容请下载）' : '' }}</pre>
            <div v-else class="cf-preview-none">该类型不支持在线预览，请下载后查看。</div>
          </div>

          <div class="cf-chat">
            <div class="cf-sub-title">协商记录</div>
            <div class="cf-msgs">
              <div v-if="!(detail.messages || []).length" class="cf-mute">还没有人发言；把你想保留这版的理由写下来。</div>
              <div v-for="m in detail.messages" :key="m.id" class="cf-msg">
                <b class="cf-msg-from">{{ m.fromName || m.fromId || '系统' }}</b>
                <span class="cf-msg-text">{{ m.text }}</span>
                <span class="cf-msg-at cf-mono">{{ fmtTime(m.at) }}</span>
              </div>
            </div>
            <div class="cf-reply">
              <NInput v-model:value="reply" size="small" placeholder="说明你的版本改了什么、希望怎么合并…"
                      @keyup.enter="send" />
              <NButton size="small" type="primary" :loading="busy" @click="send">发送</NButton>
            </div>
          </div>

          <div v-if="selected.aiStatus || selected.aiNote" class="cf-ai">
            <span class="cf-sub-title">AI 建议</span>
            <div class="cf-ai-body">{{ selected.aiNote || 'AI 正在通读上下文…' }}<span v-if="selected.aiStatus" class="cf-mono">（{{ selected.aiStatus }}）</span></div>
          </div>
        </section>

        <section v-else class="bz-card bz-empty cf-placeholder">
          <div class="bz-empty-title">选一个协商开始</div>
          <div class="bz-empty-desc">左边点一条记录，左右对照两版内容并在会话里沟通。</div>
        </section>
      </div>
    </template>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.cf-page { display: flex; flex-direction: column; }
.cf-toolbar { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; margin-bottom: 14px; }
.cf-select { height: 32px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); color: var(--fg); font-family: inherit; font-size: 12px; }
.cf-check { display: inline-flex; align-items: center; gap: 5px; font-size: 11px; color: var(--fg-3); }
.cf-count { margin-left: auto; font-family: var(--mono); font-size: 11px; color: var(--fg-3); }
.cf-cols { display: grid; grid-template-columns: 320px 1fr; gap: 14px; align-items: start; }
.cf-list { padding: 6px; }
.cf-row { display: block; width: 100%; text-align: left; padding: 10px; border: 1px solid transparent; border-radius: 2px; background: transparent; color: var(--fg-2); font-family: inherit; cursor: pointer; }
.cf-row:hover { background: var(--surface-2); }
.cf-row.on { border-color: var(--accent); background: var(--accent-soft); }
.cf-row-main { display: flex; align-items: center; gap: 8px; }
.cf-row-name { font-size: 13px; color: var(--fg); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cf-badge { padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px; font-family: var(--mono); font-size: 10px; color: var(--fg-3); flex: none; }
.cf-badge.open { color: var(--warn); border-color: color-mix(in srgb, var(--warn) 40%, transparent); }
.cf-badge.resolved { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 40%, transparent); }
.cf-badge.mine { color: var(--accent); border-color: color-mix(in srgb, var(--accent) 40%, transparent); }
.cf-badge.abandoned { color: var(--fg-3); }
.cf-row-meta { margin-top: 4px; font-size: 10px; color: var(--fg-3); }
.cf-detail { min-width: 0; }
.cf-detail-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.cf-detail-title { margin: 0; font-size: 15px; }
.cf-detail-meta { margin-top: 3px; font-size: 10px; color: var(--fg-3); }
.cf-detail-actions { display: flex; gap: 8px; }
.cf-versions { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin-top: 14px; }
.cf-version { border: 1px solid var(--line); padding: 11px; }
.cf-version.mine { border-color: color-mix(in srgb, var(--accent) 45%, var(--line)); }
.cf-version.picked { border-color: color-mix(in srgb, var(--ok) 45%, var(--line)); }
.cf-version-head { display: flex; align-items: center; gap: 7px; font-size: 12px; }
.cf-version-meta { margin-top: 4px; font-size: 10px; color: var(--fg-3); }
.cf-version-actions { display: flex; gap: 5px; flex-wrap: wrap; margin-top: 8px; }
.cf-preview { margin-top: 14px; border: 1px solid var(--line); }
.cf-preview-head { display: flex; align-items: baseline; gap: 9px; padding: 8px 10px; border-bottom: 1px solid var(--line); font-size: 12px; }
.cf-preview-meta { font-size: 10px; color: var(--fg-3); }
.cf-x { margin-left: auto; border: 0; background: transparent; color: var(--fg-3); font-size: 16px; cursor: pointer; }
.cf-preview-img { display: block; max-width: 100%; max-height: 360px; margin: 10px auto; }
.cf-preview-text { margin: 0; padding: 10px; max-height: 360px; overflow: auto; white-space: pre-wrap; word-break: break-word; font-family: var(--mono); font-size: 12px; color: var(--fg-2); }
.cf-preview-none { padding: 20px 10px; text-align: center; font-size: 12px; color: var(--fg-3); }
.cf-chat { margin-top: 18px; }
.cf-sub-title { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.cf-msgs { max-height: 260px; overflow: auto; margin: 8px 0; border-top: 1px solid var(--line); }
.cf-msg { display: flex; gap: 8px; align-items: baseline; padding: 7px 0; border-bottom: 1px solid var(--line); font-size: 12px; }
.cf-msg-from { flex: none; color: var(--fg-3); font-size: 11px; }
.cf-msg-text { flex: 1; min-width: 0; color: var(--fg-2); word-break: break-word; }
.cf-msg-at { flex: none; font-size: 10px; color: var(--fg-3); }
.cf-mute { padding: 10px 0; font-size: 12px; color: var(--fg-3); }
.cf-reply { display: flex; gap: 8px; }
.cf-ai { margin-top: 14px; border-top: 1px solid var(--line); padding-top: 10px; }
.cf-ai-body { margin-top: 6px; font-size: 12px; color: var(--fg-2); line-height: 1.6; }
.cf-placeholder { min-height: 220px; justify-content: center; }
.cf-mono { font-family: var(--mono); }
@media (max-width: 900px) {
  .cf-cols { grid-template-columns: 1fr; }
  .cf-versions { grid-template-columns: 1fr; }
}
</style>
