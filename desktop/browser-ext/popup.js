// 白泽浏览器控制 · 弹窗：保存本机地址与令牌，让 background 重连。
const $ = id => document.getElementById(id);

async function load() {
  const st = await chrome.storage.local.get(['wsUrl', 'token', 'status']);
  $('addr').value = st.wsUrl || 'ws://127.0.0.1:47121/ext/ws';
  $('token').value = st.token || '';
  paint(st.status || '');
}

function paint(status) {
  const el = $('state');
  el.className = '';
  if (!status) { el.textContent = '未连接'; return; }
  if (status === 'ok') { el.className = 'ok'; el.textContent = '已连接白泽桌面端'; return; }
  el.className = 'err';
  el.textContent = '连接失败：' + status;
}

$('save').onclick = async () => {
  const wsUrl = ($('addr').value || '').trim();
  const token = ($('token').value || '').trim();
  if (!wsUrl || !token) { paint('地址和令牌都要填'); return; }
  await chrome.storage.local.set({ wsUrl: wsUrl, token: token, status: '' });
  chrome.runtime.sendMessage({ type: 'reconnect' }, () => {
    // 给 background 一点时间连上，再回读状态
    setTimeout(async () => {
      const st = await chrome.storage.local.get(['status']);
      paint(st.status || 'ok');
    }, 900);
  });
};

chrome.storage.onChanged.addListener((changes) => {
  if (changes.status) paint(changes.status.newValue || '');
});

load();
