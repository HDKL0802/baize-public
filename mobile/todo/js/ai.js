/* 白泽待办中心 - AI 接入层
   只走两家协议：OpenAI 兼容 / Anthropic，BaseURL 与模型全部由用户自填。
   关键点：这是一个「干活」的队列，不是聊天窗口 ——
     · 所有模型请求串行排队，避免并发抢占与限流；
     · 语音转文字由原生/浏览器负责，与模型请求互不阻塞；
     · 请求失败一律回落到本地规则，保证没网/没配 Key 也能用。 */
'use strict';
window.AI = (function () {
  const TIMEOUT_MS = 45000;

  /* ---------------- 串行任务队列 ---------------- */
  const queue = [];
  let busy = false;
  const statusListeners = [];

  function onStatus(fn) { statusListeners.push(fn); }
  function emit(state, info) {
    statusListeners.forEach(fn => { try { fn(state, info); } catch (e) { } });
  }

  function enqueue(label, job) {
    return new Promise((resolve, reject) => {
      queue.push({ label, job, resolve, reject });
      pump();
    });
  }

  async function pump() {
    if (busy) return;
    const item = queue.shift();
    if (!item) { emit('idle'); return; }
    busy = true;
    emit('working', { label: item.label, pending: queue.length });
    try {
      item.resolve(await item.job());
    } catch (e) {
      item.reject(e);
    }
    busy = false;
    if (queue.length) emit('working', { label: queue[0].label, pending: queue.length });
    else emit('idle');
    pump();
  }

  /* ---------------- 多模型配置 ----------------
     settings.ai = { models: [ {id,name,protocol,baseUrl,apiKey,model} ], activeId } */
  const PRESETS = {
    deepseek: { name: 'DeepSeek', protocol: 'openai', baseUrl: 'https://api.deepseek.com/v1', model: 'deepseek-chat' },
    openai:   { name: 'OpenAI 兼容', protocol: 'openai', baseUrl: 'https://api.openai.com/v1', model: 'gpt-4o-mini' },
    anthropic:{ name: 'Anthropic', protocol: 'anthropic', baseUrl: 'https://api.anthropic.com/v1', model: 'claude-3-5-haiku-latest' },
  };
  const DEFAULTS = PRESETS.deepseek;

  function blankModel(preset) {
    const p = Object.assign({}, preset || DEFAULTS);
    return {
      id: 'm' + Date.now().toString(36) + Math.random().toString(36).slice(2, 6),
      name: p.name, protocol: p.protocol, baseUrl: p.baseUrl, model: p.model, apiKey: '',
    };
  }

  /** 读取模型列表（兼容旧的单模型结构，并保证第一次进入就有一个 DeepSeek 预设） */
  function list() {
    const s = Store.getSettings();
    let ai = s.ai || {};
    let models = Array.isArray(ai.models) ? ai.models : null;
    if (!models) {
      // 迁移旧结构；没有则用 DeepSeek 预设初始化
      const legacy = (ai.baseUrl || ai.apiKey || ai.model) ? ai : null;
      const m = blankModel(DEFAULTS);
      if (legacy) {
        m.protocol = legacy.protocol || m.protocol;
        m.baseUrl = legacy.baseUrl || m.baseUrl;
        m.model = legacy.model || m.model;
        m.apiKey = legacy.apiKey || '';
      }
      models = [m];
      ai = { models, activeId: m.id };
      s.ai = ai;
      Store.saveSettings(s);
    }
    if (!ai.activeId || !models.some(x => x.id === ai.activeId)) {
      ai.activeId = models[0] ? models[0].id : null;
    }
    return models;
  }

  function saveList(models, activeId) {
    const s = Store.getSettings();
    s.ai = { models, activeId };
    Store.saveSettings(s);
  }

  function active() {
    const models = list();
    const s = Store.getSettings();
    return models.find(m => m.id === s.ai.activeId) || models[0] || blankModel(DEFAULTS);
  }

  function setActive(id) {
    const models = list();
    if (models.some(m => m.id === id)) saveList(models, id);
  }

  function add(presetKey) {
    const models = list();
    const m = blankModel(PRESETS[presetKey] || DEFAULTS);
    models.push(m);
    saveList(models, m.id);
    return m;
  }

  function update(id, patch) {
    const models = list();
    const m = models.find(x => x.id === id);
    if (m) Object.assign(m, patch);
    saveList(models, Store.getSettings().ai.activeId);
    return m;
  }

  function remove(id) {
    let models = list();
    if (models.length <= 1) return false;   // 至少留一个
    models = models.filter(m => m.id !== id);
    const cur = Store.getSettings().ai.activeId;
    saveList(models, cur === id ? models[0].id : cur);
    return true;
  }

  /** 取某个模型的配置；不传 id 就是当前生效的那个（其它模块沿用这个入口） */
  function cfg(id) {
    const models = list();
    const m = (id && models.find(x => x.id === id)) || active();
    return {
      id: m.id, name: m.name, protocol: m.protocol || 'openai',
      baseUrl: m.baseUrl || '', apiKey: m.apiKey || '', model: m.model || '',
    };
  }

  function isReady(c) { return !!(c.baseUrl && c.apiKey && c.model); }
  function ready() { return isReady(cfg()); }

  /* ---------------- 消息内容：支持纯文本与多模态（图片） ----------------
     内部统一用 OpenAI 的写法：[{type:'text',text},{type:'image_url',image_url:{url:dataUrl}}]
     Anthropic 那侧在发之前转成 [{type:'image',source:{type:'base64',...}}]。 */

  /** 取消息里的纯文本部分（任何一侧都用它做系统提示与摘要） */
  function textOf(content) {
    if (typeof content === 'string') return content;
    if (Array.isArray(content)) {
      return content.filter(p => p && p.type === 'text').map(p => p.text || '').join('\n');
    }
    return '';
  }

  function dataUrlParts(dataUrl) {
    const m = String(dataUrl).match(/^data:([^;]+);base64,(.*)$/);
    if (!m) return null;
    return { mime: m[1], data: m[2] };
  }

  function toAnthropicContent(content) {
    if (typeof content === 'string') return content;
    const out = [];
    (content || []).forEach(p => {
      if (!p) return;
      if (p.type === 'text') { if (p.text) out.push({ type: 'text', text: p.text }); return; }
      if (p.type === 'image_url') {
        const url = (p.image_url && p.image_url.url) || '';
        const d = dataUrlParts(url);
        if (d) out.push({ type: 'image', source: { type: 'base64', media_type: d.mime, data: d.data } });
        else if (url) out.push({ type: 'text', text: '[图片：' + url + ']' });
      }
    });
    return out.length ? out : '';
  }

  /* ---------------- 底层请求 ---------------- */
  async function request(messages, opts) {
    const o = opts || {};
    const c = cfg(o.modelId);           // 可以指定用哪个模型（群发时每个智能体各调一次）
    if (!isReady(c)) throw new Error('未配置 AI 接口（' + (c.name || '未命名') + '）');
    const base = c.baseUrl.replace(/\/+$/, '');
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), o.timeout || TIMEOUT_MS);

    let url, headers, body;
    if (c.protocol === 'anthropic') {
      url = base + '/messages';
      headers = {
        'Content-Type': 'application/json',
        'x-api-key': c.apiKey,
        'anthropic-version': '2023-06-01',
      };
      // 系统提示必须单独放 system 字段（Anthropic 不接受 role=system）
      const sys = messages.filter(m => m.role === 'system').map(m => textOf(m.content)).join('\n');
      const rest = messages.filter(m => m.role !== 'system')
        .map(m => ({ role: m.role === 'assistant' ? 'assistant' : 'user', content: toAnthropicContent(m.content) }));
      body = { model: c.model, max_tokens: o.maxTokens || 1200, messages: rest };
      if (sys) body.system = sys;
    } else {
      url = base + '/chat/completions';
      headers = { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + c.apiKey };
      body = {
        model: c.model,
        messages: messages.map(m => ({ role: m.role, content: m.content })),
        temperature: o.temperature === undefined ? 0.2 : o.temperature,
        max_tokens: o.maxTokens || 1200,
      };
    }

    let res;
    try {
      res = await fetch(url, { method: 'POST', headers, body: JSON.stringify(body), signal: controller.signal });
    } catch (e) {
      clearTimeout(timer);
      throw new Error(e.name === 'AbortError' ? 'AI 请求超时' : '网络错误（检查 BaseURL 或跨域）');
    }
    clearTimeout(timer);

    const text = await res.text();
    if (!res.ok) throw new Error('AI 返回 HTTP ' + res.status + '：' + text.slice(0, 120));

    let data;
    try { data = JSON.parse(text); } catch (e) { throw new Error('AI 返回不是合法 JSON'); }

    const out = extractText(data, c.protocol);
    if (o.raw) return { text: out, data, protocol: c.protocol };
    return out;
  }

  /** 取正文（正式对话内容）。注意：推理模型的「思考过程」不算正文 */
  function extractText(data, protocol) {
    if (protocol === 'anthropic') {
      return (data.content || []).filter(p => p.type === 'text').map(p => p.text).join('\n').trim();
    }
    const choice = (data.choices || [])[0];
    return String((choice && choice.message && choice.message.content) || '').trim();
  }

  /** 取推理模型的思考内容（只有 DeepSeek-R1 / o 系列这类才会返回） */
  function extractReasoning(data, protocol) {
    if (protocol === 'anthropic') {
      return (data.content || [])
        .filter(p => p.type === 'thinking' || p.type === 'reasoning')
        .map(p => p.thinking || p.text || '').join(' ').trim();
    }
    const choice = (data.choices || [])[0];
    return String((choice && choice.message && choice.message.reasoning_content) || '').trim();
  }

  /** 发起一次模型调用（自动排队） */
  function chat(messages, opts) {
    return enqueue('chat', () => request(messages, opts));
  }

  /** 指定模型、不进队列（群发用：几个智能体同时发，互不排队等对方） */
  function direct(messages, opts) {
    return request(messages, opts);
  }

  /* ---------------- 连通性测试 ----------------
     「通不通」和「有没有正文」是两件事，这里分开报，避免拿空响应当好结果。 */
  async function test() {
    return enqueue('test', async () => {
      const r = await request([
        { role: 'user', content: '只回复两个字：连通' },
      ], { maxTokens: 128, timeout: 20000, raw: true });
      if (r.text) return r.text;

      const choice = (r.data.choices || [])[0] || {};
      const finish = choice.finish_reason || '';
      const think = extractReasoning(r.data, r.protocol);
      const usage = r.data.usage || {};
      const tok = [];
      if (usage.prompt_tokens !== undefined) tok.push('输入 ' + usage.prompt_tokens + ' tokens');
      if (usage.completion_tokens !== undefined) tok.push('输出 ' + usage.completion_tokens + ' tokens');
      if (finish) tok.push('finish_reason=' + finish);
      const detail = tok.length ? '（' + tok.join('，') + '）' : '';

      if (think) {
        return '接口连通，但模型没给正文、只回了思考内容' + detail
          + '。正式用途（排期/语音解析）建议换成对话模型（如 deepseek-chat）';
      }
      throw new Error('接口能连上，但返回正文是空的' + detail
        + '。常见原因：max_tokens 被推理过程吃满 / 该模型不支持 chat 接口（视觉模型请确认支持文本输入）');
    });
  }

  /* ---------------- 从自由文本抽取 JSON ---------------- */
  function extractJson(text) {
    if (!text) return null;
    const fenced = text.match(/```(?:json)?\s*([\s\S]*?)```/i);
    const raw = fenced ? fenced[1] : text;
    const start = raw.indexOf('{');
    const arrStart = raw.indexOf('[');
    let from = start;
    if (start < 0 || (arrStart >= 0 && arrStart < start)) from = arrStart;
    if (from < 0) return null;
    const endObj = raw.lastIndexOf('}');
    const endArr = raw.lastIndexOf(']');
    const to = Math.max(endObj, endArr);
    if (to <= from) return null;
    try { return JSON.parse(raw.slice(from, to + 1)); } catch (e) { return null; }
  }

  /* ---------------- 业务：一句话 → 待办 ---------------- */
  const TODO_SCHEMA = `返回严格 JSON，不要任何解释。字段：
{"title":"任务标题","category":"工作开发|创作|日常生活","priority":"high|mid|low",
 "form":"schedule|leisure","due":"YYYY-MM-DDTHH:mm 或 空字符串","estimate":15|30|45|60|90|120|0,
 "weekly":true|false,"note":"简短备注或空字符串"}
规则：说了具体时间就 schedule 并填 due；没提时间就是 leisure 且 due 为空；estimate 只能取给定档位，判断不了就 0。`;

  async function parseTodoText(text, nowIso) {
    const out = await enqueue('parse', async () => {
      const reply = await request([
        { role: 'system', content: '你是任务录入助手，只输出 JSON。' },
        { role: 'user', content: `当前时间：${nowIso}\n把下面这句话转成待办：\n"""${text}"""\n\n${TODO_SCHEMA}` },
      ], { maxTokens: 400 });
      return extractJson(reply);
    });
    return out;
  }

  /* ---------------- 业务：给待办排顺序 ---------------- */
  async function planOrder(items, policyName) {
    if (!items.length) return null;
    const brief = items.map((t, i) => ({
      i,
      title: t.title,
      category: t.category,
      priority: t.priority,
      due: t.due || '',
      estimate: t.estimate || 0,
      deps: (t.deps || []).length,
      weekly: !!t.weekly,
    }));
    const out = await enqueue('plan', async () => {
      const reply = await request([
        { role: 'system', content: '你是日程规划助手，只输出 JSON 数组。' },
        { role: 'user', content: `当前时间：${new Date().toISOString()}\n策略偏好：${policyName}\n` +
          `请给出建议执行顺序，数组元素 {"i":原下标,"why":"不超过18字的理由"}，按先后排列。\n` +
          `任务：${JSON.stringify(brief)}` },
      ], { maxTokens: 900 });
      const arr = extractJson(reply);
      return Array.isArray(arr) ? arr : (arr && Array.isArray(arr.order) ? arr.order : null);
    });
    return out;
  }

  return {
    PRESETS, list, active, setActive, add, update, remove,
    cfg, isReady, ready, chat, direct, test, onStatus, enqueue, textOf,
    parseTodoText, planOrder, extractJson,
    isBusy: () => busy,
  };
})();
