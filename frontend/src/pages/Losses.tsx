import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { lossApi, productApi, uomApi } from '../services/api';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, NumberInput, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatDate, formatMoney, formatQty } from '../utils/format';

interface Loss {
  id: string;
  product_id: string;
  store_id: string;
  batch_no: string;
  quantity: number;
  loss_type: string;
  reason: string;
  user_id: string;
  created_at: string;
}

interface Product {
  id: string;
  name: string;
  cost_price?: number;
  stock?: number;
  /** The stocking unit. Losses are recorded in it, so it is what a quantity means. */
  uom_id?: string;
}

interface Uom {
  id: string;
  code: string;
  name: string;
}

const LOSS_TYPES = [
  { value: 'damaged', label: 'Damaged' },
  { value: 'expired', label: 'Expired' },
  { value: 'stolen', label: 'Stolen' },
  { value: 'adjustment', label: 'Adjustment' },
];

const BLANK = { product_id: '', quantity: '', loss_type: 'damaged', batch_no: '', reason: '' };

/** Tone by what the write-off says about the shop. Theft is not a mishap. */
function toneFor(type: string): 'neutral' | 'success' | 'warning' | 'danger' | 'info' {
  switch (type) {
    case 'damaged':
      return 'warning';
    case 'expired':
      return 'info';
    case 'stolen':
      return 'danger';
    case 'adjustment':
      return 'neutral';
    default:
      return 'neutral';
  }
}

function labelFor(type: string) {
  return LOSS_TYPES.find((t) => t.value === type)?.label ?? type;
}

/**
 * Inventory losses, rebuilt on the shared table and form.
 *
 * The screen this replaces listed write-offs as raw rows: a product column
 * printing the first eight characters of a uuid, a quantity with no unit and no
 * money anywhere. A shop that writes off stock is losing money, and the screen
 * for it never once said how much.
 *
 * The loss records carry no value — the API returns quantity and a product id —
 * so the value here is quantity times the product's current cost price, and the
 * column says "at cost" rather than implying it is the exact cost layer the
 * goods came out of. It is the right order of magnitude and it is the number
 * that makes a pattern of spoilage visible.
 */
