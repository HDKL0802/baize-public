<!-- 账号与共享页：接后端多用户底座 + 用户组共享文档。
     后端接口：/api/agent/auth/*、/api/agent/users、/api/agent/groups*、/api/agent/groups/{id}/docs*。 -->
<script setup>
import { ref, computed, onMounted } from 'vue';
import { NButton, NInput } from 'naive-ui';
import { API, fmtTime, fileToBase64, downloadBase64 } from '../api.js';

const me = ref({ loggedIn: false });
const users = ref([]);
const groups = ref([]);
const selectedGroup = ref('');
const groupDetail = ref({ members: [] });
const groupDocs = ref([]);
const docVersions = ref([]);
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

/* ---------- 登录 ---------- */

const loginForm = ref({ name: '', password: '' });

async function login() {
  const f = loginForm.value;
  if (!f.name.trim() || !f.password) { toast('用户名和密码都要填', 'warn'); return; }
  busy.value = true;
  const r = await API.login(f.name.trim(), f.password);
  busy.value = false;
  if (!r.ok) { toast('登录失败：' + (r.error || '未知'), 'err'); return; }
  toast('已登录', 'ok');
  loginForm.value = { name: '', password: '' };
  await refresh();
}

async function logout() {
  await API.logout();
  me.value = { loggedIn: false };
  selectedGroup.value = '';
  groupDetail.value = { members: [] };
  groupDocs.value = [];
  docVersions.value = [];
  await refresh();
}

/* ---------- 用户 ---------- */

const newUser = ref({ name: '', password: '', admin: false });

async function loadUsers() {
  const r = await API.get('/api/agent/users');
  if (r.ok) users.value = (r.data && r.data.users) || [];
}

async function createUser() {
  const f = newUser.value;
  if (!f.name.trim() || !f.password) { toast('用户名和密码都要填', 'warn'); return; }
  busy.value = true;
  const r = await API.post('/api/agent/users', { name: f.name.trim(), password: f.password, admin: !!f.admin });
  busy.value = false;
  if (!r.ok) { toast(r.error || '创建失败', 'err'); return; }
  newUser.value = { name: '', password: '', admin: false };
  toast('已创建用户', 'ok');
  loadUsers();
}

async function removeUser(u) {
  if (!window.confirm(`删除用户「${u.name}」？其私人数据不会自动清除。`)) return;
  const r = await API.call('/api/agent/users/' + encodeURIComponent(u.id), 'DELETE');
  if (!r.ok) { toast(r.error || '删除失败', 'err'); return; }
  toast('已删除 ' + u.name, 'ok');
  loadUsers();
  if (selectedGroup.value) openGroup(selectedGroup.value);
}

/* ---------- 用户组 ---------- */

const newGroup = ref('');

async function loadGroups() {
  const r = await API.get('/api/agent/groups');
  if (r.ok) groups.value = (r.data && r.data.groups) || [];
}

async function createGroup() {
  const name = newGroup.value.trim();
  if (!name) { toast('组名不能为空', 'warn'); return; }
  busy.value = true;
  const r = await API.post('/api/agent/groups', { name });
  busy.value = false;
  if (!r.ok) { toast(r.error || '创建失败', 'err'); return; }
  newGroup.value = '';
  toast('已创建用户组', 'ok');
  await loadGroups();
  if (r.data && r.data.group) openGroup(r.data.group.id);
}

async function removeGroup(g) {
  if (!window.confirm(`删除用户组「${g.name}」？组内共享文档会一起删除。`)) return;
  const r = await API.call('/api/agent/groups/' + encodeURIComponent(g.id), 'DELETE');
  if (!r.ok) { toast(r.error || '删除失败', 'err'); return; }
  toast('已删除 ' + g.name, 'ok');
  if (selectedGroup.value === g.id) selectedGroup.value = '';
  await loadGroups();
}

