/*
 * The app shell, cached so the till loads with no connection. Nothing else.
 *
 * This worker used to do considerably more and each extra job was a way to lose
 * a sale:
 *
 *   - It cached /api/v1/ responses network-first with a cache fallback. For a
 *     point of sale that means a stale catalogue or a stale stock figure served
 *     as if it were current, with nothing on screen to say so. The till now
 *     keeps its own snapshot in IndexedDB, stamped with when it was taken and
 *     shown to the cashier when it gets old — a cache cannot do that, because a
 *     cache has no way to tell the difference between "this is fine" and "this
 *     is three days out of date".
 *   - It ran a background sync that posted queued invoices. It read an
 *     IndexedDB the app never wrote to, and its token getter returned an empty
 *     string, so every sale it ever tried to sync was refused unauthenticated.
 *     Sync needs a session; the session lives in the app, and so now does sync.
 *
 * What is left is the one thing a service worker is actually for here: making
 * the application itself open when the network is down. A till that cannot load
 * its own HTML cannot bill, whatever else is in place.
 */

const SHELL = 'gbos-shell-v2';

// Requests that must always go to the network, and must never be cached.
function isApiRequest(url) {
  return url.pathname.startsWith('/api/');
}

self.addEventListener('install', (event) => {
  // Take over as soon as this version is installed. The alternative is a till
  // running last week's shell until every tab is closed, which in a shop means
  // until somebody reboots the machine.
  self.skipWaiting();
  event.waitUntil(
    caches.open(SHELL).then((cache) => cache.addAll(['/', '/index.html', '/manifest.json']).catch(() => undefined))
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((names) => Promise.all(names.filter((n) => n !== SHELL).map((n) => caches.delete(n))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (event) => {
  const request = event.request;
  if (request.method !== 'GET') return;

  const url = new URL(request.url);

  // Anything the shop's data goes through is the app's business, not this
  // worker's. Letting it fall through means an offline API call fails as an
  // offline API call, which is what the till is written to handle.
  if (isApiRequest(url)) return;
  if (url.origin !== self.location.origin) return;

  // A navigation offline has to land on the app rather than the browser's error
  // page, or the cashier has nothing at all.
  if (request.mode === 'navigate') {
    event.respondWith(
      fetch(request).catch(() => caches.match('/index.html').then((hit) => hit || Response.error()))
    );
    return;
  }

  // The build's own assets are content-hashed, so a cached one is never stale:
  // a changed file has a different name. Serve from cache when it is there and
  // fill the cache when it is not.
  event.respondWith(
    caches.match(request).then((hit) => {
      if (hit) return hit;
      return fetch(request)
        .then((response) => {
          if (response && response.status === 200 && response.type === 'basic') {
            const copy = response.clone();
            caches.open(SHELL).then((cache) => cache.put(request, copy));
          }
          return response;
        })
        .catch(() => hit || Response.error());
    })
  );
});
