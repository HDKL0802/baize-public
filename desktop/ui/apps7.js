/* 白泽桌面端 · 频道（你和白泽在「哪里」对话的接入点）
   一个频道负责两件事：把外部消息变成一条"又来了一条 goal"交给 Agent；把结论按平台方式发回去。

   数据：GET /api/agent/channels → {channels:[{id,kind,enabled,outboundUrl,format,chatIds,pollSec,hasToken,botPrefix,appId,agentId,domain}], kinds[], formats[]}
   动作：POST /api/agent/channels {action:save|toggle|remove|test, id, kind, enabled, ...}
   令牌/密钥一律：留空 = 不改（后端只在字段非空时覆盖），界面上标清。

   各类型怎么接（重要，别配错了以为能收）：
   - webhook  出站 POST 到一个 URL；入站由外部 POST /api/channels/{id}/inbound。
   - onebot   QQ OneBot V11 反向 WS：NapCat / go-cqhttp 连进来。
   - feishu   飞书：出站 OpenAPI；入站二选一（事件回调填验证令牌 / 轮询会话免公网）。
   - dingtalk 钉钉 Stream 长连接（白泽主动连出去，免公网）——QwenPaw 的推荐频道。
   - yuanbao  腾讯元宝（protobuf over WebSocket + sign-token）。
   - wechat   个人微信（官方 iLink Bot：首次扫码登录，凭证落盘）。
   （另有 qq / xiaoyi 两个适配器，源码保留但「有源码打不开」——需要实名/开发者账号，故界面不给入口。） */
'use strict';

const CH_KIND_META = {
  webhook:  { hint: '出站 POST 到一个 URL（飞书/钉钉/Slack 群机器人都只要一个 URL）；入站由外部 POST /api/channels/{id}/inbound，用 token 校验。', fields: ['outboundUrl', 'format', 'token'] },
  onebot:   { hint: 'QQ OneBot V11 反向 WebSocket：让 NapCat / go-cqhttp 连到 <span class="mono">/api/channels/{id}/ws?access_token=令牌</span>。必须设入站令牌，否则谁都能连进来派活。', fields: ['token'] },
  feishu:   { hint: '飞书：出站走 OpenAPI（要 appId/appSecret）。入站二选一 —— 事件回调（填「验证令牌」，需公网），或填「轮询会话 chatIds」由白泽主动拉（免公网）。', fields: ['appId', 'appSecret', 'token', 'domain', 'chatIds', 'pollSec'] },
  dingtalk: { hint: '钉钉 Stream 长连接：钉钉开发者后台建应用 → 加「机器人」→ 消息接收选 <b>Stream 模式</b> → 取 Client ID / Client Secret。白泽主动连出去，免公网；回复走消息里的 sessionWebhook。', fields: ['appId', 'appSecret', 'domain'] },
  yuanbao:  { hint: '腾讯元宝：protobuf over WebSocket + sign-token。填 app_id（appId）/ app_secret（appSecret）；接入点缺省用官方，一般不用改。', fields: ['appId', 'appSecret', 'domain', 'outboundUrl'] },
  wechat:   { hint: '个人微信：官方 iLink Bot。首次要在后端日志里扫码登录（登录后凭证落盘，之后自动收发）。一般不用填令牌；换自建/测试地址才填 domain。', fields: ['token', 'domain'] },
};

const CH_FIELD_DEF = {
  appId:      { label: 'App ID（小艺=AK）', ph: '按平台填' },
  appSecret:  { label: 'App Secret（小艺=SK）', ph: '留空 = 不改', secret: true },
  agentId:    { label: 'Agent ID', ph: '小艺开放平台的 Agent ID' },
  token:      { label: '入站令牌 / 验证令牌', ph: '留空 = 不改', secret: true },
  outboundUrl:{ label: '出站地址 URL', ph: 'http(s)://… 或 ws(s)://…' },
  format:     { label: '出站格式', select: 'formats' },
  domain:     { label: '域名（可选，默认官方）', ph: '留空 = 用官方地址' },
  chatIds:    { label: '轮询会话 chatIds（逗号分隔）', ph: 'oc_xxx,oc_yyy（配了就走轮询入站）' },
  pollSec:    { label: '轮询间隔（秒）', ph: '5' },
};

