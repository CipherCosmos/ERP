import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { expiryApi, productApi, uomApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { DataTable, Column } from '../components/DataTable';
import { FormError, Select } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatDate, formatMoney, formatQty } from '../utils/format';

interface Batch {
  id: string;
  product_id: string;
  store_id: string;
  product_name: string;
  batch_no: string;
  quantity: number;
  reserved_qty: number;
  expiry_date: string;
  mfg_date: string | null;
  cost_price: number;
  /** Whole days from today. Negative means the batch is already past its date. */
  days_until_expiry: number;
}

interface Product {
  id: string;
  uom_id?: string;
}

interface Uom {
  id: string;
  code: string;
}

const DAY_OPTIONS = [
  { value: '7', label: 'Next 7 days' },
  { value: '14', label: 'Next 14 days' },
  { value: '30', label: 'Next 30 days' },
  { value: '60', label: 'Next 60 days' },
  { value: '90', label: 'Next 90 days' },
];

/** What a batch is worth at what it cost to buy — the money actually at stake. */
function valueOf(b: Batch): number {
  return (b.quantity || 0) * (b.cost_price || 0);
}

function sumValue(batches: Batch[]): number {
  return batches.reduce((total, b) => total + valueOf(b), 0);
}

/**
 * Days rendered as a shopkeeper would say them.
 *
 * "0 days" is the sort of thing a machine writes. The batch expiring today is
 * the one somebody has to walk to the shelf about, so it says Today.
 */
function daysLabel(days: number): string {
  if (days < 0) return days === -1 ? 'Yesterday' : `${Math.abs(days)} days ago`;
  if (days === 0) return 'Today';
  if (days === 1) return 'Tomorrow';
  return `${days} days`;
}

function daysTone(days: number): 'neutral' | 'warning' | 'danger' {
  if (days <= 3) return 'danger';
  if (days <= 7) return 'warning';
  return 'neutral';
}

/**
 * Expiry tracking, rebuilt on the shared table.
 *
 * The screen this replaces was a bare `<table>`: no search, no sorting, no
 * paging, and rows in whatever order the server sent them — which for the one
 * screen whose entire purpose is "what runs out first" is the one ordering that
 * matters and the one it did not have. It showed a quantity as `19.00` with no
 * unit, so 19 could be kilos or sacks, and it never said what any of it was
 * worth. A shopkeeper looks at this screen to decide what to discount before it
 * becomes a write-off, and that decision is a money decision.
 *
 * The two views are disjoint at the server now — `/expiry/expiring` returns
 * only batches with days_until_expiry >= 0 and `/expiry/expired` only those
 * below it — so a batch appears in exactly one of them and the totals can be
 * added up without counting anything twice. Both counts stay on screen in the
 * summary for that reason: switching view does not hide the other fact.
 */
