/**
 * Sending the till's queued sales to the server.
 *
 * It runs in the app, not in a service worker. The service worker that used to
 * own this could not read the token — its getter returned an empty string — so
 * every request it made was unauthenticated and every sale it "synced" was
 * refused. Sync needs a session, and the session lives here.
 *
 * Three things it has to get right, and each is a way sales get lost:
 *
 *   - Exactly once. Every sale carries the idempotency key it was queued under.
 *     A reply the till never sees is retried and the server returns the original
 *     sale rather than raising a second.
 *   - Order. A till's sales are sent oldest first, because the invoice numbers
 *     are consecutive and a shop reading its own day back should not find them
 *     interleaved at random.
 *   - Knowing when to stop. A sale the server refuses for a reason retrying
 *     cannot fix is set aside for a person instead of being attempted for ever.
 */

import { OutboxEntry, pending, recordFailure, settle } from './outbox';

export type SyncOutcome = {
  sent: number;
  rejected: number;
  /** Set when the run stopped early because the connection went. */
  stopped?: string;
};

/** What the caller has to be able to do for a sale to be sent. */
export type SyncPorts = {
  createInvoice: (
    body: Record<string, unknown>,
    idempotencyKey: string
  ) => Promise<{ invoice?: { id?: string; invoice_no?: string }; id?: string }>;
  addSplitPayments: (
    invoiceId: string,
    payments: { payment_mode: string; amount: number }[],
    idempotencyKey: string
  ) => Promise<unknown>;
  /** Told the number the server confirmed, so the till's counter never goes back. */
  observeIssued?: (invoiceNo: string) => Promise<void>;
};

/**
 * Whether an error means "this will never work" rather than "try again later".
 *
 * A 4xx other than 408, 425 and 429 is the server saying the request itself is
 * wrong: a duplicate invoice number, a product that no longer exists, a customer
 * deleted since. None of those improve by waiting, so the sale stops and waits
 * for a person. Everything else — no response, a 5xx, a timeout — is the
 * connection or the server having a bad moment, and the sale stays queued.
 *
 * A 409 is NOT success, and getting that wrong loses sales. It was wrong here
 * first: a replayed sale does not come back as a conflict, it comes back as the
 * original sale with a 201, because that is what the idempotency key is for. A
 * 409 means something genuinely clashed — almost always that the invoice number
 * this till allocated is already on another bill, which happens when a device is
 * provisioned without being told where the shop's series had got to. Treating it
 * as "the server already has this" deleted the queue entry and the sale with it.
 * Measured, on a till that had billed one customer.
 */
export function classify(err: any): { permanent: boolean; message: string } {
  const status = err?.response?.status as number | undefined;
  const message =
    err?.response?.data?.error ||
    err?.response?.data?.message ||
    err?.message ||
    'could not reach the server';

  if (status === undefined) return { permanent: false, message };
  if (status === 408 || status === 425 || status === 429) return { permanent: false, message };
  if (status >= 400 && status < 500) return { permanent: true, message };
  return { permanent: false, message };
}

/** Sends everything waiting. Safe to call at any time, including concurrently. */
let running = false;

export async function flush(ports: SyncPorts): Promise<SyncOutcome> {
  // One run at a time. Two runs would send the same sale twice — harmless,
  // because the key makes the second a replay, but it wastes a connection that
  // by definition is not reliable.
  if (running) return { sent: 0, rejected: 0 };
  running = true;
  const outcome: SyncOutcome = { sent: 0, rejected: 0 };
  try {
    const queue = await pending();
    for (const entry of queue) {
      if (entry.status === 'rejected') continue;
      try {
        await send(entry, ports);
        await settle(entry.key);
        outcome.sent += 1;
      } catch (err: any) {
        const { permanent, message } = classify(err);
        await recordFailure(entry.key, message, permanent);
        if (permanent) {
          outcome.rejected += 1;
          continue;
        }
        // The connection has gone. Stop rather than working through the rest:
        // they will all fail the same way, and each failure counts an attempt
        // against a sale that never had a chance.
        outcome.stopped = message;
        break;
      }
    }
    return outcome;
  } finally {
    running = false;
  }
}

async function send(entry: OutboxEntry, ports: SyncPorts): Promise<void> {
  const created = await ports.createInvoice(entry.invoice, entry.key);
  const invoiceId = created?.invoice?.id ?? created?.id;
  const invoiceNo = created?.invoice?.invoice_no;
  if (invoiceNo && ports.observeIssued) {
    await ports.observeIssued(invoiceNo);
  }
  const splits = entry.splitPayments;
  if (splits && splits.length > 0 && invoiceId) {
    // Its own key, derived from the sale's: the settlement is a second request
    // and needs a second key, but one that is the same on every retry.
    await ports.addSplitPayments(invoiceId, splits, `${entry.key}:split`);
  }
}

/**
 * Retry delay after n consecutive failures, in milliseconds.
 *
 * Backing off matters on the connection a shop actually has: a phone hotspot
 * that comes and goes. Hammering it every second neither helps the till nor the
 * connection. It caps at a minute so a shop that has been down all morning is
 * not still waiting five minutes after the line comes back.
 */
export function backoffMs(failures: number): number {
  const base = Math.min(60_000, 2_000 * 2 ** Math.max(0, failures - 1));
  // A little jitter, so several tills in one shop do not all retry together.
  return Math.round(base * (0.75 + Math.random() * 0.5));
}