async function renderChannels(root) {
  let data = { channels: [], kinds: [], formats: [] };
  let editing = '';

  root.innerHTML = `
    <div class="page">
      <h3>频道（你和白泽在「对话」之外还能在哪说话）</h3>
      <div class="sub">
        一个频道 = 一个接入点：把外部消息变成一条「又来了一条 goal」交给白泽，白泽跑完把结论按平台方式发回去。
        心跳的 <span class="mono">last</span> / <span class="mono">inbox</span> 分发也走这里。
        <b>密钥 / 令牌一律「留空 = 不改」</b>，界面不回显。
      </div>

      <div class="sect">
        <h3>已配置 <span class="count" id="chCount">0</span></h3>
        <div id="chList"></div>
      </div>

      <div class="sect">
        <h3>加 / 改一条</h3>
        <div class="fields" style="grid-template-columns:1fr 220px">
          <div><label>频道 id（字母 / 数字 / - / _）</label><input id="chId" placeholder="如 dingtalk-ops"></div>
          <div><label>类型</label><select id="chKind"></select></div>
        </div>
        <div id="chHint" class="sub" style="margin:0 0 var(--sp-3)"></div>

        <div id="chFields"></div>

        <div class="bar tight">
          <label class="sub" style="margin:0"><input type="checkbox" id="chEnabled" style="margin-right:6px" checked>启用</label>
        </div>
        <div class="fields" style="grid-template-columns:1fr">
          <div><label>回复前缀（可选）</label><input id="chPrefix" placeholder="如 [白泽] "></div>
        </div>
        <div class="bar">
          <button class="btn" id="chSave">保存</button>
          <button class="btn ghost" id="chClear">清空表单</button>
          <span class="sub" id="chMsg" style="margin:0"></span>
        </div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  const metaOf = k => CH_KIND_META[k] || { hint: '', fields: ['token'] };

  // 构造/刷新「按类型显示哪些字段」
  const buildFields = () => {
    const kind = $i('chKind').value;
    const meta = metaOf(kind);
    $i('chHint').innerHTML = meta.hint || '';
    $i('chFields').innerHTML = meta.fields.map(f => {
      const d = CH_FIELD_DEF[f];
      if (!d) return '';
      if (d.select === 'formats') {
        return `<div><label>${d.label}</label><select id="ch_${f}"></select></div>`;
      }
      const type = d.secret ? 'password' : 'text';
      return `<div><label>${d.label}</label><input id="ch_${f}" type="${type}" placeholder="${esc(d.ph || '')}"></div>`;
    }).join('');
    const sel = $i('ch_format');
    if (sel) sel.innerHTML = (data.formats && data.formats.length ? data.formats : ['generic', 'feishu', 'dingtalk', 'slack'])
      .map(x => `<option value="${esc(x)}">${esc(x)}</option>`).join('');
  };

  const kv = id => { const e = $i('ch_' + id); return e ? e.value.trim() : ''; };

  const renderList = () => {
    const list = data.channels || [];
    $i('chCount').textContent = list.length;
    $i('chList').innerHTML = list.length ? list.map(c => {
      const bits = [];
      if (c.outboundUrl) bits.push('出站 ' + esc(c.outboundUrl));
      if (c.appId) bits.push('appId ' + esc(c.appId));
      if (c.agentId) bits.push('agentId ' + esc(c.agentId));
      if (c.domain) bits.push('domain ' + esc(c.domain));
      if (c.format) bits.push('格式 ' + esc(c.format));
      if (c.chatIds && c.chatIds.length) bits.push('轮询 ' + c.chatIds.length + ' 个会话');
      return `<div class="row">
        <div class="who"><b>${esc(c.id)}</b>
          <span class="mono">${esc(c.kind)}</span>
          ${bits.length ? `<span>${bits.join(' · ')}</span>` : '<span>（还没填连接信息）</span>'}
        </div>
        <div class="tags">
          <span class="tag ${c.enabled ? 'on' : 'off'}">${c.enabled ? '启用' : '停用'}</span>
          ${c.hasToken ? '<span class="tag on">有令牌</span>' : '<span class="tag">无令牌</span>'}
          <button class="btn ghost sm" data-test="${esc(c.id)}">测试发送</button>
          <button class="btn ghost sm" data-toggle="${esc(c.id)}">${c.enabled ? '停用' : '启用'}</button>
          <button class="btn ghost sm" data-edit="${esc(c.id)}">编辑</button>
          <button class="btn ghost sm danger" data-rm="${esc(c.id)}">删除</button>
        </div>
      </div>`;
    }).join('') : '<div class="empty">还没有频道。白泽现在只能在控制台/应用里跟你说话，不在任何 IM 里。</div>';

    $i('chList').querySelectorAll('[data-test]').forEach(b => b.onclick = async () => {
      b.disabled = true; b.textContent = '发送中…';
      const r = await API.post('/api/agent/channels', { action: 'test', id: b.dataset.test, text: '白泽频道连通性测试。' });
      b.disabled = false; b.textContent = '测试发送';
      if (r.ok) Shell.toast('已发送到「' + b.dataset.test + '」', 'ok');
      else Shell.toast('发送失败：' + (r.error || '未知错误'), 'err');
    });
    $i('chList').querySelectorAll('[data-toggle]').forEach(b => b.onclick = async () => {
      const r = await API.post('/api/agent/channels', { action: 'toggle', id: b.dataset.toggle });
      if (!r.ok) Shell.toast('操作失败：' + r.error, 'err');
      await load();
    });
    $i('chList').querySelectorAll('[data-edit]').forEach(b => b.onclick = () => {
      const c = (data.channels || []).find(x => x.id === b.dataset.edit);
      if (!c) return;
      editing = c.id;
      $i('chId').value = c.id;
      $i('chKind').value = c.kind;
      buildFields(); // 先按类型建出字段，再回填
      if (c.outboundUrl) $i('ch_outboundUrl') && ($i('ch_outboundUrl').value = c.outboundUrl);
      if (c.format) $i('ch_format') && ($i('ch_format').value = c.format);
      if (c.appId) $i('ch_appId') && ($i('ch_appId').value = c.appId);
      if (c.agentId) $i('ch_agentId') && ($i('ch_agentId').value = c.agentId);
      if (c.domain) $i('ch_domain') && ($i('ch_domain').value = c.domain);
      if (c.chatIds && c.chatIds.length) $i('ch_chatIds') && ($i('ch_chatIds').value = c.chatIds.join(','));
      if (c.pollSec) $i('ch_pollSec') && ($i('ch_pollSec').value = c.pollSec);
      $i('chEnabled').checked = !!c.enabled;
      $i('chPrefix').value = c.botPrefix || '';
      $i('chMsg').innerHTML = `<span class="sub">改「${esc(c.id)}」：密钥/令牌留空 = 不改</span>`;
    });
    $i('chList').querySelectorAll('[data-rm]').forEach(b => b.onclick = async () => {
      if (!confirm('删除频道「' + b.dataset.rm + '」？')) return;
      const r = await API.post('/api/agent/channels', { action: 'remove', id: b.dataset.rm });
      if (!r.ok) Shell.toast('删除失败：' + r.error, 'err');
      await load();
    });
  };

  const load = async () => {
    const r = await API.get('/api/agent/channels');
    if (!r.ok) { showErr($i('chList'), r); return; }
    data = r.data || data;
    const sel = $i('chKind');
    if (sel.options.length === 0) {
      sel.innerHTML = (data.kinds || []).map(k => `<option value="${esc(k)}">${esc(k)}</option>`).join('');
      buildFields();
    }
    renderList();
  };

  const save = async () => {
    const id = $i('chId').value.trim();
    if (!id) { $i('chMsg').innerHTML = '<span class="err">频道 id 不能为空</span>'; return; }
    const kind = $i('chKind').value;
    const body = { action: 'save', id: id, kind: kind, enabled: $i('chEnabled').checked, botPrefix: $i('chPrefix').value };
    const meta = metaOf(kind);
    // 只带该类型相关的字段；密钥/令牌留空 = 不带（后端保留原值）
    const put = (f, v) => { body[f] = v; };
    if (meta.fields.includes('outboundUrl')) put('outboundUrl', kv('outboundUrl'));
    if (meta.fields.includes('format') && kv('format')) put('format', kv('format'));
    if (meta.fields.includes('domain') && kv('domain')) put('domain', kv('domain'));
    if (meta.fields.includes('appId') && kv('appId')) put('appId', kv('appId'));
    if (meta.fields.includes('agentId') && kv('agentId')) put('agentId', kv('agentId'));
    if (meta.fields.includes('appSecret') && kv('appSecret')) put('appSecret', kv('appSecret'));
    if (meta.fields.includes('token') && kv('token')) put('token', kv('token'));
    if (meta.fields.includes('chatIds') && kv('chatIds')) {
      put('chatIds', kv('chatIds').split(',').map(s => s.trim()).filter(Boolean));
    }
    if (meta.fields.includes('pollSec') && kv('pollSec')) put('pollSec', Number(kv('pollSec')));
    // webhook 的出站地址允许被清空（这是它的必填项，清空后端会拒）
    if (kind === 'webhook' && !kv('outboundUrl')) put('outboundUrl', '');

    $i('chMsg').textContent = '保存中…';
    const r = await API.post('/api/agent/channels', body);
    if (r.ok) {
      $i('chMsg').innerHTML = '<span class="ok">已保存（立即生效）</span>';
      editing = '';
      clearForm();
      await load();
    } else {
      $i('chMsg').innerHTML = `<span class="err">${esc(r.error || '保存失败')}</span>`;
    }
  };

  const clearForm = () => {
    editing = '';
    $i('chId').value = ''; $i('chPrefix').value = ''; $i('chEnabled').checked = true;
    buildFields();
    $i('chMsg').textContent = '';
  };

  $i('chKind').onchange = buildFields;
  $i('chSave').onclick = save;
  $i('chClear').onclick = clearForm;

  await load();
  pollWhileMounted(root, load, 10000);
}

/* ================= 回填注册表 ================= */
(function wireChannels() {
  const app = window.APP_BY_ID && window.APP_BY_ID.channels;
  if (app) app.render = renderChannels;
})();
