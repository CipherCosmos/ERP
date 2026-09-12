import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { purchaseApi, stockApi, terminalApi } from '../services/api';
import { ReorderItem } from '../types';
import { Column, DataTable } from '../components/DataTable';
import { FormError, NumberInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatMoney, formatQty, todayISO } from '../utils/format';

/** A supplier's worth of the reorder list. */
interface Group {
  vendorId: string | null;
  vendorName: string;
  items: ReorderItem[];
}

/** The id used for the pile of products nobody has named a supplier for. */
const NO_VENDOR = '';

/**
 * What is running out, grouped by who supplies it.
 *
 * The product form has asked for a preferred supplier since the day the column
 * was added, and its own comment says what for: so a low-stock list can become
 * an order rather than a list of names to look up one at a time. Nothing used
 * it. The low-stock report was a product name, a number on the shelf and a
 * number it should not go below, which leaves a buyer three lookups per line —
 * who supplies this, what does a pack hold, what does one cost — on a list that
 * is forty lines long the morning after a good weekend.
 *
 * Quantities are in the unit the shop buys in, not the unit it sells in. A
 * reorder line reads "3 sacks", not "63.5 kg", because 63.5 kg is not something
 * anybody can order. Every line is editable: the suggestion is a starting
 * figure, and the shop knows its own turnover.
 */