export default function Losses() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [losses, setLosses] = useState<Loss[]>([]);
  const [products, setProducts] = useState<Product[]>([]);
  const [uoms, setUoms] = useState<Uom[]>([]);
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [viewing, setViewing] = useState<Loss | null>(null);
  const [form, setForm] = useState(BLANK);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [filterType, setFilterType] = useState('');
  const [period, setPeriod] = useState<'all' | '7' | '30' | 'month'>('all');

  const loadLosses = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await lossApi.list({ store_id: store.id, loss_type: filterType || undefined });
      setLosses(data as Loss[]);
    } catch (err) {
      console.error('Failed to load losses:', err);
    } finally {
      setLoading(false);
    }
  }, [store, filterType]);

  const loadProducts = useCallback(async () => {
    if (!store) return;
    try {
      const { data } = await productApi.list({ store_id: store.id });
      setProducts(data as unknown as Product[]);
    } catch (err) {
      console.error('Failed to load products:', err);
    }
  }, [store]);

  // Units of measure are global, so this closes over nothing and never changes
  // identity — the effect below cannot loop on it.
  const loadUoms = useCallback(async () => {
    try {
      const { data } = await uomApi.list();
      setUoms(data as Uom[]);
    } catch (err) {
      console.error('Failed to load units of measure:', err);
    }
  }, []);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadLosses();
    loadProducts();
    loadUoms();
  }, [user, store, navigate, loadLosses, loadProducts, loadUoms]);

  const productsById = useMemo(() => {
    const map: Record<string, Product> = {};
    products.forEach((p) => {
      map[p.id] = p;
    });
    return map;
  }, [products]);

  /**
   * The unit a write-off is counted in.
   *
   * Stock is one pool per product held in the product's stocking unit, so a
   * loss quantity is in that unit and nothing else. A bare "2" beside a sack of
   * rice is unreadable — 2 kg written off is a spill, 2 sacks is a disaster.
   */
  const unitOf = useCallback(
    (productId: string) => {
      const uomId = productsById[productId]?.uom_id;
      return uoms.find((u) => u.id === uomId)?.code ?? '';
    },
    [productsById, uoms]
  );

  const valueOf = useCallback(
    (l: Loss) => {
      const cost = productsById[l.product_id]?.cost_price;
      return cost === undefined ? undefined : cost * (l.quantity || 0);
    },
    [productsById]
  );

  const openNew = () => {
    setForm({ ...BLANK, loss_type: filterType || 'damaged' });
    setSaveError(null);
    setCreating(true);
  };

  const close = () => {
    setCreating(false);
    setSaveError(null);
  };

  const save = async () => {
    if (!store) return;
    if (!form.product_id) {
      setSaveError('Choose the product being written off.');
      return;
    }
    const quantity = parseFloat(form.quantity);
    if (!Number.isFinite(quantity) || quantity <= 0) {
      setSaveError('Enter how much is being written off.');
      return;
    }
    if (!form.loss_type) {
      setSaveError('Say what kind of loss this is.');
      return;
    }
    setSaving(true);
    setSaveError(null);
    try {
      await lossApi.create({
        product_id: form.product_id,
        store_id: store.id,
        quantity,
        loss_type: form.loss_type,
        batch_no: form.batch_no || undefined,
        reason: form.reason,
      });
      close();
      await loadLosses();
    } catch (err: any) {
      // Flattened to a string by the axios layer — "quantity must be greater
      // than zero", or the stock shortfall the write-off ran into.
      setSaveError(err?.response?.data?.error || 'Could not record this loss.');
    } finally {
      setSaving(false);
    }
  };

  const since = useMemo(() => {
    if (period === 'all') return null;
    const from = new Date();
    if (period === 'month') from.setDate(1);
    else from.setDate(from.getDate() - (period === '7' ? 6 : 29));
    from.setHours(0, 0, 0, 0);
    return from;
  }, [period]);

  const rows = useMemo(() => {
    if (!since) return losses;
    return losses.filter((l) => l.created_at && new Date(l.created_at) >= since);
  }, [losses, since]);

  const totals = useMemo(() => {
    let value = 0;
    let unknown = 0;
    const byType: Record<string, number> = {};
    rows.forEach((l) => {
      const v = valueOf(l);
      if (v === undefined) unknown += 1;
      else {
        value += v;
        byType[l.loss_type] = (byType[l.loss_type] || 0) + v;
      }
    });
    const worst = Object.entries(byType).reduce<[string, number] | null>(
      (top, entry) => (!top || entry[1] > top[1] ? entry : top),
      null
    );
    return { value, unknown, worst };
  }, [rows, valueOf]);

  const periodLabel =
    period === 'all' ? 'all time' : period === 'month' ? 'this month' : `last ${period} days`;
  const windowLabel = period === 'month' ? 'month so far' : periodLabel;

  // Named for whichever filters are actually narrowing the list, so the empty
  // table says what to change rather than implying the shelves are clean.
  const emptyMessage = filterType
    ? period === 'all'
      ? `No ${labelFor(filterType).toLowerCase()} write-offs recorded.`
      : `No ${labelFor(filterType).toLowerCase()} write-offs in the ${windowLabel}.`
    : period === 'all'
      ? 'Nothing written off yet. Record damage or expiry here so stock stays true.'
      : `Nothing written off in the ${windowLabel}.`;

  const selectedProduct = productsById[form.product_id];
  const selectedUnit = unitOf(form.product_id);
  const estimate =
    selectedProduct?.cost_price !== undefined && parseFloat(form.quantity) > 0
      ? selectedProduct.cost_price * parseFloat(form.quantity)
      : undefined;

  const columns: Column<Loss>[] = [
    {
      key: 'created_at',
      header: 'Date',
      render: (l) => formatDate(l.created_at),
      // ISO first so the column sorts chronologically, readable form after it
      // so a search for "10-09-2026" still matches.
      value: (l) => (l.created_at ? `${l.created_at} ${formatDate(l.created_at)}` : ''),
    },
    {
      key: 'product',
      header: 'Product',
      render: (l) => {
        const p = productsById[l.product_id];
        return p ? (
          <span style={{ fontWeight: 600 }}>{p.name}</span>
        ) : (
          <span style={styles.muted} title={l.product_id}>
            Unknown product
          </span>
        );
      },
      value: (l) => productsById[l.product_id]?.name ?? l.product_id,
    },
    {
      key: 'loss_type',
      header: 'Type',
      render: (l) => <span style={badge(toneFor(l.loss_type))}>{labelFor(l.loss_type)}</span>,
      value: (l) => labelFor(l.loss_type),
    },
    {
      key: 'batch_no',
      header: 'Batch',
      render: (l) => l.batch_no || <span style={styles.muted}>—</span>,
      value: (l) => l.batch_no,
    },
    {
      key: 'quantity',
      header: 'Qty',
      numeric: true,
      render: (l) => formatQty(l.quantity, unitOf(l.product_id)),
      // Sorts on the number: the rendered cell carries a unit code, and
      // "19 kg" against "5 NOS" would sort alphabetically.
      value: (l) => l.quantity,
    },
    {
      key: 'value',
      header: 'Value at cost',
      numeric: true,
      render: (l) => {
        const v = valueOf(l);
        if (v === undefined) return <span style={styles.muted}>—</span>;
        return <span style={{ fontWeight: 600, color: color.danger }}>{formatMoney(v)}</span>;
      },
      value: (l) => valueOf(l) ?? null,
    },
    {
      key: 'reason',
      header: 'Reason',
      render: (l) => l.reason || <span style={styles.muted}>No reason given</span>,
      value: (l) => l.reason,
    },
  ];

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>Inventory losses</h1>
      </div>

      <div style={styles.summary}>
        <Stat label="Written off" value={String(rows.length)} note={periodLabel} />
        <Stat
          label="Value at cost"
          value={formatMoney(totals.value)}
          tone={totals.value > 0 ? 'danger' : undefined}
          note={totals.unknown ? `${totals.unknown} row(s) have no product to price` : undefined}
        />
        <Stat
          label="Costliest cause"
          value={totals.worst ? labelFor(totals.worst[0]) : '—'}
          note={totals.worst ? formatMoney(totals.worst[1]) : undefined}
        />
      </div>

      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(l) => l.id}
        loading={loading}
        onRowClick={setViewing}
        searchPlaceholder="Search product, batch, type or reason…"
        // Both filters run before the table sees the rows — the type one at the
        // server, the period one here — so only this side can say which of them
        // emptied the list. "Nothing written off yet" would read as a clean
        // shelf when it means "no expired stock in the last seven days".
        empty={emptyMessage}
        toolbar={
          <>
            <Select
              value={filterType}
              onChange={(e) => setFilterType(e.target.value)}
              style={{ width: 'auto' }}
              placeholder="All types"
              options={LOSS_TYPES}
            />
            <Select
              value={period}
              onChange={(e) => setPeriod(e.target.value as typeof period)}
              style={{ width: 'auto' }}
              options={[
                { value: 'all', label: 'All time' },
                { value: 'month', label: 'This month' },
                { value: '7', label: 'Last 7 days' },
                { value: '30', label: 'Last 30 days' },
              ]}
            />
            <button style={ui.buttonPrimary} onClick={openNew}>
              + Record loss
            </button>
          </>
        }
      />

      <Modal
        open={creating}
        title="Record a write-off"
        subtitle="This takes the goods off the shelf immediately and is attributed to you."
        onClose={close}
        footer={
          <>
            <button style={ui.button} onClick={close} disabled={saving}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={save} disabled={saving}>
              {saving ? 'Recording…' : 'Record loss'}
            </button>
          </>
        }
      >
        <FormError error={saveError} />

        <Field label="Product" required htmlFor="l-product">
          <Select
            id="l-product"
            value={form.product_id}
            onChange={(e) => setForm({ ...form, product_id: e.target.value })}
            placeholder="Choose a product"
            options={products.map((p) => ({
              value: p.id,
              label: `${p.name} · ${formatQty(p.stock, unitOf(p.id), '0')} in stock`,
            }))}
          />
        </Field>

        <FormGrid>
          <Field
            label={selectedUnit ? `Quantity (${selectedUnit})` : 'Quantity'}
            required
            hint={
              selectedProduct
                ? `${formatQty(selectedProduct.stock, selectedUnit, '0')} in stock · ${formatMoney(
                    selectedProduct.cost_price
                  )} per ${selectedUnit || 'unit'} at cost`
                : undefined
            }
            htmlFor="l-qty"
          >
            <NumberInput
              id="l-qty"
              value={form.quantity}
              min={0}
              onChange={(e) => setForm({ ...form, quantity: e.target.value })}
            />
          </Field>
          <Field label="Type" required htmlFor="l-type">
            <Select
              id="l-type"
              value={form.loss_type}
              onChange={(e) => setForm({ ...form, loss_type: e.target.value })}
              options={LOSS_TYPES}
            />
          </Field>
        </FormGrid>

        <FormGrid>
          {/* What the server actually does with this: it is a hint to the FEFO
              walk, not an instruction, and the batch written onto the record is
              whichever one the stock came out of — several, if the write-off is
              bigger than any one of them. Saying "the batch to write off" would
              promise control the caller does not have. */}
          <Field
            label="Batch"
            hint="Optional starting point. Stock is taken first-expiring-first, and the batch it came from is what gets recorded."
            htmlFor="l-batch"
          >
            <TextInput
              id="l-batch"
              value={form.batch_no}
              onChange={(e) => setForm({ ...form, batch_no: e.target.value })}
            />
          </Field>
          <Field label="Reason" hint="What happened. An unexplained write-off looks like theft later." htmlFor="l-reason">
            <TextInput
              id="l-reason"
              value={form.reason}
              onChange={(e) => setForm({ ...form, reason: e.target.value })}
            />
          </Field>
        </FormGrid>

        {estimate !== undefined && (
          <div style={styles.estimate}>
            This write-off costs about <strong>{formatMoney(estimate)}</strong> at the product's current cost price.
          </div>
        )}
      </Modal>

      <Modal
        open={!!viewing}
        title={viewing ? (productsById[viewing.product_id]?.name ?? 'Write-off') : ''}
        subtitle={viewing ? `${labelFor(viewing.loss_type)} · ${formatDate(viewing.created_at)}` : undefined}
        onClose={() => setViewing(null)}
        width="32rem"
        footer={
          <button style={ui.button} onClick={() => setViewing(null)}>
            Close
          </button>
        }
      >
        {viewing && (
          <>
            <div style={styles.detailGrid}>
              <div>
                <div style={styles.statLabel}>Quantity</div>
                <div style={styles.detailValue}>{formatQty(viewing.quantity, unitOf(viewing.product_id))}</div>
              </div>
              <div>
                <div style={styles.statLabel}>Value at cost</div>
                <div style={styles.detailValue}>{formatMoney(valueOf(viewing))}</div>
              </div>
              <div>
                <div style={styles.statLabel}>Batch</div>
                <div style={styles.detailValue}>{viewing.batch_no || '—'}</div>
              </div>
            </div>
            <div>
              <div style={styles.statLabel}>Reason</div>
              <div>{viewing.reason || 'No reason given'}</div>
            </div>
            <div style={styles.warn}>
              A write-off cannot be edited or reversed here — it has already moved the stock. Correct one with a
              stock adjustment in Inventory.
            </div>
          </>
        )}
      </Modal>
    </div>
  );
}

function Stat({ label, value, note, tone }: { label: string; value: string; note?: string; tone?: 'danger' }) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 10rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div style={{ ...styles.statValue, color: tone === 'danger' ? color.danger : color.text }}>{value}</div>
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
  sub: { fontSize: font.size.xs, color: color.textMuted },
  muted: { color: color.textFaint },
  detailGrid: { display: 'flex', gap: space.xl, flexWrap: 'wrap' },
  detailValue: { marginTop: space.xs, fontSize: font.size.lg, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
  estimate: {
    padding: space.md,
    background: color.surfaceAlt,
    border: `1px solid ${color.border}`,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
  warn: {
    padding: space.md,
    background: color.warningSoft,
    border: `1px solid ${color.warning}33`,
    color: color.warning,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
};
