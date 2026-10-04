/* 白泽待办中心 - 语音助手（Web Speech 识别 + 自然语言意图解析） */
'use strict';
window.Voice = (function () {
  const UI_ = window.UI;
  const $ = UI_.$;
  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  let rec = null, listening = false;
  let pending = null; // {type, ...parsed}

  /* ================= 自然语言解析 =================
     规则（后续由后端 Agent 意图接管）：
     - 待办：加一个{时间}{修饰}{内容}   /  {时间}要做{内容}  /  记录：{内容}
     - 密码：存{平台}账号...密码...  /  存{平台}密码
  */
  const TIME_WORDS = {
    '今天': 0, '今晚': 0, '今天早上': 0, '明天': 1, '明早': 1, '明天早上': 1, '后天': 2,
    '大后天': 3, '下周一': 'nextMonday', '下星期二': 'nextTue', '下周三': 'nextWed',
    '周末': 'weekend', '下周末': 'nextweekend', '下个月': 'nextMonth', '周五': 'thisFri',
    '星期六': 'thisSat', '周日': 'thisSun', '星期六': 'thisSat',
  };
  const PRIORITY_WORDS = { '紧急': 'high', '马上': 'high', '优先': 'high', '高优': 'high', '尽快': 'high' };
  const CATEGORY_WORDS = [
    { w: '工作', c: '工作' }, { w: '开会', c: '工作' }, { w: '会议', c: '工作' }, { w: '项目', c: '工作' },
    { w: '复习', c: '学习' }, { w: '学习', c: '学习' }, { w: '读书', c: '学习' }, { w: '作业', c: '学习' }, { w: '背单词', c: '学习' }, { w: '考试', c: '学习' },
    { w: '买', c: '生活' }, { w: '做饭', c: '生活' }, { w: '打扫', c: '生活' }, { w: '洗衣', c: '生活' }, { w: '取', c: '生活' }, { w: '交', c: '生活' },
    { w: '写', c: '创作' }, { w: '画', c: '创作' }, { w: '剪', c: '创作' }, { w: '拍', c: '创作' },
    { w: '跑', c: '健身' }, { w: '练', c: '健身' }, { w: '健身', c: '健身' }, { w: '瑜伽', c: '健身' },
  ];

  function parseTime(text) {
    for (const [w, off] of Object.entries(TIME_WORDS)) {
      if (text.includes(w)) {
        const d = new Date();
        if (off === 'nextMonday') { d.setDate(d.getDate() + ((8 - d.getDay()) % 7 || 7)); }
        else if (off === 'nextTue') { d.setDate(d.getDate() + ((9 - d.getDay()) % 7 || 7)); }
        else if (off === 'nextWed') { d.setDate(d.getDate() + ((10 - d.getDay()) % 7 || 7)); }
        else if (off === 'thisFri') { d.setDate(d.getDate() + ((5 - d.getDay() + 7) % 7)); }
        else if (off === 'thisSat') { d.setDate(d.getDate() + ((6 - d.getDay() + 7) % 7)); }
        else if (off === 'thisSun') { d.setDate(d.getDate() + ((7 - d.getDay() + 7) % 7)); }
        else if (off === 'weekend') { const diff = (6 - d.getDay() + 7) % 7; d.setDate(d.getDate() + (diff === 0 ? 7 : diff)); }
        else if (off === 'nextweekend') { const diff = (6 - d.getDay() + 7) % 7; d.setDate(d.getDate() + (diff === 0 ? 14 : diff + 7)); }
        else if (off === 'nextMonth') { d.setMonth(d.getMonth() + 1); }
        else { d.setDate(d.getDate() + off); }
        // 时间点（早/晚/点），下午/晚上自动 +12h
        let hour = null;
        const hm = text.match(/(\d{1,2})\s*(?:点|时)/);
        if (hm) hour = +hm[1];
        const isAfternoon = text.includes('下午');
        const isEvening = text.includes('晚上') || text.includes('今晚');
        if (hour === null) {
          if (text.includes('早上') || text.includes('早晨')) hour = 8;
          else if (text.includes('上午')) hour = 10;
          else if (isEvening) hour = 20;
          else if (isAfternoon) hour = 15;
          else hour = 9;
        } else if (isAfternoon && hour < 12) hour += 12;
        else if (isEvening && hour <= 12) hour += 12;
        d.setHours(hour, 0, 0, 0);
        return d;
      }
    }
    const hm = text.match(/(\d{1,2})\s*(?:点|时)/);
    if (hm) { const d = new Date(); d.setHours(+hm[1], 0, 0, 0); return d; }
    return null;
  }

  function parseTodo(text) {
    const t = { title: text, category: '其他', priority: 'mid', due: null };
    const time = parseTime(text);
    if (time) t.due = toLocal(time);
    // 去除时间词
    let title = text
      .replace(/^(请|帮我|麻烦)(加(一个|一条)?|添加|加上|记(录|一下)|安排)/, '')
      .replace(/^(加(一个|一条)?|添加|加上|记录一下|记一下|安排|做一下|要去做|要做)/, '')
      .replace(/^(今天|明天|后天|明早|明晚|今晚|下周一|下周二|下周三|下周四|下周五|下周末|下个月|周末|周五|周六|周日|大后天)/, '')
      .replace(/(待办|的事|事情|任务)$/, '')
      .trim();
    // 类别
    for (const { w, c } of CATEGORY_WORDS) {
      if (title.includes(w)) { t.category = c; break; }
    }
    // 优先级
    for (const [w, p] of Object.entries(PRIORITY_WORDS)) {
      if (text.includes(w)) { t.priority = p; break; }
    }
    if (!title) title = text;
    t.title = title;
    return t;
  }

  function parsePassword(text) {
    // 匹配：存{平台}账号{账号}密码{密码}  或 {平台}的密码是{密码} / 密码{密码}
    const m = text.match(/存(?:一个)?(.+?)账号(.+?)密码(.+)|存(?:一个)?(.+?)密码(.+)/);
    if (m) {
      const title = (m[1] || m[4] || '认证').trim();
      const pwd = (m[3] || m[5] || '').trim().replace(/[。，、,.]$/, '');
      const account = m[2] ? m[2].trim().replace(/(是|为).*/, '').replace(/[。，、,.]$/, '') : '';
      return { title, account, password: pwd.replace(/^(是|为|就是)/, '') };
    }
    return null;
  }

  function toLocal(d) {
    const pad = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }

  /* ================= 识别 ================= */
  function initRec() {
    if (!SR) return false;
    rec = new SR();
    rec.lang = 'zh-CN';
    rec.interimResults = false;
    rec.maxAlternatives = 3;
    rec.continuous = false;
    rec.onresult = (e) => {
      const arr = [];
      for (let i = 0; i < e.results[0].length; i++) arr.push(e.results[0][i].transcript);
      const text = arr[0] || '';
      setWave(false); setListening(false, '松开即可');
      handleText(text);
    };
    rec.onerror = (e) => {
      setWave(false); setListening(false, '识别出错');
      if (e.error === 'not-allowed' || e.error === 'service-not-allowed') {
        UI_.toast('未授权麦克风，请在浏览器的站点权限中开启');
      } else if (e.error === 'network') {
        UI_.toast('语音识别需要网络（HTTPS/localhost 环境）');
      } else {
        UI_.toast('语音识别失败：' + e.error);
      }
    };
    rec.onend = () => { if (listening) { setListening(false, '已完成'); } };
    return true;
  }

  /* ================= 识别入口（优先用 Android 原生桥，其次 Web Speech） ================= */
  function nativeBridge() {
    return (window.BzNative && typeof window.BzNative.isAvailable === 'function' && window.BzNative.isAvailable())
      ? window.BzNative : null;
  }

  function start() {
    const nb = nativeBridge();
    if (nb) {
      if (!nb.hasPermission()) { setListening(false, '需要麦克风权限…'); nb.requestPermission(); return; }
      setListening(true, '正在聆听…');
      nb.startSpeech();
      return;
    }
    if (!SR) { UI_.toast('当前环境不支持语音识别'); return; }
    if (!rec && !initRec()) return;
    try {
      rec.start(); setListening(true, '正在聆听…');
    } catch (e) { /* 已在运行 */ }
  }

  function stop() {
    const nb = nativeBridge();
    if (nb) { nb.stopSpeech(); setListening(false, '已完成'); return; }
    if (rec) { try { rec.stop(); } catch (e) {} }
  }

  /* ---- 供 Android 壳回调 ---- */
  function onNativeState(state) {
    if (state === 'listening') setListening(true, '正在聆听…');
    else if (state === 'processing') setListening(true, '识别中…');
    else setListening(false, '按住说话，例如「加一个明天复习待办」或「存微信密码」');
  }
  function onNativeResult(text) {
    setListening(false, '已完成');
    handleText(text);
  }
  function onNativeError(msg) {
    setListening(false, '识别未成功');
    UI_.toast(msg || '语音识别失败');
  }
  function onNativePermission(ok) {
    if (!ok) UI_.toast('未授权麦克风，语音功能不可用');
  }

  function setListening(on, hint) {
    listening = on;
    $('voiceBall').classList.toggle('listening', on);
    if (hint) $('voiceHint').textContent = hint;
  }
  function setWave(on) {
    const w = $('voiceWave');
    if (on) w.innerHTML = '<canvas id="waveCanvas" width="300" height="26"></canvas>';
    else w.innerHTML = '';
  }

  /* 识别文本 -> 意图 -> 确认弹层 */
  function handleText(text) {
    if (!text) { UI_.toast('未识别到内容'); return; }
    log(text);
    let type = null, parsed = null;
    if (/存|记(录|下).*密码|密码(是|为)/.test(text)) {
      parsed = parsePassword(text);
      if (parsed) type = 'password';
    }
    if (!type && /加|添加|记|安排|做|买|复习|开会|写|跑|练|取|读/.test(text)) {
      parsed = parseTodo(text);
      type = 'todo';
    }
    if (!type) { // 兜底当待办
      parsed = parseTodo(text); type = 'todo';
    }
    pending = { type, parsed };
    $('vmText').textContent = text;
    let desc = '';
    if (type === 'todo') {
      desc = `→ 待办「${parsed.title}」` +
        (parsed.category !== '其他' ? ` · ${parsed.category}` : '') +
        (parsed.priority === 'high' ? ' · 高优' : '') +
        (parsed.due ? ` · ${UI_.fmtDate(new Date(parsed.due))}` : '（未排期）');
    } else {
      desc = `→ 密码「${parsed.title}」 ${parsed.account ? parsed.account + ' / ' : ''}${'•'.repeat(parsed.password.length || 4)}`;
    }
    $('vmType').textContent = desc;
    UI_.openModal('voiceModal');
  }

  function confirmPending() {
    if (!pending) return;
    if (pending.type === 'todo') {
      Todo.addByIntent(pending.parsed);
      UI_.toast('待办已入库');
    } else {
      (async () => {
        if (Store.isVaultLocked()) { UI_.toast('密码本已锁定，请先在密码本解锁'); return; }
        await Store.saveVault((await Store.getVault()).concat({
          id: Store.uid(), title: pending.parsed.title, account: pending.parsed.account,
          password: pending.parsed.password, url: '', note: '语音录入', source: 'voice', createdAt: Date.now(),
        }));
        Vault.reload();
        UI_.toast('密码已入库');
      })();
    }
    UI_.closeModal('voiceModal');
    pending = null;
  }

  function log(text) {
    const el = $('voiceLog');
    const line = document.createElement('div');
    line.textContent = new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }) + ' ' + text;
    el.prepend(line);
  }

  function init() {
    const ball = $('voiceBall');
    // 按住说话
    ball.addEventListener('pointerdown', (e) => { e.preventDefault(); start(); });
    ball.addEventListener('pointerup', () => stop());
    ball.addEventListener('pointercancel', () => stop());
    ball.addEventListener('pointerleave', () => stop());

    // 快捷标签
    document.querySelectorAll('.chip').forEach(c => c.addEventListener('click', () => handleText(c.dataset.phrase)));

    $('vmOk').addEventListener('click', confirmPending);
    $('vmCancel').addEventListener('click', () => { UI_.closeModal('voiceModal'); pending = null; });
    UI_.closeOnMask('voiceModal');

    // 识别结果自动触发通知栏
  }

  return {
    init, start, stop, parseTodo, parsePassword, parseTime, handleText,
    onNativeState, onNativeResult, onNativeError, onNativePermission,
  };
})();