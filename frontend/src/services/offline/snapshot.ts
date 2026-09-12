/**
 * The copy of the shop the till bills from.
 *
 * A till that cannot reach the server still has to price a sale, work out the
 * tax on it and print a compliant invoice, so it keeps everything those need:
 * the shop's own details for the header, the terminal whose series it numbers
 * from, and every sellable product with its units, prices, tax rate and HSN
 * code.
 *
 * It is refreshed whenever the till is online and used whenever it is not. The
 * age of it is the thing to watch: a snapshot taken this morning prices today's
 * sales correctly, and one taken three weeks ago prices them at three-week-old
 * prices and the shop finds out at reconciliation. See isStale.
 */

import { SNAPSHOT, readOne, writeOne } from './db';

/** How long a snapshot may be relied on before the till starts saying so. */
export const STALE_AFTER_HOURS = 24;

export type SnapshotUnit = {
  id: string;
  label: string;
  qty_in_base: number;
  is_default: boolean;
  is_sellable: boolean;
  barcode: string;
  sell_price: number;
  wholesale_sell_price: number;
  mrp: number;
  has_price: boolean;
};

export type SnapshotProduct = {
  id: string;
  name: string;
  barcode: string;
  hsn_code: string;
  gst_rate: number;
  allow_decimal: boolean;
  category_name: string;
  /** What the shelf held when the snapshot was taken. Advisory: see the POS. */
  stock: number;
  units: SnapshotUnit[];
};

export type SnapshotCustomer = {
  id: string;
  name: string;
  phone: string;
  gstin: string;
  credit_limit: number;
  is_wholesale: boolean;
};

export type Snapshot = {
  key: 'catalogue';
  takenAt: string;
  store: { id: string; name: string; gstin: string; address: string; phone: string };
  terminal: { id: string; code: string; name: string } | null;
  products: SnapshotProduct[];
  customers: SnapshotCustomer[];
};

/** Reshapes the products endpoint's rows into what the till actually needs. */
export function toSnapshotProducts(rows: any[]): SnapshotProduct[] {
  return (rows ?? [])
    .filter((p) => p?.is_active !== false)
    .map((p) => ({
      id: String(p.id),
      name: String(p.name ?? ''),
      barcode: String(p.barcode ?? ''),
      hsn_code: String(p.hsn_code ?? ''),
      gst_rate: Number(p.gst_rate ?? 0),
      allow_decimal: Boolean(p.allow_decimal),
      category_name: String(p.category_name ?? ''),
      stock: Number(p.stock ?? 0),
      units: (p.units ?? [])
        .filter((u: any) => u?.unit?.is_active !== false && u?.unit?.is_sellable)
        .map((u: any) => ({
          id: String(u.unit.id),
          label: String(u.unit.label ?? ''),
          qty_in_base: Number(u.unit.qty_in_base ?? 1),
          is_default: Boolean(u.unit.is_default),
          is_sellable: Boolean(u.unit.is_sellable),
          barcode: String(u.unit.barcode ?? ''),
          // A unit with no price row is not sellable offline: billing it at
          // zero is how a sack of rice leaves the shop for nothing.
          has_price: Boolean(u.has_price),
          sell_price: Number(u.price?.sell_price ?? 0),
          wholesale_sell_price: Number(u.price?.wholesale_sell_price ?? 0),
          mrp: Number(u.price?.mrp ?? 0),
        })),
    }));
}

/** Stores a fresh snapshot, stamped with the moment it was taken. */
export async function saveSnapshot(snapshot: Omit<Snapshot, 'key' | 'takenAt'>): Promise<Snapshot> {
  const stored: Snapshot = { ...snapshot, key: 'catalogue', takenAt: new Date().toISOString() };
  await writeOne(SNAPSHOT, stored);
  return stored;
}

/** Returns the stored snapshot, or null when the till has never been online. */
export async function loadSnapshot(): Promise<Snapshot | null> {
  try {
    return (await readOne<Snapshot>(SNAPSHOT, 'catalogue')) ?? null;
  } catch {
    // A browser with IndexedDB blocked. The till can still bill online; it just
    // cannot bill offline, and the caller says so rather than crashing.
    return null;
  }
}

/** How old the snapshot is, in hours. Infinity when there is none. */
export function ageInHours(snapshot: Snapshot | null, now = Date.now()): number {
  if (!snapshot) return Infinity;
  const taken = Date.parse(snapshot.takenAt);
  if (Number.isNaN(taken)) return Infinity;
  return Math.max(0, (now - taken) / 3_600_000);
}

/**
 * Whether the till should be warning about the age of what it is pricing from.
 *
 * It does not stop the till billing. A shop whose connection has been down for
 * two days still has to sell, and refusing would be the system choosing its own
 * tidiness over the business. It does mean every sale raised from it is flagged
 * for reconciliation, so somebody checks the prices afterwards.
 */
export function isStale(snapshot: Snapshot | null, now = Date.now()): boolean {
  return ageInHours(snapshot, now) > STALE_AFTER_HOURS;
}

/** The unit a sale means when the cashier names none. */
export function defaultUnit(product: SnapshotProduct): SnapshotUnit | undefined {
  return product.units.find((u) => u.is_default && u.has_price) ?? product.units.find((u) => u.has_price);
}

/** Finds a product by a barcode on the product or on any of its units. */
export function findByBarcode(
  snapshot: Snapshot | null,
  barcode: string
): { product: SnapshotProduct; unit: SnapshotUnit } | null {
  const code = barcode.trim();
  if (!snapshot || !code) return null;
  for (const product of snapshot.products) {
    // A unit's own barcode wins: a sack scans differently from a loose kilo,
    // and that is the whole point of giving a unit its own code.
    const unit = product.units.find((u) => u.barcode && u.barcode === code);
    if (unit) return { product, unit };
  }
  const product = snapshot.products.find((p) => p.barcode && p.barcode === code);
  if (!product) return null;
  const unit = defaultUnit(product);
  return unit ? { product, unit } : null;
}