export default function Reorder() {
  const navigate = useNavigate();
  const { user, store, terminal } = useAppStore();

  const [items, setItems] = useState<ReorderItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [placing, setPlacing] = useState<string | null>(null);
  const [placed, setPlaced] = useState<string | null>(null);
  const [terminalId, setTerminalId] = useState(terminal?.id || '');

  // Edited order quantities, keyed by product. Absent means "as suggested".
  const [qty, setQty] = useState<Record<string, string>>({});

  const load = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    setError(null);
    try {
      const { data } = await stockApi.low(store.id);
      setItems(data || []);
      setQty({});
    } catch (err: any) {
      setError(err?.response?.data?.error || 'The reorder list could not be loaded.');
      setItems([]);
    } finally {
      setLoading(false);
    }
  }, [store]);

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
      return;
    }
    load();
  }, [user, navigate, load]);

  // A purchase order is numbered from a terminal's own series, so the server
  // requires one. The session usually carries it; an older session, or a login
  // that could not reach the terminal endpoint, does not.
  useEffect(() => {
    if (!store || terminal?.id) {
      if (terminal?.id) setTerminalId(terminal.id);
      return;
    }
    let cancelled = false;
    terminalApi
      .list(store.id)
      .then((res) => {
        if (!cancelled) setTerminalId(((res.data || []).find((t) => t.is_active) || null)?.id || '');
      })
      .catch(() => {
        if (!cancelled) setTerminalId('');
      });
    return () => {
      cancelled = true;
    };
  }, [store, terminal]);

  const orderQty = useCallback(
    (item: ReorderItem) => {
      const edited = qty[item.product_id];
      if (edited === undefined) return item.suggested_units;
      const n = Number(edited);
      return Number.isFinite(n) && n > 0 ? n : 0;
    },
    [qty]
  );

  /** Can this line become an order line at all? */
  const orderable = useCallback(
    (item: ReorderItem) => !!item.purchase_unit_id && item.last_cost > 0 && orderQty(item) > 0,
    [orderQty]
  );

  const groups = useMemo<Group[]>(() => {
    const byVendor = new Map<string, Group>();
    items.forEach((item) => {
      const key = item.preferred_vendor_id || NO_VENDOR;
      if (!byVendor.has(key)) {
        byVendor.set(key, {
          vendorId: item.preferred_vendor_id || null,
          vendorName: item.preferred_vendor_name || 'No supplier named',
          items: [],
        });
      }
      byVendor.get(key)!.items.push(item);
    });
    // Named suppliers first, alphabetically; the unassigned pile last, because
    // it is the one that needs a different kind of work.
    return Array.from(byVendor.values()).sort((a, b) => {
      if (!a.vendorId) return 1;
      if (!b.vendorId) return -1;
      return a.vendorName.localeCompare(b.vendorName);
    });
  }, [items]);

  const totals = useMemo(() => {
    const value = items.reduce((sum, i) => sum + orderQty(i) * i.last_cost, 0);
    return {
      lines: items.length,
      suppliers: new Set(items.filter((i) => i.preferred_vendor_id).map((i) => i.preferred_vendor_id)).size,
      unassigned: items.filter((i) => !i.preferred_vendor_id).length,
      unpriced: items.filter((i) => i.last_cost <= 0).length,
      value,
    };
  }, [items, orderQty]);

  const placeOrder = async (group: Group) => {
    if (!store || !group.vendorId) return;
    if (!terminalId) {
      setError('This counter has no terminal, and a purchase order is numbered from one.');
      return;
    }
    const lines = group.items.filter(orderable);
    if (!lines.length) {
      setError('Nothing in this group can be ordered yet — check the quantities and costs.');
      return;
    }

    setPlacing(group.vendorId);
    setError(null);
    try {
      await purchaseApi.create({
        store_id: store.id,
        vendor_id: group.vendorId,
        terminal_id: terminalId,
        order_date: todayISO(),
        items: lines.map((item) => ({
          product_id: item.product_id,
          product_unit_id: item.purchase_unit_id,
          unit_qty: orderQty(item),
          // The last cost, which is what the shop paid rather than what it
          // expects to pay. The buyer edits the order before sending it.
          rate: item.last_cost,
        })),
      });
      setPlaced(`${lines.length} ${lines.length === 1 ? 'line' : 'lines'} ordered from ${group.vendorName}.`);
      await load();
    } catch (err: any) {
      setError(err?.response?.data?.error || `Could not raise an order for ${group.vendorName}.`);
    } finally {
      setPlacing(null);
    }
  };

  const columns: Column<ReorderItem>[] = [
    {
      key: 'name',
      header: 'Product',
      render: (i) => (
        <div>
          <div style={{ fontWeight: 600 }}>{i.name}</div>
          <div style={styles.sub}>
            {formatQty(i.current_stock)} on the shelf, reorder at {formatQty(i.min_stock)}
          </div>
        </div>
      ),
      value: (i) => i.name,
    },
    {
      key: 'purchase_unit_label',
      header: 'Ordered as',
      render: (i) =>
        i.purchase_unit_id ? (
          <div>
            <div>{i.purchase_unit_label}</div>
            <div style={styles.sub}>{formatQty(i.qty_in_base)} each</div>
          </div>
        ) : (
          <span style={styles.problem}>nothing to order it in</span>
        ),
      value: (i) => i.purchase_unit_label ?? '',
    },
    {
      key: 'suggested_units',
      header: 'Order',
      numeric: true,
      width: '9rem',
      sortable: false,
      render: (i) =>
        i.purchase_unit_id ? (
          <NumberInput
            min={0}
            step="any"
            value={qty[i.product_id] ?? String(i.suggested_units)}
            onChange={(e) => setQty({ ...qty, [i.product_id]: e.target.value })}
            style={{ width: '6rem', textAlign: 'right' }}
            aria-label={`Order quantity for ${i.name}`}
          />
        ) : (
          <span style={styles.muted}>—</span>
        ),
    },
    {
      key: 'last_cost',
      header: 'Last cost',
      numeric: true,
      render: (i) =>
        i.last_cost > 0 ? formatMoney(i.last_cost) : <span style={styles.problem}>never bought</span>,
      value: (i) => i.last_cost,
    },
    {
      key: 'value',
      header: 'Value',
      numeric: true,
      render: (i) =>
        orderable(i) ? (
          <span style={{ fontWeight: 600 }}>{formatMoney(orderQty(i) * i.last_cost)}</span>
        ) : (
          <span style={styles.muted}>—</span>
        ),
      value: (i) => orderQty(i) * i.last_cost,
    },
  ];

  if (!user || user.role === 'cashier') return null;

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>What to reorder</h1>
        <button style={{ ...ui.button, marginLeft: 'auto' }} onClick={() => navigate('/purchases')}>
          Purchase orders →
        </button>
      </div>

      <FormError error={error} />
      {placed && (
        <div style={styles.done} role="status">
          {placed}{' '}
          <button style={styles.link} onClick={() => navigate('/purchases')}>
            See the order →
          </button>
        </div>
      )}

      <div style={styles.summary}>
        <Stat label="Lines" value={String(totals.lines)} />
        <Stat label="Suppliers" value={String(totals.suppliers)} />
        <Stat
          label="No supplier named"
          value={String(totals.unassigned)}
          tone={totals.unassigned ? 'warning' : undefined}
          note={totals.unassigned ? 'set one on the product' : undefined}
        />
        <Stat
          label="Never bought"
          value={String(totals.unpriced)}
          tone={totals.unpriced ? 'warning' : undefined}
          note={totals.unpriced ? 'no cost to order at' : undefined}
        />
        <Stat label="Order value" value={formatMoney(totals.value)} />
      </div>

      {!loading && !items.length && (
        <div style={styles.empty}>
          Nothing is at its reorder level. A product only appears here once somebody has set one on it.
        </div>
      )}

      {groups.map((group) => {
        const orderableLines = group.items.filter(orderable);
        const value = orderableLines.reduce((sum, i) => sum + orderQty(i) * i.last_cost, 0);
        return (
          <section key={group.vendorId || NO_VENDOR} style={styles.section}>
            <div style={styles.sectionHead}>
              <h2 style={styles.h2}>{group.vendorName}</h2>
              {group.vendorId ? (
                <span style={badge('info')}>
                  {orderableLines.length} of {group.items.length} ready
                </span>
              ) : (
                <span style={badge('warning')}>needs a supplier before it can be ordered</span>
              )}
              <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: space.md }}>
                {group.vendorId && <span style={styles.groupValue}>{formatMoney(value)}</span>}
                {group.vendorId && (
                  <button
                    style={ui.buttonPrimary}
                    onClick={() => placeOrder(group)}
                    disabled={!!placing || !orderableLines.length}
                  >
                    {placing === group.vendorId ? 'Raising…' : 'Raise a purchase order'}
                  </button>
                )}
              </div>
            </div>
            <DataTable
              rows={group.items}
              columns={columns}
              rowKey={(i) => i.product_id}
              loading={loading}
              searchable={false}
              empty="Nothing here."
              rowTone={(i) => (orderable(i) ? undefined : 'warning')}
            />
          </section>
        );
      })}
    </div>
  );
}

