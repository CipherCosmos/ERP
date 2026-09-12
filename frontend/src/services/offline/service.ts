/**
 * What the app calls: keep the snapshot fresh while there is a connection, and
 * send the outbox whenever there is one again.
 *
 * It is one module rather than a hook so the counter, the menu and the
 * reconciliation screen all read the same state, and so the sync loop survives
 * a screen being unmounted mid-flight.
 */

import { customerApi, invoiceApi, paymentApi, productApi, storeApi, terminalApi } from '../api';
import { allocateInvoiceNumber, observeIssuedNumber, peekNextNumber, seedCounter } from './numbering';
import { OutboxEntry, pending, pendingCount, rejectedCount, retry } from './outbox';
import { Snapshot, isStale, ageInHours, loadSnapshot, saveSnapshot, toSnapshotProducts } from './snapshot';
import { backoffMs, flush } from './sync';

export type TillState = {
  online: boolean;
  /** Sales the server has not yet heard about. */
  queued: number;
  /** Sales the server refused in a way retrying cannot fix. */
  rejected: number;
  /** How old the catalogue the till is pricing from is, in hours. */
  snapshotAgeHours: number;
  stale: boolean;
  /** True once a snapshot exists: before that the till cannot bill offline. */
  ready: boolean;
  syncing: boolean;
  lastError?: string;
};

type Listener = (state: TillState) => void;

let state: TillState = {
  online: typeof navigator === 'undefined' ? true : navigator.onLine,
  queued: 0,
  rejected: 0,
  snapshotAgeHours: Infinity,
  stale: true,
  ready: false,
  syncing: false,
};

const listeners = new Set<Listener>();
let snapshot: Snapshot | null = null;
let failures = 0;
let timer: ReturnType<typeof setTimeout> | null = null;

/** Subscribes to till state. Returns the unsubscribe. */
export function subscribe(fn: Listener): () => void {
  listeners.add(fn);
  fn(state);
  return () => listeners.delete(fn);
}

export function tillState(): TillState {
  return state;
}

function publish(patch: Partial<TillState>) {
  state = { ...state, ...patch };
  listeners.forEach((fn) => fn(state));
}

/** The catalogue the till is pricing from, or null before the first refresh. */
export function currentSnapshot(): Snapshot | null {
  return snapshot;
}

async function refreshCounts() {
  publish({ queued: await pendingCount(), rejected: await rejectedCount() });
}

/**
 * Takes a fresh copy of the shop.
 *
 * Everything the till needs to price a sale and print an invoice, in one place,
 * so a sale offline reads one record rather than assembling it from whatever
 * happened to be cached.
 */
export async function refreshSnapshot(storeId: string): Promise<Snapshot | null> {
  const [products, customers, store, terminals] = await Promise.all([
    productApi.list({ store_id: storeId }),
    customerApi.list(storeId),
    storeApi.get(storeId),
    terminalApi.list(storeId),
  ]);

  const shop = (store.data ?? {}) as any;
  const counters = (terminals.data ?? []) as any[];
  const active = counters.find((t) => t.is_active) ?? counters[0] ?? null;

  // Where the shop's series has actually reached. A till provisioned today must
  // not start at one: the shop has been trading, and a bill numbered 00001 in
  // September collides with the one it issued in April. The server reports the
  // position for every counter, and the till's own counter only moves forward.
  for (const counter of counters) {
    const next = Number(counter?.next_invoice_seq ?? 0);
    if (counter?.id && Number.isInteger(next) && next > 1) {
      await seedCounter(String(counter.id), next);
    }
  }

  snapshot = await saveSnapshot({
    store: {
      id: String(shop.id ?? storeId),
      name: String(shop.name ?? ''),
      gstin: String(shop.gstin ?? ''),
      address: String(shop.address ?? ''),
      phone: String(shop.phone ?? ''),
    },
    terminal: active
      ? { id: String(active.id), code: String(active.code ?? ''), name: String(active.name ?? '') }
      : null,
    products: toSnapshotProducts(products.data as any[]),
    customers: ((customers.data ?? []) as any[]).map((c) => ({
      id: String(c.id),
      name: String(c.name ?? ''),
      phone: String(c.phone ?? ''),
      gstin: String(c.gstin ?? ''),
      credit_limit: Number(c.credit_limit ?? 0),
      is_wholesale: Boolean(c.is_wholesale),
    })),
  });

  publish({
    ready: true,
    snapshotAgeHours: ageInHours(snapshot),
    stale: isStale(snapshot),
  });
  return snapshot;
}