async function openGroup(id) {
  selectedGroup.value = id;
  groupDetail.value = { members: [] };
  groupDocs.value = [];
  docVersions.value = [];
  const r = await API.get('/api/agent/groups/' + encodeURIComponent(id));
  if (!r.ok) { toast(r.error || '读取用户组失败', 'err'); return; }
  groupDetail.value = { group: r.data.group, members: r.data.members || [] };
  await loadDocs();
}

const selectedGroupObj = computed(() => groups.value.find(g => g.id === selectedGroup.value) || groupDetail.value.group || {});
const memberIds = computed(() => new Set((groupDetail.value.members || []).map(m => m.userId)));

async function addMember(u) {
  if (!selectedGroup.value) return;
  const r = await API.post('/api/agent/groups/' + encodeURIComponent(selectedGroup.value) + '/members', { action: 'add', userId: u.id });
  if (!r.ok) { toast(r.error || '添加失败', 'err'); return; }
  groupDetail.value.members = (r.data && r.data.members) || [];
  toast('已加入 ' + u.name, 'ok');
  loadGroups();
}

async function removeMember(m) {
  if (!selectedGroup.value) return;
  const r = await API.post('/api/agent/groups/' + encodeURIComponent(selectedGroup.value) + '/members', { action: 'remove', userId: m.userId });
  if (!r.ok) { toast(r.error || '移除失败', 'err'); return; }
  groupDetail.value.members = (r.data && r.data.members) || [];
  toast('已移出 ' + m.userName, 'ok');
  loadGroups();
}

/* ---------- 共享文档 ---------- */

async function loadDocs() {
  if (!selectedGroup.value) return;
  const r = await API.get('/api/agent/groups/' + encodeURIComponent(selectedGroup.value) + '/docs?limit=500');
  if (!r.ok) { toast(r.error || '读取共享文档失败', 'err'); return; }
  groupDocs.value = (r.data && r.data.docs) || [];
  docVersions.value = (r.data && r.data.versions) || [];
}

const fileInput = ref(null);

function pickFile() { fileInput.value?.click(); }

async function uploadFile(e) {
  const file = e.target.files && e.target.files[0];
  e.target.value = '';
  if (!file || !selectedGroup.value) return;
  busy.value = true;
  const b64 = await fileToBase64(file);
  const r = await API.post('/api/agent/groups/' + encodeURIComponent(selectedGroup.value) + '/docs', {
    name: file.name,
    kind: file.type && file.type.startsWith('image/') ? 'image' : 'file',
    mime: file.type || 'application/octet-stream',
    dataBase64: b64,
  });
  busy.value = false;
  if (!r.ok) { toast(r.error || '上传失败', 'err'); return; }
  toast(r.data && r.data.conflict ? '已上传；同名存在多版，已自动发起冲突协商' : '已上传', 'ok');
  await loadDocs();
}

async function downloadDoc(d) {
  const r = await API.get('/api/agent/groups/' + encodeURIComponent(selectedGroup.value) + '/docs/' + encodeURIComponent(d.id));
  if (!r.ok || !r.data || !r.data.dataBase64) { toast(r.error || '下载失败', 'err'); return; }
  downloadBase64(d.name, r.data.dataBase64);
}

async function removeDoc(d) {
  if (!window.confirm(`删除共享文档「${d.name}」？`)) return;
  const r = await API.call('/api/agent/groups/' + encodeURIComponent(selectedGroup.value) + '/docs/' + encodeURIComponent(d.id), 'DELETE');
  if (!r.ok) { toast(r.error || '删除失败', 'err'); return; }
  toast('已删除', 'ok');
  await loadDocs();
}

/* ---------- 汇总 ---------- */

async function refresh() {
  loading.value = true;
  const who = await API.whoami();
  me.value = (who.ok && who.data) || { loggedIn: false };
  if (me.value.loggedIn) {
    await Promise.all([loadUsers(), loadGroups()]);
    if (selectedGroup.value) await openGroup(selectedGroup.value);
  } else {
    users.value = [];
    groups.value = [];
  }
  loading.value = false;
}

