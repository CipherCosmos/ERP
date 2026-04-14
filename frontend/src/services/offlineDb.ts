/**
 * GBOS Offline Database Service
 *
 * Raw IndexedDB wrapper for offline data persistence.
 * Stores: products, customers, categories, stock
 * No external libraries used.
 */

const DB_NAME = 'gbos-offline-db';
const DB_VERSION = 1;

const STORE_NAMES = ['products', 'customers', 'categories', 'stock'] as const;
type StoreName = (typeof STORE_NAMES)[number];

let dbInstance: IDBDatabase | null = null;

/**
 * Opens (or creates/upgrades) the IndexedDB database.
 * Returns a promise that resolves with the database instance.
 * Caches the connection so subsequent calls reuse the same instance.
 */
export function initDB(): Promise<IDBDatabase> {
  if (dbInstance) {
    return Promise.resolve(dbInstance);
  }

  return new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open(DB_NAME, DB_VERSION);

    request.onerror = () => {
      console.error('[OfflineDB] Failed to open database:', request.error);
      reject(request.error);
    };

    request.onsuccess = () => {
      dbInstance = request.result;

      // If the connection is closed externally, clear our cached reference
      dbInstance.onclose = () => {
        dbInstance = null;
      };

      resolve(dbInstance);
    };

    request.onupgradeneeded = (event) => {
      const db = (event.target as IDBOpenDBRequest).result;

      for (const storeName of STORE_NAMES) {
        if (!db.objectStoreNames.contains(storeName)) {
          db.createObjectStore(storeName, { keyPath: 'id' });
        }
      }
    };
  });
}

// ─── Generic helpers ───────────────────────────────────────────────────────────

/**
 * Bulk-save an array of records into a given object store.
 * Clears existing data first, then writes all records in a single transaction.
 */
function bulkSave<T extends { id: string }>(
  storeName: StoreName,
  items: T[]
): Promise<void> {
  return initDB().then((db) => {
    return new Promise<void>((resolve, reject) => {
      const tx = db.transaction(storeName, 'readwrite');
      const store = tx.objectStore(storeName);

      // Clear existing data before writing fresh data
      store.clear();

      for (const item of items) {
        store.put(item);
      }

      tx.oncomplete = () => resolve();
      tx.onerror = () => {
        console.error(`[OfflineDB] bulkSave(${storeName}) failed:`, tx.error);
        reject(tx.error);
      };
    });
  });
}

/**
 * Retrieve all records from a given object store.
 */
function getAll<T>(storeName: StoreName): Promise<T[]> {
  return initDB().then((db) => {
    return new Promise<T[]>((resolve, reject) => {
      const tx = db.transaction(storeName, 'readonly');
      const store = tx.objectStore(storeName);
      const request = store.getAll();

      request.onsuccess = () => resolve(request.result as T[]);
      request.onerror = () => {
        console.error(`[OfflineDB] getAll(${storeName}) failed:`, request.error);
        reject(request.error);
      };
    });
  });
}

// ─── Products ──────────────────────────────────────────────────────────────────

export function saveProducts<T extends { id: string }>(products: T[]): Promise<void> {
  return bulkSave('products', products);
}

export function getProducts<T = Record<string, unknown>>(): Promise<T[]> {
  return getAll<T>('products');
}

// ─── Customers ─────────────────────────────────────────────────────────────────

export function saveCustomers<T extends { id: string }>(customers: T[]): Promise<void> {
  return bulkSave('customers', customers);
}

export function getCustomers<T = Record<string, unknown>>(): Promise<T[]> {
  return getAll<T>('customers');
}

// ─── Categories ────────────────────────────────────────────────────────────────

export function saveCategories<T extends { id: string }>(categories: T[]): Promise<void> {
  return bulkSave('categories', categories);
}

export function getCategories<T = Record<string, unknown>>(): Promise<T[]> {
  return getAll<T>('categories');
}

// ─── Stock ─────────────────────────────────────────────────────────────────────

export function saveStock<T extends { id: string }>(stock: T[]): Promise<void> {
  return bulkSave('stock', stock);
}

export function getStock<T = Record<string, unknown>>(): Promise<T[]> {
  return getAll<T>('stock');
}

// ─── Utilities ─────────────────────────────────────────────────────────────────

/**
 * Clear all records from a specific object store.
 */
export function clearStore(storeName: string): Promise<void> {
  return initDB().then((db) => {
    return new Promise<void>((resolve, reject) => {
      if (!db.objectStoreNames.contains(storeName)) {
        reject(new Error(`[OfflineDB] Store "${storeName}" does not exist.`));
        return;
      }

      const tx = db.transaction(storeName, 'readwrite');
      const store = tx.objectStore(storeName);
      const request = store.clear();

      request.onsuccess = () => resolve();
      request.onerror = () => {
        console.error(`[OfflineDB] clearStore(${storeName}) failed:`, request.error);
        reject(request.error);
      };
    });
  });
}
