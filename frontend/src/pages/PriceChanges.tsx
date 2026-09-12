import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { priceChangeApi } from '../services/api';
import { PriceMovement } from '../types';
import { DataTable, Column } from '../components/DataTable';
import { Select } from '../components/Form';
import { color, font, space, ui } from '../theme';
import { formatDate, formatMoney } from '../utils/format';

const RANGES = [
  { value: '7', label: 'Last 7 days' },
  { value: '30', label: 'Last 30 days' },
  { value: '90', label: 'Last 3 months' },
  { value: '365', label: 'Last year' },
];

/**
 * What the shop changed, and why.
 *
 * The per-product history answers "why is this ₹45 when it was ₹42 last week".
 * This answers the other half of the same question: what has been changing at
 * all. A margin that has quietly been squeezed across a whole category, or a
 * price somebody typed with a digit missing, shows up here and nowhere else.
 *
 * Entries are not paired with a previous figure the way a single product's
 * history is. These rows are from different products, so the one below is not
 * what this price used to be, and showing a difference would invent a
 * comparison. The margin is shown instead, which is a fact about the row
 * itself.
 */
export default function PriceChanges() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [days, setDays] = useState('30');
  const [changes, setChanges] = useState<PriceMovement[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const storeId = store?.id;

  const load = useCallback(async () => {
    if (!storeId) return;
    setLoading(true);
    setError(null);
    try {
      const { data } = await priceChangeApi.list(storeId, Number(days));
      setChanges(data || []);
    } catch (err: any) {
      setError(
        err?.response?.status === 403
          ? 'Price changes include cost prices, which your role cannot see.'
          : err?.response?.data?.error || 'The price changes could not be loaded.'
      );
      setChanges([]);
    } finally {
      setLoading(false);
    }
  }, [storeId, days]);

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
      return;
    }
    load();
  }, [user, navigate, load]);

  const totals = useMemo(() => {
    const products = new Set(changes.map((c) => c.product_id));
    return {
      changes: changes.length,
      products: products.size,
      typed: changes.filter((c) => c.source === 'manual').length,
      imported: changes.filter((c) => c.source === 'import').length,
      // A negative margin is selling below cost. It is sometimes deliberate and
      // always worth knowing about.
      belowCost: changes.filter((c) => c.sell_price < c.cost_price).length,
    };
  }, [changes]);

  const columns: Column<PriceMovement>[] = [
    {
      key: 'at',
      header: 'When',
      width: '10rem',
      render: (c) => (
        <div>
          <div>{formatDate(c.at)}</div>
          <div style={styles.sub}>{sourceLabel(c.source)}</div>
        </div>
      ),
      value: (c) => c.at,
    },
    {
      key: 'product_name',
      header: 'Product',
      render: (c) => (
        <div>
          <div style={{ fontWeight: 600 }}>{c.product_name || '—'}</div>
          <div style={styles.sub}>{c.unit_label}</div>
        </div>
      ),
      value: (c) => `${c.product_name} ${c.unit_label}`,
    },
    {
      key: 'sell_price',
      header: 'Selling',
      numeric: true,
      render: (c) => formatMoney(c.sell_price),
      value: (c) => c.sell_price,
    },
    {
      key: 'cost_price',
      header: 'Cost',
      numeric: true,
      render: (c) => formatMoney(c.cost_price),
      value: (c) => c.cost_price,
    },
    {
      key: 'margin',
      header: 'Margin',
      numeric: true,
      render: (c) => {
        const margin = c.sell_price - c.cost_price;
        return (
          <span style={{ fontWeight: 600, color: margin < 0 ? color.danger : color.text }}>
            {formatMoney(margin)}
          </span>
        );
      },
      value: (c) => c.sell_price - c.cost_price,
    },
    {
      key: 'who',
      header: 'Who and why',
      render: (c) => (
        <div>
          <div>{c.changed_by_name || <span style={styles.sub}>—</span>}</div>
          {c.note && <div style={styles.sub}>{c.note}</div>}
        </div>
      ),
      value: (c) => `${c.changed_by_name ?? ''} ${c.note ?? ''}`,
    },
  ];

  if (!user || user.role === 'cashier') return null;

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>Price changes</h1>
        <button style={{ ...ui.button, marginLeft: 'auto' }} onClick={() => navigate('/products')}>
          Products →
        </button>
      </div>

      <div style={styles.summary}>
        <Stat label="Changes" value={String(totals.changes)} />
        <Stat label="Products affected" value={String(totals.products)} />
        <Stat label="Typed in" value={String(totals.typed)} />
        <Stat label="From a sheet" value={String(totals.imported)} />
        <Stat
          label="Below cost"
          value={String(totals.belowCost)}
          tone={totals.belowCost ? 'danger' : undefined}
        />
      </div>

      <DataTable
        rows={changes}
        columns={columns}
        rowKey={(c) => c.id}
        loading={loading}
        searchPlaceholder="Search product, person or reason…"
        empty={error ?? 'No price has changed in this period.'}
        rowTone={(c) => (c.sell_price < c.cost_price ? 'danger' : undefined)}
        toolbar={
          <Select
            value={days}
            onChange={(e) => setDays(e.target.value)}
            options={RANGES}
            style={{ width: 'auto' }}
            aria-label="Period"
          />
        }
      />
    </div>
  );
}

function sourceLabel(source: PriceMovement['source']): string {
  switch (source) {
    case 'opening':
      return 'where it started';
    case 'import':
      return 'from a catalogue sheet';
    default:
      return 'typed in';
  }
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'danger' }) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 8rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div style={{ ...styles.statValue, color: tone === 'danger' ? color.danger : color.text }}>{value}</div>
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
  sub: { fontSize: font.size.xs, color: color.textMuted },
};