onMounted(refresh);
</script>

<template>
  <div class="bz-page acc-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">账号与共享</h1>
        <p class="bz-sub">多用户登录 + 用户组共享空间。组内成员上传的文档彼此可见；同名多份会进入冲突协商。</p>
      </div>
      <div class="bz-head-actions">
        <NButton v-if="me.loggedIn" size="small" @click="logout">退出登录</NButton>
        <NButton size="small" :loading="loading" @click="refresh">刷新</NButton>
      </div>
    </header>

    <!-- 未登录 -->
    <section v-if="!me.loggedIn" class="bz-card acc-login">
      <div class="acc-login-main">
        <h2 class="acc-sec-title">登录后使用协作功能</h2>
        <p class="acc-login-desc">不登录时仍是原来的单人用法（默认主体）；登录后记忆库与知识库会按用户分区。</p>
        <div class="acc-login-fields">
          <NInput v-model:value="loginForm.name" size="small" placeholder="用户名" />
          <NInput v-model:value="loginForm.password" size="small" type="password" placeholder="密码" show-eye-on-hover
                   @keyup.enter="login" />
          <NButton size="small" type="primary" :loading="busy" @click="login">登录</NButton>
        </div>
        <p class="acc-login-note">还没有账号？先由管理员在下方「用户」里创建（首位管理员可从后端 CLI 创建）。</p>
      </div>
      <div class="acc-login-side">
        <div class="acc-me-card">
          <span class="acc-me-label">当前主体</span>
          <b class="acc-me-name">{{ me.loggedIn ? me.user?.name : '默认主体（未登录）' }}</b>
        </div>
      </div>
    </section>

    <template v-else>
      <section class="bz-card acc-me">
        <div>
          <div class="acc-me-label">当前身份</div>
          <div class="acc-me-name">{{ me.user?.name }}<span v-if="me.user?.admin" class="acc-badge">管理员</span></div>
        </div>
        <div class="acc-me-groups">
          <button v-for="g in (me.groups || [])" :key="g.id" class="acc-chip" @click="openGroup(g.id)">{{ g.name }}</button>
          <span v-if="!(me.groups || []).length" class="acc-mute">还没有加入任何用户组</span>
        </div>
      </section>

      <div class="acc-cols">
        <section class="bz-card acc-users">
          <div class="acc-sec-head">
            <h2 class="acc-sec-title">用户</h2>
            <span class="acc-count">{{ users.length }}</span>
          </div>
          <div class="acc-users-list">
            <div v-for="u in users" :key="u.id" class="acc-user">
              <div class="acc-user-main">
                <b>{{ u.name }}</b>
                <span v-if="u.admin" class="acc-badge">管理员</span>
                <span class="acc-user-meta acc-mono">{{ u.id }}</span>
              </div>
              <button class="acc-del" @click="removeUser(u)">删除</button>
            </div>
            <div v-if="!users.length" class="acc-mute">还没有用户</div>
          </div>
          <div class="acc-add">
            <NInput v-model:value="newUser.name" size="small" placeholder="新用户名" />
            <NInput v-model:value="newUser.password" size="small" type="password" placeholder="密码" show-eye-on-hover />
            <label class="acc-check"><input v-model="newUser.admin" type="checkbox" /> 管理员</label>
            <NButton size="small" :loading="busy" @click="createUser">创建</NButton>
          </div>
        </section>

        <section class="bz-card acc-groups">
          <div class="acc-sec-head">
            <h2 class="acc-sec-title">用户组</h2>
            <span class="acc-count">{{ groups.length }}</span>
          </div>
          <div class="acc-groups-list">
            <button v-for="g in groups" :key="g.id"
                    :class="['acc-group', { on: selectedGroup === g.id }]" @click="openGroup(g.id)">
              <span class="acc-group-name">{{ g.name }}</span>
              <span class="acc-group-meta">{{ g.memberCount || 0 }} 人</span>
              <span class="acc-group-x" title="删除" @click.stop="removeGroup(g)">×</span>
            </button>
            <div v-if="!groups.length" class="acc-mute">还没有用户组</div>
          </div>
          <div class="acc-add">
            <NInput v-model:value="newGroup" size="small" placeholder="新用户组名称" @keyup.enter="createGroup" />
            <NButton size="small" :loading="busy" @click="createGroup">创建</NButton>
          </div>
        </section>
      </div>

      <section v-if="selectedGroup" class="bz-card acc-detail">
        <div class="acc-detail-head">
          <h2 class="acc-sec-title">「{{ selectedGroupObj.name }}」· 共享空间</h2>
          <div class="acc-detail-actions">
            <input ref="fileInput" type="file" hidden @change="uploadFile" />
            <NButton size="small" type="primary" :loading="busy" @click="pickFile">上传文档</NButton>
            <NButton size="small" @click="loadDocs">刷新文档</NButton>
          </div>
        </div>

        <div class="acc-members">
          <div class="acc-sub-title">成员 {{ groupDetail.members.length }}</div>
          <div class="acc-member-list">
            <div v-for="m in groupDetail.members" :key="m.userId" class="acc-member">
              <b>{{ m.userName }}</b>
              <span class="acc-badge">{{ m.role === 'owner' ? '组长' : '成员' }}</span>
              <span class="acc-user-meta acc-mono">{{ m.userId }}</span>
              <button v-if="m.role !== 'owner'" class="acc-del" @click="removeMember(m)">移出</button>
            </div>
          </div>
          <div class="acc-add acc-add-wrap">
            <template v-for="u in users" :key="u.id">
              <button v-if="!memberIds.has(u.id)" class="acc-chip" @click="addMember(u)">+ {{ u.name }}</button>
            </template>
          </div>
        </div>

        <div class="acc-docs">
          <div class="acc-sub-title">共享文档 {{ groupDocs.length }} 份</div>
          <div v-if="!groupDocs.length" class="acc-mute">还没有共享文档；上传后组内成员都能看到并下载。</div>
          <div v-for="d in groupDocs" :key="d.id" class="acc-doc">
            <div class="acc-doc-main">
              <b class="acc-doc-name">{{ d.name }}</b>
              <span class="acc-doc-meta acc-mono">{{ d.by || '—' }} · {{ (d.size || 0).toLocaleString() }} 字节 · {{ fmtTime(d.at) }}</span>
            </div>
            <div class="acc-doc-actions">
              <NButton size="tiny" @click="downloadDoc(d)">下载</NButton>
              <button class="acc-del" @click="removeDoc(d)">删除</button>
            </div>
          </div>
          <div v-if="docVersions.some(v => v.forked)" class="acc-fork-note">
            <span v-for="v in docVersions.filter(x => x.forked)" :key="v.name" class="acc-fork">
              「{{ v.name }}」有 {{ v.versions.length }} 个版本 → 已自动开冲突协商
            </span>
          </div>
        </div>
      </section>
    </template>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.acc-page { display: flex; flex-direction: column; }
