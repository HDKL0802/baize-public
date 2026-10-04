/* 白泽待办中心 - Service Worker（在线取最新，离线回退缓存） */
'use strict';
const CACHE = 'baize-todo-v9';
const ASSETS = [
  './',
  './index.html',
  './css/app.css',
  './js/store.js',
  './js/device.js',
  './js/reminders.js',
  './js/app.js',
  './js/nlparse.js',
  './js/ai.js',
  './js/chat.js',
  './js/vault.js',
  './js/todo.js',
  './js/plan.js',
  './js/settings.js',
  './js/main.js',
  './icon.svg',
  './manifest.webmanifest',
];

self.addEventListener('install', (e) => {
  e.waitUntil(
    caches.open(CACHE).then(c => c.addAll(ASSETS)).then(() => self.skipWaiting())
  );
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches.keys()
      .then(keys => Promise.all(keys.filter(k => k !== CACHE).map(k => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

/* network-first：保证前端更新能立即生效；断网时回退到缓存副本 */
self.addEventListener('fetch', (e) => {
  if (e.request.method !== 'GET') return;
  const sameOrigin = e.request.url.startsWith(self.location.origin);
  e.respondWith(
    fetch(e.request)
      .then(res => {
        if (res.ok && sameOrigin) {
          const copy = res.clone();
          caches.open(CACHE).then(c => c.put(e.request, copy));
        }
        return res;
      })
      .catch(() => caches.match(e.request, { ignoreSearch: true }))
  );
});
