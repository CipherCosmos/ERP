/**
 * The terminal's invoice series, owned by the till.
 *
 * A till that cannot reach the server still has to hand the customer a tax
 * invoice, and a tax invoice carries a number. A provisional slip followed later
 * by a real invoice is not what Rule 46 asks for, so the number has to exist at
 * the counter — which means the till allocates it.
 *
 * It allocates online too. That is the important part: one path, exercised on
 * every sale, rather than a second path that runs only when the network is down
 * and is therefore never tested until the day it matters.
 *
 * The rule this rests on is one till per terminal code. Two devices billing on
 * one code both reach 00001 and the server refuses the second on sync, loudly —
 * which is the right failure, and the remedy is to register a counter per till.
 */

import { COUNTERS, transact, request } from './db';

/** Prefix for the invoice series. The server checks it. */
const INVOICE_PREFIX = 'INV';

type Counter = {
  key: string;
  next: number;
};

/**
 * The Indian financial year containing t, as '2026-27'.
 *
 * April to March, read in the shop's own zone. The server computes it the same
 * way from the timestamp the till sends, and the two must agree or the sale is
 * refused for claiming a year it does not fall in. India has observed UTC+05:30
 * since 1945, so the offset is the rule.
 */
export function financialYear(t: Date = new Date()): string {
  // getTime is already UTC, so the shift is the offset itself and nothing about
  // where the device happens to be. Reading the result with the getUTC accessors
  // then gives the shop's own wall clock whatever the till's timezone is set to,
  // which matters: a device left on the wrong zone must not file a sale in the
  // wrong year.
  const IST_OFFSET_MINUTES = 5 * 60 + 30;
  const ist = new Date(t.getTime() + IST_OFFSET_MINUTES * 60_000);
  const year = ist.getUTCMonth() < 3 ? ist.getUTCFullYear() - 1 : ist.getUTCFullYear();
  return `${year}-${String((year + 1) % 100).padStart(2, '0')}`;
}

/** Renders a sequence value the way the server does. */
export function formatNumber(terminalCode: string, year: string, seq: number): string {
  return `${INVOICE_PREFIX}/${terminalCode}/${year}/${String(seq).padStart(5, '0')}`;
}

function counterKey(terminalId: string, year: string): string {
  return `invoice:${terminalId}:${year}`;
}

/**
 * Takes the next number in this terminal's series, and records that it is taken
 * before returning it.
 *
 * The read and the write are one transaction, so two sales started in the same
 * instant cannot take the same number. The number is spent whether or not the
 * sale then completes: a gap is a far smaller problem than a duplicate, and the
 * cashier who abandons a bill has not issued a document.
 */
export async function allocateInvoiceNumber(
  terminalId: string,
  terminalCode: string,
  at: Date = new Date()
): Promise<{ invoiceNo: string; seq: number; year: string }> {
  const year = financialYear(at);
  const key = counterKey(terminalId, year);
  const seq = await transact([COUNTERS], 'readwrite', async (tx) => {
    const store = tx.objectStore(COUNTERS);
    const current = await request(store.get(key) as IDBRequest<Counter | undefined>);
    const next = current?.next ?? 1;
    await request(store.put({ key, next: next + 1 } as Counter));
    return next;
  });
  return { invoiceNo: formatNumber(terminalCode, year, seq), seq, year };
}

/**
 * Moves the counter past a number the server has issued, so the till's next
 * number does not collide with one the shop already has.
 *
 * Called after every online sale and after a sync: the server is the one that
 * knows about sales raised from other devices that share this terminal — which
 * should not happen, but a counter that only ever moves forward makes the
 * mistake recoverable instead of permanent.
 */
export async function observeIssuedNumber(terminalId: string, invoiceNo: string): Promise<void> {
  const parts = invoiceNo.split('/');
  if (parts.length !== 4) return;
  const [, , year, seqText] = parts;
  const seq = Number(seqText);
  if (!Number.isInteger(seq) || seq < 1) return;

  const key = counterKey(terminalId, year);
  await transact([COUNTERS], 'readwrite', async (tx) => {
    const store = tx.objectStore(COUNTERS);
    const current = await request(store.get(key) as IDBRequest<Counter | undefined>);
    const next = Math.max(current?.next ?? 1, seq + 1);
    await request(store.put({ key, next } as Counter));
  });
}

/**
 * Moves the counter to at least seq, without issuing anything.
 *
 * This is how a newly provisioned till learns where the shop's series has got
 * to. Without it a device starts at one and its first offline sale collides with
 * a bill the shop issued months ago — which the server refuses, correctly, and
 * which the cashier can neither see nor fix.
 *
 * Only forward, like every other write to this counter. A server reporting a
 * lower number than the till holds means the till has issued sales the server
 * has not seen yet, and those are in the outbox waiting to go.
 */
export async function seedCounter(terminalId: string, seq: number, at: Date = new Date()): Promise<void> {
  if (!Number.isInteger(seq) || seq < 1) return;
  const key = counterKey(terminalId, financialYear(at));
  await transact([COUNTERS], 'readwrite', async (tx) => {
    const store = tx.objectStore(COUNTERS);
    const current = await request(store.get(key) as IDBRequest<Counter | undefined>);
    const next = Math.max(current?.next ?? 1, seq);
    await request(store.put({ key, next } as Counter));
  });
}

/** What the till would number next, without taking it. For the settings screen. */
export async function peekNextNumber(
  terminalId: string,
  terminalCode: string,
  at: Date = new Date()
): Promise<string> {
  const year = financialYear(at);
  const counter = await transact([COUNTERS], 'readonly', (tx) =>
    request(tx.objectStore(COUNTERS).get(counterKey(terminalId, year)) as IDBRequest<Counter | undefined>)
  );
  return formatNumber(terminalCode, year, counter?.next ?? 1);
}
