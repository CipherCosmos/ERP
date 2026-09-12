// Service Worker Registration for GBOS ERP
// Registers the service worker in production by default.
// Pass { enableInDev: true } to also register in development mode.

type Config = {
  enableInDev?: boolean;
  onSuccess?: (registration: ServiceWorkerRegistration) => void;
  onUpdate?: (registration: ServiceWorkerRegistration) => void;
  onOffline?: () => void;
};

const isLocalhost = Boolean(
  window.location.hostname === 'localhost' ||
    window.location.hostname === '[::1]' ||
    // 127.0.0.0/8 range
    /^127(?:\.(?:25[0-5]|2[0-4]\d|[01]?\d\d?)){3}$/.test(
      window.location.hostname
    )
);

function registerValidSW(swUrl: string, config?: Config) {
  navigator.serviceWorker
    .register(swUrl)
    .then((registration) => {
      registration.onupdatefound = () => {
        const installingWorker = registration.installing;
        if (installingWorker == null) {
          return;
        }
        installingWorker.onstatechange = () => {
          if (installingWorker.state === 'installed') {
            if (navigator.serviceWorker.controller) {
              // New content is available; the old service worker has been replaced.
              console.log(
                'GBOS: New content is available and will be used when all tabs are closed.'
              );
              if (config?.onUpdate) {
                config.onUpdate(registration);
              }
            } else {
              // Content is cached for the first time (offline use).
              console.log('GBOS: Content is cached for offline use.');
              if (config?.onSuccess) {
                config.onSuccess(registration);
              }
            }
          }
        };
      };
    })
    .catch((error) => {
      console.error('Error during service worker registration:', error);
    });
}

function checkValidServiceWorker(swUrl: string, config?: Config) {
  // Check if the service worker can be found. If it can't reload the page.
  fetch(swUrl, { headers: { 'Service-Worker': 'script' } })
    .then((response) => {
      const contentType = response.headers.get('content-type');
      if (
        response.status === 404 ||
        (contentType != null && contentType.indexOf('javascript') === -1)
      ) {
        // No service worker found. Probably a different app, or a deploy that
        // did not publish one. Unregister and reload ONCE.
        //
        // Once matters. Without the guard this is a loop: the reload runs the
        // same check, which fails the same way, which reloads. A till in that
        // state hammers the server until somebody closes the tab — and it is
        // how this one exhausted its own rate limit, after which every request
        // came back 429 and the browser reported it as a CORS failure, which
        // says nothing about either.
        const RELOADED = 'gbos-sw-reloaded';
        let alreadyTried = true;
        try {
          alreadyTried = sessionStorage.getItem(RELOADED) === '1';
          sessionStorage.setItem(RELOADED, '1');
        } catch {
          // Private browsing, or storage disabled. Assume we have tried, which
          // errs towards not reloading: a missing service worker costs offline
          // loading, and a reload loop costs the shop its till.
        }
        navigator.serviceWorker.ready.then((registration) => {
          registration.unregister().then(() => {
            if (!alreadyTried) window.location.reload();
          });
        });
      } else {
        // Service worker found. Proceed as normal.
        registerValidSW(swUrl, config);
      }
    })
    .catch(() => {
      console.log(
        'GBOS: No internet connection found. App is running in offline mode.'
      );
      if (config?.onOffline) {
        config.onOffline();
      }
    });
}

export function register(config?: Config) {
  const isProduction = process.env.NODE_ENV === 'production';
  const enableInDev = config?.enableInDev === true;

  if (!('serviceWorker' in navigator)) {
    console.log('GBOS: Service workers are not supported in this browser.');
    return;
  }

  if (!isProduction && !enableInDev) {
    console.log(
      'GBOS: Service worker registration skipped in development mode. ' +
        'Pass { enableInDev: true } to register() to enable.'
    );
    return;
  }

  window.addEventListener('load', () => {
    const swUrl = `${process.env.PUBLIC_URL || ''}/service-worker.js`;

    if (isLocalhost) {
      // Running on localhost. Check if a service worker still exists or not.
      checkValidServiceWorker(swUrl, config);

      navigator.serviceWorker.ready.then(() => {
        console.log(
          'GBOS: This web app is being served cache-first by a service worker.'
        );
      });
    } else {
      // Not localhost. Just register the service worker.
      registerValidSW(swUrl, config);
    }
  });

  // Listen for online/offline events and notify via config callback
  window.addEventListener('offline', () => {
    console.log('GBOS: Browser went offline.');
    if (config?.onOffline) {
      config.onOffline();
    }
  });

  window.addEventListener('online', () => {
    console.log('GBOS: Browser came back online.');
  });
}

export function unregister() {
  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.ready
      .then((registration) => {
        registration.unregister();
      })
      .catch((error) => {
        console.error(error.message);
      });
  }
}
