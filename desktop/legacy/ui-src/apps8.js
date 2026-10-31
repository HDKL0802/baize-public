/* 白泽桌面端 · 电脑文件（电脑控制第 1 档）
   本机文件浏览 + 一键整理进知识库。render 在文件末尾回填到 window.APP_BY_ID。

   口径（与后端设备侧一致）：
   - **所有读写都先过后端权限闸门**（本机 Go 侧 permGate）：档位 < 2（只读指定目录）时读不了文件；
     越界目录会被明确拒绝并告诉你去哪儿放开（设置 → 桌面控制）。界面把 permTitle 原样显示，不假装能读。
   - 走的是本机回环接口 /api/local/files/*，不经 NAS；配对令牌与会话令牌都不在这一层。
   - 「整理进知识库」= 本机读文件 → base64 → POST /api/be/api/kb/files（后端知识库附件）。 */

async function renderFiles(root) {
  root.innerHTML = `
    <div class="page">
      <h3>电脑文件</h3>
      <div class="sub">浏览这台电脑上的文件，挑几个<b>整理进知识库</b>（后端从 NAS 读不到你的磁盘，只能由桌面端读）。
        能读到哪儿由「设置 → 桌面控制」的权限决定。</div>
      <div class="bar">
        <button class="btn ghost" id="pfUp">↑ 上一层</button>
        <button class="btn ghost" id="pfHome">此电脑</button>
        <button class="btn ghost" id="pfReload">刷新</button>
        <span class="tag" id="pfPerm"></span>
        <span style="flex:1"></span>
        <button class="btn primary" id="pfImport">整理选中进知识库</button>
      </div>
      <div class="pre mono" id="pfPath" style="margin-top:8px">—</div>
      <div class="sub" id="pfMsg" style="margin:6px 0 8px"></div>
      <div id="pfList"></div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  let cur = '';                 // 当前目录（空 = 此电脑）
  const picked = new Map();     // path → entry

  const paintPerm = (perm, permTitle, scopes) => {
    const el = $i('pfPerm');
    if (perm == null) { el.textContent = ''; return; }
    const scopeTxt = (perm === 2 || perm === 3) && scopes && scopes.length
      ? '（' + scopes.join('、') + '）' : '';
    el.textContent = '权限：' + (permTitle || ('档位 ' + perm)) + scopeTxt;
    el.className = 'tag' + (perm >= 2 ? ' on' : ' warn');
  };

  const load = async (path) => {
    $i('pfMsg').textContent = '读取中…';
    $i('pfList').innerHTML = '';
    const r = await API.localGet('/files/list?path=' + encodeURIComponent(path || ''));
    paintPerm(r.perm, r.permTitle, r.scopes);
    if (!r.ok) {
      $i('pfMsg').innerHTML = '<span class="err">' + esc(r.error || '读不到') + '</span>';
      // 权限不够时给出「去哪儿改」的指引
      if (r.hint) $i('pfMsg').innerHTML += '<div class="sub" style="margin-top:4px">' + esc(r.hint) + '</div>';
      return;
    }
    cur = r.path || '';
    $i('pfPath').textContent = cur || '此电脑';
    $i('pfMsg').textContent = (r.entries || []).length ? '' : (r.hint || '这个目录是空的');
    if (r.truncated) $i('pfMsg').textContent = '（条目太多，只显示前 500 项）';
    picked.clear();

    const rows = [];
    if (!cur) {
      // 此电脑：盘符列表
      (r.roots || []).forEach(v => rows.push(fileRow({ name: v.name, path: v.path, isDir: true }, true)));
    } else {
      if (r.parent !== undefined) rows.push('<div class="row"><div class="who"><b>..</b></div><div class="tags"><button class="btn ghost sm" id="pfDotdot">进入</button></div></div>');
      (r.entries || []).forEach(e => {
        e.path = cur.replace(/[\\/]+$/, '') + '\\' + e.name;
        rows.push(fileRow(e, false));
      });
    }
    $i('pfList').innerHTML = rows.join('') || '<div class="empty">这里没有可显示的东西</div>';

    const dot = $i('pfDotdot');
    if (dot) dot.onclick = () => load(r.parent || '');

    // 目录：点名字进去；文件：勾选
    $i('pfList').querySelectorAll('[data-dir]').forEach(b => b.onclick = () => load(b.dataset.dir));
    $i('pfList').querySelectorAll('input[type=checkbox]').forEach(cb => cb.onchange = () => {
      if (cb.checked) picked.set(cb.dataset.path, { name: cb.dataset.name, path: cb.dataset.path });
      else picked.delete(cb.dataset.path);
      $i('pfMsg').textContent = picked.size ? ('已选 ' + picked.size + ' 个文件') : '';
    });
  };

  const fileRow = (e, isRoot) => {
    if (e.isDir) {
      return `<div class="row">
        <div class="who"><b>📁 ${esc(e.name)}</b><span class="mono">${esc(e.path || '')}</span></div>
        <div class="tags"><button class="btn ghost sm" data-dir="${esc(e.path)}">进入</button></div>
      </div>`;
    }
    return `<div class="row">
      <div class="who"><label class="sub" style="margin:0;display:flex;gap:8px;align-items:center">
        <input type="checkbox" data-path="${esc(e.path)}" data-name="${esc(e.name)}">
        <b>${esc(e.name)}</b></label>
        <span class="mono">${bytes(e.size)}${e.modTime ? ' · ' + fmtTime(e.modTime) : ''}</span></div>
      <div class="tags"><span class="sub" style="margin:0">${esc(e.path || '')}</span></div>
    </div>`;
  };

  const importPicked = async () => {
    if (!picked.size) { $i('pfMsg').innerHTML = '<span class="warn">先勾选要整理的文件</span>'; return; }
    const list = Array.from(picked.values());
    let ok = 0; const errs = [];
    for (let i = 0; i < list.length; i++) {
      const it = list[i];
      $i('pfMsg').textContent = '整理中 ' + (i + 1) + '/' + list.length + '：' + it.name;
      const fr = await API.localGet('/files/read?path=' + encodeURIComponent(it.path));
      if (!fr.ok) { errs.push(it.name + '：' + (fr.error || '读不到')); continue; }
      const up = await API.post('/api/kb/files', {
        name: fr.name, kind: fr.kind || 'file', mime: fr.mime || '', dataBase64: fr.dataBase64,
      });
      if (up.ok) ok++; else errs.push(it.name + '：' + (up.error || '上传失败'));
    }
    picked.clear();
    $i('pfList').querySelectorAll('input[type=checkbox]').forEach(cb => { cb.checked = false; });
    if (errs.length) {
      $i('pfMsg').innerHTML = `<span class="warn">已整理 ${ok} 个，${errs.length} 个没成：</span>` +
        '<div class="sub" style="margin-top:4px">' + errs.map(esc).join('<br>') + '</div>';
    } else {
      $i('pfMsg').innerHTML = `<span class="ok">已把 ${ok} 个文件整理进知识库（到「知识库 → 附件」里看）</span>`;
    }
  };

  $i('pfUp').onclick = async () => {
    if (!cur) { $i('pfMsg').textContent = '已经在「此电脑」了'; return; }
    const r = await API.localGet('/files/list?path=' + encodeURIComponent(cur));
    load(r.parent || '');
  };
  $i('pfHome').onclick = () => load('');
  $i('pfReload').onclick = () => load(cur);
  $i('pfImport').onclick = importPicked;

  await load('');
}

/* 回填注册表（同 apps2.js 末尾的说明：不能写 render: renderXxx 前向引用） */
(function wireFilesApp() {
  const app = window.APP_BY_ID && window.APP_BY_ID['files'];
  if (app) app.render = renderFiles;
})();
