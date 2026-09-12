/**
 * Presentation helpers shared by the pages.
 *
 * The API speaks RFC3339, so a purchase order's order date arrives as
 * "2026-09-10T00:00:00Z" — that is simply what a Go time.Time marshals to.
 * Dropped straight into a table cell it reads as machine output, so every
 * screen that shows a server date runs it through formatDate first.
 */

/** An ISO calendar date at the head of a string, with or without a time after it. */
const ISO_DATE = /^(\d{4})-(\d{2})-(\d{2})/;

/**
 * formatDate renders a server date as DD-MM-YYYY, the form Indian invoices and
 * the GST offline utility use.
 *
 * The calendar parts are read out of the string rather than through `new Date`
 * on purpose. These values are dates, not instants: the server stamps them at
 * UTC midnight, so converting one to local time moves it to the previous day
 * for any viewer west of Greenwich — a bill dated the 10th would print as the
 * 9th. Reading the digits keeps the day the server meant.
 *
 * An empty value (null, undefined, "", or Go's zero time) yields the fallback.
 * Anything else that is not an ISO date is passed through unchanged, which
 * tells more than hiding an unexpected value behind a dash.
 */
export function formatDate(value?: string | null, fallback = '-'): string {
  if (!value) return fallback;
  const raw = String(value).trim();
  if (!raw) return fallback;

  const parts = ISO_DATE.exec(raw);
  if (!parts) return raw;

  const [, year, month, day] = parts;
  if (year === '0001') return fallback;
  return `${day}-${month}-${year}`;
}

/**
 * formatMoney renders an amount the way a bill does: grouped in the Indian
 * system and always to the paisa.
 *
 * `₹{x.toFixed(2)}` was written out by hand 128 times across 24 files, which is
 * why a lakh printed as ₹100000.00 on every screen in the app. en-IN grouping
 * makes that ₹1,00,000.00 — the reading a shopkeeper can check at a glance.
 *
 * The server sends amounts as JSON numbers with a fixed scale, so the value
 * arriving here is already exact; this only chooses how to show it. Anything
 * unparseable renders as the fallback rather than "₹NaN".
 */
export function formatMoney(value?: number | string | null, fallback = '—'): string {
  const n = typeof value === 'string' ? Number(value) : value;
  if (n === null || n === undefined || Number.isNaN(n)) return fallback;
  return `₹${n.toLocaleString('en-IN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}

/**
 * formatQty renders a quantity without the trailing zeros a fixed scale
 * carries: the server sends 3 decimal places, so 197 arrives as "197.000" and
 * 2.5 kg as "2.500". A stock column full of dead zeros is harder to scan, and
 * the zeros claim a precision the count does not have.
 *
 * `unit` appends the label when there is one, so a cell reads "2.5 kg" rather
 * than leaving the reader to guess which unit a bare number is in — the whole
 * point of the units work.
 */
export function formatQty(value?: number | string | null, unit?: string, fallback = '—'): string {
  const n = typeof value === 'string' ? Number(value) : value;
  if (n === null || n === undefined || Number.isNaN(n)) return fallback;
  // Up to three places, none of them forced: 197 stays 197, 0.25 stays 0.25.
  const text = n.toLocaleString('en-IN', { maximumFractionDigits: 3 });
  return unit ? `${text} ${unit}` : text;
}

export default formatDate;