/** Reads whatever snapshot is already on the device, without the network. */
export async function loadStoredSnapshot(): Promise<Snapshot | null> {
  snapshot = await loadSnapshot();
  publish({
    ready: snapshot !== null,
    snapshotAgeHours: ageInHours(snapshot),
    stale: isStale(snapshot),
  });
  return snapshot;
}

/** Sends the outbox. Called on reconnect, after a sale, and on a timer. */
export async function syncNow(): Promise<void> {
  if (!state.online) return;
  publish({ syncing: true });
  try {
    const outcome = await flush({
      createInvoice: async (body, key) => (await invoiceApi.create(body as any, key)).data as any,
      addSplitPayments: async (invoiceId, payments, key) =>
        paymentApi.addSplitPayments({ invoice_id: invoiceId, payments }, key),
      observeIssued: async (invoiceNo) => {
        if (snapshot?.terminal) await observeIssuedNumber(snapshot.terminal.id, invoiceNo);
      },
    });
    failures = outcome.stopped ? failures + 1 : 0;
    publish({ lastError: outcome.stopped });
  } catch (err: any) {
    failures += 1;
    publish({ lastError: err?.message ?? 'sync failed' });
  } finally {
    publish({ syncing: false });
    await refreshCounts();
    schedule();
  }
}

/** Queues the next sync attempt, backing off while the connection is bad. */
function schedule() {
  if (timer) clearTimeout(timer);
  if (state.queued === 0 && state.rejected === 0) return;
  timer = setTimeout(() => {
    void syncNow();
  }, failures > 0 ? backoffMs(failures) : 15_000);
}

/** The sales waiting, for the screen that lists them. */
export async function queuedSales(): Promise<OutboxEntry[]> {
  return pending();
}

/** Puts a rejected sale back in the queue after somebody has fixed the cause. */
export async function retrySale(key: string): Promise<void> {
  await retry(key);
  await refreshCounts();
  void syncNow();
}

export { allocateInvoiceNumber, observeIssuedNumber, peekNextNumber };

/**
 * Starts the till: read what is stored, then keep it current.
 *
 * Called once when a session is established. It returns a stop function so a
 * sign-out does not leave a loop running against a token that no longer works.
 */
export function startTill(storeId: string): () => void {
  let stopped = false;

  // Nothing here works without a session, and everything here retries. A till
  // started on a persisted store id whose token has expired sends four requests,
  // gets four 401s, and does it again on the next tick — which is a shop's own
  // server being hammered by a till nobody is using.
  if (!localStorage.getItem('token')) {
    return () => undefined;
  }

  const onOnline = () => {
    publish({ online: true });
    failures = 0;
    void syncNow();
    void refreshSnapshot(storeId).catch(() => {
      // The catalogue can wait; the outbox cannot. A refresh that fails leaves
      // the previous snapshot in place, which is exactly what it is for.
    });
  };
  const onOffline = () => publish({ online: false });

  window.addEventListener('online', onOnline);
  window.addEventListener('offline', onOffline);

  void (async () => {
    await loadStoredSnapshot();
    await refreshCounts();
    if (stopped) return;
    if (state.online) {
      await refreshSnapshot(storeId).catch(() => undefined);
      await syncNow();
    }
  })();

  return () => {
    stopped = true;
    window.removeEventListener('online', onOnline);
    window.removeEventListener('offline', onOffline);
    if (timer) clearTimeout(timer);
    timer = null;
  };
}
