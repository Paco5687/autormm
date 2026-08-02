// autormm service worker.
//
// Deliberately caches NOTHING. Its only job is to satisfy the installability
// requirement so the dashboard can be installed as an app. The hub updates
// itself often and the viewer/dashboard code is served straight from the
// binary, so caching app code here would strand users on a stale build — a
// problem we have already hit with ordinary browser caching.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

self.addEventListener('fetch', (e) => {
  // Pass through to the network. The catch only exists so an offline hub gives
  // a readable message instead of the browser's error page.
  e.respondWith(
    fetch(e.request).catch(() =>
      new Response('autormm hub unreachable — check the connection and reload.', {
        status: 503,
        headers: { 'Content-Type': 'text/plain' },
      })
    )
  );
});