.acc-sec-title { margin: 0; font-size: 13px; font-weight: 600; }
.acc-sec-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.acc-count { font-family: var(--mono); font-size: 11px; color: var(--fg-3); }
.acc-sub-title { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; margin-bottom: 8px; }
.acc-mute { font-size: 12px; color: var(--fg-3); }

.acc-login { display: flex; gap: 24px; justify-content: space-between; flex-wrap: wrap; }
.acc-login-main { flex: 1; min-width: 260px; }
.acc-login-desc { margin: 4px 0 12px; font-size: 12px; color: var(--fg-3); line-height: 1.6; }
.acc-login-fields { display: flex; gap: 8px; flex-wrap: wrap; }
.acc-login-fields .n-input { max-width: 200px; }
.acc-login-note { margin: 12px 0 0; font-size: 11px; color: var(--fg-3); }
.acc-login-side { width: 240px; flex: none; }
.acc-me-card { border: 1px solid var(--line); padding: 14px; }

.acc-me { display: flex; align-items: center; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
.acc-me-label { font-size: 10px; color: var(--fg-3); letter-spacing: .08em; }
.acc-me-name { margin-top: 3px; font-size: 15px; color: var(--fg); display: flex; align-items: center; gap: 8px; }
.acc-me-groups { display: flex; gap: 6px; flex-wrap: wrap; }
.acc-badge { padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px; color: var(--fg-3); font-family: var(--mono); font-size: 10px; }
.acc-chip { padding: 3px 9px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-2); font-family: inherit; font-size: 11px; cursor: pointer; }
.acc-chip:hover { border-color: var(--accent); color: var(--accent); }

