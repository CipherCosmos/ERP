/**
 * Sales the till has made and the server has not yet heard about.
 *
 * The rule the whole thing turns on: a sale is written here, durably, BEFORE the
 * cashier is told it is done. Everything after that — printing, clearing the
 * cart, syncing — can fail and be retried. If the write fails, the cashier is
 * told the sale was not recorded, and that is the only honest answer.
 *
 * The previous mechanism wrote to localStorage and nothing ever read it. A
 * cashier saw "saved" and the sale was gone. What makes this different is not
 * IndexedDB, it is that the write is awaited and its failure is surfaced.
 */

import { OUTBOX, readAll, removeOne, transact, request, writeOne } from './db';

/** Why a queued sale is not moving. */
export type OutboxStatus =
  /** Waiting for a connection, or for its turn. */
  | 'pending'
  /** The server refused it in a way that retrying cannot fix. Needs a person. */
  | 'rejected';

export type OutboxEntry = {
  /** The idempotency key. Also the primary key, so one sale cannot be queued twice. */
  key: string;
  /** The body to POST to /invoices, with the number the till already allocated. */
  invoice: Record<string, unknown>;
  /** Split tenders to record once the invoice exists. */
  splitPayments?: { payment_mode: string; amount: number }[];
  /** The invoice number on the paper the customer already has. */
  invoiceNo: string;
  /** When the customer was served, by the till's clock. */
  billedAt: string;
  /** What the receipt said, so it can be reprinted before the server has it. */
  receipt: { invoice: Record<string, unknown>; lines: Record<string, unknown>[] };
  status: OutboxStatus;
  attempts: number;
  lastAttemptAt?: string;
  lastError?: string;
};

/** Everything waiting, oldest sale first — the order a shop would expect. */
export async function pending(): Promise<OutboxEntry[]> {
  const all = await readAll<OutboxEntry>(OUTBOX);
  return all.sort((a, b) => a.billedAt.localeCompare(b.billedAt));
}

/** How many sales the server has not yet heard about. */
export async function pendingCount(): Promise<number> {
  try {
    return (await pending()).filter((e) => e.status !== 'rejected').length;
  } catch {
    return 0;
  }
}

/** How many need a person to look at them. */
export async function rejectedCount(): Promise<number> {
  try {
    return (await pending()).filter((e) => e.status === 'rejected').length;
  } catch {
    return 0;
  }
}

/**
 * Writes a sale to the outbox and does not return until it is committed.
 *
 * The caller must await this before telling the cashier anything. A rejection
 * here means the sale is not recorded anywhere, and the cashier has to know that
 * rather than hand over the goods.
 */
export async function enqueue(
  entry: Omit<OutboxEntry, 'status' | 'attempts'>
): Promise<OutboxEntry> {
  const stored: OutboxEntry = { ...entry, status: 'pending', attempts: 0 };
  await writeOne(OUTBOX, stored);
  return stored;
}

/** Removes a sale the server has accepted. */
export async function settle(key: string): Promise<void> {
  await removeOne(OUTBOX, key);
}

/** Records an attempt that failed, and whether it is worth trying again. */
export async function recordFailure(key: string, message: string, permanent: boolean): Promise<void> {
  await transact([OUTBOX], 'readwrite', async (tx) => {
    const store = tx.objectStore(OUTBOX);
    const entry = await request(store.get(key) as IDBRequest<OutboxEntry | undefined>);
    if (!entry) return;
    entry.attempts += 1;
    entry.lastAttemptAt = new Date().toISOString();
    entry.lastError = message;
    // A sale the server will never accept is not retried into eternity. It stops
    // and says so: somebody has to look at it, and a queue that grows silently
    // is how an offline day turns into a week of missing sales.
    if (permanent) entry.status = 'rejected';
    await request(store.put(entry));
  });
}

/**
 * Puts a rejected sale back in the queue, for after somebody has fixed whatever
 * the server objected to.
 */
export async function retry(key: string): Promise<void> {
  await transact([OUTBOX], 'readwrite', async (tx) => {
    const store = tx.objectStore(OUTBOX);
    const entry = await request(store.get(key) as IDBRequest<OutboxEntry | undefined>);
    if (!entry) return;
    entry.status = 'pending';
    entry.lastError = undefined;
    await request(store.put(entry));
  });
}
