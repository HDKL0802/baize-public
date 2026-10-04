/* 待办中心功能自测（Node 原生 WebSocket + CDP，零依赖） */
const { spawn } = require('child_process');
const fs = require('fs');
const path = require('path');

const EDGE = 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe';
const URL = 'file:///d:/xm/白泽智能体/todo-app/index.html';
const PORT = 9333 + Math.floor(Math.random() * 400);
const PROFILE = path.join(process.env.TEMP, 'bz-test-' + Date.now());

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

async function main() {
  const edge = spawn(EDGE, [
    '--headless=new', '--disable-gpu', '--window-size=420,860',
    `--remote-debugging-port=${PORT}`, `--user-data-dir=${PROFILE}`, 'about:blank',
  ], { stdio: 'ignore' });

  let target = null;
  for (let i = 0; i < 40; i++) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${PORT}/json`)).json();
      target = list.find(t => t.type === 'page');
      if (target) break;
    } catch (e) { }
    await sleep(400);
  }
  if (!target) { console.log('FAIL: 调试端口未就绪'); edge.kill(); process.exit(1); }

  const ws = new WebSocket(target.webSocketDebuggerUrl);
  let id = 0;
  const pending = new Map();
  const send = (method, params = {}) => new Promise((res, rej) => {
    const mid = ++id; pending.set(mid, { res, rej });
    ws.send(JSON.stringify({ id: mid, method, params }));
  });
  ws.onmessage = (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) { const p = pending.get(m.id); pending.delete(m.id); m.error ? p.rej(m.error) : p.res(m.result); }
  };
  await new Promise(r => ws.onopen = r);
  await send('Runtime.enable');
  await send('Page.enable');

  const evalJs = async (expr) => {
    const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
    if (r.exceptionDetails) throw new Error(String(r.exceptionDetails.exception?.description || r.exceptionDetails.text).slice(0, 220));
    return r.result.value;
  };

  const results = [];
  const check = (name, ok, extra = '') => {
    results.push({ name, ok });
    console.log((ok ? 'PASS' : 'FAIL') + ' | ' + name + (extra ? ' | ' + String(extra).slice(0, 130) : ''));
  };

  try {
    await send('Page.navigate', { url: URL });
    await sleep(1600);

    /* ---------- 主题与环境 ---------- */
    check('页面标题', (await evalJs('document.title')) === '白泽 · Baize');
    check('暗色底 #0B0F19', (await evalJs(`getComputedStyle(document.body).backgroundColor`)) === 'rgb(11, 15, 25)');
    check('主色翠绿 #059669', (await evalJs(`getComputedStyle(document.documentElement).getPropertyValue('--accent').trim()`)) === '#059669');
    check('空态提示可见', (await evalJs(`!document.getElementById('todoEmpty').hidden`)) === true);
    check('底栏三入口（对话 / 知识库 / 其他功能）',
      (await evalJs(`JSON.stringify([...document.querySelectorAll('.nav-item span')].map(e => e.textContent))`))
      === '["对话","知识库","其他功能"]',
      await evalJs(`JSON.stringify([...document.querySelectorAll('.nav-item span')].map(e => e.textContent))`));
    check('待办与密码本不再各自占底栏（合进知识库）',
      (await evalJs(`!document.querySelector('.nav-item[data-tab="view-todo"]') && !document.querySelector('.nav-item[data-tab="view-vault"]') && !!document.querySelector('.nav-item[data-tab="view-kb"]')`)) === true);

    /* ---------- 知识库：主要 / 资源 / 配置 三类 ---------- */
    check('知识库一级分类 = 主要 / 资源 / 配置',
      (await evalJs(`JSON.stringify([...document.querySelectorAll('#view-todo .kb-seg .seg-btn')].map(e => e.textContent))`))
      === '["主要","资源","配置"]',
      await evalJs(`JSON.stringify([...document.querySelectorAll('#view-todo .kb-seg .seg-btn')].map(e => e.textContent))`));
    check('主要类下 = 待办 / 密码本',
      (await evalJs(`JSON.stringify([...document.querySelectorAll('#view-todo .kb-seg .vs-btn')].map(e => e.textContent))`))
      === '["待办","密码本"]');
    check('资源类下 = 附件 / 记忆',
      (await evalJs(`JSON.stringify([...document.querySelectorAll('#view-files .kb-seg .vs-btn')].map(e => e.textContent))`))
      === '["附件","记忆"]');
    check('配置类下 = API 服务 / 模型审批',
      (await evalJs(`JSON.stringify([...document.querySelectorAll('#view-keys .kb-seg .vs-btn')].map(e => e.textContent))`))
      === '["API 服务","模型审批"]');
    await evalJs(`document.querySelector('#view-todo .kb-seg .seg-btn[data-kbcat="res"]').click()`);
    await sleep(200);
    check('点「资源」跳到附件页且底栏仍高亮知识库',
      (await evalJs(`!document.getElementById('view-files').hidden && document.querySelector('.nav-item[data-tab="view-kb"]').classList.contains('active')`)) === true);
    check('附件页有上传入口与列表容器',
      (await evalJs(`!!document.getElementById('btnFileAdd') && !!document.getElementById('fileList')`)) === true);
    check('记忆页有搜索框（走语义检索）',
      (await evalJs(`!!document.getElementById('memSearch') && !!document.getElementById('btnMemAdd')`)) === true);
    check('API 服务页能新增模型站',
      (await evalJs(`!!document.getElementById('btnKeyAdd') && !!document.getElementById('keyList')`)) === true);
    check('模型审批页能放行 / 驳回并设超时',
      (await evalJs(`!!document.getElementById('apprList') && !!document.getElementById('btnApprSave')`)) === true);

    // 记忆页：后端回的是 {hits:[{chunk:{…},score,why,parts}]} 这种嵌套结构，
    // 按扁平字段读会整页空白 —— 这条用例就是防它再犯。
    const memProbe = await evalJs(`(async () => {
      const orig = { a: BzDevice.available, c: BzDevice.call };
      let lastPath = '', rebuiltPath = '';
      const stub = (path) => {
        lastPath = path;
        if (path.indexOf('/api/agent/memory/tree/rebuild') === 0) { rebuiltPath = path; return { ok: true, days: 2 }; }
        if (path.indexOf('/api/agent/memory/tree') === 0) {
          return { staleCount: 1, nodes: [
            { id: 1, level: 1, title: '2026-10-01 记忆', summary: '【摘要】今天统一了记忆检索的阈值口径', chunkCount: 3 },
            { id: 2, level: 2, title: '2026 第 40 周 记忆', summary: '【摘要】本周做完了知识库三分法', chunkCount: 9 },
          ] };
        }
        if (path.indexOf('/api/agent/memory') === 0) {
          return {
            query: '密码本', profile: 'balanced', vectorUsed: true, candidates: 3, filtered: 2, minVector: 0.55,
            hits: [{
              chunk: { id: 7, title: '密码本加密方案', content: '密码本用 PBKDF2 + AES-GCM 加密', kind: 'decision',
                       source: 'agent', createdAt: 1790845000000, hasVector: true },
              score: 0.62, why: '语义 0.71 + 关键词 0.30',
              parts: { vector: 0.71, keyword: 0.3, graph: 0, freshness: 1 },
            }],
          };
        }
        return {};
      };
      BzDevice.available = () => true;
      BzDevice.call = stub;
      const wait = () => new Promise(r => setTimeout(r, 200));
      UI.switchTab('view-memory');

      // 不搜索时：列表 + 记忆树一起显示
      document.getElementById('memSearch').value = '';
      document.getElementById('btnMemRefresh').click();
      await wait();
      const tree = {
        hidden: document.getElementById('memTree').hidden,
        head: document.getElementById('memTreeHead').textContent,
        cards: [...document.querySelectorAll('#memTreeList .vault-card')].length,
        summary: (document.querySelector('#memTreeList .kb-body-text') || {}).textContent || '',
      };

      // 搜索时：只显示结果，树收起来
      document.getElementById('memSearch').value = '密码本';
      document.getElementById('btnMemRefresh').click();
      await wait();
      const afterSearch = {
        path: lastPath,
        treeHidden: document.getElementById('memTree').hidden,
        stat: document.getElementById('memStat').textContent,
        title: (document.querySelector('#memList .vc-title') || {}).textContent || '',
        why: (document.querySelector('#memList .vc-line') || {}).textContent || '',
        count: (document.getElementById('memCount') || {}).textContent || '',
        profiles: [...document.querySelectorAll('#memProfile .vs-btn')].map(b => b.dataset.p),
      };
      // 切档位要真的带上 profile 重查
      document.querySelector('#memProfile .vs-btn[data-p="semantic"]').click();
      await wait();
      const afterProfile = lastPath;

      // 一条都搜不到时：说清是被阈值挡了，还是真没有
      BzDevice.call = () => ({ query: 'x', vectorUsed: true, candidates: 1, filtered: 1, minVector: 0.55,
        vectorNote: '有 1 条记忆的语义相似度低于 0.55，都没够上阈值（可在「记忆术」里调低）', hits: [] });
      document.getElementById('btnMemRefresh').click();
      await wait();
      const empty = { hidden: document.getElementById('memEmpty').hidden, text: document.getElementById('memEmpty').textContent };

      // 记忆整理：先弹操作单（重建摘要 / 合并重复），点第一项要打到后端的重建接口
      BzDevice.call = stub;
      document.getElementById('memSearch').value = '';
      document.getElementById('btnMemTree').click();
      await wait();
      const sheet = [...document.querySelectorAll('#actionSheetBox .as-item')].map(b => b.textContent);
      const danger = [...document.querySelectorAll('#actionSheetBox .as-item')].map(b => b.classList.contains('danger'));
      document.querySelectorAll('#actionSheetBox .as-item')[0].click();
      await wait(); await wait();
      const rebuilt = { sheet, danger, path: rebuiltPath, refetched: lastPath, head: document.getElementById('memTreeHead').textContent };

      BzDevice.available = orig.a; BzDevice.call = orig.c;
      UI.switchTab('view-baize');
      await new Promise(r => setTimeout(r, 120));
      return { tree, afterSearch, afterProfile, empty, rebuilt };
    })()`);
    check('记忆页能画出记忆树（天/周摘要）',
      memProbe.tree.hidden === false && memProbe.tree.cards === 2
      && memProbe.tree.summary.includes('统一了记忆检索的阈值口径')
      && memProbe.tree.head.includes('1 天摘要没跟上'), JSON.stringify(memProbe.tree));
    check('搜索时记忆树让位给结果', memProbe.afterSearch.treeHidden === true, JSON.stringify(memProbe.afterSearch));
    check('记忆整理的操作单同时给出「重建摘要 / 合并重复」',
      memProbe.rebuilt.sheet.length === 3
      && memProbe.rebuilt.sheet[0].includes('重建记忆树摘要')
      && memProbe.rebuilt.sheet[1].includes('整理重复记忆'), JSON.stringify(memProbe.rebuilt.sheet));
    check('「整理重复记忆」标成危险项（删除类操作要一眼看出来）',
      memProbe.rebuilt.danger[1] === true && memProbe.rebuilt.danger[0] === false,
      JSON.stringify(memProbe.rebuilt.danger));
    check('记忆树可手动重建（打到 /memory/tree/rebuild 并重新取树）',
      memProbe.rebuilt.path.includes('/api/agent/memory/tree/rebuild')
      && memProbe.rebuilt.refetched.includes('/api/agent/memory/tree?'),
      JSON.stringify(memProbe.rebuilt));
    check('记忆页能读出后端的嵌套结构（标题/正文不再空白）',
      memProbe.afterSearch.title === '密码本加密方案' && memProbe.afterSearch.why.includes('语义 71%'),
      JSON.stringify(memProbe.afterSearch));
    check('记忆页把「挡掉几条 / 阈值」写在页头', memProbe.afterSearch.stat.includes('挡掉 2 条')
      && memProbe.afterSearch.count === '1 条', memProbe.afterSearch.stat);
    check('记忆页有四个权重档位，切换会带上 profile 重查',
      memProbe.afterSearch.profiles.join() === 'balanced,semantic,lexical,graph_first'
      && memProbe.afterProfile.includes('profile=semantic'), memProbe.afterProfile);
    check('搜不到时说清是被阈值挡的（不显示成"没有记忆"）',
      memProbe.empty.hidden === false && memProbe.empty.text.includes('0.55'), JSON.stringify(memProbe.empty));

    // 对话里遇到"要人工放行的危险操作"：必须能在手机上直接放行，而不是让人跑去电脑
    const apprProbe = await evalJs(`(async () => {
      const orig = {
        a: BzDevice.available, st: BzDevice.agentState, run: BzDevice.agentRun,
        det: BzDevice.agentRunDetail, ok: BzDevice.agentApprove, no: BzDevice.agentReject,
        online: Store.kbOnline,
      };
      const calls = [];
      let running = true;
      BzDevice.available = () => true;
      Store.kbOnline = () => true;
      BzDevice.agentRun = () => ({ ok: true, runId: 'run-test-1' });
      BzDevice.agentState = () => ({ ok: true, data: { running: running, approvals: running ? [
        { id: 'ap-1', runId: 'run-test-1', tool: 'kb_delete_todo', args: { title: '持久化验证2' }, status: 'pending' }
      ] : [] } });
      BzDevice.agentRunDetail = () => ({ ok: true, data: {
        run: { text: '已删掉那条待办。', steps: 2, toolCalls: 1, promptTokens: 10, outTokens: 5 },
        messages: [
          { role: 'user', content: '把那条待办删掉' },
          { role: 'assistant', content: '', toolCalls: [{ id: 'c1', name: 'kb_delete_todo', args: { title: '持久化验证2' } }] },
          { role: 'tool', name: 'kb_delete_todo', toolCallId: 'c1', content: '{"removed":true}' },
          { role: 'assistant', content: '已删掉那条待办。' },
        ],
      } });
      BzDevice.agentApprove = (id) => { calls.push('approve:' + id); running = false; return { ok: true }; };
      BzDevice.agentReject = (id) => { calls.push('reject:' + id); running = false; return { ok: true }; };

      Chat.select(['__baize__']);
      const keep = Chat.getMessages().length;   // 探完把这几条撤掉，别污染后面的对话用例
      document.getElementById('chatInput').value = '把标题是持久化验证2的那条待办删掉';
      document.getElementById('btnChatSend').click();
      await new Promise(r => setTimeout(r, 2600));    // 轮询间隔 2s，等第一轮
      const card = {
        hasCard: !!document.querySelector('#chatStream .chat-card'),
        head: (document.querySelector('#chatStream .chat-card .cc-head') || {}).textContent || '',
        line: (document.querySelector('#chatStream .chat-card .cc-line') || {}).textContent || '',
        hasOk: !!document.querySelector('#chatStream [data-appr-ok]'),
        hasNo: !!document.querySelector('#chatStream [data-appr-no]'),
      };
      document.querySelector('#chatStream [data-appr-ok]').click();
      await new Promise(r => setTimeout(r, 300));
      const afterOk = {
        calls: calls.slice(),
        head: (document.querySelector('#chatStream .chat-card .cc-head') || {}).textContent || '',
        hasOk: !!document.querySelector('#chatStream [data-appr-ok]'),
      };
      await new Promise(r => setTimeout(r, 2600));    // 等放行后的这一轮把结果落下来
      const bubbles = [...document.querySelectorAll('#chatStream .chat-bubble')].map(b => b.textContent);
      const finalText = bubbles[bubbles.length - 1] || '';

      // 「看过程」：把这次调用的工具链路摊开
      const traceBtn = [...document.querySelectorAll('#chatStream [data-trace]')].pop();
      const traceBtnText = traceBtn ? traceBtn.textContent : '';
      if (traceBtn) traceBtn.click();
      await new Promise(r => setTimeout(r, 250));
      const heads = [...document.querySelectorAll('#chatStream .chat-card .cc-head')].map(e => e.textContent);
      const trace = {
        btnText: traceBtnText,
        head: heads.filter(h => h.includes('这次做了什么')).join(''),
        lines: [...document.querySelectorAll('#chatStream .chat-card .cc-line')].map(e => e.textContent).join(' | '),
        collapsed: traceBtn && document.querySelector('#chatStream [data-trace]').textContent,
      };

      BzDevice.available = orig.a; BzDevice.agentState = orig.st; BzDevice.agentRun = orig.run;
      BzDevice.agentRunDetail = orig.det; BzDevice.agentApprove = orig.ok;
      BzDevice.agentReject = orig.no; Store.kbOnline = orig.online;
      const meBubble = !!document.querySelector('#chatStream .chat-row.me .chat-mini');
      Chat.getMessages().splice(keep);   // 撤掉探针造的这几条（落库的那份也一起对齐）
      Chat.render();
      Store.saveChat(Chat.getMessages());
      return { card, afterOk, finalText, meBubble, trace };
    })()`);
    check('危险操作在对话里直接给「放行 / 驳回」',
      apprProbe.card.hasCard && apprProbe.card.hasOk && apprProbe.card.hasNo
      && apprProbe.card.line.includes('kb_delete_todo'), JSON.stringify(apprProbe.card));
    check('点放行真的调了后端审批接口，并标记成已处理',
      apprProbe.afterOk.calls.join() === 'approve:ap-1'
      && apprProbe.afterOk.head.includes('已处理') && apprProbe.afterOk.hasOk === false,
      JSON.stringify(apprProbe.afterOk));
    check('放行之后这次运行接着跑完，结果落回气泡',
      apprProbe.finalText.includes('已删掉那条待办') && apprProbe.finalText.includes('1 次工具调用'),
      apprProbe.finalText);
    check('不再把人赶去电脑上批准', !apprProbe.finalText.includes('电脑'), apprProbe.finalText.slice(0, 80));
    check('我的消息带「复制 / 重发」小动作', apprProbe.meBubble === true);
    check('跑完能展开看工具链路（工具名 / 参数 / 结果）',
      apprProbe.trace.btnText.includes('看过程')
      && apprProbe.trace.head.includes('这次做了什么（1 步）')
      && apprProbe.trace.lines.includes('kb_delete_todo')
      && apprProbe.trace.lines.includes('持久化验证2')
      && apprProbe.trace.lines.includes('removed'),
      JSON.stringify(apprProbe.trace));

    // 多会话：新建 / 切换，互不干扰，切回来老消息还在
    const chatProbe = await evalJs(`(async () => {
      const wait = () => new Promise(r => setTimeout(r, 200));
      const before = { count: Chat.getMessages().length, chats: Store.chatList().length };
      const pick = () => [...document.querySelectorAll('#actionSheetBox .as-item')];

      document.getElementById('btnChatList').click();
      await wait();
      const sheet = pick().map(b => b.textContent);
      pick().find(b => b.textContent.includes('新建会话')).click();
      await wait();
      const afterNew = {
        count: Chat.getMessages().length,
        chats: Store.chatList().length,
        title: (Store.chatList().find(c => c.active) || {}).title,
        sysOnly: Chat.getMessages().every(m => m.role === 'sys'),
      };

      const back = Store.chatList().find(c => !c.active);
      document.getElementById('btnChatList').click();
      await wait();
      pick().find(b => b.textContent.includes(back.title)).click();
      await wait();
      const afterBack = {
        count: Chat.getMessages().length,
        activeIsOld: Store.chatList().find(c => c.active).id === back.id,
      };
      return { before, sheet, afterNew, afterBack, backTitle: back.title };
    })()`);
    check('会话入口列出会话清单并给出「新建会话」',
      chatProbe.sheet.some(s => s.includes('新建会话')) && chatProbe.sheet.some(s => s.includes('✓')),
      JSON.stringify(chatProbe.sheet));
    check('新建会话：消息切空（只剩开场白），清单多一条',
      chatProbe.afterNew.count === 1 && chatProbe.afterNew.sysOnly === true
      && chatProbe.afterNew.chats === chatProbe.before.chats + 1,
      JSON.stringify(chatProbe.afterNew));
    check('切回原会话：消息原样回来',
      chatProbe.afterBack.activeIsOld === true
      && chatProbe.afterBack.count === chatProbe.before.count,
      JSON.stringify(chatProbe.afterBack));

    /* ---------- 其他功能：同一套分组 ---------- */
    await evalJs(`UI.switchTab('view-more')`);
    await sleep(200);
    check('其他功能按 主要 / 资源 / 配置 分组',
      (await evalJs(`JSON.stringify([...document.querySelectorAll('#moreBody .group-title')].map(e => e.textContent))`))
      === '["主要","资源","配置"]',
      await evalJs(`JSON.stringify([...document.querySelectorAll('#moreBody .group-title')].map(e => e.textContent))`));
    check('其他功能里条目齐全（含定时任务 / 运行记录 / API 服务）',
      (await evalJs(`JSON.stringify([...document.querySelectorAll('#moreBody .li-name')].map(e => e.textContent))`)).includes('定时任务')
      && (await evalJs(`JSON.stringify([...document.querySelectorAll('#moreBody .li-name')].map(e => e.textContent))`)).includes('运行记录')
      && (await evalJs(`JSON.stringify([...document.querySelectorAll('#moreBody .li-name')].map(e => e.textContent))`)).includes('API 服务'),
      await evalJs(`JSON.stringify([...document.querySelectorAll('#moreBody .li-name')].map(e => e.textContent))`));
    check('运行记录归到「配置」、定时任务归到「资源」',
      (await evalJs(`(() => {
        const groups = [...document.querySelectorAll('#moreBody .group-title')].map(e => e.textContent);
        const names = [...document.querySelectorAll('#moreBody .li-name')].map(e => e.textContent);
        const res = names.slice(names.indexOf('设备'), names.indexOf('模型审批'));
        const cfg = names.slice(names.indexOf('模型审批'));
        return res.includes('定时任务') && cfg.includes('运行记录');
      })()`)) === true);
    await evalJs(`UI.switchTab('view-baize')`);
    await sleep(150);

    /* ---------- 待办：快速添加与统计 ---------- */
    await evalJs(`Todo.quickAdd('整理本周工作计划')`);
    await evalJs(`Todo.quickAdd('复习 Go 基础')`);
    check('快速添加写为日程排期', (await evalJs(`Todo.getList().filter(t=>t.form==='schedule').length`)) === 2);
    check('卡片渲染 2 张', (await evalJs(`document.querySelectorAll('.todo-card').length`)) === 2);
    const stat = await evalJs(`document.getElementById('todoStat').textContent`);
    check('页头统计「日程 2 项」', stat.includes('日程 2 项'), stat);
    check('分段计数 (2)', (await evalJs(`document.getElementById('cntSched').textContent`)) === '(2)');
    await evalJs(`Todo.setView('leisure')`);
    check('日程⇄闲暇视图切换', (await evalJs(`document.querySelector('#viewSwitch .vs-btn.active').dataset.view`)) === 'leisure');
    await evalJs(`Todo.setView('schedule')`);

    /* ---------- 排序切换 ---------- */
    await evalJs(`document.getElementById('btnSort').click()`);
    check('排序按钮可切换', (await evalJs(`document.getElementById('toast').textContent`)).includes('排序'));
    await evalJs(`document.getElementById('btnSort').click()`);

    /* ---------- 自然语言解析（js/nlparse.js） ---------- */
    const t = await evalJs(`(() => { const r = NL.parseTodo('加一个明天复习 Go 待办'); return { title: r.title, due: r.due }; })()`);
    check('解析:加一个明天复习Go待办', !!t.title && !!t.due, JSON.stringify(t));
    const p = await evalJs(`(() => NL.parsePassword('存 GitHub 账号 zhangsan 密码 abc1234'))()`);
    check('解析:存GitHub密码', !!p && p.title === 'GitHub' && p.account === 'zhangsan' && p.password === 'abc1234', JSON.stringify(p));
    const tm = await evalJs(`(() => { const r = NL.parseTime('今天下午3点'); return new Date(r).toISOString(); })()`);
    check('解析时间:今天下午3点', !!tm && new Date(tm).getHours() === 15, tm);

    /* ---------- 白泽智能体页：输入区布局（＋ 智能体 … 发送） ---------- */
    await evalJs(`UI.switchTab('view-baize')`);
    await sleep(200);
    const tools = await evalJs(`JSON.stringify([...document.querySelectorAll('.composer-tools > *')].map(el => el.id).filter(Boolean))`);
    check('工具行从左到右：＋ / 智能体 / 按住说话 / 发送', tools === '["btnChatPlus","btnAgentPick","btnChatMic","btnChatSend"]', tools);
    // 语音不再是"交给输入法"：应用内有自己的按住说话键，接了后端的听写通道
    check('有按住说话键，且真的接了语音模块', (await evalJs(
      `!!document.getElementById('btnChatMic') && typeof window.VzVoice === 'object'
       && typeof window.VzVoice.holdStart === 'function' && typeof window.VzVoice.speak === 'function'`)) === true);
    check('旧版那个录音键没有回来（不重复挂）', (await evalJs(`!document.getElementById('btnVoiceHold')`)) === true);
    check('输入框提示语提示可以直接 @路径', (await evalJs(`document.getElementById('chatInput').placeholder`)).includes('白泽'));

    /* ---------- 语音：说话（朗读）+ 听话（按住说话） ---------- */
    // 说话走"页面同源的内核端口"，浏览器里没有内核就得把原因说清楚，不许静默
    const vk = await evalJs(`JSON.stringify(window.VzVoice.kernel())`);
    check('浏览器（无本机内核）里语音会明确说走不通', /不是手机 App/.test(vk), vk);
    const vEmpty = await evalJs(`(async () => JSON.stringify(await window.VzVoice.speak('')))()`);
    check('念空内容直接拒绝并给原因', /空的/.test(vEmpty), vEmpty);
    const vStop = await evalJs(`(async () => JSON.stringify(await window.VzVoice.holdStop()))()`);
    check('没在录音时松手不会假装录到了', /没有在录音/.test(vStop), vStop);
    // 16k 单声道 16 位 WAV：听写通道只认这个，编错了后端会直接报"不是 PCM"
    const wav = await evalJs(`(function(){
      const b = window.VzVoice.encodeWav16(new Float32Array(800), 16000);
      return JSON.stringify({ type: b.type, size: b.size });
    })()`);
    check('录音转出的确实是 16k 单声道 WAV', /audio\/wav/.test(wav) && /"size":1644/.test(wav), wav);
    // 语音入口在「其他功能 → 配置」里，和 API 服务摆在一起
    const hasVoiceEntry = await evalJs(`(function(){
      const names = [...document.querySelectorAll('#moreBody .li-name')].map(e => e.textContent);
      return JSON.stringify(names);
    })()`);
    check('「其他功能 → 配置」里有语音入口', /语音/.test(hasVoiceEntry), hasVoiceEntry);
    check('＋ 键在最左边', (await evalJs(`document.querySelector('.composer-tools').firstElementChild.id`)) === 'btnChatPlus');
    check('发送键在最右边', (await evalJs(`document.querySelector('.composer-tools').lastElementChild.id`)) === 'btnChatSend');
    // 工具行多了「按住说话」这一格，窄屏（420px）上不许被挤到看不见
    const over = await evalJs(`(function(){
      const t = document.querySelector('.composer-tools');
      const c = document.querySelector('.chat-composer');
      return JSON.stringify({ tools: t.scrollWidth, toolsBox: t.clientWidth, composer: c.scrollWidth, composerBox: c.clientWidth });
    })()`);
    const om = JSON.parse(over);
    check('工具行在窄屏上不溢出（发送键看得见）', om.tools <= om.toolsBox + 1 && om.composer <= om.composerBox + 1, over);

    // ＋ 键：拍照 / 相册 / 文件
    await evalJs(`document.getElementById('btnChatPlus').click()`);
    await sleep(200);
    const plusItems = await evalJs(`JSON.stringify([...document.querySelectorAll('#actionSheetBox .as-item')].map(b => b.textContent))`);
    check('＋ 键给出拍照 / 相册 / 文件三项', /拍照/.test(plusItems) && /相册/.test(plusItems) && /文件/.test(plusItems), plusItems);
    await evalJs(`document.getElementById('actionSheet').hidden = true`);
    check('＋ 三项各接一个原生/浏览器取件入口', (await evalJs(
      `typeof Chat.pickAttachment === 'function' && /camera/.test(String(Chat.pickAttachment))`)) === true);

    // 原生附件：前端自己来取（大图不走 evaluateJavascript，避免静默失败）
    const pullAtt = await evalJs(`(() => {
      const tiny = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==';
      window.BzNative = {
        pickAttachment: () => {},
        takeAttachment: () => JSON.stringify({
          kind: 'file', name: 'primary:笔记/笔记/密码.md', mime: 'text/markdown', size: 2048, text: '# 密码\\n- 账号: x',
        }),
      };
      Chat.takeNativeAttachment();
      const got = Chat.getPending();
      const name = got.length ? got[got.length - 1].name : '';
      const chips = document.querySelectorAll('#chatAttachBar .attach-chip').length;
      // 再模拟一次图片
      window.BzNative.takeAttachment = () => JSON.stringify({ kind: 'image', name: 'photo.jpg', mime: 'image/jpeg', size: 300, base64: tiny.split(',')[1] });
      Chat.takeNativeAttachment();
      window.__bzAttachError('读不到这个文件（选择器没给读取权限）');
      const sysTexts = Chat.getMessages().filter(m => m.role === 'sys').map(m => m.text);
      delete window.BzNative;
      return {
        fileName: name, chips,
        pending: Chat.getPending().length,
        errBubbled: sysTexts.some(t => /附件没进来/.test(t)),
        errVisible: !document.getElementById('toast').textContent ? '' : document.getElementById('toast').textContent,
      };
    })()`);
    check('原生附件能被前端取回并去掉 primary: 前缀', pullAtt.fileName === '密码.md' && pullAtt.chips >= 1, JSON.stringify(pullAtt));
    check('附件失败会明确说出来（不再静默）', pullAtt.errBubbled === true, JSON.stringify(pullAtt));

    /* ---------- @路径 读文件（电脑上 Agent 那种玩法） ---------- */
    const atPath = await evalJs(`(() => {
      const got = Chat.pathsIn('看看 @/sdcard/Download/笔记.md 和 @/sdcard/DCIM 这两处');
      const calls = [];
      window.BzNative = {
        hasAllFiles: () => true,
        attachPath: (p) => calls.push(p),
        openAllFilesSetting: () => calls.push('setting'),
        takeAttachment: () => '', pickAttachment: () => {}, startRecord: undefined,
      };
      const ok = Chat.attachPathsIn('看看 @/sdcard/Download/笔记.md');
      delete window.BzNative;
      // 没权限时应提示并跳设置
      const calls2 = [];
      window.BzNative = { hasAllFiles: () => false, attachPath: () => {}, openAllFilesSetting: () => calls2.push('setting'), takeAttachment: () => '' };
      const n0 = Chat.getMessages().length;
      const ok2 = Chat.attachPathsIn('@/sdcard/a.txt');
      const hinted = Chat.getMessages().some(m => m.role === 'sys' && /所有文件访问/.test(m.text));
      delete window.BzNative;
      return { paths: got, ok, calls, ok2, hint: hinted, noCallWhenNoPerm: calls2.length === 1 && calls.length === 1 };
    })()`);
    check('能识别消息里的 @路径', atPath.paths.length === 2 && atPath.paths[0] === '/sdcard/Download/笔记.md'
      && atPath.paths[1] === '/sdcard/DCIM', JSON.stringify(atPath.paths));
    check('@路径 会交给原生去读', atPath.ok === true && atPath.calls[0] === '/sdcard/Download/笔记.md', JSON.stringify(atPath.calls));
    check('没权限时提示并跳设置（不静默）', atPath.ok2 === false && atPath.hint === true, JSON.stringify(atPath));

    /* ---------- 密码：意图判断与抽取（不能悄悄降级成待办） ---------- */
    const pw = await evalJs(`(() => {
      const a = NL.parsePassword('让 AI 帮我添加一个 GitHub 的密码（名字不对，密码为 123456）');
      const b = NL.parsePassword('存一个 GitHub 账号 me@a.com 密码 123456');
      const c = NL.parsePassword('帮我加一个淘宝账号密码 abc123');
      const md = NL.parseVaultMd('# GitHub\\n- 账号: me\\n- 密码: 123\\n## 密码\\n# 微信\\n- 密码: abc');
      const d1 = Chat.detectAction('帮我添加一个 GitHub 的密码，密码为 123456');
      const d2 = Chat.detectAction('存一个 GitHub 账号 me 密码 123456');
      const d3 = Chat.detectAction('帮我加一个明天下午三点开会的待办');
      const d4 = Chat.detectAction('帮我把这些密码存进密码本', '# GitHub\\n- 账号: me\\n- 密码: 123');
      const d5 = Chat.detectAction('帮我记个密码');
      return { a, b, c, md, k1: d1 && d1.kind, k2: d2 && d2.kind, k3: d3 && d3.kind,
        k4: d4 && d4.kind, n4: d4 && (d4.items || []).length, need5: !!(d5 && d5.need) };
    })()`);
    check('密码：口语说法也能抽出平台与密码', pw.a && pw.a.title === 'GitHub' && pw.a.password === '123456', JSON.stringify(pw.a));
    check('密码：旧的「存X账号Y密码Z」仍然有效', pw.b && pw.b.account === 'me@a.com' && pw.b.password === '123456' && pw.b.title === 'GitHub', JSON.stringify(pw.b));
    check('密码：账号密码连着写也不跑偏', pw.c && pw.c.password === 'abc123' && pw.c.account === '', JSON.stringify(pw.c));
    check('密码：说记密码但没说密码 → 明确提示，不降级', pw.k1 === 'password' && pw.k2 === 'password' && pw.need5 === true, JSON.stringify(pw));
    check('待办功能不受影响', pw.k3 === 'todo', String(pw.k3));
    check('密码 MD 文件能批量识别（跳过分节标题）', pw.md.length === 2 && pw.md[0].title === 'GitHub' && pw.md[1].title === '微信', JSON.stringify(pw.md));
    check('带文件的密码走批量入库卡片', pw.k4 === 'vaultImport' && pw.n4 === 1, JSON.stringify(pw));

    // 容易漏的句式：没写「加/存」这种动词时，以前连卡片都不出，看着就像「记到别处去了」
    const pw2 = await evalJs(`(() => {
      const e1 = Chat.detectAction('GitHub 密码 123456');
      const e2 = Chat.detectAction('我 GitHub 的密码是 123456');
      const e3 = Chat.detectAction('密码本在哪');
      const e4 = Chat.detectAction('我的 GitHub 密码是多少');
      const e5 = Chat.detectAction('密码是啥');
      return {
        k1: e1 && e1.kind, p1: e1 && e1.data && e1.data.password,
        k2: e2 && e2.kind, p2: e2 && e2.data && e2.data.password,
        n3: e3 === null, n4: e4 === null, k5: e5 && e5.kind, need5: !!(e5 && e5.need),
      };
    })()`);
    check('密码：不带动词也出卡片（GitHub 密码 123456）', pw2.k1 === 'password' && pw2.p1 === '123456', JSON.stringify(pw2));
    check('密码：陈述句「密码是…」也认', pw2.k2 === 'password' && pw2.p2 === '123456', JSON.stringify(pw2));
    check('密码：提问（在哪/是多少）不会乱出卡片', pw2.n3 === true && pw2.n4 === true, JSON.stringify(pw2));
    check('密码：抽不出像密码的值就走提示卡，不硬认', pw2.k5 === 'password' && pw2.need5 === true, JSON.stringify(pw2));

    // 本地「能干活」：不依赖任何模型就能出确认卡片
    const cardsBefore = await evalJs(`Chat.getMessages().filter(m => m.role === 'card').length`);
    await evalJs(`Chat.send('加一个明天复习 Go 待办')`);
    await sleep(250);
    check('说一句话立刻出待办确认卡片', (await evalJs(`Chat.getMessages().filter(m => m.role === 'card').length`)) === cardsBefore + 1);
    check('卡片是待办类型', (await evalJs(`[...document.querySelectorAll('#chatStream .cc-head')].pop().textContent`)).includes('待办'));
    await evalJs(`[...document.querySelectorAll('#chatStream [data-card-ok]')].pop().click()`);
    await sleep(250);
    check('点「入库」才写进待办', (await evalJs('Todo.getList().length')) === 3);
    check('入库后卡片标记已处理', (await evalJs(`[...document.querySelectorAll('#chatStream .chat-card')].pop().className`)).includes('done'));

    /* ---------- 新建待办任务全屏页 ---------- */
    await evalJs(`Todo.openForm(null)`);
    await sleep(250);
    check('新建待办全屏页打开', (await evalJs(`!document.getElementById('todoPage').hidden`)) === true);
    check('表单含「所属领域」三选项', (await evalJs(`document.querySelectorAll('#tDomain .chip-opt').length`)) === 3);
    check('表单含「预计耗时」六档', (await evalJs(`document.querySelectorAll('#tEstimate .chip-opt').length`)) === 6);
    await evalJs(`document.getElementById('tTitle').value = '自动化测试任务'`);
    const domainChips = await evalJs('JSON.stringify([...document.querySelectorAll("#tDomain .chip-opt")].map(b => b.dataset.v))');
    check('所属领域只有「工作开发 / 创作 / 日常生活」', domainChips === '["工作开发","创作","日常生活"]', domainChips);
    await evalJs(`document.querySelector('#tDomain .chip-opt[data-v="创作"]').click()`);
    await evalJs(`document.querySelector('#tPriority .chip-opt[data-v="high"]').click()`);
    await evalJs(`document.querySelector('#tEstimate .chip-opt[data-v="45"]').click()`);
    await evalJs(`document.getElementById('tWeekly').click()`);
    check('领域 chip 选中态', (await evalJs(`document.querySelector('#tDomain .chip-opt.on').dataset.v`)).includes('创作'));
    check('任务形式默认跟随当前视图', (await evalJs(`document.getElementById('tFormBadge').textContent`)) === '日程排期');
    await evalJs(`document.querySelector('#tForm .chip-opt[data-v="leisure"]').click()`);
    await sleep(150);
    check('切闲暇后隐藏日期区块', (await evalJs(`document.getElementById('tDueBlock').hidden`)) === true);
    check('任务形式徽标联动', (await evalJs(`document.getElementById('tFormBadge').textContent`)) === '闲暇待办');
    await evalJs(`document.getElementById('tpSave').click()`);
    await sleep(300);
    const created = await evalJs(`(() => { const x = Todo.getList().find(v=>v.title==='自动化测试任务'); return x ? { cat:x.category, pri:x.priority, form:x.form, est:x.estimate, weekly:x.weekly } : null; })()`);
    check('保存后新字段落库', !!created && created.pri === 'high' && created.form === 'leisure' && created.est === 45 && created.weekly === true, JSON.stringify(created));

    /* ---------- AI 智能排期页 ---------- */
    await evalJs(`Plan.open()`);
    await sleep(250);
    check('AI 排期全屏页打开', (await evalJs(`!document.getElementById('planPage').hidden`)) === true);
    check('四种策略偏好', (await evalJs(`document.querySelectorAll('#policyList .policy-item').length`)) === 4);
    check('API 卡显示待排期数量', (await evalJs(`document.getElementById('ppApiSub').textContent`)).includes('待排期'), await evalJs(`document.getElementById('ppApiSub').textContent`));
    await evalJs(`document.querySelector('#policyList .policy-item[data-v="work"]').click()`);
    check('策略偏好可切换并持久化', (await evalJs(`Store.getSettings().planPolicy`)) === 'work');
    check('未配置 AI 时显示提示条', (await evalJs(`!document.getElementById('ppWarn').hidden`)) === true);
    await evalJs(`document.getElementById('ppStart').click()`);
    await sleep(250);
    check('未配置 AI 时点排期被拦下并跳去设置页', (await evalJs(`!document.getElementById('view-settings').hidden`)) === true);
    const planned = await evalJs(`(async () => { Plan.open(); const n = Plan.computePlan().length; UI.closePage('planPage'); return n; })()`);
    check('本地排期算法仍可算出顺序', planned > 0, String(planned));

    /* ---------- 密码本 ---------- */
    await evalJs(`UI.switchTab('view-vault')`);
    await sleep(200);
    check('密码本副标题', (await evalJs(`document.querySelector('#view-vault .page-sub').textContent`)).includes('取代手机自带密码库'));
    const strong = await evalJs(`Vault.generateStrongPassword(16)`);
    check('生成强密码(16位含四类)', typeof strong === 'string' && strong.length === 16 && /[A-Z]/.test(strong) && /[a-z]/.test(strong) && /[0-9]/.test(strong) && /[!#$%+\-_=@]/.test(strong), strong);
    await evalJs(`Vault.reload()`);
    await sleep(200);
    check('密码本计数文案', (await evalJs(`document.getElementById('vaultCount').textContent`)).includes('条记录'));

    const cryptoOk = await evalJs(`(async () => {
      await Store.setMasterPwd('test123');
      await Store.saveVault([{ id: Store.uid(), title: 'GitHub', account: 'u', password: 'p', url: '', note: '', source: 'manual' }]);
      const back = await Store.getVault();
      return back.length === 1 && back[0].password === 'p';
    })()`);
    check('AES-GCM 加密 roundtrip', cryptoOk === true);
    check('明文不落盘(仅密文)', (await evalJs(`!!localStorage.getItem('bz_vault_enc') && !localStorage.getItem('bz_vault')`)) === true);
    check('错误主密码被拒', (await evalJs(`Store.verifyMasterPwd('wrong')`)) === false);
    check('正确主密码可通过校验', (await evalJs(`Store.verifyMasterPwd('test123')`)) === true);
    const relock = await evalJs(`(async () => {
      Store.lock();
      const locked = Store.isVaultLocked();
      const bad = await Store.unlock('wrong');
      const good = await Store.unlock('test123');
      const back = await Store.getVault();
      return { locked, bad, good, n: back.length, unlocked: !Store.isVaultLocked() };
    })()`);
    check('锁定 ⇄ 解锁可来回切换', relock.locked === true && relock.bad === false && relock.good === true
      && relock.unlocked === true && relock.n === 1, JSON.stringify(relock));
    await evalJs(`Vault.refreshLockBtn()`);
    check('解锁后顶栏锁按钮提示解锁', (await evalJs(`document.getElementById('btnLock').title`)).includes('锁定'));
    await evalJs(`document.getElementById('btnLock').click()`);
    check('点顶栏锁按钮可再次锁定', (await evalJs(`Store.isVaultLocked()`)) === true);
    await evalJs(`(async () => { await Store.unlock('test123'); Vault.refreshLockBtn(); })()`);
    await sleep(150);

    const imp = await evalJs(`(async () => {
      const md = '### 微信\\n- 账号: wx_01\\n- 密码: mypwd123\\n';
      const r = await Vault.importMarkdown(md);
      const v = (await Store.getVault()).find(x => x.title === '微信');
      return { added: r.vault.added, pwd: v && v.password };
    })()`);
    check('Markdown 导入解析', imp && imp.added >= 1 && imp.pwd === 'mypwd123', JSON.stringify(imp));

    const roundTrip = await evalJs(`(async () => {
      const todos = JSON.parse(JSON.stringify(Store.getTodos()));
      const md = await Vault.exportMarkdown();
      Store.saveTodos([]);
      const r = await Vault.importMarkdown(md);
      const back = Store.getTodos();
      const ok = back.length === todos.length && back.some(t => t.title === '自动化测试任务' && t.category === '创作' && t.priority === 'high');
      const r2 = await Vault.importMarkdown(md);
      return { exported: md.length, back: back.length, was: todos.length, ok, dup: r2.todos.added };
    })()`);
    check('待办 Markdown 导出→导入往返一致', roundTrip.ok === true, JSON.stringify(roundTrip));
    check('重复导入不会产生重复待办', roundTrip.dup === 0, JSON.stringify(roundTrip));

    const mdOut = await evalJs(`Vault.exportMarkdown()`);
    check('Markdown 导出含待办与密码段', typeof mdOut === 'string' && mdOut.includes('## 待办') && mdOut.includes('## 密码'), (mdOut || '').length + ' 字符');

    // 后端快照基线 + 队列对账：后端删掉的待办不能被手机"复活"
    // （真机上出现过：控制台/CLI 删掉的待办，带着同样的 id/createdAt 又被手机推回来）
    const syncProbe = await evalJs(`(async function(){
      const orig = {
        avail: BzDevice.available, rc: BzDevice.remoteConfigured, rr: BzDevice.remoteReady,
        ri: BzDevice.remoteInfo, rs: BzDevice.remoteState, ro: BzDevice.remoteOp,
      };
      const calls = [];
      let server = [];
      let serverVault = [];
      let online = true;
      BzDevice.available = () => true;
      BzDevice.remoteConfigured = () => true;
      BzDevice.remoteReady = () => true;
      BzDevice.remoteInfo = () => ({ enabled: true, remote: true, stale: false });
      BzDevice.remoteState = () => ({ ok: true, data: {
        todos: JSON.parse(JSON.stringify(server)),
        vault: JSON.parse(JSON.stringify(serverVault)),
        vaultLocked: false, hasMasterPwd: false } });
      BzDevice.remoteOp = (op, a) => {
        if (!online) return { ok: false, error: '断网' };
        calls.push({ op: op, id: (a && a.id) || '' });
        if (op === 'todo.upsert') server = server.filter(x => x.id !== a.id).concat([a]);
        if (op === 'todo.remove') server = server.filter(x => x.id !== a.id);
        if (op === 'vault.upsert') serverVault = serverVault.filter(x => x.id !== a.id).concat([a]);
        if (op === 'vault.remove') serverVault = serverVault.filter(x => x.id !== a.id);
        return { ok: true };
      };
      const mkV = (id) => ({ id: id, title: 'T' + id, account: 'acc', password: 'pwd',
        group: '', history: [], updatedAt: 1 });
      const mk = (id) => ({ id: id, title: id, category: '测试', priority: 'mid', form: 'leisure',
        due: '', weekly: false, estimate: 0, deps: [], atts: [], note: '', owner: 'user',
        status: 'todo', remind: false, createdAt: 1 });
      const wipe = () => ['bz_todos', 'bz_outbox', 'bz_remote_snapshot', 'bz_sync_error'].forEach(k => localStorage.removeItem(k));
      const snapLen = () => { const s = JSON.parse(localStorage.getItem('bz_remote_snapshot') || 'null'); return s ? s.length : -1; };

      wipe();
      server = [mk('A')];
      const pulled = Store.syncFromRemote();
      const seed = { cache: Store.getTodos().length, snap: snapLen(), pulled: pulled.todos };

      // ① 拿"跟快照一模一样"的列表重存一次：不该产生任何同步操作
      calls.length = 0;
      Store.saveTodos(JSON.parse(JSON.stringify(Store.getTodos())));
      const noop = { calls: calls.length, outbox: Store.outboxCount() };

      // ② 后端已删 A 并同步过后：界面模块的列表必须被重新装载（syncFromRemote 会派发
      //    bz:syncdone，todo.js 收到就 reload），因此"过期列表"根本不会出现，A 不会被推回去
      server = [];
      Store.syncFromRemote();
      const rearmed = { todoList: (window.Todo && Todo.getList) ? Todo.getList().length : -1 };
      calls.length = 0;
      Store.saveTodos((window.Todo && Todo.getList) ? Todo.getList() : Store.getTodos());
      const resurrect = { server: server.length, upsertA: calls.some(c => c.op === 'todo.upsert' && c.id === 'A'), outbox: Store.outboxCount() };

      // ③ 真离线新增：必须能推上去
      server = [mk('B')];
      Store.syncFromRemote();
      calls.length = 0;
      Store.saveTodos(Store.getTodos().concat([mk('C')]));
      const addOk = { onServer: server.some(x => x.id === 'C'), upsertC: calls.some(c => c.op === 'todo.upsert' && c.id === 'C') };

      // ④ 真删除：必须能推上去
      server = [mk('B'), mk('C')];
      Store.syncFromRemote();
      calls.length = 0;
      Store.saveTodos(Store.getTodos().filter(x => x.id !== 'C'));
      const delOk = { onServer: server.some(x => x.id === 'C'), removed: calls.some(c => c.op === 'todo.remove' && c.id === 'C') };

      // ⑤ 密码本同理：后端删掉的记录，不能被"过期列表"当场推回去（密码本没有 outbox，
      //    所以是"当场推回"而不是"补推时复活"；靠同步后重新装载本模块列表来防）
      serverVault = [mkV('P')];
      Store.syncFromRemote();
      await Vault.reload();
      const vaultBefore = (Vault.getList ? Vault.getList().length : -1);
      serverVault = [];                                  // 后端把 P 删了
      Store.syncFromRemote();                            // 拉取 → 派发 bz:syncdone → Vault 重装载
      await new Promise(z => setTimeout(z, 30));         // 监听器里的 reload 是 async，等它落地再断言
      const vaultAfter = (Vault.getList ? Vault.getList().length : -1);
      calls.length = 0;
      await Store.saveVault(Vault.getList ? Vault.getList() : []);
      const vaultResurrect = { before: vaultBefore, after: vaultAfter,
        upsertP: calls.some(c => c.op === 'vault.upsert' && c.id === 'P'), server: serverVault.length };

      // ⑥ 跨端模式下不得把本机缓存镜像给内核（否则内核的首次迁移会在"后端被清空"时把缓存迁回去）
      const lastBefore = BzDevice.lastSyncInfo ? BzDevice.lastSyncInfo() : null;
      BzDevice.pushTodos([mk('Z')]);
      const mirror = { lastBefore: lastBefore, lastAfter: BzDevice.lastSyncInfo ? BzDevice.lastSyncInfo() : null };

      wipe();
      Object.assign(BzDevice, { available: orig.avail, remoteConfigured: orig.rc, remoteReady: orig.rr,
        remoteInfo: orig.ri, remoteState: orig.rs, remoteOp: orig.ro });
      return { seed: seed, noop: noop, rearmed: rearmed, resurrect: resurrect, addOk: addOk, delOk: delOk, vaultResurrect: vaultResurrect, mirror: mirror };
    })()`);
    check('同步基线：拉取后快照与本机缓存一致', syncProbe.seed.cache === 1 && syncProbe.seed.snap === 1, JSON.stringify(syncProbe.seed));
    check('拿没变过的列表重存：不产生任何同步操作', syncProbe.noop.calls === 0 && syncProbe.noop.outbox === 0, JSON.stringify(syncProbe.noop));
    check('同步后界面模块的待办列表被重新装载（不会再过期）', syncProbe.rearmed.todoList === 0, JSON.stringify(syncProbe.rearmed));
    check('后端删掉的待办不会被推回来', syncProbe.resurrect.server === 0 && syncProbe.resurrect.upsertA === false && syncProbe.resurrect.outbox === 0, JSON.stringify(syncProbe.resurrect));
    check('真离线新增仍能推到后端', syncProbe.addOk.onServer === true && syncProbe.addOk.upsertC === true, JSON.stringify(syncProbe.addOk));
    check('真删除仍能推到后端', syncProbe.delOk.onServer === false && syncProbe.delOk.removed === true, JSON.stringify(syncProbe.delOk));
    check('同步后密码本模块列表也被重新装载', syncProbe.vaultResurrect.before === 1 && syncProbe.vaultResurrect.after === 0, JSON.stringify(syncProbe.vaultResurrect));
    check('后端删掉的密码记录不会被推回来', syncProbe.vaultResurrect.upsertP === false && syncProbe.vaultResurrect.server === 0, JSON.stringify(syncProbe.vaultResurrect));
    check('跨端模式下不把本机缓存镜像给内核', syncProbe.mirror.lastAfter === null, JSON.stringify(syncProbe.mirror));

    // 与 Go 版 CLI（cli/bz.exe）的互操作：这里用的就是 CLI `bz export` 的真实输出
    const cliMd = '# 待办中心导出（2026-09-25）\n\n## 待办\n'
      + '- [ ] 整理本周工作计划 ｜ 类别: 工作开发 ｜ 优先级: 高优 ｜ 截止: 2026-09-26 14:00\n'
      + '- [ ] 看两章《Go 语言实战》 ｜ 优先级: 低 ｜ 备注: 睡前读\n\n'
      + '## 密码\n### GitHub\n- 账号: user@example.com\n- 密码: abc123\n'
      + '- URL: https://github.com\n- 备注: 测试账号\n';
    const cliImp = await evalJs(`(async () => {
      const r = await Vault.importMarkdown(${JSON.stringify(cliMd)});
      const t = Store.getTodos();
      const work = t.find(x => x.title === '整理本周工作计划');
      const read = t.find(x => x.title === '看两章《Go 语言实战》');
      const v = (await Store.getVault()).find(x => x.account === 'user@example.com');
      return {
        added: r.todos.added, vadded: r.vault.added,
        cat: work && work.category, pri: work && work.priority, due: work && work.due,
        readPri: read && read.priority, readForm: read && read.form,
        pwd: v && v.password, url: v && v.url,
      };
    })()`);
    check('能导入 Go 版 CLI 导出的 Markdown', cliImp.added === 2 && cliImp.vadded === 1, JSON.stringify(cliImp));
    check('CLI 格式字段解析正确', cliImp.cat === '工作开发' && cliImp.pri === 'high'
      && cliImp.due === '2026-09-26T14:00' && cliImp.readPri === 'low' && cliImp.readForm === 'leisure'
      && cliImp.pwd === 'abc123' && cliImp.url === 'https://github.com', JSON.stringify(cliImp));

    /* ---------- 密码：同网站多账号 + 同账号多密码（历史）+ 归属分组 ---------- */
    const vaultNew = await evalJs(`(async () => {
      await Store.saveVault([]);            // 清空记录（主密码保持不动）
      await Vault.addPassword({ title: 'Trae 国际站', account: 'me@a.com', group: 'Trae', password: 'p1', source: 'manual' });
      await Vault.addPassword({ title: 'Trae 国内站', account: 'me@a.com', group: 'Trae', password: 'p2', source: 'manual' });
      await Vault.addPassword({ title: 'Trae 国际站', account: 'me@a.com', group: 'Trae', password: 'p1new' });
      const all = await Store.getVault();
      const a = all.find(x => x.title === 'Trae 国际站');
      Vault.reload();
      await new Promise(r => setTimeout(r, 150));
      return {
        n: all.length,
        titles: all.map(x => x.title),
        cur: a && a.password,
        history: a ? a.history.map(h => h.pwd) : [],
        heads: [...document.querySelectorAll('#vaultList .vault-group-head')].map(e => e.textContent.trim()),
      };
    })()`);
    check('同一个网站可以放多个账号（各自一条记录）', vaultNew.n === 2 && vaultNew.titles.join(',') === 'Trae 国际站,Trae 国内站', JSON.stringify(vaultNew));
    check('同一账号换密码 → 新的是当前、旧的进历史', vaultNew.cur === 'p1new' && vaultNew.history.indexOf('p1') === 0, JSON.stringify(vaultNew));
    check('同一个归属的记录分到一组（列表里有分组标题）', vaultNew.heads.length === 1 && /Trae/.test(vaultNew.heads[0]), JSON.stringify(vaultNew.heads));

    const hisUse = await evalJs(`(async () => {
      const a = (await Store.getVault()).find(x => x.title === 'Trae 国际站');
      Vault.reload();
      await new Promise(r => setTimeout(r, 120));
      document.querySelector('#vaultList [data-his]').click();     // 展开历史
      const rows = document.querySelectorAll('#vaultList .vc-his-row').length;
      document.querySelector('#vaultList [data-huse]').click();    // 把历史那条换回当前
      await new Promise(r => setTimeout(r, 200));
      const b = (await Store.getVault()).find(x => x.id === a.id);
      return { rows, cur: b.password, history: b.history.map(h => h.pwd) };
    })()`);
    check('历史密码可展开、可换回当前（当前那条自动沉到历史）', hisUse.rows === 1
      && hisUse.cur === 'p1' && hisUse.history.indexOf('p1new') === 0, JSON.stringify(hisUse));

    const mdRT = await evalJs(`(async () => {
      const md = await Vault.exportVaultMarkdown();
      await Store.saveVault([]);
      Vault.reload();
      await Vault.importMarkdown(md);
      const a = (await Store.getVault()).find(x => x.title === 'Trae 国际站');
      await Vault.importMarkdown(md);        // 再导一次：不该翻倍，也不该丢历史
      const all = await Store.getVault();
      const b = all.find(x => x.title === 'Trae 国际站');
      return {
        hasGroup: md.indexOf('- 归属: Trae') >= 0,
        hasHis: md.indexOf('- 历史密码: p1') >= 0,
        group: b && b.group, cur: b && b.password,
        his: b ? b.history.map(h => h.pwd) : [], n: all.length,
      };
    })()`);
    check('导出的 MD 带归属与历史密码', mdRT.hasGroup === true && mdRT.hasHis === true, JSON.stringify(mdRT));
    check('导出→导入往返不丢归属/历史，重复导入不翻倍', mdRT.group === 'Trae' && mdRT.cur === 'p1'
      && mdRT.his.indexOf('p1new') >= 0 && mdRT.n === 2, JSON.stringify(mdRT));

    /* ---------- 待办附件 ---------- */
    const attTodo = await evalJs(`(async () => {
      Todo.openForm(null);
      document.getElementById('tTitle').value = '带附件的任务';
      const real = window.BzNative;
      window.BzNative = { takeAttachmentFor: (dest) => dest === 'todo'
        ? JSON.stringify({ dest: 'todo', kind: 'image', name: '照片.jpg', mime: 'image/jpeg', size: 12345,
            path: '/data/user/0/com.baize.todo/files/att/x.jpg', thumb: 'data:image/jpeg;base64,AAAA' })
        : '' };
      Todo.takeTodoAttachment();
      window.BzNative = real;
      const chips = document.querySelectorAll('#tAtts .att-item').length;
      document.getElementById('tpSave').click();
      await new Promise(r => setTimeout(r, 250));
      const t = Todo.getList().find(x => x.title === '带附件的任务');
      return {
        chips,
        saved: t ? (t.atts || []).length : -1,
        kind: t && t.atts && t.atts[0] && t.atts[0].kind,
        cardThumbs: document.querySelectorAll('#todoList .tc-att-thumb').length,
      };
    })()`);
    check('待办能加附件（原生回传后进草稿并显示缩略图）', attTodo.chips === 1, JSON.stringify(attTodo));
    check('附件随待办一起保存，卡片上也看得到', attTodo.saved === 1 && attTodo.kind === 'image'
      && attTodo.cardThumbs >= 1, JSON.stringify(attTodo));

    const attClaim = await evalJs(`(() => {
      const real = window.BzNative;
      const calls = [];
      window.BzNative = { takeAttachmentFor: (d) => { calls.push(d); return ''; } };
      Chat.takeNativeAttachment();
      Todo.takeTodoAttachment();
      window.BzNative = real;
      return calls;
    })()`);
    check('对话与待办各自只认领自己的附件（不会互相抢）', attClaim.join(',') === 'chat,todo', JSON.stringify(attClaim));

    check('待办表单里有附件入口', (await evalJs(`!!document.getElementById('tAttAdd')`)) === true);
    check('附件查看页（预览/打开/另存/删除）存在', (await evalJs(
      `!!document.getElementById('attPage') && typeof Todo.openAtt === 'function'`)) === true);

    /* ---------- 系统与配置页 ---------- */
    await evalJs(`UI.switchTab('view-settings')`);
    await sleep(200);
    check('Markdown 数据中心可导出', (await evalJs(`!!document.getElementById('btnExport')`)) === true);
    check('导出走原生/Blob 通道方法', (await evalJs(`typeof UI.saveText === 'function' && typeof UI.pickText === 'function'`)) === true);
    check('通知权限状态已渲染', (await evalJs(`document.getElementById('notifyStateText').textContent.length`)) > 0);
    check('测试通知按钮存在', (await evalJs(`!!document.getElementById('btnTestNotify')`)) === true);
    check('已移除无效的系统跳转项', (await evalJs(`document.querySelectorAll('.link-item[data-sys]').length`)) === 0);
    check('已移除应用内悬浮球', (await evalJs(`!document.querySelector('.float-widget') && !document.getElementById('widgetPicker')`)) === true);

    /* ---------- AI 智能体：配置在总设置里，多模型可切换 ---------- */
    check('AI 配置已移到总设置页', (await evalJs(`!!document.querySelector('#view-settings #aiGroup #sApiKey')`)) === true);
    check('旧弹层已删除', (await evalJs(`!document.getElementById('apiModal')`)) === true);
    check('AI 协议只有两种', (await evalJs(`document.querySelectorAll('#aiProtocol .chip-opt').length`)) === 2);
    check('内置 DeepSeek 预设', (await evalJs(`AI.list()[0].baseUrl`)).includes('deepseek'), await evalJs(`AI.list()[0].baseUrl`));
    check('未配 Key 时 AI.ready()=false', (await evalJs(`AI.ready()`)) === false);

    await evalJs(`document.querySelector('#aiProtocol .chip-opt[data-v="anthropic"]').click()`);
    check('协议切换立即生效', (await evalJs(`AI.active().protocol`)) === 'anthropic');
    check('协议已即时落库', (await evalJs(`Store.getSettings().ai.models[0].protocol`)) === 'anthropic');
    check('协议选中态立即刷新', (await evalJs(`document.querySelector('#aiProtocol .chip-opt.on').dataset.v`)) === 'anthropic');
    await evalJs(`document.querySelector('#aiProtocol .chip-opt[data-v="openai"]').click()`);

    await evalJs(`(() => { const el = document.getElementById('sModel'); el.value = 'deepseek-chat'; el.dispatchEvent(new Event('input')); })()`);
    check('输入模型 ID 即时落库', (await evalJs(`Store.getSettings().ai.models[0].model`)) === 'deepseek-chat');
    check('输入后列表行即时刷新', (await evalJs(`document.querySelector('#aiModelList .am-sub').textContent`)).includes('deepseek-chat'));
    await evalJs(`(() => { const el = document.getElementById('sApiKey'); el.value = 'sk-test'; el.dispatchEvent(new Event('input')); })()`);
    check('填完 Key 后 AI.ready()=true', (await evalJs(`AI.ready()`)) === true);
    check('配置完成后提示条转绿', (await evalJs(`document.getElementById('aiStatus').className`)).includes('ok'));
    check('配置完成后语音页提示隐藏', (await evalJs(`document.getElementById('voiceCfgWarn').hidden`)) === true);

    /* 连通性测试：用假 fetch 覆盖四种返回，验证「通不通」和「有没有正文」分开报 */
    const conn = await evalJs(`(async () => {
      const orig = window.fetch;
      const fake = (payload) => async () => ({ ok: true, status: 200, text: async () => JSON.stringify(payload) });
      const out = {};

      window.fetch = fake({ choices: [{ message: { content: '连通' }, finish_reason: 'stop' }], usage: { prompt_tokens: 5, completion_tokens: 2 } });
      out.normal = await AI.test().then(v => v, e => 'ERR:' + e.message);

      window.fetch = fake({ choices: [{ message: { content: '', reasoning_content: '用户让我回复连通…' }, finish_reason: 'length' }], usage: { prompt_tokens: 5, completion_tokens: 128 } });
      out.reasoningOnly = await AI.test().then(v => v, e => 'ERR:' + e.message);

      window.fetch = fake({ choices: [{ message: { content: '' }, finish_reason: 'stop' }], usage: { prompt_tokens: 5, completion_tokens: 0 } });
      out.empty = await AI.test().then(v => 'NO_ERROR:' + v, e => e.message);

      window.fetch = async () => ({ ok: false, status: 401, text: async () => 'Unauthorized' });
      out.http = await AI.test().then(v => 'NO_ERROR:' + v, e => e.message);

      window.fetch = orig;
      return out;
    })()`);
    check('连通性测试：正常返回正文', conn.normal === '连通', JSON.stringify(conn.normal));
    check('连通性测试：只回思考内容算连通并提醒换模型', /^接口连通，但/.test(conn.reasoningOnly)
      && conn.reasoningOnly.includes('finish_reason=length') && conn.reasoningOnly.includes('deepseek-chat'), conn.reasoningOnly);
    check('连通性测试：正文为空要报错，不能当好结果', /返回正文是空的/.test(conn.empty), conn.empty);
    check('连通性测试：HTTP 401 明确报错', /HTTP 401/.test(conn.http), conn.http);
    check('连通性测试不再出现「空响应」当作通过', !/\(空响应\)/.test(conn.normal + conn.reasoningOnly + conn.empty + conn.http));

    for (let i = 0; i < 6; i++) {
      await evalJs(`(() => { const ms = AI.list(); if (ms.length < 5) AI.add('openai'); Settings.refresh(); })()`);
    }
    check('最多可存 5 个模型', (await evalJs(`AI.list().length`)) === 5, String(await evalJs(`AI.list().length`)));
    check('达到上限后按钮禁用', (await evalJs(`document.getElementById('btnAddModel').disabled`)) === true);
    await evalJs(`AI.setActive(AI.list()[1].id); Settings.refresh();`);
    check('可切换使用中的模型', (await evalJs(`document.querySelectorAll('#aiModelList .ai-model.on').length`)) === 1);
    await evalJs(`(() => { const el = document.getElementById('mName'); el.value = '备用模型'; el.dispatchEvent(new Event('input')); })()`);
    check('改名即时反映到列表', (await evalJs(`document.querySelector('#aiModelList .ai-model.on .am-name').textContent`)).includes('备用模型'));
    await evalJs(`(() => { const ms = AI.list(); AI.remove(ms[2].id); Settings.refresh(); })()`);
    check('模型可删除', (await evalJs(`AI.list().length`)) === 4, String(await evalJs(`AI.list().length`)));

    check('AI 具备串行队列与两家协议实现', (await evalJs(
      `typeof AI.enqueue === 'function' && typeof AI.chat === 'function' && typeof AI.parseTodoText === 'function'`)) === true);
    check('可解析围栏 JSON', (await evalJs(`JSON.stringify(AI.extractJson('\`\`\`json\\n{"title":"x"}\\n\`\`\`'))`)) === '{"title":"x"}');
    check('本地规则兜底解析仍可用', (await evalJs(
      `(function(){ var r = NL.parseTodo('加一个明天复习 Go 待办'); return r.category === '工作开发' && !!r.due; })()`)) === true);
    check('解析规则已抽到 NL 模块', (await evalJs(`typeof NL.parseTodo === 'function' && typeof NL.parsePassword === 'function'`)) === true);
    check('语音输入已整体移除', (await evalJs(
      `typeof Voice === 'undefined' && typeof window.__bzTranscriptError === 'undefined'`)) === true);

    /* ---------- AI 助手：群发（用假 fetch，不打真网络） ---------- */
    const group = await evalJs(`(async () => {
      // 准备两个都填好 Key 的智能体
      const first = AI.active().id;
      const second = AI.add('openai').id;
      AI.update(second, { name: '备胎模型', apiKey: 'sk-2', model: 'gpt-4o-mini' });
      AI.update(first, { name: '主力模型', apiKey: 'sk-1', model: 'deepseek-chat' });

      const orig = window.fetch;
      window.fetch = async (url, opt) => {
        const body = JSON.parse(opt.body);
        await new Promise(r => setTimeout(r, body.model === 'deepseek-chat' ? 120 : 30));
        return { ok: true, status: 200, text: async () => JSON.stringify({
          choices: [{ message: { content: '来自 ' + body.model + ' 的回复' } }] }) };
      };

      // 勾选两个 → 群发
      const before = Chat.getMessages().length;
      // 先用 UI 勾选验证多选交互
      const sel = (function () {
        document.getElementById('btnAgentPick').click();
        const rows = [...document.querySelectorAll('#agentList .agent-row')];
        const usable = rows.filter(r => !r.classList.contains('disabled'));
        const off = usable.find(r => !r.classList.contains('on'));
        if (off) off.click();              // 勾上一个未选的
        document.getElementById('agentModalOk').click();
        return {
          names: Chat.selectedAgents().map(a => a.name),
          rows: rows.length, usable: usable.length,
          onBefore: usable.filter(r => r.classList.contains('on')).length,
        };
      })();
      // 再明确选这两个已填 Key 的智能体（保证群发一定发两份）
      const picked = Chat.select([first, second]).map(a => a.name);

      const cardsBefore = Chat.getMessages().filter(m => m.role === 'card').length;
      document.getElementById('chatInput').value = '今天有什么安排？';
      document.getElementById('btnChatSend').click();
      await new Promise(r => setTimeout(r, 500));
      const replies = Chat.getMessages().filter(m => m.role === 'agent' && m.state === 'ok').map(m => m.agentName + '|' + m.text);
      const cardsAfterAsk = Chat.getMessages().filter(m => m.role === 'card').length;

      window.fetch = orig;
      return {
        sel, picked, before, replies, cardsBefore, cardsAfterAsk,
        pendingLeft: Chat.getMessages().filter(m => m.state === 'pending').length,
        saved: Store.getChat().length,
      };
    })()`);
    check('弹层里可直接勾选智能体', group.sel.names.length >= 1 && group.sel.rows >= 2 && group.sel.onBefore >= 1,
      JSON.stringify(group.sel));
    check('可指定多个智能体（群发选择生效）', group.picked.length === 2, JSON.stringify(group.picked));
    check('群发后两个智能体各自回复', group.replies.length === 2
      && group.replies.some(r => r.startsWith('主力模型|') && r.includes('deepseek-chat'))
      && group.replies.some(r => r.startsWith('备胎模型|') && r.includes('gpt-4o-mini')), JSON.stringify(group.replies));
    check('回复到达后没有卡在「思考中」', group.pendingLeft === 0, JSON.stringify(group.pendingLeft));
    check('提问（今天有什么安排）不会乱建待办卡片', group.cardsAfterAsk === group.cardsBefore, JSON.stringify(group));
    check('对话记录已落盘', group.saved > group.before, JSON.stringify({ saved: group.saved, before: group.before }));
    check('白泽智能体排在第一位且是真接入（不再是灰掉的占位）', (await evalJs(
      `(() => { const a = Chat.Agents()[0]; return a.kind === 'baize' && a.name === '白泽智能体' && a.disabled !== true; })()`)) === true);
    check('适配层可扩展（llm 适配器就位）', (await evalJs(`typeof Chat.adapters.llm.send === 'function'`)) === true);

    /* ---------- 附件：图片进请求 / 只存缩略图 / Anthropic 转换 ---------- */
    const TINY_PNG = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==';
    const attachTest = await evalJs(`(async () => {
      await Chat.addAttachment({ kind: 'image', name: '测试图.png', mime: 'image/png', size: 1234, dataUrl: '${TINY_PNG}' });
      await Chat.addAttachment({ kind: 'file', name: 'note.md', mime: 'text/markdown', size: 20, text: '# 标题\\n正文' });
      const p = Chat.getPending();
      const chips = document.querySelectorAll('#chatAttachBar .attach-chip').length;
      const barHidden = document.getElementById('chatAttachBar').hidden;
      return {
        n: p.length, chips, barHidden,
        imgHasThumb: !!(p[0] && p[0].thumb), thumbJpeg: /^data:image\\/jpeg/.test((p[0] || {}).thumb || ''),
        fileText: p[1] && p[1].text,
      };
    })()`);
    check('可添加图片与文件附件（附件条显示）', attachTest.n === 2 && attachTest.chips === 2 && attachTest.barHidden === false, JSON.stringify(attachTest));
    check('图片自动压成 JPEG 并生成缩略图', attachTest.imgHasThumb && attachTest.thumbJpeg, JSON.stringify(attachTest));
    check('文本文件带上了正文', attachTest.fileText && attachTest.fileText.includes('# 标题'), attachTest.fileText);

    const mm = await evalJs(`(async () => {
      const mainId = AI.list().find(m => m.model === 'deepseek-chat') .id;
      Chat.select([mainId]);
      const orig = window.fetch;
      let seen = null;
      window.fetch = async (url, opt) => {
        seen = { url: url, body: JSON.parse(opt.body) };
        return { ok: true, status: 200, text: async () => JSON.stringify({ choices: [{ message: { content: '图里是空白' } }] }) };
      };
      document.getElementById('chatInput').value = '这张图里写了什么';
      document.getElementById('btnChatSend').click();
      await new Promise(r => setTimeout(r, 600));
      window.fetch = orig;
      const last = seen.body.messages[seen.body.messages.length - 1];
      const stored = Chat.getMessages().filter(m => m.role === 'user').pop();
      const storedJson = JSON.stringify(stored.atts || []);
      return {
        contentIsArray: Array.isArray(last.content),
        types: Array.isArray(last.content) ? last.content.map(p => p.type) : [],
        hasImageData: Array.isArray(last.content) && last.content.some(p => p.type === 'image_url' && /^data:image\\/jpeg;base64,/.test(p.image_url.url)),
        fileInlined: Array.isArray(last.content) && last.content.some(p => p.type === 'text' && /# 标题/.test(p.text || '')),
        historyHasSystem: typeof seen.body.messages[0].content === 'string' && seen.body.messages[0].content.indexOf('白泽') >= 0,
        storedHasDataUrl: /dataUrl/.test(storedJson),
        storedHasThumb: /thumb/.test(storedJson),
        pendingCleared: Chat.getPending().length === 0,
        barHidden: document.getElementById('chatAttachBar').hidden,
        replyArrived: Chat.getMessages().some(m => m.role === 'agent' && m.state === 'ok' && /空白/.test(m.text)),
      };
    })()`);
    check('图片作为 image_url 真的进了请求', mm.contentIsArray && mm.types.join(',') === 'text,image_url,text'
      && mm.hasImageData, JSON.stringify(mm));
    check('文本文件正文被内联进请求', mm.fileInlined === true, JSON.stringify(mm));
    check('系统提示仍在最前面', mm.historyHasSystem === true);
    check('历史只存缩略图（不存原图）', mm.storedHasDataUrl === false && mm.storedHasThumb === true, JSON.stringify(mm));
    check('发送后附件条清空', mm.pendingCleared === true && mm.barHidden === true, JSON.stringify(mm));
    check('带图消息能收到回复', mm.replyArrived === true, JSON.stringify(mm));

    const anth = await evalJs(`(async () => {
      const a = AI.add('anthropic');
      AI.update(a.id, { name: 'Claude', apiKey: 'sk-ant', model: 'claude-3-5-haiku-latest' });
      Chat.select([a.id]);
      await Chat.addAttachment({ kind: 'image', name: 'x.png', mime: 'image/png', size: 10, dataUrl: '${TINY_PNG}' });
      const orig = window.fetch;
      let seen = null;
      window.fetch = async (url, opt) => {
        seen = { url: url, body: JSON.parse(opt.body) };
        return { ok: true, status: 200, text: async () => JSON.stringify({ content: [{ type: 'text', text: '收到' }] }) };
      };
      Chat.send('看看这张');
      await new Promise(r => setTimeout(r, 600));
      window.fetch = orig;
      const last = seen.body.messages[seen.body.messages.length - 1];
      const img = (last.content || []).find(p => p.type === 'image');
      return {
        url: seen.url,
        hasSystem: typeof seen.body.system === 'string' && seen.body.system.indexOf('白泽') >= 0,
        types: (last.content || []).map(p => p.type),
        base64Only: !!(img && img.source && img.source.type === 'base64' && /^[A-Za-z0-9+/=]+$/.test(img.source.data)),
        media: img && img.source && img.source.media_type,
      };
    })()`);
    check('Anthropic 协议下走 /messages 且系统提示独立', /\/messages$/.test(anth.url) && anth.hasSystem === true, JSON.stringify(anth));
    check('Anthropic 图片转成 base64 source（无 data: 前缀）', anth.types.join(',') === 'text,image'
      && anth.base64Only === true && anth.media === 'image/jpeg', JSON.stringify(anth));

    /* ---------- 原生提醒下发接口存在 ---------- */
    check('提醒同步接口已导出', (await evalJs(`typeof UI.syncNativeReminders === 'function'`)) === true);
    check('浏览器下 syncNativeReminders 返回 false', (await evalJs(`UI.syncNativeReminders()`)) === false);

    /* ---------- 密码本锁定时：写入必须报错，不能偷偷落到明文槽 ----------
       以前 saveVault 在「有主密码但没解锁」时会写明文槽，
       于是数据分成两份、界面上看不到 —— 用户说的「加错文件夹」就是这种。 */
    const lockedWrite = await evalJs(`(async () => {
      Store.lock();
      const lockState = Store.isVaultLocked();
      let threw = '';
      try { await Store.saveVault([{ id: 'x', title: 'T', account: 'a', password: 'p' }]); }
      catch (e) { threw = e.message; }
      return {
        lockState, threw,
        hasPlain: !!localStorage.getItem('bz_vault'),
        hasEnc: !!localStorage.getItem('bz_vault_enc'),
      };
    })()`);
    check('密码本锁定时写入被明确拒绝', lockedWrite.lockState === true && !!lockedWrite.threw, JSON.stringify(lockedWrite));
    check('锁定时不写明文槽（不再出现两份密码本）', lockedWrite.hasPlain === false && lockedWrite.hasEnc === true, JSON.stringify(lockedWrite));

    const lockedCard = await evalJs(`(async () => {
      Chat.send('加一个淘宝的密码 abc12345');
      await new Promise(r => setTimeout(r, 200));
      const box = [...document.querySelectorAll('#chatStream .chat-card')].pop();
      const btn = box && box.querySelector('[data-card-ok]');
      if (btn) btn.click();
      await new Promise(r => setTimeout(r, 400));
      const after = [...document.querySelectorAll('#chatStream .chat-card')].pop();
      const sys = Chat.getMessages().filter(m => m.role === 'sys').map(m => m.text).join(' | ');
      return { hasBtn: !!btn, done: after.className.indexOf('done') >= 0, hint: sys.indexOf('密码本已锁定') >= 0 };
    })()`);
    check('锁定时点「入库」：说清先去密码本解锁', lockedCard.hint === true, JSON.stringify(lockedCard));
    check('锁定时点「入库」：卡片保留，解锁后能重试', lockedCard.done === false, JSON.stringify(lockedCard));
    await evalJs(`(async () => { await Store.unlock('test123'); Vault.reload(); })()`);

    await evalJs(`Store.lock()`);
    check('密码本可锁定', (await evalJs(`Store.isVaultLocked()`)) === true);

  } catch (e) {
    check('测试执行异常', false, e.message);
  }

  ws.close(); edge.kill();
  try { fs.rmSync(PROFILE, { recursive: true, force: true }); } catch (e) { }
  const failed = results.filter(r => !r.ok).length;
  console.log(`\n=== ${results.length - failed}/${results.length} 通过 ===`);
  process.exit(failed ? 1 : 0);
}
main().catch(e => { console.error(e); process.exit(1); });
