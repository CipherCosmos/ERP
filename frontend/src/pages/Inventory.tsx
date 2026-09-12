import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { productApi, stockApi, uomApi } from '../services/api';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, NumberInput, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatDate, formatMoney, formatQty } from '../utils/format';

interface Batch {
  id: string;
  product_id: string;
  product_name: string;
  batch_no: string;
  quantity: number;
  reserved_qty: number;
  expiry_date: string | null;
  mfg_date: string | null;
  cost_price: number;
}

interface Adjustment {
  id: string;
  product_id: string;
  product_name: string;
  batch_no: string;
  quantity: number;
  adjustment_type: string;
  reason: string;
  created_at: string;
}

interface ProductUnitOption {
  id: string;
  label: string;
  qty_in_base: number;
  is_purchasable?: boolean;
  is_active?: boolean;
}

interface ProductLite {
  id: string;
  name: string;
  uom_id: string;
  min_stock: number;
  stock: number;
  units?: { unit: ProductUnitOption }[];
  default_product_unit_id?: string;
}

const ADJUST_TYPES = [
  { value: 'add', label: 'Stock in — goods arrived' },
  { value: 'remove', label: 'Stock out — correction' },
  { value: 'loss', label: 'Write off — damaged or expired' },
];

/**
 * Stock.
 *
 * Stock is one pool per product per store, counted in that product's base unit.
 * The screen's job is to make that pool legible: what is on hand, what is
 * running out, what is about to expire, and an honest history of every movement.
 *
 * The adjustment form is where the units work matters most on this screen. Goods
 * arrive as sacks, and a storekeeper booking in a delivery should be able to say
 * "eight sacks" rather than working out that it is two hundred kilos — which is
 * exactly the arithmetic people get wrong at the end of a long day.
 */
