import { useEffect, useState } from 'react';

const DB_NAME = 'gbos-offline';
const STORE_NAME = 'app-data';
const SYNC_TIME_KEY = 'last-sync-time';
const SYNC_INTERVAL = 60 * 60 * 1000; // 1 hour

let db: IDBDatabase | null = null;

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    if (db) {
      resolve(db);
      return;
    }

    const request = indexedDB.open(DB_NAME, 1);
    request.onerror = () => reject(request.error);
    request.onsuccess = () => {
      db = request.result;
      resolve(db);
    };
    request.onupgradeneeded = (event: IDBVersionChangeEvent) => {
      const database = (event.target as IDBOpenDBRequest).result;
      if (!database.objectStoreNames.contains(STORE_NAME)) {
        database.createObjectStore(STORE_NAME, { keyPath: 'key' });
      }
    };
  });
}

async function saveToIndexedDB(key: string, data: unknown): Promise<void> {
  const database = await openDB();
  return new Promise((resolve, reject) => {
    const tx = database.transaction(STORE_NAME, 'readwrite');
    const store = tx.objectStore(STORE_NAME);
    const request = store.put({
      key,
      data,
      timestamp: Date.now(),
    });
    request.onerror = () => reject(request.error);
    request.onsuccess = () => resolve();
  });
}

async function getFromIndexedDB(key: string): Promise<unknown | null> {
  const database = await openDB();
  return new Promise((resolve, reject) => {
    const tx = database.transaction(STORE_NAME, 'readonly');
    const store = tx.objectStore(STORE_NAME);
    const request = store.get(key);
    request.onerror = () => reject(request.error);
    request.onsuccess = () => {
      const result = request.result;
      resolve(result ? result.data : null);
    };
  });
}

async function getSyncTime(): Promise<number> {
  const database = await openDB();
  return new Promise((resolve) => {
    const tx = database.transaction(STORE_NAME, 'readonly');
    const store = tx.objectStore(STORE_NAME);
    const request = store.get(SYNC_TIME_KEY);
    request.onerror = () => resolve(0);
    request.onsuccess = () => {
      const result = request.result;
      resolve(result ? result.data : 0);
    };
  });
}

async function setSyncTime(time: number): Promise<void> {
  await saveToIndexedDB(SYNC_TIME_KEY, time);
}

interface UseOfflineDataOptions {
  key: string;
  fetcher: () => Promise<unknown>;
  ttl?: number;
}

export function useOfflineData({ key, fetcher, ttl = SYNC_INTERVAL }: UseOfflineDataOptions) {
  const [data, setData] = useState<unknown | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const [isOnline, setIsOnline] = useState(navigator.onLine);

  useEffect(() => {
    const handleOnline = () => setIsOnline(true);
    const handleOffline = () => setIsOnline(false);

    window.addEventListener('online', handleOnline);
    window.addEventListener('offline', handleOffline);

    return () => {
      window.removeEventListener('online', handleOnline);
      window.removeEventListener('offline', handleOffline);
    };
  }, []);

  useEffect(() => {
    let isMounted = true;

    async function loadData() {
      try {
        setLoading(true);
        setError(null);

        if (isOnline) {
          try {
            const freshData = await fetcher();
            if (isMounted) {
              setData(freshData);
              await saveToIndexedDB(key, freshData);
              await setSyncTime(Date.now());
            }
          } catch (fetchError) {
            const cachedData = await getFromIndexedDB(key);
            if (cachedData) {
              if (isMounted) {
                setData(cachedData);
              }
            } else if (isMounted) {
              setError(fetchError as Error);
            }
          }
        } else {
          const cachedData = await getFromIndexedDB(key);
          if (isMounted) {
            if (cachedData) {
              setData(cachedData);
            } else {
              setError(new Error('No cached data available offline'));
            }
          }
        }
      } catch (err) {
        if (isMounted) {
          setError(err as Error);
        }
      } finally {
        if (isMounted) {
          setLoading(false);
        }
      }
    }

    loadData();

    return () => {
      isMounted = false;
    };
  }, [key, fetcher, isOnline]);

  useEffect(() => {
    let isMounted = true;

    async function checkAndRefresh() {
      if (!isOnline) return;

      const lastSync = await getSyncTime();
      const now = Date.now();

      if (now - lastSync > SYNC_INTERVAL) {
        try {
          const freshData = await fetcher();
          if (isMounted) {
            setData(freshData);
            await saveToIndexedDB(key, freshData);
            await setSyncTime(now);
          }
        } catch (err) {
          console.error('Auto-refresh failed:', err);
        }
      }
    }

    const timer = setTimeout(checkAndRefresh, SYNC_INTERVAL);

    return () => {
      isMounted = false;
      clearTimeout(timer);
    };
  }, [key, fetcher, isOnline]);

  return { data, loading, error, isOffline: !isOnline };
}

export { saveToIndexedDB, getFromIndexedDB, setSyncTime, getSyncTime };