.acc-cols { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; margin: 14px 0; }
.acc-users, .acc-groups { display: flex; flex-direction: column; }
.acc-users-list, .acc-groups-list { flex: 1; }
.acc-user { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 8px 0; border-top: 1px solid var(--line); }
.acc-user:first-child { border-top: 0; }
.acc-user-main { min-width: 0; display: flex; align-items: center; gap: 7px; flex-wrap: wrap; font-size: 13px; }
.acc-user-meta { font-size: 10px; color: var(--fg-3); }
.acc-del { padding: 2px 8px; border: 1px solid transparent; border-radius: 2px; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 11px; cursor: pointer; }
.acc-del:hover { border-color: var(--err); color: var(--err); }
.acc-add { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; margin-top: 10px; padding-top: 10px; border-top: 1px solid var(--line); }
.acc-add .n-input { min-width: 110px; }
.acc-add-wrap { margin-top: 8px; }
.acc-check { display: inline-flex; align-items: center; gap: 5px; font-size: 11px; color: var(--fg-3); }
.acc-group { display: flex; align-items: center; gap: 8px; width: 100%; padding: 8px 10px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-2); font-family: inherit; font-size: 12px; cursor: pointer; margin-bottom: 6px; text-align: left; }
.acc-group:hover { border-color: var(--line-strong); }
.acc-group.on { border-color: var(--accent); background: var(--accent-soft); color: var(--fg); }
.acc-group-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.acc-group-meta { font-family: var(--mono); font-size: 10px; color: var(--fg-3); }
.acc-group-x { width: 18px; text-align: center; color: var(--fg-3); font-size: 13px; }
.acc-group-x:hover { color: var(--err); }

.acc-detail-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.acc-detail-actions { display: flex; gap: 8px; }
.acc-members { margin-top: 14px; }
.acc-member-list { display: flex; flex-direction: column; }
.acc-member { display: flex; align-items: center; gap: 8px; padding: 7px 0; border-top: 1px solid var(--line); font-size: 12px; }
.acc-member:first-child { border-top: 0; }
.acc-docs { margin-top: 18px; }
.acc-doc { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 9px 0; border-top: 1px solid var(--line); }
.acc-doc-main { min-width: 0; }
.acc-doc-name { font-size: 13px; display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.acc-doc-meta { display: block; margin-top: 2px; font-size: 10px; color: var(--fg-3); }
.acc-doc-actions { display: flex; gap: 6px; align-items: center; flex: none; }
.acc-fork-note { margin-top: 10px; display: flex; flex-direction: column; gap: 4px; }
.acc-fork { font-size: 11px; color: var(--warn); }
.acc-mono { font-family: var(--mono); }

@media (max-width: 900px) {
  .acc-cols { grid-template-columns: 1fr; }
  .acc-login-side { width: 100%; }
}
</style>
