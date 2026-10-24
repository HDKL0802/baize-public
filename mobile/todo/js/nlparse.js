/* 白泽待办中心 - 自然语言解析（纯逻辑，无 DOM 依赖）
   语音页与桌面小组件快速录入页共用这一份规则，避免两处实现漂移。
   规则（后续由后端 Agent 意图接管）：
   - 待办：加一个{时间}{修饰}{内容}   /   {时间}要做{内容}  /  记录：{内容}
   - 密码：存{平台}账号...密码...  /  存{平台}密码 */
'use strict';
window.NL = (function () {
  const TIME_WORDS = {
    '今天': 0, '今晚': 0, '今天早上': 0, '明天': 1, '明早': 1, '明天早上': 1, '后天': 2,
    '大后天': 3, '下周一': 'nextMonday', '下星期二': 'nextTue', '下周三': 'nextWed',
    '周末': 'weekend', '下周末': 'nextweekend', '下个月': 'nextMonth', '周五': 'thisFri',
    '星期六': 'thisSat', '周日': 'thisSun',
  };
  const PRIORITY_WORDS = { '紧急': 'high', '马上': 'high', '优先': 'high', '高优': 'high', '尽快': 'high' };
  const CATEGORY_WORDS = [
    { w: '工作', c: '工作开发' }, { w: '开会', c: '工作开发' }, { w: '会议', c: '工作开发' }, { w: '项目', c: '工作开发' },
    { w: '复习', c: '工作开发' }, { w: '学习', c: '工作开发' }, { w: '读书', c: '日常生活' }, { w: '作业', c: '工作开发' }, { w: '背单词', c: '工作开发' }, { w: '考试', c: '工作开发' },
    { w: '买', c: '日常生活' }, { w: '做饭', c: '日常生活' }, { w: '打扫', c: '日常生活' }, { w: '洗衣', c: '日常生活' }, { w: '取', c: '日常生活' }, { w: '交', c: '日常生活' },
    { w: '写', c: '创作' }, { w: '画', c: '创作' }, { w: '剪', c: '创作' }, { w: '拍', c: '创作' },
    { w: '跑', c: '日常生活' }, { w: '练', c: '日常生活' }, { w: '健身', c: '日常生活' }, { w: '瑜伽', c: '日常生活' },
  ];

  function toLocal(d) {
    const pad = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }

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
    const t = { title: text, category: '日常生活', priority: 'mid', due: null };
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

  /** 从一句话里抽密码条目。兼容多种说法，抽不到密码值就返回 null（由上层明确报错，不许静默降级）：
      存 GitHub 账号 me@a.com 密码 123456
      帮我添加一个 GitHub 的密码（名字不对，密码为 123456）
      GitHub 的密码是 123456
      -------------- */
  function parsePassword(text) {
    const raw = String(text || '');
    const NOISE = '[^\\s，。；、）)(（:：=＝】》]+';
    const val = (re) => {
      const m = raw.match(re);
      return m ? m[1].trim().replace(/[」』"\'，。；、,.]+$/, '') : '';
    };
    const password = val(new RegExp('密码\\s*(?:是|为|＝|=|:|：)?\\s*(' + NOISE + ')'));
    if (!password) return null;
    // 抽出来的"值"其实是动词短语（例如「密码存进密码本」）→ 当作没听出密码
    if (/密码|口令|账号|帐号|密码本/.test(password)) return null;
    // 密码得像个密码（至少含一个字母或数字），否则多半是把「密码是啥」这类话当成了值
    if (!/[A-Za-z0-9]/.test(password)) return null;
    const account = val(new RegExp('(?:账号|帐号|用户名)\\s*(?:是|为|＝|=|:|：)?\\s*(?!密码|口令)(' + NOISE + ')'));
    let title = raw
      .replace(/[（(][^）)]*[）)]/g, ' ')                 // 括号里多是补充说明
      .split(password).join(' ');                        // 别把密码当成平台名的一部分
    if (account) title = title.split(account).join(' ');
    title = title
      .replace(/让\s*AI/gi, ' ')
      .replace(/(请|帮我|给我|麻烦|帮忙)/g, ' ')
      .replace(/(添加|新增|保存|写入|录入|记录|记下|记|存|加)(一个|一条|个|条)?/g, ' ')
      .replace(/密码|口令|账号|帐号|用户名/g, ' ')
      .replace(/[的是为了吧啊呢，。；、：:,=＝\s]+/g, ' ')
      .trim()
      .replace(/^[的\s]+|[的\s]+$/g, '');
    return { title: title || '认证', account, password };
  }

  /** 解析密码本 Markdown（与 App 导出格式一致，# 站点 + - 账号/- 密码/- URL/- 备注 行）
      用于「@一个文件 / 传一个文件，让它把这些密码存进去」 */
  function parseVaultMd(text) {
    const items = [];
    let cur = null;
    String(text || '').split(/\r?\n/).forEach((line) => {
      if (/^#{1,6}\s+/.test(line)) {
        if (cur && (cur.account || cur.password)) items.push(cur);
        const title = line.replace(/^#{1,6}\s+/, '').trim();
        // 分节标题（## 待办 / ## 密码 / # 白泽待办中心）不是条目
        cur = /^(待办|密码|密码本|白泽|数据中心|导出)/.test(title)
          ? null : { title, account: '', password: '', url: '', note: '' };
        return;
      }
      if (!cur) return;
      const g = (re) => { const m = line.match(re); return m ? m[1].trim() : ''; };
      const a = g(/^[-\*]\s*账号[:：]\s*(.+)/); if (a) { cur.account = a; return; }
      const p = g(/^[-\*]\s*密码[:：]\s*(.+)/); if (p) { cur.password = p; return; }
      const u = g(/^[-\*]\s*URL[:：]\s*(.+)/i); if (u) { cur.url = u; return; }
      const n = g(/^[-\*]\s*备注[:：]\s*(.+)/); if (n) { cur.note = n; }
    });
    if (cur && (cur.account || cur.password)) items.push(cur);
    return items.filter(x => x.title && (x.account || x.password));
  }

  return { parseTime, parseTodo, parsePassword, parseVaultMd, toLocal, CATEGORY_WORDS };
})();