export default function Expiry() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [expiring, setExpiring] = useState<Batch[]>([]);
  const [expired, setExpired] = useState<Batch[]>([]);
  const [products, setProducts] = useState<Product[]>([]);
  const [uoms, setUoms] = useState<Uom[]>([]);
  const [loadingSoon, setLoadingSoon] = useState(false);
  const [loadingPast, setLoadingPast] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [view, setView] = useState<'expiring' | 'expired'>('expiring');
  const [days, setDays] = useState('30');

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user, navigate]);

  const loadExpiring = useCallback(async () => {
    if (!store) return;
    setLoadingSoon(true);
    try {
      const { data } = await expiryApi.expiring({ store_id: store.id, days });
      setExpiring(data as Batch[]);
      setLoadError(null);
    } catch (err: any) {
      // The axios layer has already flattened the server envelope to a string.
      // Rendering the object here is what unmounted pages elsewhere.
      setLoadError(err?.response?.data?.error || 'Could not load batches nearing expiry.');
    } finally {
      setLoadingSoon(false);
    }
  }, [store, days]);

  const loadExpired = useCallback(async () => {
    if (!store) return;
    setLoadingPast(true);
    try {
      const { data } = await expiryApi.expired({ store_id: store.id });
      setExpired(data as Batch[]);
    } catch (err: any) {
      setLoadError(err?.response?.data?.error || 'Could not load expired batches.');
    } finally {
      setLoadingPast(false);
    }
  }, [store]);

  const loadProducts = useCallback(async () => {
    if (!store) return;
    try {
      const { data } = await productApi.list({ store_id: store.id });
      setProducts(data as unknown as Product[]);
    } catch (err) {
      console.error('Failed to load products:', err);
    }
  }, [store]);

  // Global, so this closes over nothing and keeps its identity across renders.
  const loadUoms = useCallback(async () => {
    try {
      const { data } = await uomApi.list();
      setUoms(data as Uom[]);
    } catch (err) {
      console.error('Failed to load units of measure:', err);
    }
  }, []);

  // Only loadExpiring depends on `days`, so changing the window refetches the
  // one list it changes rather than both — the expired set is not a function of
  // how far ahead you are looking.
  useEffect(() => {
    loadExpiring();
  }, [loadExpiring]);

  useEffect(() => {
    loadExpired();
    loadProducts();
    loadUoms();
  }, [loadExpired, loadProducts, loadUoms]);

  const unitById = useMemo(() => {
    const uomCode: Record<string, string> = {};
    uoms.forEach((u) => {
      uomCode[u.id] = u.code;
    });
    const map: Record<string, string> = {};
    products.forEach((p) => {
      map[p.id] = p.uom_id ? (uomCode[p.uom_id] ?? '') : '';
    });
    return map;
  }, [products, uoms]);

  const totals = useMemo(
    () => ({
      soon: expiring.length,
      soonValue: sumValue(expiring),
      past: expired.length,
      pastValue: sumValue(expired),
      // The batch a shopkeeper should deal with today, named rather than left
      // to be found by reading down the table.
      next: expiring.reduce<Batch | null>(
        (first, b) => (!first || b.days_until_expiry < first.days_until_expiry ? b : first),
        null
      ),
    }),
    [expiring, expired]
  );

  /**
   * Soonest first, which is the whole point of the screen.
   *
   * The table sorts on demand but starts in the order it is given, so the
   * ordering has to be right here. Ascending days works for both views: in the
   * near view the batch closest to expiring leads, and in the past view the one
   * that has been sitting expired longest does — the worst case either way.
   */
  const rows = useMemo(() => {
    const source = view === 'expiring' ? expiring : expired;
    return [...source].sort((a, b) => a.days_until_expiry - b.days_until_expiry);
  }, [view, expiring, expired]);

  const columns: Column<Batch>[] = [
    {
      key: 'product_name',
      header: 'Product',
      render: (b) => <span style={{ fontWeight: 600 }}>{b.product_name || 'Unnamed product'}</span>,
      value: (b) => b.product_name,
    },
    {
      key: 'batch_no',
      header: 'Batch',
      render: (b) =>
        b.batch_no ? (
          <span style={{ fontFamily: font.mono, fontSize: font.size.sm }}>{b.batch_no}</span>
        ) : (
          <span style={styles.muted}>No batch number</span>
        ),
      value: (b) => b.batch_no,
    },
    {
      key: 'quantity',
      header: 'Quantity',
      numeric: true,
      render: (b) => (
        <div>
          <div style={{ fontWeight: 600 }}>{formatQty(b.quantity, unitById[b.product_id])}</div>
          {/* Reserved stock is spoken for by an open order, so it cannot be
              discounted away — it is a different problem from the rest. */}
          {b.reserved_qty > 0 && (
            <div style={styles.sub}>{formatQty(b.reserved_qty, unitById[b.product_id])} reserved</div>
          )}
        </div>
      ),
      // The number alone: the cell carries a unit code, and sorting "9 kg"
      // against "10 kg" as text puts them the wrong way round.
      value: (b) => b.quantity,
    },
    {
      key: 'expiry_date',
      header: 'Expires',
      render: (b) => formatDate(b.expiry_date),
      // ISO first so the column sorts chronologically, the readable form after
      // it so a search for "25-09-2026" still finds the row.
      value: (b) => (b.expiry_date ? `${b.expiry_date} ${formatDate(b.expiry_date)}` : ''),
    },
    {
      key: 'days_until_expiry',
      header: view === 'expiring' ? 'Time left' : 'Expired',
      numeric: true,
      render: (b) => <span style={badge(daysTone(b.days_until_expiry))}>{daysLabel(b.days_until_expiry)}</span>,
      value: (b) => b.days_until_expiry,
    },
    {
      key: 'value',
      header: view === 'expiring' ? 'Value at risk' : 'Value lost',
      numeric: true,
      render: (b) => (
        <span style={{ fontWeight: 600, color: b.days_until_expiry < 0 ? color.danger : color.text }}>
          {formatMoney(valueOf(b))}
        </span>
      ),
      value: (b) => valueOf(b),
    },
  ];

  if (!user || user.role === 'cashier') return null;

  const windowLabel = DAY_OPTIONS.find((d) => d.value === days)?.label.replace('Next ', '') ?? `${days} days`;

  return (
    <AppLayout>
      <div style={{ ...ui.page, padding: 0 }}>
        <div style={styles.header}>
          <h1 style={styles.h1}>Batch Expiry & Shelf-Life Tracker</h1>
        </div>

        <div style={styles.summary}>
        <Stat
          label={`Expiring in ${windowLabel}`}
          value={String(totals.soon)}
          tone={totals.soon ? 'warning' : undefined}
          note={totals.next ? `soonest: ${totals.next.product_name} ${daysLabel(totals.next.days_until_expiry).toLowerCase()}` : 'nothing on the clock'}
        />
        <Stat
          label="Value at risk"
          value={formatMoney(totals.soonValue)}
          tone={totals.soonValue > 0 ? 'warning' : undefined}
          note="at what the stock cost"
        />
        <Stat
          label="Already expired"
          value={String(totals.past)}
          tone={totals.past ? 'danger' : undefined}
          note={totals.past ? 'still on the shelf' : 'shelf is clean'}
        />
        <Stat
          label="Value already lost"
          value={formatMoney(totals.pastValue)}
          tone={totals.pastValue > 0 ? 'danger' : undefined}
          note="write these off to make stock true"
        />
      </div>

      {loadError && (
        <div style={{ marginBottom: space.lg }}>
          <FormError error={loadError} />
        </div>
      )}

      <div style={styles.tabs}>
        <button
          style={view === 'expiring' ? styles.tabActive : styles.tab}
          onClick={() => setView('expiring')}
          aria-pressed={view === 'expiring'}
        >
          Still in date ({expiring.length})
        </button>
        <button
          style={view === 'expired' ? styles.tabActive : styles.tab}
          onClick={() => setView('expired')}
          aria-pressed={view === 'expired'}
        >
          Past their date ({expired.length})
        </button>
      </div>

      {/* Said once, plainly: these two lists used to overlap, so the same batch
          could be counted twice and the totals were quietly wrong. */}
      <div style={styles.note}>
        A batch is in exactly one of these lists — still in date, or past it. Nothing is counted twice, so the two
        values above add up to {formatMoney(totals.soonValue + totals.pastValue)} of stock worth watching.
      </div>

      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(b) => b.id}
        loading={view === 'expiring' ? loadingSoon : loadingPast}
        searchPlaceholder="Search product or batch…"
        // The view and the day window both narrow the rows before the table
        // sees them, so only this side can say which. "Nothing is expiring"
        // when four batches expire next month would send someone away happy.
        empty={
          view === 'expiring'
            ? `Nothing expires in the next ${windowLabel}. Look further ahead, or check what is already past its date.`
            : 'Nothing on the shelf is past its expiry date.'
        }
        // Red is for the batch you can no longer sell or nearly cannot — every
        // expired row is negative, so they all land there. Amber is the week
        // you still have to shift it in. Tinting the whole 30-day window would
        // tint every row, which is the same as tinting none of them.
        rowTone={(b) =>
          b.days_until_expiry <= 3 ? 'danger' : b.days_until_expiry <= 7 ? 'warning' : undefined
        }
        toolbar={
          view === 'expiring' ? (
            <Select
              value={days}
              onChange={(e) => setDays(e.target.value)}
              style={{ width: 'auto' }}
              options={DAY_OPTIONS}
              aria-label="How far ahead to look"
            />
          ) : (
            <button style={ui.button} onClick={() => navigate('/losses')}>
              Write these off →
            </button>
          )
        }
      />
      </div>
    </AppLayout>
  );
}