export default function Inventory() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [tab, setTab] = useState<'stock' | 'low' | 'history'>('stock');
  const [batches, setBatches] = useState<Batch[]>([]);
  const [lowStock, setLowStock] = useState<any[]>([]);
  const [adjustments, setAdjustments] = useState<Adjustment[]>([]);
  const [products, setProducts] = useState<ProductLite[]>([]);
  const [uoms, setUoms] = useState<{ id: string; code: string }[]>([]);
  const [loading, setLoading] = useState(false);

  const [adjustOpen, setAdjustOpen] = useState(false);
  const [form, setForm] = useState({
    product_id: '',
    product_unit_id: '',
    unit_qty: '',
    type: 'add',
    batch_no: '',
    reason: '',
    cost_price: '',
    expiry_date: '',
  });
  const [adjustError, setAdjustError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const loadStock = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await stockApi.list(store.id);
      setBatches(data as Batch[]);
    } catch (err) {
      console.error('Failed to load stock:', err);
    } finally {
      setLoading(false);
    }
  }, [store]);

  const loadLowStock = useCallback(async () => {
    if (!store) return;
    try {
      const { data } = await stockApi.low(store.id);
      setLowStock(data as any[]);
    } catch (err) {
      console.error('Failed to load low stock:', err);
    }
  }, [store]);

  const loadAdjustments = useCallback(async () => {
    if (!store) return;
    try {
      const { data } = await stockApi.adjustments(store.id);
      setAdjustments(data as Adjustment[]);
    } catch (err) {
      console.error('Failed to load stock history:', err);
    }
  }, [store]);

  const loadProducts = useCallback(async () => {
    if (!store) return;
    try {
      const { data } = await productApi.list({ store_id: store.id });
      setProducts(data as unknown as ProductLite[]);
    } catch (err) {
      console.error('Failed to load products:', err);
    }
  }, [store]);

  const loadUoms = useCallback(async () => {
    try {
      const { data } = await uomApi.list();
      setUoms(data as { id: string; code: string }[]);
    } catch (err) {
      console.error('Failed to load units:', err);
    }
  }, []);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadStock();
    loadLowStock();
    loadAdjustments();
    loadProducts();
    loadUoms();
  }, [user, store, navigate, loadStock, loadLowStock, loadAdjustments, loadProducts, loadUoms]);

  const productById = useMemo(() => new Map(products.map((p) => [p.id, p])), [products]);
  const unitCodeOf = useCallback(
    (productId: string) => uoms.find((u) => u.id === productById.get(productId)?.uom_id)?.code ?? '',
    [uoms, productById]
  );

  /** The units the chosen product may be booked in. */
  const formUnits = useMemo(() => {
    const p = productById.get(form.product_id);
    return (p?.units ?? [])
      .map((x) => x.unit)
      .filter((u) => u.is_active !== false);
  }, [form.product_id, productById]);

  const chosenUnit = formUnits.find((u) => u.id === form.product_unit_id);
  const baseEquivalent =
    chosenUnit && Number(form.unit_qty) > 0 ? Number(form.unit_qty) * chosenUnit.qty_in_base : null;

  const openAdjust = () => {
    setForm({
      product_id: '',
      product_unit_id: '',
      unit_qty: '',
      type: 'add',
      batch_no: '',
      reason: '',
      cost_price: '',
      expiry_date: '',
    });
    setAdjustError(null);
    setAdjustOpen(true);
  };

  const chooseProduct = (id: string) => {
    const p = productById.get(id);
    const units = (p?.units ?? []).map((x) => x.unit).filter((u) => u.is_active !== false);
    // Default to the product's own default unit, so the common case needs no
    // second decision.
    const preferred = units.find((u) => u.id === p?.default_product_unit_id) ?? units[0];
    setForm((f) => ({ ...f, product_id: id, product_unit_id: preferred?.id ?? '' }));
  };

  const submitAdjust = async () => {
    if (!store) return;
    if (!form.product_id) {
      setAdjustError('Choose a product.');
      return;
    }
    const qty = Number(form.unit_qty);
    if (!Number.isFinite(qty) || qty <= 0) {
      setAdjustError('Enter how much is moving.');
      return;
    }
    if (!form.reason.trim()) {
      setAdjustError('Say why — this is the only record of it.');
      return;
    }
    setBusy(true);
    setAdjustError(null);
    try {
      await stockApi.adjust({
        product_id: form.product_id,
        store_id: store.id,
        type: form.type,
        reason: form.reason.trim(),
        batch_no: form.batch_no.trim() || undefined,
        cost_price: form.cost_price ? Number(form.cost_price) : undefined,
        expiry_date: form.expiry_date || undefined,
        // Say it in the unit the storekeeper counted in; the server converts.
        ...(form.product_unit_id
          ? { product_unit_id: form.product_unit_id, unit_qty: qty }
          : { quantity: qty }),
      });
      setAdjustOpen(false);
      await Promise.all([loadStock(), loadLowStock(), loadAdjustments(), loadProducts()]);
    } catch (err: any) {
      setAdjustError(err?.response?.data?.error || 'Could not record this movement.');
    } finally {
      setBusy(false);
    }
  };

  const totals = useMemo(() => {
    const value = batches.reduce((s, b) => s + (b.quantity || 0) * (b.cost_price || 0), 0);
    const reserved = batches.reduce((s, b) => s + (b.reserved_qty || 0), 0);
    const expiringSoon = batches.filter((b) => {
      if (!b.expiry_date) return false;
      const days = (Date.parse(b.expiry_date.slice(0, 10)) - Date.parse(new Date().toISOString().slice(0, 10))) / 86_400_000;
      return days >= 0 && days <= 30;
    }).length;
    return { value, reserved, expiringSoon };
  }, [batches]);

  const daysToExpiry = (iso: string | null) => {
    if (!iso) return null;
    return Math.round((Date.parse(iso.slice(0, 10)) - Date.parse(new Date().toISOString().slice(0, 10))) / 86_400_000);
  };

  const stockColumns: Column<Batch>[] = [
    { key: 'product_name', header: 'Product', render: (b) => <span style={{ fontWeight: 600 }}>{b.product_name}</span>, value: (b) => b.product_name },
    {
      key: 'batch_no',
      header: 'Batch',
      render: (b) => <span style={{ fontFamily: font.mono, fontSize: font.size.sm }}>{b.batch_no || '—'}</span>,
      value: (b) => b.batch_no,
    },
    {
      key: 'quantity',
      header: 'On hand',
      numeric: true,
      render: (b) => <span style={{ fontWeight: 600 }}>{formatQty(b.quantity, unitCodeOf(b.product_id))}</span>,
      value: (b) => b.quantity,
    },
    {
      key: 'reserved_qty',
      header: 'Reserved',
      numeric: true,
      // Reserved stock is on the shelf but already spoken for. Showing it as a
      // separate column stops someone selling the same goods twice.
      render: (b) => (b.reserved_qty > 0 ? formatQty(b.reserved_qty, unitCodeOf(b.product_id)) : <span style={styles.muted}>—</span>),
      value: (b) => b.reserved_qty,
    },
    {
      key: 'expiry_date',
      header: 'Expires',
      render: (b) => {
        const d = daysToExpiry(b.expiry_date);
        if (d === null) return <span style={styles.muted}>—</span>;
        const tone = d < 0 ? 'danger' : d <= 30 ? 'warning' : 'neutral';
        return (
          <span>
            {formatDate(b.expiry_date)}
            <span style={{ ...badge(tone), marginLeft: space.xs }}>{d < 0 ? `${-d}d ago` : `${d}d`}</span>
          </span>
        );
      },
      value: (b) => b.expiry_date ?? '',
    },
    { key: 'cost_price', header: 'Cost', numeric: true, render: (b) => formatMoney(b.cost_price), value: (b) => b.cost_price },
    {
      key: 'value',
      header: 'Value',
      numeric: true,
      render: (b) => <span style={{ fontWeight: 600 }}>{formatMoney((b.quantity || 0) * (b.cost_price || 0))}</span>,
      value: (b) => (b.quantity || 0) * (b.cost_price || 0),
    },
  ];

  const historyColumns: Column<Adjustment>[] = [
    { key: 'created_at', header: 'When', render: (a) => formatDate(a.created_at), value: (a) => a.created_at },
    { key: 'product_name', header: 'Product', value: (a) => a.product_name },
    {
      key: 'adjustment_type',
      header: 'Movement',
      render: (a) => {
        const tone = a.adjustment_type === 'add' || a.adjustment_type === 'transfer_in' ? 'success'
          : a.adjustment_type === 'loss' ? 'danger' : 'neutral';
        return <span style={badge(tone)}>{a.adjustment_type.replace('_', ' ')}</span>;
      },
      value: (a) => a.adjustment_type,
    },
    {
      key: 'quantity',
      header: 'Quantity',
      numeric: true,
      render: (a) => formatQty(a.quantity, unitCodeOf(a.product_id)),
      value: (a) => a.quantity,
    },
    {
      key: 'batch_no',
      header: 'Batch',
      render: (a) => <span style={{ fontFamily: font.mono, fontSize: font.size.sm }}>{a.batch_no || '—'}</span>,
      value: (a) => a.batch_no,
    },
    // The reason is the only account of why stock moved, and the server writes
    // the counted unit into it ("8 x 25 kg sack"), so it is worth the width.
    { key: 'reason', header: 'Reason', render: (a) => a.reason || <span style={styles.muted}>—</span>, value: (a) => a.reason },
  ];

  const lowColumns: Column<any>[] = [
    { key: 'product_name', header: 'Product', render: (p) => <span style={{ fontWeight: 600 }}>{p.product_name ?? p.name}</span>, value: (p) => p.product_name ?? p.name },
    {
      key: 'quantity',
      header: 'On hand',
      numeric: true,
      render: (p) => <span style={{ color: color.danger, fontWeight: 700 }}>{formatQty(p.quantity ?? p.stock, unitCodeOf(p.product_id ?? p.id))}</span>,
      value: (p) => p.quantity ?? p.stock,
    },
    { key: 'min_stock', header: 'Reorder at', numeric: true, render: (p) => formatQty(p.min_stock, unitCodeOf(p.product_id ?? p.id)), value: (p) => p.min_stock },
  ];

  const TABS: { id: typeof tab; label: string; count?: number }[] = [
    { id: 'stock', label: 'On hand', count: batches.length },
    { id: 'low', label: 'Running out', count: lowStock.length },
    { id: 'history', label: 'Movements', count: adjustments.length },
  ];

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>← Back</button>
        <h1 style={styles.h1}>Stock</h1>
        <button style={{ ...ui.buttonPrimary, marginLeft: 'auto' }} onClick={openAdjust}>
          Record a movement
        </button>
      </div>

      <div style={styles.summary}>
        <Stat label="Stock value at cost" value={formatMoney(totals.value)} />
        <Stat label="Batches" value={String(batches.length)} />
        <Stat label="Running out" value={String(lowStock.length)} tone={lowStock.length ? 'danger' : undefined} />
        <Stat label="Expiring within 30 days" value={String(totals.expiringSoon)} tone={totals.expiringSoon ? 'warning' : undefined} />
      </div>

      <div style={styles.tabs}>
        {TABS.map((x) => (
          <button key={x.id} style={tab === x.id ? styles.tabActive : styles.tab} onClick={() => setTab(x.id)}>
            {x.label}
            {x.count !== undefined && <span style={styles.tabCount}> {x.count}</span>}
          </button>
        ))}
      </div>

      {tab === 'stock' && (
        <DataTable
          rows={batches}
          columns={stockColumns}
          rowKey={(b) => b.id}
          loading={loading}
          searchPlaceholder="Search product or batch…"
          empty="No stock recorded yet. Record a movement, or receive against a purchase order."
          rowTone={(b) => {
            const d = daysToExpiry(b.expiry_date);
            if (d === null) return undefined;
            return d < 0 ? 'danger' : d <= 30 ? 'warning' : undefined;
          }}
        />
      )}

      {tab === 'low' && (
        <DataTable
          rows={lowStock}
          columns={lowColumns}
          rowKey={(p) => p.product_id ?? p.id}
          searchPlaceholder="Search product…"
          empty="Nothing is below its reorder level."
          rowTone={() => 'warning'}
        />
      )}

      {tab === 'history' && (
        <DataTable
          rows={adjustments}
          columns={historyColumns}
          rowKey={(a) => a.id}
          searchPlaceholder="Search product, batch or reason…"
          empty="No stock movements recorded yet."
        />
      )}

      <Modal
        open={adjustOpen}
        title="Record a stock movement"
        subtitle="Count in whatever unit the goods came in. The system converts."
        width="38rem"
        onClose={() => setAdjustOpen(false)}
        footer={
          <>
            <button style={ui.button} onClick={() => setAdjustOpen(false)} disabled={busy}>Cancel</button>
            <button style={ui.buttonPrimary} onClick={submitAdjust} disabled={busy}>
              {busy ? 'Recording…' : 'Record movement'}
            </button>
          </>
        }
      >
        <FormError error={adjustError} />

        <Field label="Product" required htmlFor="a-product">
          <Select
            id="a-product"
            value={form.product_id}
            onChange={(e) => chooseProduct(e.target.value)}
            placeholder="Choose a product…"
            options={products.map((p) => ({ value: p.id, label: p.name }))}
          />
        </Field>

        <FormGrid>
          <Field label="What happened" required htmlFor="a-type">
            <Select
              id="a-type"
              value={form.type}
              onChange={(e) => setForm({ ...form, type: e.target.value })}
              options={ADJUST_TYPES}
            />
          </Field>
          <Field label="Counted in" hint="Sacks, bags, or the loose unit." htmlFor="a-unit">
            <Select
              id="a-unit"
              value={form.product_unit_id}
              onChange={(e) => setForm({ ...form, product_unit_id: e.target.value })}
              options={formUnits.map((u) => ({ value: u.id, label: u.label }))}
              placeholder={form.product_id ? undefined : 'Choose a product first'}
              disabled={!form.product_id}
            />
          </Field>
          <Field label="How many" required htmlFor="a-qty">
            <NumberInput
              id="a-qty"
              min={0}
              value={form.unit_qty}
              onChange={(e) => setForm({ ...form, unit_qty: e.target.value })}
            />
          </Field>
        </FormGrid>

        {/* The conversion, shown before it is committed — this is the number
            that actually lands in stock, and the one worth double-checking. */}
        {baseEquivalent !== null && chosenUnit && chosenUnit.qty_in_base !== 1 && (
          <div style={styles.conversion}>
            {form.unit_qty} × {chosenUnit.label} ={' '}
            <strong>{formatQty(baseEquivalent, unitCodeOf(form.product_id))}</strong>{' '}
            {form.type === 'add' ? 'into stock' : 'out of stock'}
          </div>
        )}

        <FormGrid>
          <Field label="Batch" hint="Leave blank to use the default batch." htmlFor="a-batch">
            <TextInput id="a-batch" value={form.batch_no} onChange={(e) => setForm({ ...form, batch_no: e.target.value })} />
          </Field>
          <Field label="Cost price" hint={chosenUnit && chosenUnit.qty_in_base !== 1 ? `Per ${chosenUnit.label}.` : 'Per unit.'} htmlFor="a-cost">
            <NumberInput id="a-cost" min={0} value={form.cost_price} onChange={(e) => setForm({ ...form, cost_price: e.target.value })} />
          </Field>
          <Field label="Expiry date" htmlFor="a-exp">
            <TextInput id="a-exp" type="date" value={form.expiry_date} onChange={(e) => setForm({ ...form, expiry_date: e.target.value })} />
          </Field>
        </FormGrid>

        <Field label="Reason" required hint="The only account of why stock moved. Be specific enough to answer a question in six months.">
          <TextInput value={form.reason} onChange={(e) => setForm({ ...form, reason: e.target.value })} />
        </Field>
      </Modal>
    </div>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'danger' | 'warning' }) {
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
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  header: { display: 'flex', alignItems: 'center', gap: space.lg, marginBottom: space.lg },
  h1: { margin: 0, fontSize: font.size.xxl },
  summary: { display: 'flex', gap: space.lg, marginBottom: space.lg, flexWrap: 'wrap' },
  statLabel: { fontSize: font.size.xs, textTransform: 'uppercase', letterSpacing: '0.04em', color: color.textMuted, fontWeight: 700 },
  statValue: { marginTop: space.xs, fontSize: font.size.xl, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
  tabs: { display: 'flex', gap: space.xs, marginBottom: space.lg, flexWrap: 'wrap' },
  tab: { ...ui.button, borderRadius: '999px' },
  tabActive: { ...ui.buttonPrimary, borderRadius: '999px' },
  tabCount: { opacity: 0.65, fontWeight: 400 },
  muted: { color: color.textFaint },
  conversion: {
    padding: space.md,
    background: color.primarySoft,
    border: `1px solid ${color.primary}22`,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
};
