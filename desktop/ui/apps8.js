/* 白泽桌面端 · 截图提问（桌面端独有：抓屏 / 框选，浏览器做不到）
   抓屏与框选在原生侧（shot_windows.go），图片落到本机服务；界面在这里读图并做事：
   复制（CF_DIB 进系统剪贴板）/ 提取文字 / 翻译（中英双语）。

   数据：GET /api/local/shot/latest → {ok,w,h,at,sizeBytes,imageBase64}
         GET /api/local/shot/image?t=<at> → PNG 原图（给 <img> 直接显示）
   动作：POST /api/local/shot/capture（触发框选，异步；结果靠 at 变化判断）
         POST /api/local/shot/copy（写剪贴板）
         POST /api/agent/vision {action:extract|translate, lang, imageBase64}（后端多模态模型）
   看图要一条多模态通道：模型通道里 kinds 含 vision，或任意支持图片的模型。 */
'use strict';

async function renderShot(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <h3>截图提问</h3>
      <div class="sub">
        拖框选中屏幕上一块，回来<b>复制 / 提取文字 / 翻译</b>；也可以在悬浮球右键点「截图提问」。
        看图走多模态模型（模型通道里把 <span class="mono">kinds</span> 标成
        <span class="mono">vision</span>，或任意支持图片的模型，如 qwen-vl-max / gpt-4o）。
      </div>

      <div style="display:flex;gap:8px;align-items:center;margin:12px 0;flex-wrap:wrap">
        <button class="btn" id="shotNew">截图（框选）</button>
        <button class="btn ghost" id="shotCopy" disabled>复制图片</button>
        <span class="sub" id="shotMeta" style="margin:0"></span>
      </div>

      <div class="sect" style="margin-top:6px">
        <div id="shotPreview" class="empty">还没有截图。点「截图（框选）」，或悬浮球右键「截图提问」。</div>
      </div>

      <div class="sect">
        <h3>看图做事</h3>
        <div class="fields" style="grid-template-columns:1fr 160px 1fr">
          <div style="display:flex;gap:8px;align-items:flex-end">
            <button class="btn" id="shotExtract" disabled>提取文字</button>
            <button class="btn" id="shotTranslate" disabled>翻译</button>
          </div>
          <div><label>翻译目标语言</label>
            <select id="shotLang"><option value="zh">中文</option><option value="en">英文</option></select></div>
          <div style="display:flex;align-items:flex-end">
            <button class="btn ghost" id="shotCopyText" hidden>复制文字</button>
          </div>
        </div>
        <div id="shotResult" class="pre" style="min-height:64px;margin-top:10px;white-space:pre-wrap"></div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  let lastAt = 0;
  let latest = null; // 最近一次 /latest 的返回（含 imageBase64）
  let capturing = false;

  const setButtons = () => {
    const has = !!latest;
    $i('shotCopy').disabled = !has;
    $i('shotExtract').disabled = !has;
    $i('shotTranslate').disabled = !has;
  };

  const showImage = () => {
    if (!latest) {
      $i('shotPreview').className = 'empty';
      $i('shotPreview').textContent = '还没有截图。点「截图（框选）」，或悬浮球右键「截图提问」。';
      $i('shotMeta').textContent = '';
      return;
    }
    $i('shotPreview').className = '';
    $i('shotPreview').innerHTML =
      `<img alt="截图" src="/api/local/shot/image?t=${latest.at}" style="max-width:100%;border:1px solid var(--border-strong);display:block">`;
    $i('shotMeta').textContent = latest.w + '×' + latest.h + ' · ' +
      bytes(latest.sizeBytes || 0) + ' · ' + fmtTime(latest.at);
  };

  // refresh 汇报"有没有新图"（at 变了）；新图来了就顺带清掉上一次的识别结果
  const refresh = async () => {
    const r = await fetch('/api/local/shot/latest').then(x => x.json()).catch(() => null);
    if (!r || !r.ok) return false;
    if (r.at === lastAt) return false;
    lastAt = r.at;
    latest = r;
    showImage();
    setButtons();
    $i('shotResult').textContent = '';
    $i('shotCopyText').hidden = true;
    return true;
  };

  // 触发截图：发指令后轮询，等 at 变化就刷新预览（有人会慢慢选，给 60 秒）
  const capture = async () => {
    if (capturing) return;
    capturing = true;
    $i('shotNew').disabled = true;
    const r = await fetch('/api/local/shot/capture', { method: 'POST' }).then(x => x.json()).catch(() => null);
    if (!r || !r.ok) {
      Shell.toast('启动截图失败：' + ((r && r.error) || '未知错误'), 'err');
      capturing = false; $i('shotNew').disabled = false;
      return;
    }
    Shell.toast('拖动鼠标框选；按 Esc 取消', 'ok');
    const start = Date.now();
    const timer = setInterval(async () => {
      if (await refresh()) { done(); return; }
      if (Date.now() - start > 60000) done();
    }, 800);
    const done = () => {
      clearInterval(timer);
      capturing = false;
      const b = $i('shotNew'); if (b) b.disabled = false;
    };
  };

  const copyImage = async () => {
    const r = await fetch('/api/local/shot/copy', { method: 'POST' }).then(x => x.json()).catch(() => null);
    if (r && r.ok) Shell.toast('已复制到剪贴板', 'ok');
    else Shell.toast('复制失败：' + ((r && r.error) || '未知错误'), 'err');
  };

  const askVision = async (action) => {
    if (!latest) { Shell.toast('先截一张图', 'err'); return; }
    const btn = action === 'translate' ? $i('shotTranslate') : $i('shotExtract');
    btn.disabled = true;
    $i('shotResult').textContent = action === 'translate' ? '翻译中…（可能几秒）' : '识别中…（可能几秒）';
    const body = { action: action, imageBase64: latest.imageBase64 };
    if (action === 'translate') body.lang = $i('shotLang').value;
    const r = await API.post('/api/agent/vision', body);
    btn.disabled = false;
    if (!r.ok) {
      $i('shotResult').innerHTML = '<span class="err">失败：' + esc(r.error || '未知错误') + '</span>';
      return;
    }
    const d = r.data || {};
    $i('shotResult').textContent = d.text || '（没有识别到内容）';
    $i('shotCopyText').hidden = false;
    if (d.model) Shell.toast('模型 ' + d.model + ' · ' + (d.latencyMs || 0) + ' ms', 'ok');
  };

  $i('shotNew').onclick = capture;
  $i('shotCopy').onclick = copyImage;
  $i('shotExtract').onclick = () => askVision('extract');
  $i('shotTranslate').onclick = () => askVision('translate');
  $i('shotCopyText').onclick = async () => {
    const t = $i('shotResult').textContent || '';
    if (!t) return;
    try { await navigator.clipboard.writeText(t); Shell.toast('文字已复制', 'ok'); }
    catch (e) { Shell.toast('复制失败：' + (e && e.message), 'err'); }
  };

  await refresh();
  setButtons();
  // 悬浮球截完图会直接导航到这里；这条轮询再兜一层（也覆盖别处截的新图）
  pollWhileMounted(root, refresh, 3000);
}

/* ================= 回填注册表 ================= */
(function wireShot() {
  const app = window.APP_BY_ID && window.APP_BY_ID.shot;
  if (app) app.render = renderShot;
})();