function Stat({
  label,
  value,
  note,
  tone,
}: {
  label: string;
  value: string;
  note?: string;
  tone?: 'warning' | 'danger';
}) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 10rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div
        style={{
          ...styles.statValue,
          color: tone === 'danger' ? color.danger : tone === 'warning' ? color.warning : color.text,
        }}
      >
        {value}
      </div>
      {note && <div style={styles.sub}>{note}</div>}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  header: { display: 'flex', alignItems: 'center', gap: space.lg, marginBottom: space.lg },
  h1: { margin: 0, fontSize: font.size.xxl },
  summary: { display: 'flex', gap: space.lg, marginBottom: space.lg, flexWrap: 'wrap' },
  statLabel: {
    fontSize: font.size.xs,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    color: color.textMuted,
    fontWeight: 700,
  },
  statValue: { marginTop: space.xs, fontSize: font.size.xl, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
  sub: { marginTop: space.xs, fontSize: font.size.xs, color: color.textMuted },
  muted: { color: color.textFaint },
  tabs: { display: 'flex', gap: space.xs, marginBottom: space.md },
  tab: { ...ui.button, borderRadius: '999px', padding: '0.375rem 0.875rem' },
  tabActive: { ...ui.buttonPrimary, borderRadius: '999px', padding: '0.375rem 0.875rem' },
  note: {
    padding: space.md,
    marginBottom: space.lg,
    background: color.primarySoft,
    border: `1px solid ${color.primary}22`,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
};
