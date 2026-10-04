/* 白泽待办中心 - AI 助手对话页
   · 底部输入栏：左=切换/多选智能体，右（从边上数）=①发送 ②录音，无「＋」
   · 群发：勾多个智能体就同时发给它们，各自回复分别成气泡
   · 能干活：话里说要记东西 → 立刻给出「入库」确认卡片（点一下才写，不背着你写）

   ★ 白泽智能体接入位 ★
   所有对外沟通都经过下面的 adapters：今天只有 'llm'（用户自己配的 OpenAI 兼容 / Anthropic 模型）。
   以后接入白泽智能体，只需要在 adapters 里加一个 kind（例如 'baize'，把消息发到本机 Agent 的
   接口），Agents.list() 自动把它列进输入栏左边的选择器，UI 一行都不用改。 */
'use strict';
window.Chat = (function () {
  const UI_ = window.UI;
  const $ = UI_.$;
  const MAX_MSGS = 200;      // 对话最多存这么多条，超出丢最早的
  const CTX_TURNS = 10;      // 每次请求带上最近 N 条作为上下文

  let msgs = [];
  let selected = [];         // 选中的智能体 id（1 个=单聊，多个=群发）
  let pending = [];          // 待发送附件（拍照 / 相册 / 文件）
  const MAX_ATTACH = 3;      // 一次最多带几个
  const SEND_SIDE = 1280;    // 发给模型前的图片最长边
  const THUMB_SIDE = 340;    // 存历史用的缩略图最长边
  const KEEP_THUMBS = 6;     // 历史里最多保留几张图的缩略图（localStorage 有限额）

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g,
      c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  }
  function fmtTime(ts) {
    const d = new Date(ts);
    const p = (n) => String(n).padStart(2, '0');
    return p(d.getHours()) + ':' + p(d.getMinutes());
  }
  function fmtSize(n) {
    if (!n) return '未知大小';
    return n > 1024 * 1024 ? (n / 1024 / 1024).toFixed(1) + 'MB' : Math.max(1, Math.round(n / 1024)) + 'KB';
  }
  /** 安卓给的 URI 尾段常是 "primary:笔记/笔记/密码.md" 这种，取最后一段当文件名 */
  function prettyName(n) {
    const s = String(n == null ? '' : n);
    return s.split(/[\\/]/).pop().replace(/^primary:/i, '').trim();
  }

  /* ================= 附件：拍照 / 相册 / 文件 ================= */

  /** 压到指定最长边并转 JPEG（体积与清晰度折中；视觉模型 1280 足够） */
  function shrink(dataUrl, maxSide, q) {
    return new Promise((resolve) => {
      const img = new Image();
      img.onload = () => {
        const scale = Math.min(1, maxSide / Math.max(img.width, img.height));
        const w = Math.max(1, Math.round(img.width * scale));
        const h = Math.max(1, Math.round(img.height * scale));
        const cv = document.createElement('canvas');
        cv.width = w; cv.height = h;
        cv.getContext('2d').drawImage(img, 0, 0, w, h);
        try { resolve(cv.toDataURL('image/jpeg', q)); } catch (e) { resolve(dataUrl); }
      };
      img.onerror = () => resolve(dataUrl);
      img.src = dataUrl;
    });
  }

  function readAsDataUrl(file) {
    return new Promise((resolve, reject) => {
      const fr = new FileReader();
      fr.onload = () => resolve(String(fr.result));
      fr.onerror = () => reject(new Error('读取失败'));
      fr.readAsDataURL(file);
    });
  }

  async function addAttachment(a) {
    if (pending.length >= MAX_ATTACH) { UI_.toast('一次最多带 ' + MAX_ATTACH + ' 个附件'); return; }
    if (a.kind === 'image') {
      // 先压出「发出去用」的图和「存历史用」的缩略图
      const small = await shrink(a.dataUrl, SEND_SIDE, 0.75);
      a.thumb = await shrink(a.dataUrl, THUMB_SIDE, 0.6);
      a.dataUrl = small;
      a.mime = 'image/jpeg';
    }
    pending.push(a);
    renderAttachBar();
    UI_.toast('已添加：' + a.name);
  }

  function renderAttachBar() {
    const bar = $('chatAttachBar');
    bar.innerHTML = '';
    bar.hidden = pending.length === 0;
    pending.forEach((a, i) => {
      const el = document.createElement('div');
      el.className = 'attach-chip';
      el.innerHTML = (a.kind === 'image' && (a.thumb || a.dataUrl))
        ? `<img src="${a.thumb || a.dataUrl}" alt="">`
        : `<div class="ac-file">📎<br>${esc(a.name)}</div>`;
      const del = document.createElement('div');
      del.className = 'ac-del';
      del.textContent = '×';
      del.title = '移除';
      del.addEventListener('click', () => { pending.splice(i, 1); renderAttachBar(); });
      el.appendChild(del);
      bar.appendChild(el);
    });
  }

  function openPlusMenu() {
    UI_.actionSheet([
      { label: '📷 拍照', onTap: () => pickAttachment('camera') },
      { label: '🖼 从相册选图', onTap: () => pickAttachment('album') },
      { label: '📎 选文件（md / txt / pdf…）', onTap: () => pickAttachment('file') },
      { label: '取消', cancel: true },
    ]);
  }

  /** 取回一个附件：APK 走原生桥，浏览器走 <input type=file> */
  function pickAttachment(source) {
    if (pending.length >= MAX_ATTACH) { UI_.toast('一次最多带 ' + MAX_ATTACH + ' 个附件'); return; }
    const nb = window.BzNative;
    if (nb && typeof nb.pickAttachment === 'function') {
      try { nb.pickAttachment(source); } catch (e) { UI_.toast('打不开：' + e.message); }
      return;
    }

    // 浏览器回退
    const input = document.createElement('input');
    input.type = 'file';
    if (source === 'camera') { input.accept = 'image/*'; input.capture = 'environment'; }
    else if (source === 'album') input.accept = 'image/*';
    else input.accept = '*/*';
    input.addEventListener('change', async () => {
      const f = input.files && input.files[0];
      if (!f) return;
      try {
        if (source === 'file') {
          const textLike = /^text\/|json|markdown|xml|csv|javascript|x-yaml/.test(f.type)
            || /\.(md|markdown|txt|json|js|ts|csv|log|html|htm|yml|yaml)$/i.test(f.name);
          const text = textLike && f.size <= 200 * 1024 ? await f.text() : '';
          await addAttachment({ kind: 'file', name: f.name, mime: f.type || '', size: f.size, text });
        } else {
          await addAttachment({ kind: 'image', name: f.name || 'photo.jpg', mime: f.type || 'image/jpeg', size: f.size, dataUrl: await readAsDataUrl(f) });
        }
      } catch (e) { UI_.toast('附件读取失败：' + e.message); }
    });
    input.click();
  }

  /** 把附件拼成模型能读的内容（OpenAI 写法，Anthropic 那侧由 ai.js 转换） */
  function contentFor(text, atts) {
    if (!atts || !atts.length) return text;
    const parts = [];
    if (text) parts.push({ type: 'text', text });
    else parts.push({ type: 'text', text: '看看这个附件' });
    atts.forEach(a => {
      const url = a.dataUrl || a.thumb;
      if (a.kind === 'image' && url) {
        parts.push({ type: 'image_url', image_url: { url } });
      } else if (a.text) {
        parts.push({ type: 'text', text: '【文件 ' + a.name + '】\n' + a.text });
      } else {
        parts.push({ type: 'text', text: '【文件 ' + a.name + '（' + fmtSize(a.size) + '，非文本内容，读不到正文）】' });
      }
    });
    return parts;
  }

  /** 原生把附件存在那边，前端自己去取（大图不会走字符串转义，也就不会静默失败）。
      只认领 dest=chat 的：待办附件由待办页自己取，两边不抢。 */
  function takeNativeAttachment() {
    const nb = window.BzNative;
    if (!nb) return;
    let json = '';
    try {
      json = (typeof nb.takeAttachmentFor === 'function')
        ? (nb.takeAttachmentFor('chat') || '')
        : (typeof nb.takeAttachment === 'function' ? (nb.takeAttachment() || '') : '');
    } catch (e) { return; }
    if (!json) return;
    let p = null;
    try { p = JSON.parse(json); } catch (e) { p = null; }
    if (!p) { UI_.toast('附件读取失败'); return; }
    if (p.kind === 'image') {
      addAttachment({
        kind: 'image', name: prettyName(p.name) || 'photo.jpg',
        mime: p.mime || 'image/jpeg', size: p.size || 0,
        dataUrl: 'data:' + (p.mime || 'image/jpeg') + ';base64,' + p.base64,
      });
    } else {
      addAttachment({ kind: 'file', name: prettyName(p.name) || 'file', mime: p.mime || '', size: p.size || 0, text: p.text || '' });
    }
  }
  /** 消息里的 @路径（电脑上那种玩法）：/sdcard/Download/笔记.md、/storage/emulated/0/x.txt */
  function pathsIn(text) {
    const out = [];
    const re = /@\s*(\/[^\s，。；、"'）)]+)/g;
    let m;
    while ((m = re.exec(String(text || ''))) !== null) out.push(m[1]);
    return out;
  }

  /** 把 @路径 交给原生读成附件（没权限就带用户去开一次） */
  function attachPathsIn(text) {
    const nb = window.BzNative;
    const paths = pathsIn(text);
    if (!paths.length) return false;
    if (!nb || typeof nb.attachPath !== 'function') {
      push({ role: 'sys', text: '网页版读不了本地路径（浏览器没有文件系统权限），这条在手机上装 APK 用。' });
      return false;
    }
    let ok = true;
    if (typeof nb.hasAllFiles === 'function' && !nb.hasAllFiles()) {
      ok = false;
      push({
        role: 'sys',
        text: '读 @路径 需要「所有文件访问」权限：我帮你跳到那个开关，打开后回来再发一次就行。\n'
          + '（设置路径：系统设置 → 应用 → 特殊访问权限 → 所有文件访问权限 → 待办中心）',
      });
      try { nb.openAllFilesSetting(); } catch (e) { /* 忽略 */ }
    }
    if (ok) paths.forEach(p => { try { nb.attachPath(p); } catch (e) { /* 忽略 */ } });
    return ok;
  }

  // 原生读好了会叫我们一声（统一由 UI 分发；回到前台/获得焦点时也兜底再取一次）
  UI_.onAttachmentReady(takeNativeAttachment);
  window.__bzAttachError = (msg) => {
    push({ role: 'sys', text: '附件没进来：' + msg });
    UI_.toast('附件没进来：' + String(msg).slice(0, 40));
  };

  /** 附件在气泡里的样子（图片显示缩略图，文件显示名字） */
  function attachHtml(atts) {
    if (!atts || !atts.length) return '';
    return atts.map(a => {
      const url = a.thumb || a.dataUrl;
      if (a.kind === 'image' && url) return `<img class="chat-img" src="${url}" alt="${esc(a.name)}">`;
      return `<div class="chat-file-tag">📎 ${esc(a.name)} · ${fmtSize(a.size)}</div>`;
    }).join('');
  }

  /* ================= 智能体适配层 ================= */
  function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

  /** 从消息里抠出纯文本（多模态消息是分段的） */
  function textOf(m) {
    if (!m) return '';
    if (typeof m.content === 'string') return m.content;
    if (Array.isArray(m.content)) {
      return m.content.filter(p => p && p.type === 'text').map(p => p.text || '').join('\n');
    }
    return m.text || '';
  }

  /** 审批单里那条操作，给用户看个大概（别把整串参数糊上去） */
  function approvalLine(a) {
    const args = a.args || {};
    const what = args.title || args.id || args.path || args.query || '';
    return '· ' + (a.tool || '操作') + (what ? '　' + String(what).slice(0, 40) : '');
  }

  /** 活动追踪：把后端「现在在干什么」（/api/agent/state 里的 activity）翻成气泡里的一行提示。
      阶段名对齐控制台；细节能带就带（工具名 / 重试原因 / 目标），长了截断。
      拿不到快照（旧后端或本机模式）就退回原来那句笼统的提示。 */
  const STAGE_HINT = {
    running: '开始处理', thinking: '正在问模型', retrying: '模型接口在重试',
    tool: '正在调工具', tool_done: '工具已返回', tool_error: '工具报错',
    blocked: '有操作在等你放行', compressing: '上下文太长，正在压缩',
    checkpoint: '变更前打快照', memory: '正在落盘记忆', done: '正在收尾', error: '这次出错了',
  };
  function actHint(act) {
    const a = act || {};
    const d = String(a.detail || '').trim();
    const cut = (s, n) => (s.length > n ? s.slice(0, n) + '…' : s);
    let line = STAGE_HINT[a.stage];
    if (!line) return '白泽正在干活…';
    if (d) {
      if (a.stage === 'tool' || a.stage === 'tool_done') line += '：' + cut(d, 28);
      else if (a.stage === 'running') line = '开始处理：' + cut(d, 36);
      else if (a.stage === 'thinking') line = d.indexOf('问模型') === 0 ? '正在' + cut(d, 30) : cut(d, 36);
      else if (a.stage === 'retrying') {
        const why = d.replace(/^第\s*\d+\s*次重试[：:]\s*/, '');   // 「第 2 次重试：xxx」这层壳剥掉，别念两遍
        line += why ? '：' + cut(why, 40) : '';
      } else if (a.stage === 'checkpoint') line += '：' + cut(d.replace(/^变更前快照[：:]\s*/, ''), 24);
      else if (a.stage === 'tool_error' || a.stage === 'error') line += '：' + cut(d, 40);
    }
    line += '…';   // 省略号跟住动作；时长缀在后面，别让句子读起来像没说完
    const secs = a.sinceMs ? Math.round((Date.now() - a.sinceMs) / 1000) : 0;
    if (secs >= 10) line += ' · 已跑 ' + (secs < 60 ? secs + ' 秒' : Math.floor(secs / 60) + ' 分 ' + (secs % 60) + ' 秒');
    return line;
  }

  /** 后端 Agent 是「给个目标就干活」，跑完才落运行记录；这里轮询等它出结果。
      轮询间隔 2s：走的是内核回环代理，局域网一次往返几十毫秒，不会卡界面。
      state 里顺便带着活动追踪（activity）——下面把它翻成气泡提示，「现在在干什么」直接看得见。 */
  /** 等一次运行跑完。
      遇到"要人工放行的危险操作"不再把人赶去电脑 —— 直接把这个审批交给界面
      （onApproval），并**继续轮询**：用户在手机上放行之后，这次运行会接着往下做，
      结果照样能落到这个气泡里。 */
  /* 内核的 /api/agent/* 是**原样转给后端**的，回来的是后端的原始 JSON —— 没有 {ok,data}
     信封；只有内核自己的那几个接口（/api/state、/api/op、/api/device…）才带信封。
     以前这里一律按信封读（st.ok / dr.data / r.ok），碰上后端原始形状就被判成
     「跟白泽后端断了」「没找到运行记录」，明明跑成功了也显示失败。统一走这个 helper，
     两种形状都归一成 {ok, data, error}，跟 voice.js / kb.js 那边的写法保持一致。 */
  function unpack(r) {
    if (!r) return { ok: false, error: '内核没有响应', data: null };
    if (r.ok === false) return { ok: false, error: r.error || '未知原因', data: null };
    if (r.ok === true) return { ok: true, error: '', data: r.data === undefined ? r : r.data };
    if (r.error) return { ok: false, error: r.error, data: null };   // 后端原始错误形状 {error:...}
    return { ok: true, error: '', data: r };   // 后端原始形状（无信封）
  }

  async function waitRun(d, runId, onTick, onApproval) {
    const deadline = Date.now() + 180000;
    let shownSig = '';   // 已经展示过的审批 id 串，别每 2 秒重建一次卡片
    while (Date.now() < deadline) {
      await sleep(2000);
      const su = unpack(d.agentState());
      if (!su.ok) throw new Error(su.error || '跟白泽后端断了');
      const data = su.data || {};
      const pending = (data.approvals || [])
        .filter(a => a.status === 'pending' && (!a.runId || a.runId === runId));
      if (pending.length) {
        if (onTick) onTick('有操作在等你放行…');
        const sig = pending.map(a => a.id).join(',');
        if (onApproval && sig !== shownSig) { shownSig = sig; onApproval(pending); }
        continue;   // 接着等：用户放行后这次运行还会继续
      }
      if (onTick) onTick(data.running ? actHint(data.activity) : '整理结果…');
      if (!data.running) {
        // 运行记录可能比 running=false 晚一丁点才落盘（写完才可查），单次取容易扑空，
        // 给它几次机会再放弃；顺便把真实原因带出来，别只丢一句"没找到"让人瞎猜。
        let du = null;
        for (let i = 0; i < 4; i++) {
          du = unpack(d.agentRunDetail(runId));
          if (du.ok && du.data && du.data.run) break;
          await sleep(700);
        }
        if (du && du.ok && du.data && du.data.run) {
          const run = du.data.run || {};
          const body = (run.text || '').trim();
          const tail = '\n\n— 跑了 ' + (run.steps || 0) + ' 步 · ' + (run.toolCalls || 0) + ' 次工具调用 · '
            + ((run.promptTokens || 0) + (run.outTokens || 0)) + ' tokens';
          if (body) return body + tail;
          if (run.error) return '这次没干成：' + run.error + tail;
          return '这次跑完了，但没留下正文。' + tail;
        }
        return '这次的活已经跑完了，但没取到运行记录' + (du && du.error ? '（' + du.error + '）' : '（可能被清掉了）') + '。';
      }
    }
    return '等太久了（超过 3 分钟），先不等了。去控制台的「运行记录」能看到结果。';
  }

  const adapters = {
    /* 已配置的大模型：每个模型就是一个「智能体」
       raw:true 拿回原始响应，方便在「正文为空」时给出准确诊断（推理模型常见） */
    llm: {
      ready: (id) => AI.isReady(AI.cfg(id)),
      send: async (id, messages, opts) => {
        const c = AI.cfg(id);
        const r = await AI.direct(messages, Object.assign({
          modelId: id,
          maxTokens: 4096,      // 推理模型很吃 token，给小了正文就是空的
          raw: true,
          timeout: 90000,
        }, opts || {}));
        if (r.text) return r.text;

        const choice = (r.data.choices || [])[0] || {};
        const finish = choice.finish_reason || '';
        const usage = r.data.usage || {};
        const think = (choice.message && choice.message.reasoning_content) || '';
        const bits = [];
        if (usage.completion_tokens !== undefined) bits.push('输出 ' + usage.completion_tokens + ' tokens');
        if (finish) bits.push('finish_reason=' + finish);
        const detail = bits.length ? '（' + bits.join('，') + '）' : '';

        if (think) {
          return think + '\n\n——\n（' + (c.model || '该模型') + ' 这次只回了思考内容、没给正文' + detail
            + '。日常对话建议把模型换成普通的对话模型（如 deepseek-chat））';
        }
        throw new Error('模型返回正文为空' + detail
          + '。可能是：① 该模型是纯推理模型（选 deepseek-chat 这类对话模型）；'
          + '② max_tokens 被思考过程吃满（界面已放宽到 4096）；'
          + '③ 模型 ID 或 BaseURL 不支持 chat 接口。可在「设置 → AI 智能体 → 测试连通性」看详细返回。');
      },
    },

    /* 白泽智能体：跑在后端（NAS）的那个 Agent，跟电脑端控制台是同一个。
       走内核回环代理，配对令牌不出原生层。
       它直接读写后端知识库：加待办/加密码随便加；删除或看密码明文会挂进审批柜，
       要你在电脑端点头才继续 —— 也就是用户定的「不能让它随便删」。 */
    baize: {
      ready: () => Store.kbOnline(),
      send: async (id, messages, opts) => {
        const d = window.BzDevice;
        if (!d || !d.agentRun) throw new Error('当前入口不是手机 App，连不上白泽后端');
        const last = [...messages].reverse().find(m => m.role === 'user');
        const goal = textOf(last).trim();
        if (!goal) throw new Error('这句没有内容可以派给白泽');
        const r = d.agentRun(goal, false);
        if (r.error) throw new Error(r.error);
        const runId = r.runId || (r.data && r.data.runId);
        if (!runId) throw new Error('后端没有接下这个活（没拿到运行 id）');
        const tick = opts && opts.onTick;
        // 把运行 id 留在消息上：回头能凭它去取「这次做了什么」（工具链路）
        if (opts && opts.onRunId) opts.onRunId(runId);
        if (tick) tick('已经派给白泽了…');
        return await waitRun(d, runId, tick, opts && opts.onApproval);
      },
    },
  };

  /** 可选的智能体列表（输入栏左边那个按钮里显示的就是它） */
  function Agents() {
    const out = [];
    // 白泽智能体排在第一位：它是默认选项，也是「这台手机 + 后端」的正主
    out.push({
      id: '__baize__', kind: 'baize', name: '白泽智能体',
      model: Store.kbOnline() ? '后端在跑' : (Store.kbInfo() && Store.kbInfo().enabled ? '后端连不上' : '没连后端'),
      ready: adapters.baize.ready(),
    });
    AI.list().forEach(m => {
      out.push({
        id: m.id, kind: 'llm', name: m.name || '未命名',
        model: m.model || '', ready: adapters.llm.ready(m.id),
      });
    });
    return out;
  }

  function agentOf(id) { return Agents().find(a => a.id === id) || null; }

  const BAIZE_ID = '__baize__';

  function selectedAgents() {
    return selected.map(agentOf).filter(a => a && !a.disabled);
  }

  function loadSelection() {
    const usable = Agents().filter(a => !a.disabled).map(a => a.id);
    const saved = Array.isArray(Store.getSettings().chatAgents) ? Store.getSettings().chatAgents : [];
    selected = saved.filter(id => usable.indexOf(id) >= 0);
    // 默认就是白泽智能体（连上后端时）；没连后端才退回本机配的模型
    if (!selected.length) selected = [Store.kbOnline() ? BAIZE_ID : AI.active().id];
    renderAgentBtn();
  }

  /** 直接指定用哪几个智能体（供程序化调用；传多个即群发） */
  function select(ids) {
    const usable = Agents().filter(a => !a.disabled).map(a => a.id);
    selected = (ids || []).filter(id => usable.indexOf(id) >= 0);
    if (!selected.length && usable.length) selected = [usable[0]];
    saveSelection();
    renderChatSub();
    return selectedAgents();
  }

  function saveSelection() {
    const s = Store.getSettings();
    s.chatAgents = selected.slice();
    Store.saveSettings(s);
    renderAgentBtn();
  }

  function renderAgentBtn() {
    const list = selectedAgents();
    const btn = $('btnAgentPick');
    const name = $('agentName');
    const av = $('agentAvatar');
    if (!list.length) {
      name.textContent = '未选智能体';
      av.textContent = '白';
      btn.classList.add('warn');
      return;
    }
    btn.classList.remove('warn');
    if (list.length === 1) {
      name.textContent = list[0].name;
      av.textContent = (list[0].name || 'AI').slice(0, 1).toUpperCase();
    } else {
      name.textContent = list.length + ' 个智能体群发';
      av.textContent = '群';
    }
  }

  /* ================= 对话存储 ================= */
  function load() {
    msgs = Store.getChat();
    if (!msgs.length) {
      msgs = [{
        id: Store.uid(), role: 'sys', ts: Date.now(),
        text: '我是白泽智能体，跟你在电脑上用的那个是同一个（跑在 NAS 上）。\n'
          + '· 说「明天下午三点开会」，我直接把待办写进知识库，手机电脑都能看到\n'
          + '· 说「存一下 GitHub 账号 me 密码 xxx」，我帮你存进密码本\n'
          + '· 让我删待办/删密码，我会先挂一个审批单，等你点头才动手\n'
          + '· 左边可以切换或勾选多个智能体群发\n'
          + '（没连后端时退化成手机本地的小助手，只有本地规则可用）',
      }];
      Store.saveChat(msgs);
    }
  }
  function persist() {
    if (msgs.length > MAX_MSGS) msgs = msgs.slice(msgs.length - MAX_MSGS);
    // 历史里只留最近几张图的缩略图，免得把 localStorage 撑爆
    let kept = 0;
    for (let i = msgs.length - 1; i >= 0; i--) {
      const atts = msgs[i].atts;
      if (!atts) continue;
      atts.forEach(a => {
        if (a.kind !== 'image') return;
        if (kept < KEEP_THUMBS) kept++;
        else delete a.thumb;
      });
    }
    Store.saveChat(msgs);
  }

  /* ================= 渲染 ================= */
  function bubble(m) {
    const el = document.createElement('div');
    if (m.role === 'user') {
      el.className = 'chat-row me';
      el.innerHTML = `<div class="chat-bubble">${attachHtml(m.atts)}${esc(m.text)}</div>
        <div class="chat-meta">${fmtTime(m.ts)}
          <button class="chat-mini" data-copy="1">复制</button>
          <button class="chat-mini" data-resend="1">重发</button>
        </div>`;
      el.querySelector('[data-copy]').addEventListener('click', () => copyText(m.text));
      el.querySelector('[data-resend]').addEventListener('click', () => {
        if (m.text) send(m.text, { quietInput: true });
      });
      return el;
    }
    if (m.role === 'sys') {
      el.className = 'chat-row sys';
      el.innerHTML = `<div class="chat-bubble">${esc(m.text)}</div>`;
      return el;
    }
    if (m.role === 'card') {
      el.className = 'chat-row agent';
      const c = m.card || {};
      const HEAD = { password: '密码入库确认', vaultImport: '密码批量入库确认', todo: '待办入库确认' };
      const head = HEAD[c.kind] || '入库确认';
      let lines;
      if (c.kind === 'vaultImport') {
        const items = c.items || [];
        const names = items.slice(0, 6).map(x => x.title).join('、');
        lines = [`共 ${items.length} 条密码`, names + (items.length > 6 ? ' 等' : '')];
      } else if (c.kind === 'password') {
        lines = c.need ? [c.hint]
          : [`平台：${c.data.title}`, c.data.account ? `账号：${c.data.account}` : '', `密码：${'•'.repeat(Math.max(4, (c.data.password || '').length))}`];
      } else {
        lines = [`标题：${c.data.title}`, `领域：${c.data.category}`, `优先级：${{ high: '高优', mid: '中', low: '低' }[c.data.priority] || c.data.priority}`,
          c.data.due ? `时间：${String(c.data.due).replace('T', ' ')}` : '时间：闲暇待办（未排期）'];
      }
      el.innerHTML = `<div class="chat-card ${c.done ? 'done' : ''}">
          <div class="cc-head">${head}${c.done ? ' · 已处理' : ''}</div>
          ${lines.filter(Boolean).map(l => `<div class="cc-line">${esc(l)}</div>`).join('')}
          ${(c.done || c.need) ? '' : `<div class="cc-actions">
            <button class="btn ghost sm" data-card-skip="1">忽略</button>
            <button class="btn primary sm" data-card-ok="1">入库</button>
          </div>`}
        </div>`;
      if (!c.done && !c.need) {
        el.querySelector('[data-card-skip]').addEventListener('click', () => { c.done = 'skip'; persist(); render(); });
        el.querySelector('[data-card-ok]').addEventListener('click', () => { doCard(m); });
      }
      return el;
    }
    // agent
    el.className = 'chat-row agent';
    const pending = m.state === 'pending';
    const err = m.state === 'err';
    el.innerHTML = `
      <div class="chat-avatar">${esc((m.agentName || 'AI').slice(0, 1).toUpperCase())}</div>
      <div class="chat-main">
        <div class="chat-who">${esc(m.agentName || 'AI')}${err ? ' · 失败' : ''}</div>
        <div class="chat-bubble ${err ? 'err' : ''} ${pending ? 'pending' : ''}">${
          pending ? esc(m.hint || '思考中…') : esc(m.text || '(空回复)')}</div>
        ${approvalHtml(m)}
        ${m.traceOpen && m.trace ? traceHtml(m) : ''}
        <div class="chat-meta">${fmtTime(m.ts)}
          ${m.text && !pending ? '<button class="chat-mini" data-speak="1">🔊 朗读</button>' : ''}
          ${m.text ? '<button class="chat-mini" data-copy="1">复制</button>' : ''}
          ${m.runId ? `<button class="chat-mini" data-trace="1">${traceLabel(m)}</button>` : ''}
          ${err ? '<button class="chat-mini" data-retry="1">重试</button>' : ''}
        </div>
      </div>`;
    // 朗读这条回复（说话走内核转发的语音通道，手机上不留文件）
    if (window.VzVoice) window.VzVoice.mountSpeakBtn(el.querySelector('[data-speak]'), m.text || '');
    const tr = el.querySelector('[data-trace]');
    if (tr) tr.addEventListener('click', () => toggleTrace(m));
    const cp = el.querySelector('[data-copy]');
    if (cp) cp.addEventListener('click', () => copyText(m.text));
    const rt = el.querySelector('[data-retry]');
    if (rt) rt.addEventListener('click', () => {
      const prev = [...msgs].reverse().find(x => x.role === 'user' && x.ts < m.ts);
      if (prev && prev.text) send(prev.text, { quietInput: true });
    });
    const okBtn = el.querySelector('[data-appr-ok]');
    const noBtn = el.querySelector('[data-appr-no]');
    if (okBtn) okBtn.addEventListener('click', () => decideApprovals(m, true));
    if (noBtn) noBtn.addEventListener('click', () => decideApprovals(m, false));
    return el;
  }

  /* ================= 「这次做了什么」：把工具链路摊开 =================
     后端把每次运行的对话消息都留着（assistant.toolCalls + 对应的 tool 结果），
     所以手机端能还原出「调了哪个工具、给了什么参数、拿回什么」。 */

  function traceLabel(m) {
    if (m.traceOpen) return '收起过程';
    if (m.trace) return m.trace.length ? `看过程（${m.trace.length} 步）` : '看过程';
    return '看过程';
  }

  function traceHtml(m) {
    const steps = m.trace || [];
    if (!steps.length) {
      return '<div class="chat-card done"><div class="cc-head">这次没调用工具</div>' +
        '<div class="cc-line">纯对话，白泽没动你的数据。</div></div>';
    }
    return `<div class="chat-card" style="border-color:var(--line-2);background:var(--surface-2)">
      <div class="cc-head">这次做了什么（${steps.length} 步）</div>
      ${steps.map(s => `<div class="cc-line"><b>${esc(s.tool)}</b></div>` +
        (s.args ? `<div class="cc-line" style="opacity:.7;word-break:break-all">${esc(s.args)}</div>` : '') +
        (s.result ? `<div class="cc-line" style="opacity:.7;word-break:break-all">↳ ${esc(s.result)}</div>` : '')
      ).join('')}
    </div>`;
  }

  /** 从运行的消息里还原工具链路：assistant 的 toolCalls + 紧随其后的 tool 结果 */
  function stepsFromMessages(list) {
    const out = [];
    const byId = {};
    (list || []).forEach((m) => {
      if (m.role === 'assistant' && Array.isArray(m.toolCalls)) {
        m.toolCalls.forEach((c) => {
          const rec = { tool: c.name || '工具', args: clip(JSON.stringify(c.args || {}), 200), result: '' };
          out.push(rec);
          if (c.id) byId[c.id] = rec;
        });
        return;
      }
      if (m.role === 'tool') {
        const text = clip(String(m.content || ''), 200);
        const rec = m.toolCallId ? byId[m.toolCallId] : null;
        if (rec) rec.result = text;
        else out.push({ tool: m.name || '工具', args: '', result: text });
      }
    });
    return out;
  }

  /* ================= 多会话：新建 / 切换 / 删除 =================
     会话只存在手机上（后端那边每次派活都是独立的一次运行，
     逐次记录本来就都在「运行记录」里）。 */

  function openChatList() {
    const list = Store.chatList();
    const items = list.map(c => ({
      label: (c.active ? '✓ ' : '') + c.title + '（' + c.count + ' 条）',
      onTap: () => {
        if (c.active) return;
        Store.switchChat(c.id);
        load(); render(); renderChatSub();
        UI_.toast('已切到「' + c.title + '」');
      },
    }));
    items.push({
      label: '＋ 新建会话',
      onTap: () => { Store.newChat(); load(); render(); renderChatSub(); UI_.toast('开了一个新会话'); },
    });
    if (list.length > 1) {
      items.push({ label: '删除会话…', danger: true, onTap: () => openChatDelete(list) });
    }
    items.push({ label: '取消', cancel: true });
    UI_.actionSheet(items);
  }

  function openChatDelete(list) {
    const items = list.map(c => ({
      label: '删除「' + c.title + '」（' + c.count + ' 条）', danger: true,
      onTap: () => {
        Store.delChat(c.id);
        load(); render(); renderChatSub();
        UI_.toast('已删除「' + c.title + '」');
      },
    }));
    items.push({ label: '取消', cancel: true });
    UI_.actionSheet(items);
  }

  function clip(s, n) {
    const t = String(s == null ? '' : s).replace(/\s+/g, ' ');
    return t.length > n ? t.slice(0, n) + '…' : t;
  }

  function toggleTrace(m) {
    if (m.traceOpen) { update(m.id, { traceOpen: false }); return; }
    if (m.trace) { update(m.id, { traceOpen: true }); return; }
    const d = window.BzDevice;
    if (!d || typeof d.agentRunDetail !== 'function') { UI_.toast('要在手机 App 里才看得到过程'); return; }
    const r = unpack(d.agentRunDetail(m.runId));
    if (!r.ok || !r.data || !r.data.messages) { UI_.toast('取不到这次的运行记录：' + (r.error || '未知原因'), 3600); return; }
    const steps = stepsFromMessages(r.data.messages);
    update(m.id, { trace: steps, traceOpen: true });
    if (!steps.length) UI_.toast('这次没调用工具（纯对话）');
  }

  /** 危险操作的内联审批卡：手机上直接放行 / 驳回，不用跑去电脑 */
  function approvalHtml(m) {
    const list = m.approvals || [];
    if (!list.length) return '';
    // 概要 + 原始参数都给出来：要放行删除这类操作，得让人看清它到底要动什么
    const items = list.map(a => `<div class="cc-line">${esc(approvalLine(a))}</div>` +
      (a.args && Object.keys(a.args).length
        ? `<div class="cc-line" style="opacity:.7;word-break:break-all">${esc(JSON.stringify(a.args).slice(0, 160))}</div>`
        : '')
    ).join('');
    if (m.approvalsDone) {
      return `<div class="chat-card done"><div class="cc-head">等待你放行 · 已处理</div>${items}</div>`;
    }
    return `<div class="chat-card">
      <div class="cc-head">⚠ 有危险操作等你放行</div>
      ${items}
      <div class="cc-actions">
        <button class="btn danger-ghost sm" data-appr-no="1">驳回</button>
        <button class="btn primary sm" data-appr-ok="1">放行</button>
      </div>
      <div class="cc-line" style="opacity:.7">批准之前什么都不会发生。</div>
    </div>`;
  }

  /** 在手机上处理这批审批；放行后后端会接着把这次运行跑完 */
  function decideApprovals(m, approve) {
    const d = window.BzDevice;
    const list = m.approvals || [];
    if (!d || !list.length) return;
    let ok = 0, fail = '';
    list.forEach((a) => {
      const r = approve ? d.agentApprove(a.id) : d.agentReject(a.id, '手机端驳回');
      const u = unpack(r);   // 后端原始形状是 {approved,by}（没有 ok），别当成失败
      if (u.ok) ok++;
      else if (!fail) fail = u.error || '未知原因';
    });
    m.approvalsDone = true;
    persist(); render();
    if (fail) { UI_.toast('有 ' + (list.length - ok) + ' 条没处理成功：' + fail, 3600); return; }
    UI_.toast(approve ? '已放行，白泽接着跑' : '已驳回，那个操作不会执行');
    // 这次运行已经不在等了（比如页面重开过）：说一句结果去哪儿看，别让人干等
    if (m.state !== 'pending') {
      push({
        role: 'sys',
        text: approve
          ? '已放行。白泽会接着把这次跑完，结果在「其他功能 → 运行记录」里可以看到。'
          : '已驳回，那个操作不会执行。',
      });
    }
  }

  function copyText(text) {
    const t = String(text || '');
    const done = () => UI_.toast('已复制');
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(t).then(done).catch(() => fallbackCopy(t, done));
        return;
      }
    } catch (e) { /* 落到下面兜底 */ }
    fallbackCopy(t, done);
  }

  function fallbackCopy(t, done) {
    const ta = document.createElement('textarea');
    ta.value = t;
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); done(); } catch (e) { UI_.toast('复制失败，请长按选择'); }
    ta.remove();
  }

  function render() {
    const box = $('chatStream');
    box.innerHTML = '';
    msgs.forEach(m => box.appendChild(bubble(m)));
    box.scrollTop = box.scrollHeight;
  }

  function push(m) {
    msgs.push(Object.assign({ id: Store.uid(), ts: Date.now() }, m));
    persist();
    render();
  }

  function update(id, patch) {
    const m = msgs.find(x => x.id === id);
    if (!m) return;
    Object.assign(m, patch);
    persist();
    render();
  }

  /* ================= 能干活：本地识别 + 确认卡片 =================
     只在「确实是在让你记东西」时才出卡片；打招呼、提问不会乱建待办。
     所以说话要明确一点：加/记/记一下/存…密码，或者话里带「待办/任务」。 */
  const GREET = /^(你好|您好|hi|hello|嗨|在吗|谢谢|多谢|好的|好嘞|嗯|ok|测试|test)/i;
  const QUESTION = /[?？]\s*$|吗[?？]?$|什么|怎么|为什么|几点|多少|哪个|哪些|哪里|哪|如何|能不能|可不可以/;

  function looksLikeCommand(text) {
    const t = String(text || '').trim();
    if (!t || GREET.test(t)) return false;
    if (QUESTION.test(t)) return false;
    if (/^(请|帮我|给我|麻烦|帮忙)?\s*(把|将)?/.test(t)
      && /(加|添加|新增|记|记录|记住|安排|存|保存|入库|导入|整理|提醒|别忘|要做)/.test(t)) return true;
    return /(待办|任务)/.test(t);
  }

  function detectAction(text, attText) {
    const t = String(text || '').trim();
    const vaultItems = attText ? NL.parseVaultMd(attText) : [];
    const mentionsPass = /密码|口令|账号|帐号/.test(t);
    // ① 附件/@路径 里解析出密码条目 → 批量入库
    if (vaultItems.length && /(导入|入库|存|整理|收|密码|口令|账号|帐号)/.test(t)) {
      return { kind: 'vaultImport', items: vaultItems, text: attText };
    }
    // ② 话里提到密码 → 一定给卡片：抽得到就入库，抽不到就说清格式。
    //    绝不静默（以前不满足"命令句式"就连卡片都不出，看着像"记到别处去了"）
    if (mentionsPass && !QUESTION.test(t)) {
      const p = NL.parsePassword(t);
      if (p) return { kind: 'password', data: p };
      return {
        kind: 'password', need: true,
        hint: attText
          ? '这个文件里我没认出密码条目。能认的格式是：\n# 站点名\n- 账号: xxx\n- 密码: xxx\n（也就是密码本导出的那种格式）'
          : '这句我看出是要记密码，但没听出密码是多少。\n格式例如：存 GitHub 账号 me@a.com 密码 123456',
      };
    }
    // ③ 其余按「让你记东西」判断（打招呼、问问题不会乱建待办）
    if (!looksLikeCommand(t)) return null;
    return { kind: 'todo', data: NL.parseTodo(t) };
  }

  async function doCard(m) {
    const c = m.card;
    if (c.need) { c.done = 'skip'; persist(); render(); return; }   // 只是提示，没有可入库的东西
    // 密码本锁着就先别写：写下去会落到另一个库，看着就像「加错地方了」
    if (c.kind === 'vaultImport' || c.kind === 'password') {
      if (Store.isVaultLocked()) {
        push({ role: 'sys', text: '密码本已锁定，这条没有写进去。\n先在「密码本」页输入主密码解锁，再回来点卡片上的「入库」。' });
        UI_.toast('密码本已锁定，先去密码本解锁');
        return;
      }
    }
    if (c.kind === 'vaultImport') {
      const before = (await Store.getVault()).length;
      try { await Vault.importMarkdown(c.text || ''); } catch (e) { UI_.toast('导入失败：' + (e.message || e)); return; }
      const after = (await Store.getVault()).length;
      Vault.reload();
      UI_.toast('已入库 ' + Math.max(0, after - before) + ' 条密码（密码本共 ' + after + ' 条）');
      c.done = 'ok'; persist(); render();
      return;
    }
    if (c.kind === 'password') {
      let res = '';
      try {
        res = await Vault.addPassword({
          title: c.data.title, account: c.data.account || '',
          password: c.data.password || '', note: 'AI 助手录入', source: 'agent',
        });
      } catch (e) {
        push({ role: 'sys', text: '密码没能写进密码本：' + (e.message || e) });
        UI_.toast('写入失败：' + (e.message || e));
        return;
      }
      const total = (await Store.getVault()).length;
      Vault.reload();
      UI_.toast(res === 'updated'
        ? '这个账号原来就有，新密码已记进它的历史（共 ' + total + ' 条）'
        : (res === 'same' ? '这个账号已有同样密码，没重复添加' : '已存进密码本「' + c.data.title + '」（共 ' + total + ' 条）'));
    } else {
      const d = c.data;
      Todo.addByIntent({
        title: d.title, category: d.category, priority: d.priority,
        form: d.form || (d.due ? 'schedule' : 'leisure'), due: d.due,
        weekly: !!d.weekly, estimate: Number(d.estimate) || 0, note: d.note || '',
      });
      UI_.toast('待办已入库');
    }
    c.done = 'ok';
    persist();
    render();
  }

  /* ================= 发消息 ================= */
  function contextSystem() {
    const now = new Date();
    if (Store.kbOnline()) {
      // 白泽页现在说的是白泽智能体：它有 kb_list_todos 自己查，不用把待办糊进提示里。
      // （这段只在本机模型被选中时用得上，写在这里是为了不发一堆没用的上下文）
      return '你是「白泽」个人助理。用户手机上的待办与密码本归白泽后端知识库管，'
        + '你可以让用户直接在白泽智能体那边操作。回答简短、直接给结论。\n'
        + '当前时间：' + UI_.fmtDate(now);
    }
    const todos = Store.getTodos()
      .filter(t => t.status !== 'done' && t.status !== 'cancelled')
      .slice(0, 40)
      .map(t => ({ t: t.title, due: t.due || '', pri: t.priority, cat: t.category, form: t.form }));
    return '你是「白泽」个人助理，帮用户管理待办与密码本。回答简短、直接给结论，不要客套。\n'
      + '当前时间：' + UI_.fmtDate(now) + '\n'
      + '用户未完成的待办（JSON，最多 40 条）：' + JSON.stringify(todos) + '\n'
      + '如果用户是在让你记东西，不用输出 JSON —— 界面会自动给出「入库」确认卡片。';
  }

  function historyFor() {
    const recent = msgs.filter(m => m.role === 'user' || (m.role === 'agent' && m.state !== 'pending')).slice(-CTX_TURNS);
    return recent.map(m => ({
      role: m.role === 'user' ? 'user' : 'assistant',
      content: m.role === 'user'
        ? (m.text + ((m.atts || []).length ? '（附 ' + m.atts.length + ' 个附件）' : ''))
        : ('（' + m.agentName + '）' + m.text),
    }));
  }

  async function askAgent(agent, payload, history) {
    const rec = Object.assign(
      { id: Store.uid(), ts: Date.now() },
      { role: 'agent', agentId: agent.id, agentName: agent.name, text: '', state: 'pending', hint: '' });
    msgs.push(rec);
    persist(); render(); renderChatSub();
    try {
      const reply = await adapters[agent.kind].send(agent.id, [
        { role: 'system', content: contextSystem() },
        ...(history || []),
        { role: 'user', content: payload.content || payload.text },
      ], {
        // 白泽这类「派活型」智能体跑得久，中途把进度显示在气泡上，别让人对着"思考中"干等
        onTick: (t) => { rec.hint = t; update(rec.id, { hint: t }); },
        // 要人工放行的危险操作：直接在气泡里给「放行 / 驳回」，不用跑去电脑
        onApproval: (list) => update(rec.id, { approvals: list, approvalsDone: false }),
        onRunId: (id) => update(rec.id, { runId: id }),
      });
      update(rec.id, { text: (reply || '').trim() || '(空回复)', state: 'ok' });
      // 开着「回复自动朗读」就念出来（开关在后端 voice.autoSpeak，手机端「语音」页里改）
      maybeAutoSpeak(rec.text);
      // 白泽刚往知识库写过东西：拉一次，让待办/密码本页跟着刷新
      if (agent.kind === 'baize') {
        const r = Store.syncFromRemote();
        if (r.ok) {
          try { Todo.reload(); } catch (e) { /* 忽略 */ }
          try { Vault.reload(); } catch (e) { /* 忽略 */ }
          try { window.dispatchEvent(new CustomEvent('bz:syncdone')); } catch (e) { /* 忽略 */ }
        }
      }
    } catch (e) {
      update(rec.id, { text: String(e.message || e), state: 'err' });
    }
    renderChatSub();
  }

  /** 自动朗读：只在「开着开关」且「当前没在念」且「用户没在按着说话」时念 */
  function maybeAutoSpeak(text) {
    const V = window.VzVoice;
    if (!V || !text) return;
    if (V.isSpeaking() || V.isHolding()) return;
    V.autoSpeakOn().then((on) => {
      if (!on) return;
      if (V.isSpeaking() || V.isHolding()) return;
      V.speak(text);
    }).catch(() => { /* 读开关失败就静静不念，别在回复后弹一堆错 */ });
  }

  function renderChatSub() {
    const list = selectedAgents();
    const pending = msgs.filter(m => m.role === 'agent' && m.state === 'pending').length;
    if (pending) { $('chatSub').textContent = `${pending} 个智能体正在回复…`; return; }
    if (list.length > 1) { $('chatSub').textContent = `群发中：${list.map(a => a.name).join(' · ')}`; return; }
    if (!Store.kbOnline()) {
      $('chatSub').textContent = '没连上白泽后端 · 现在是本机小助手（只有本地规则可用）';
      return;
    }
    $('chatSub').textContent = '说一句就能顺手记成待办 · 加东西随便加，删除会先问你';
  }

  function send(text, opts) {
    const o = opts || {};
    const t = String(text == null ? $('chatInput').value : text).trim();
    const atts = pending.slice();
    if (!t && !atts.length) return;
    // 重发/重试走的是同一条路，但别把用户正在输入的新内容也清掉
    if (!o.quietInput) $('chatInput').value = '';

    // 先把「要发出去的内容」拼好（含原图），再落库（落库只留缩略图）
    const content = contentFor(t, atts);
    const stored = atts.map(a => ({
      kind: a.kind, name: a.name, size: a.size,
      thumb: a.thumb, text: (a.text || '').slice(0, 2048),
    }));
    // 先把附件的正文抠出来（要用来判断"这批是不是密码"），再清空附件区
    const attText = pending.filter(a => a.text).map(a => a.text).join('\n\n');
    pending = [];
    renderAttachBar();

    const history = historyFor();      // 当前这句之前的上下文
    push({ role: 'user', text: t, atts: stored });

    // 直接 @路径 读文件（先交原生去读，读好会回来变成上面的附件卡片）
    attachPathsIn(t);

    // 本地先给结果：说得像要记东西，立刻出确认卡片（离线也能用）。
    // 连上后端时不出这张卡 —— 那种情况由白泽智能体直接写进知识库，
    // 两边都动手会变成「记了两遍」。
    if (!Store.kbOnline()) {
      const act = detectAction(t, attText);
      if (act) push({ role: 'card', card: Object.assign({ done: false }, act) });
    }

    const list = selectedAgents();
    if (!list.length) {
      push({ role: 'sys', text: '还没选智能体：点左下角选一个（白泽智能体需要先在「设置 → 跨端」连上后端）。' });
      return;
    }
    const ready = list.filter(a => a.ready);
    const notReady = list.filter(a => !a.ready);
    if (notReady.length) {
      push({ role: 'sys', text: '这些智能体现在用不了，已跳过：' + notReady.map(a => a.name).join('、') });
    }
    // 群发：并行发出，谁先回谁先显示
    const payload = { text: t, content };
    ready.forEach(a => { askAgent(a, payload, history); });
  }

  /* ================= 智能体选择弹层 ================= */
  function renderAgentList() {
    const box = $('agentList');
    box.innerHTML = '';
    Agents().forEach(a => {
      const on = selected.indexOf(a.id) >= 0;
      const row = document.createElement('button');
      row.className = 'agent-row' + (on ? ' on' : '') + (a.disabled ? ' disabled' : '');
      row.innerHTML = `
        <span class="ar-box">✓</span>
        <span class="ar-main">
          <span class="ar-name">${esc(a.name)}</span>
          <span class="ar-sub">${esc(a.kind === 'baize'
            ? ('白泽智能体 · ' + a.model)
            : (a.model + (a.ready ? '' : ' · 缺 API Key')))}</span>
        </span>`;
      if (!a.disabled) {
        row.addEventListener('click', () => {
          const i = selected.indexOf(a.id);
          if (i >= 0) selected.splice(i, 1); else selected.push(a.id);
          if (!selected.length) selected = [a.id];   // 至少留一个
          renderAgentList();
        });
      }
      box.appendChild(row);
    });
  }

  /* ================= 输入区事件 ================= */
  function bindBar() {
    $('btnChatSend').addEventListener('click', () => send());
    $('btnChatPlus').addEventListener('click', openPlusMenu);
    $('chatInput').addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { e.preventDefault(); send(); }
    });
    // 语音输入交给用户自己的输入法（系统键盘上的麦克风），应用里不再做语音模块
  }

  function init() {
    load();
    render();
    bindBar();
    loadSelection();
    renderChatSub();

    $('btnAgentPick').addEventListener('click', () => {
      renderAgentList();
      UI_.openModal('agentModal');
    });
    $('agentModalClose').addEventListener('click', () => UI_.closeModal('agentModal'));
    $('agentModalOk').addEventListener('click', () => {
      saveSelection();
      UI_.closeModal('agentModal');
      renderChatSub();
      UI_.toast(selectedAgents().length > 1
        ? '已选 ' + selectedAgents().length + ' 个智能体，将群发'
        : '当前智能体：' + (selectedAgents()[0] || {}).name);
    });
    UI_.closeOnMask('agentModal');

    $('btnChatList').addEventListener('click', openChatList);
    $('btnChatClear').addEventListener('click', () => {
      UI_.actionSheet([
        {
          label: '清空当前会话', danger: true,
          onTap: () => { msgs = []; Store.saveChat([]); load(); render(); renderChatSub(); UI_.toast('当前会话已清空'); },
        },
        {
          label: '清空全部会话', danger: true,
          onTap: () => { Store.clearAllChats(); load(); render(); renderChatSub(); UI_.toast('全部会话已清空'); },
        },
        { label: '取消', cancel: true },
      ]);
    });

    // 切到这一页时滚到最新一条
    const navBtn = document.querySelector('.nav-item[data-tab="view-baize"]');
    if (navBtn) {
      navBtn.addEventListener('click', () => setTimeout(() => {
        const box = $('chatStream');
        box.scrollTop = box.scrollHeight;
      }, 60));
    }

    // 设置页改了智能体列表 / 跨端连上了 → 这里跟着刷新
    const resync = () => {
      const usable = Agents().filter(a => !a.disabled).map(a => a.id);
      selected = selected.filter(id => usable.indexOf(id) >= 0);
      if (!selected.length) selected = [Store.kbOnline() ? BAIZE_ID : AI.active().id];
      renderAgentBtn();
      renderChatSub();
    };
    window.addEventListener('bz:aichanged', resync);
    window.addEventListener('bz:syncdone', resync);
  }

  return {
    init, send, render, renderChatSub, select,
    Agents, selectedAgents, adapters, doCard, detectAction,
    pickAttachment, addAttachment, openPlusMenu, contentFor, attachHtml, takeNativeAttachment,
    pathsIn, attachPathsIn,
    getPending: () => pending.slice(),
    getMessages: () => msgs,
  };
})();
