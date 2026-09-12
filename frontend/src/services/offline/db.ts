/**
 * The till's own storage: one IndexedDB database holding what it needs to keep
 * billing when it cannot reach the server, and what it owes the server when it
 * can again.
 *
 * There used to be two half-built mechanisms. A service worker read an
 * IndexedDB that nothing wrote to, and the app wrote a queue in localStorage
 * that nothing read; a cashier was told a sale was safe while it was quietly
 * lost. This replaces both, and it is deliberately one database with one
 * version, because two stores that can disagree about whether a sale happened
 * are worse than none.
 *
 * IndexedDB and not localStorage. localStorage is synchronous, capped around
 * five megabytes and stringly typed — a catalogue of a few thousand products
 * with their units and prices does not fit, and a write that fails there fails
 * silently in several browsers. A queued sale that cannot be written has to be
 * something the caller learns about, which is why every function here returns a
 * promise that can reject.
 */

const DB_NAME = 'gbos';
const DB_VERSION = 1;

/** Stores, and what each one is for. */
export const SNAPSHOT = 'snapshot';
export const OUTBOX = 'outbox';
export const COUNTERS = 'counters';

let open: Promise<IDBDatabase> | null = null;

/**
 * Opens the database, creating it on first use.
 *
 * The promise is cached rather than the connection: two callers racing on first
 * load would otherwise both run the upgrade.
 */
export function database(): Promise<IDBDatabase> {
  if (open) return open;
  open = new Promise<IDBDatabase>((resolve, reject) => {
    if (typeof indexedDB === 'undefined') {
      reject(new Error('This browser has no IndexedDB, so the till cannot bill offline.'));
      return;
    }
    const req = indexedDB.open(DB_NAME, DB_VERSION);
    req.onerror = () => reject(req.error ?? new Error('could not open the till database'));
    req.onblocked = () =>
      reject(new Error('Another tab is holding the till database open. Close it and reload.'));
    req.onupgradeneeded = () => {
      const db = req.result;
      // The catalogue, the store and the terminal, keyed by what they are:
      // there is one of each and a till is never in two shops at once.
      if (!db.objectStoreNames.contains(SNAPSHOT)) {
        db.createObjectStore(SNAPSHOT, { keyPath: 'key' });
      }
      // Sales waiting to reach the server, oldest first. The key is the
      // idempotency key, so writing the same sale twice is impossible rather
      // than merely unlikely.
      if (!db.objectStoreNames.contains(OUTBOX)) {
        const outbox = db.createObjectStore(OUTBOX, { keyPath: 'key' });
        outbox.createIndex('billedAt', 'billedAt');
      }
      // The terminal's invoice counter. See allocateInvoiceNumber for why the
      // till owns it.
      if (!db.objectStoreNames.contains(COUNTERS)) {
        db.createObjectStore(COUNTERS, { keyPath: 'key' });
      }
    };
    req.onsuccess = () => {
      const db = req.result;
      db.onclose = () => {
        open = null;
      };
      // A later version in another tab must not be blocked by this connection.
      db.onversionchange = () => {
        db.close();
        open = null;
      };
      resolve(db);
    };
  });
  return open;
}

/** Runs fn inside a transaction and resolves when it has actually committed. */
export async function transact<T>(
  stores: string[],
  mode: IDBTransactionMode,
  fn: (tx: IDBTransaction) => Promise<T> | T
): Promise<T> {
  const db = await database();
  return new Promise<T>((resolve, reject) => {
    const tx = db.transaction(stores, mode);
    let result: T;
    let settled = false;
    // oncomplete, not the request callbacks: a write is not durable until the
    // transaction commits, and resolving earlier is how a queued sale gets
    // reported as saved and then is not there after a crash.
    tx.oncomplete = () => {
      settled = true;
      resolve(result);
    };
    tx.onerror = () => {
      if (!settled) reject(tx.error ?? new Error('the till database refused the write'));
    };
    tx.onabort = () => {
      if (!settled) reject(tx.error ?? new Error('the till database aborted the write'));
    };
    Promise.resolve(fn(tx))
      .then((value) => {
        result = value;
      })
      .catch((err) => {
        try {
          tx.abort();
        } catch {
          // Already finished; the handlers above have reported it.
        }
        reject(err);
      });
  });
}

/** Wraps one IndexedDB request as a promise. */
export function request<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error ?? new Error('the till database refused the request'));
  });
}

/** Reads every row of a store. */
export async function readAll<T>(store: string): Promise<T[]> {
  return transact([store], 'readonly', (tx) =>
    request(tx.objectStore(store).getAll() as IDBRequest<T[]>)
  );
}

/** Reads one row by key, or undefined. */
export async function readOne<T>(store: string, key: string): Promise<T | undefined> {
  return transact([store], 'readonly', (tx) =>
    request(tx.objectStore(store).get(key) as IDBRequest<T | undefined>)
  );
}

/** Writes one row. */
export async function writeOne<T extends { key: string }>(store: string, value: T): Promise<void> {
  await transact([store], 'readwrite', (tx) => request(tx.objectStore(store).put(value)));
}

/** Removes one row. */
export async function removeOne(store: string, key: string): Promise<void> {
  await transact([store], 'readwrite', (tx) => request(tx.objectStore(store).delete(key)));
}