function Stat({
  label,
  value,
  tone,
  note,
}: {
  label: string;
  value: string;
  tone?: 'warning';
  note?: string;
}) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 9rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div style={{ ...styles.statValue, color: tone === 'warning' ? color.warning : color.text }}>{value}</div>
      {note && <div style={styles.sub}>{note}</div>}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  header: { display: 'flex', alignItems: 'center', gap: space.lg, marginBottom: space.lg },
  h1: { margin: 0, fontSize: font.size.xxl },
  h2: { margin: 0, fontSize: font.size.lg },
  section: { marginTop: space.xl },
  sectionHead: { display: 'flex', alignItems: 'center', gap: space.md, marginBottom: space.sm, flexWrap: 'wrap' },
  groupValue: { fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
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
  muted: { color: color.textFaint },
  problem: { color: color.warning, fontSize: font.size.sm },
  empty: { ...ui.card, padding: space.xl, color: color.textMuted, fontSize: font.size.md },
  done: {
    padding: space.lg,
    marginBottom: space.lg,
    background: color.successSoft,
    border: `1px solid ${color.success}33`,
    color: color.success,
    borderRadius: '8px',
    fontSize: font.size.md,
  },
  link: {
    background: 'none',
    border: 'none',
    color: 'inherit',
    textDecoration: 'underline',
    cursor: 'pointer',
    font: 'inherit',
    padding: 0,
  },
};
