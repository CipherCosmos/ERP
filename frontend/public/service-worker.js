// GBOS ERP - Service Worker
// Provides offline-first caching for the app shell and API responses.
// Includes background sync for pending invoice submissions.

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

// ─── Background Sync ──────────────────────────────────────────────────────────
self.addEventListener('sync', (event) => {
  if (event.tag === 'sync-pending-invoices') {
    console.log('[SW] Background sync triggered for pending invoices');
    event.waitUntil(syncPendingInvoices());
  }
});

async function syncPendingInvoices() {
  try {
    // Open IndexedDB to get pending invoices
    const db = await openIndexedDB();
    const pendingInvoices = await getPendingInvoicesFromDB(db);

    if (pendingInvoices.length === 0) {
      console.log('[SW] No pending invoices to sync');
      return;
    }

    console.log('[SW] Syncing', pendingInvoices.length, 'pending invoices');

    // Try to sync each pending invoice
    for (const invoice of pendingInvoices) {
      try {
        const response = await fetch('/api/v1/invoices', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'Authorization': 'Bearer ' + (await getTokenFromStorage()),
            'X-Store-ID': await getStoreIdFromStorage(),
          },
          body: JSON.stringify(invoice.data),
        });

        if (response.ok) {
          // Mark as synced in IndexedDB
          await markInvoiceAsSynced(db, invoice.id);
          console.log('[SW] Synced invoice:', invoice.id);

          // Notify all clients that sync succeeded
          notifyClients({
            type: 'SYNC_SUCCESS',
            invoiceId: invoice.id,
          });
        }
      } catch (error) {
        console.error('[SW] Failed to sync invoice:', invoice.id, error);
      }
    }
  } catch (error) {
    console.error('[SW] Background sync error:', error);
    throw error; // Re-throw to retry later
  }
}

function openIndexedDB() {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open('gbos-offline', 1);
    request.onerror = () => reject(request.error);
    request.onsuccess = () => resolve(request.result);
    request.onupgradeneeded = (event) => {
      const db = event.target.result;
      if (!db.objectStoreNames.contains('pending-invoices')) {
        db.createObjectStore('pending-invoices', { keyPath: 'id' });
      }
    };
  });
}

function getPendingInvoicesFromDB(db) {
  return new Promise((resolve, reject) => {
    const tx = db.transaction('pending-invoices', 'readonly');
    const store = tx.objectStore('pending-invoices');
    const request = store.getAll();
    request.onerror = () => reject(request.error);
    request.onsuccess = () => resolve(request.result);
  });
}

function markInvoiceAsSynced(db, invoiceId) {
  return new Promise((resolve, reject) => {
    const tx = db.transaction('pending-invoices', 'readwrite');
    const store = tx.objectStore('pending-invoices');
    const request = store.delete(invoiceId);
    request.onerror = () => reject(request.error);
    request.onsuccess = () => resolve();
  });
}

async function getTokenFromStorage() {
  return new Promise((resolve) => {
    const clients = self.clients.matchAll();
    clients.then((clientList) => {
      if (clientList.length > 0) {
        clientList[0].postMessage({ type: 'GET_TOKEN' });
        self.onmessage = (event) => {
          if (event.data.type === 'TOKEN') {
            resolve(event.data.token);
          }
        };
      }
      resolve('');
    });
  });
}

async function getStoreIdFromStorage() {
  return new Promise((resolve) => {
    const clients = self.clients.matchAll();
    clients.then((clientList) => {
      if (clientList.length > 0) {
        clientList[0].postMessage({ type: 'GET_STORE_ID' });
        self.onmessage = (event) => {
          if (event.data.type === 'STORE_ID') {
            resolve(event.data.storeId);
          }
        };
      }
      resolve('');
    });
  });
}

function notifyClients(message) {
  self.clients.matchAll().then((clientList) => {
    clientList.forEach((client) => {
      client.postMessage(message);
    });
  });
}

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
