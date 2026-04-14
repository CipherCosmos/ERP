// GBOS ERP - Service Worker
// Provides offline-first caching for the app shell and API responses.

const CACHE_VERSION = 'v1';
const STATIC_CACHE = 'gbos-static-' + CACHE_VERSION;
const API_CACHE = 'gbos-api-' + CACHE_VERSION;

// App shell files to pre-cache
const APP_SHELL = [
  '/',
  '/index.html',
  '/manifest.json',
];

// ─── Install ───────────────────────────────────────────────────────────────────
self.addEventListener('install', (event) => {
  console.log('[SW] Installing service worker', CACHE_VERSION);
  event.waitUntil(
    caches
      .open(STATIC_CACHE)
      .then((cache) => {
        console.log('[SW] Pre-caching app shell');
        return cache.addAll(APP_SHELL);
      })
      .then(() => {
        // Skip waiting so the new SW activates immediately
        return self.skipWaiting();
      })
  );
});

// ─── Activate ──────────────────────────────────────────────────────────────────
self.addEventListener('activate', (event) => {
  console.log('[SW] Activating service worker', CACHE_VERSION);
  event.waitUntil(
    caches
      .keys()
      .then((cacheNames) => {
        return Promise.all(
          cacheNames
            .filter((name) => {
              // Delete old caches that don't match current version
              return (
                (name.startsWith('gbos-static-') && name !== STATIC_CACHE) ||
                (name.startsWith('gbos-api-') && name !== API_CACHE)
              );
            })
            .map((name) => {
              console.log('[SW] Deleting old cache:', name);
              return caches.delete(name);
            })
        );
      })
      .then(() => {
        // Claim all clients immediately so the SW controls the page right away
        return self.clients.claim();
      })
  );
});

// ─── Fetch ─────────────────────────────────────────────────────────────────────
self.addEventListener('fetch', (event) => {
  const { request } = event;
  const url = new URL(request.url);

  // Only handle GET requests
  if (request.method !== 'GET') {
    return;
  }

  // Strategy: API requests (/api/v1/) -> network-first with cache fallback
  if (url.pathname.startsWith('/api/v1/') || url.pathname.startsWith('/api/')) {
    event.respondWith(networkFirstStrategy(request));
    return;
  }

  // Strategy: Static assets (JS, CSS, images, fonts) -> cache-first
  if (isStaticAsset(url.pathname)) {
    event.respondWith(cacheFirstStrategy(request));
    return;
  }

  // Strategy: Navigation requests (HTML) -> cache-first with network fallback
  if (request.mode === 'navigate') {
    event.respondWith(cacheFirstStrategy(request, '/index.html'));
    return;
  }

  // Default: cache-first for everything else
  event.respondWith(cacheFirstStrategy(request));
});

// ─── Strategies ────────────────────────────────────────────────────────────────

/**
 * Network-first strategy: try the network, fall back to cache.
 * Used for API requests so we always get fresh data when online.
 */
function networkFirstStrategy(request) {
  return fetch(request)
    .then((response) => {
      // Only cache successful responses
      if (response.ok) {
        const responseClone = response.clone();
        caches.open(API_CACHE).then((cache) => {
          cache.put(request, responseClone);
        });
      }
      return response;
    })
    .catch(() => {
      // Network failed -> try cache
      return caches.match(request).then((cachedResponse) => {
        if (cachedResponse) {
          return cachedResponse;
        }
        // Nothing in cache either - return a JSON offline response
        return new Response(
          JSON.stringify({
            error: 'offline',
            message: 'You are offline and this data is not cached.',
          }),
          {
            status: 503,
            headers: { 'Content-Type': 'application/json' },
          }
        );
      });
    });
}

/**
 * Cache-first strategy: try the cache, fall back to network.
 * Used for static assets (JS, CSS, images) that rarely change.
 * @param {Request} request
 * @param {string} [fallbackUrl] - Optional URL to fall back to in cache (e.g., '/index.html' for SPA navigation)
 */
function cacheFirstStrategy(request, fallbackUrl) {
  return caches.match(request).then((cachedResponse) => {
    if (cachedResponse) {
      return cachedResponse;
    }

    return fetch(request)
      .then((response) => {
        if (response.ok) {
          const responseClone = response.clone();
          caches.open(STATIC_CACHE).then((cache) => {
            cache.put(request, responseClone);
          });
        }
        return response;
      })
      .catch(() => {
        // If a fallback URL is provided, try to serve that from cache
        if (fallbackUrl) {
          return caches.match(fallbackUrl);
        }
        return new Response('Offline', { status: 503 });
      });
  });
}

/**
 * Check if a URL path refers to a static asset.
 */
function isStaticAsset(pathname) {
  return /\.(js|css|png|jpg|jpeg|gif|svg|ico|woff|woff2|ttf|eot|json)$/i.test(
    pathname
  );
}

// ─── Messages ──────────────────────────────────────────────────────────────────
self.addEventListener('message', (event) => {
  if (event.data && event.data.type === 'SKIP_WAITING') {
    self.skipWaiting();
  }
});
