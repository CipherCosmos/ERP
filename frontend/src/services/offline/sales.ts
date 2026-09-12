/**
 * The bills this till has raised, kept so a return can be taken against one when
 * the connection is down.
 *
 * A return needs the invoice it is coming back on: which bill, which product,
 * how much was sold and at what price. Online the till asks the server. Offline
 * it can only know the bills it raised itself, so it keeps them — including the
 * ones already synced, because a customer comes back with goods the next morning
 * and the connection has nothing to do with it.
 *
 * Bounded on purpose. A till running for a year would otherwise accumulate every
 * bill it ever printed, and the reason to hold them is a few days of returns, not
 * an archive. The server has the archive.
 */

import { OUTBOX, SALES, request, transact } from './db';

/** How many recent bills a till keeps for returns. */
export const KEEP_RECENT = 500;

const STORE = SALES;

export type RecordedLine = {
  product_id: string;
  product_name: string;
  product_unit_id?: string;
  unit_label: string;
  unit_qty: number;
  quantity: number;
  rate: number;
  total: number;
};

export type RecordedSale = {
  /** The invoice number. Also the key: one bill, one record. */
  key: string;
  /** The server's id once it has one. Absent while the sale is still queued. */
  invoiceId?: string;
  billedAt: string;
  total: number;
  customerId?: string | null;
  lines: RecordedLine[];
};

/**
 * Remembers a bill.
 *
 * Keyed on the invoice number rather than a generated id, so recording the same
 * bill twice — once when it is raised and again when the server confirms it —
 * updates one record instead of making two.
 */
export async function recordSale(sale: RecordedSale): Promise<void> {
  await transact([STORE], 'readwrite', async (tx) => {
    const store = tx.objectStore(STORE);
    const existing = await request(store.get(sale.key) as IDBRequest<RecordedSale | undefined>);
    await request(store.put({ ...existing, ...sale }));
  });
  await prune();
}

/** Attaches the server's id to a bill once it has synced. */
export async function noteSynced(invoiceNo: string, invoiceId: string): Promise<void> {
  await transact([STORE], 'readwrite', async (tx) => {
    const store = tx.objectStore(STORE);
    const existing = await request(store.get(invoiceNo) as IDBRequest<RecordedSale | undefined>);
    if (!existing) return;
    await request(store.put({ ...existing, invoiceId }));
  });
}

/** The bills this till has raised, newest first. */
export async function recentSales(limit = 50): Promise<RecordedSale[]> {
  const all = await transact([STORE], 'readonly', (tx) =>
    request(tx.objectStore(STORE).getAll() as IDBRequest<RecordedSale[]>)
  );
  return all.sort((a, b) => b.billedAt.localeCompare(a.billedAt)).slice(0, limit);
}

/** One bill by its number, for a return taken against it. */
export async function saleByNumber(invoiceNo: string): Promise<RecordedSale | undefined> {
  return transact([STORE], 'readonly', (tx) =>
    request(tx.objectStore(STORE).get(invoiceNo) as IDBRequest<RecordedSale | undefined>)
  );
}

/** Drops the oldest bills once there are more than the till keeps. */
async function prune(): Promise<void> {
  await transact([STORE], 'readwrite', async (tx) => {
    const store = tx.objectStore(STORE);
    const all = await request(store.getAll() as IDBRequest<RecordedSale[]>);
    if (all.length <= KEEP_RECENT) return;
    const doomed = all
      .sort((a, b) => a.billedAt.localeCompare(b.billedAt))
      .slice(0, all.length - KEEP_RECENT);
    for (const sale of doomed) {
      await request(store.delete(sale.key));
    }
  });
}

// The outbox is imported for its name only, so this module and db.ts cannot
// drift about which stores exist.
export const RELATED_STORES = [STORE, OUTBOX];
