import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { returnApi, productApi, uomApi } from '../services/api';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, NumberInput, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatDate, formatMoney, formatQty } from '../utils/format';

interface SalesReturn {
  id: string;
  invoice_id: string | null;
  product_id: string;
  store_id: string;
  quantity: number;
  amount: number;
  reason: string;
  refund_mode: string;
  status: string;
  created_at: string;
}

interface Product {
  id: string;
  name: string;
  sell_price?: number;
  stock?: number;
  /** The stocking unit. A returned quantity is counted in it. */
  uom_id?: string;
}

interface Uom {
  id: string;
  code: string;
  name: string;
}

const REFUND_MODES = [
  { value: 'cash', label: 'Cash' },
  { value: 'upi', label: 'UPI' },
  { value: 'card', label: 'Card' },
];

const BLANK = { product_id: '', quantity: '', amount: '', reason: '', refund_mode: 'cash' };

/**
 * Customer returns, rebuilt on the shared table and form.
 *
 * The status the server keeps is only ever `pending` or `completed`, and the
 * difference is the whole screen: a pending return is a promise, a completed
 * one has already put the goods back on the shelf and the money back in the
 * customer's hand. The screen this replaces printed that word in coloured text
 * at the end of a row, with the Approve button — the one action here that moves
 * stock — as an unlabelled last column, and reported a failed approval to the
 * browser console.
 *
 * So: pending returns are called out at the top, tinted in the table, and the
 * approval says what it did or why it could not.
 */
export default function Returns() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [returns, setReturns] = useState<SalesReturn[]>([]);
  const [products, setProducts] = useState<Product[]>([]);
  const [uoms, setUoms] = useState<Uom[]>([]);
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [viewing, setViewing] = useState<SalesReturn | null>(null);
  const [form, setForm] = useState(BLANK);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [approvingId, setApprovingId] = useState<string | null>(null);
  const [pageError, setPageError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState<'all' | 'pending' | 'completed'>('all');

  const loadReturns = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await returnApi.list(store.id);
      setReturns(data as SalesReturn[]);
    } catch (err) {
      console.error('Failed to load returns:', err);
    } finally {
      setLoading(false);
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

  // Units of measure are global, so this closes over nothing and keeps its
  // identity across renders — the effect below cannot loop on it.
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
    loadReturns();
    loadProducts();
    loadUoms();
  }, [user, store, navigate, loadReturns, loadProducts, loadUoms]);

  const productsById = useMemo(() => {
    const map: Record<string, Product> = {};
    products.forEach((p) => {
      map[p.id] = p;
    });
    return map;
  }, [products]);

  /**
   * The unit a return is counted in — the product's stocking unit, because
   * that is the single pool approving a return puts the goods back into.
   * "1" coming back is a bar of soap or a 25 kg sack depending on the row.
   */
  const unitOf = useCallback(
    (productId: string) => {
      const uomId = productsById[productId]?.uom_id;
      return uoms.find((u) => u.id === uomId)?.code ?? '';
    },
    [productsById, uoms]
  );

  const openNew = () => {
    setForm(BLANK);
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
      setSaveError('Choose the product coming back.');
      return;
    }
    const quantity = parseFloat(form.quantity);
    if (!Number.isFinite(quantity) || quantity <= 0) {
      setSaveError('Enter how much is coming back.');
      return;
    }
    if (!form.reason.trim()) {
      setSaveError('A return needs a reason — it is the only record of why the goods came back.');
      return;
    }
    setSaving(true);
    setSaveError(null);
    try {
      await returnApi.create({
        product_id: form.product_id,
        quantity,
        // Zero is legitimate: an exchange, or goods taken back as a goodwill
        // gesture with nothing refunded.
        amount: parseFloat(form.amount) || 0,
        reason: form.reason.trim(),
        refund_mode: form.refund_mode,
        store_id: store.id,
      });
      close();
      setNotice('Return recorded. It stays pending until approved — the goods go back on the shelf then, not now.');
      await loadReturns();
    } catch (err: any) {
      setSaveError(err?.response?.data?.error || 'Could not record this return.');
    } finally {
      setSaving(false);
    }
  };

  const approve = async (r: SalesReturn) => {
    if (!store) return;
    setApprovingId(r.id);
    setPageError(null);
    setNotice(null);
    try {
      await returnApi.approve(r.id, store.id);
      const name = productsById[r.product_id]?.name ?? 'the goods';
      setNotice(`Approved — ${formatQty(r.quantity, unitOf(r.product_id))} of ${name} is back in stock.`);
      setViewing(null);
      await loadReturns();
    } catch (err: any) {
      // The reason an approval fails is worth reading: the return may already be
      // completed, or the product no longer stocked. The console is not a place
      // the counter looks.
      setPageError(err?.response?.data?.error || 'Could not approve this return.');
    } finally {
      setApprovingId(null);
    }
  };

  const rows = useMemo(
    () => (statusFilter === 'all' ? returns : returns.filter((r) => r.status === statusFilter)),
    [returns, statusFilter]
  );

  const totals = useMemo(() => {
    const pending = returns.filter((r) => r.status === 'pending');
    const done = returns.filter((r) => r.status === 'completed');
    return {
      pending: pending.length,
      pendingValue: pending.reduce((sum, r) => sum + (r.amount || 0), 0),
      done: done.length,
      doneValue: done.reduce((sum, r) => sum + (r.amount || 0), 0),
    };
  }, [returns]);

  const columns: Column<SalesReturn>[] = [
    {
      key: 'created_at',
      header: 'Date',
      render: (r) => formatDate(r.created_at),
      // ISO first for a chronological sort, readable form after it so a search
      // for "10-09-2026" matches too.
      value: (r) => (r.created_at ? `${r.created_at} ${formatDate(r.created_at)}` : ''),
    },
    {
      key: 'product',
      header: 'Product',
      render: (r) => {
        const p = productsById[r.product_id];
        return p ? (
          <span style={{ fontWeight: 600 }}>{p.name}</span>
        ) : (
          <span style={styles.muted} title={r.product_id}>
            Unknown product
          </span>
        );
      },
      value: (r) => productsById[r.product_id]?.name ?? r.product_id,
    },
    {
      key: 'quantity',
      header: 'Qty',
      numeric: true,
      render: (r) => formatQty(r.quantity, unitOf(r.product_id)),
      // The number, not the rendered "1 NOS": a column of unit codes would
      // sort alphabetically and put 9 kg above 10 kg.
      value: (r) => r.quantity,
    },
    {
      key: 'amount',
      header: 'Refund',
      numeric: true,
      render: (r) =>
        r.amount ? <span style={{ fontWeight: 600 }}>{formatMoney(r.amount)}</span> : <span style={styles.muted}>No refund</span>,
      value: (r) => r.amount,
    },
    {
      key: 'refund_mode',
      header: 'Mode',
      render: (r) => (r.refund_mode ? REFUND_MODES.find((m) => m.value === r.refund_mode)?.label ?? r.refund_mode : '—'),
      value: (r) => r.refund_mode,
    },
    {
      key: 'reason',
      header: 'Reason',
      render: (r) => r.reason || <span style={styles.muted}>—</span>,
      value: (r) => r.reason,
    },
    {
      key: 'status',
      header: 'Status',
      render: (r) => (
        <span style={badge(r.status === 'completed' ? 'success' : 'warning')}>
          {r.status === 'completed' ? 'Approved' : 'Awaiting approval'}
        </span>
      ),
      value: (r) => (r.status === 'completed' ? 'Approved' : 'Awaiting approval pending'),
    },
    {
      key: 'actions',
      header: '',
      sortable: false,
      width: '7rem',
      render: (r) =>
        r.status === 'pending' ? (
          <button
            style={{ ...ui.buttonPrimary, padding: '0.25rem 0.625rem', fontSize: font.size.sm }}
            disabled={approvingId === r.id}
            onClick={(e) => {
              e.stopPropagation();
              approve(r);
            }}
          >
            {approvingId === r.id ? 'Approving…' : 'Approve'}
          </button>
        ) : (
          <span style={styles.muted}>Done</span>
        ),
    },
  ];

  const selectedProduct = productsById[form.product_id];
  const selectedUnit = unitOf(form.product_id);

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>Customer returns</h1>
      </div>

      <div style={styles.summary}>
        <Stat
          label="Awaiting approval"
          value={String(totals.pending)}
          tone={totals.pending ? 'warning' : undefined}
          note={totals.pending ? `${formatMoney(totals.pendingValue)} to refund` : 'nothing waiting'}
        />
        <Stat label="Approved" value={String(totals.done)} note={`${formatMoney(totals.doneValue)} refunded`} />
        <Stat label="Returns in all" value={String(returns.length)} />
      </div>

      {pageError && (
        <div style={{ marginBottom: space.lg }}>
          <FormError error={pageError} />
        </div>
      )}
      {notice && (
        <div style={styles.notice} role="status">
          {notice}
        </div>
      )}

      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(r) => r.id}
        loading={loading}
        onRowClick={setViewing}
        searchPlaceholder="Search product, reason or status…"
        // The status filter runs here, before the table sees the rows, so this
        // side has to say why the list is empty: "no returns recorded" while two
        // sit pending would be flatly untrue.
        empty={
          statusFilter === 'pending'
            ? 'No returns are waiting for approval.'
            : statusFilter === 'completed'
              ? 'No returns have been approved yet.'
              : 'No returns recorded. Raise one when a customer brings goods back.'
        }
        rowTone={(r) => (r.status === 'pending' ? 'warning' : undefined)}
        toolbar={
          <>
            <Select
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value as typeof statusFilter)}
              style={{ width: 'auto' }}
              options={[
                { value: 'all', label: 'All returns' },
                { value: 'pending', label: 'Awaiting approval' },
                { value: 'completed', label: 'Approved' },
              ]}
            />
            <button style={ui.buttonPrimary} onClick={openNew}>
              + New return
            </button>
          </>
        }
      />

      <Modal
        open={creating}
        title="New return"
        subtitle="Recorded as pending. Stock moves and the refund counts only once it is approved."
        onClose={close}
        footer={
          <>
            <button style={ui.button} onClick={close} disabled={saving}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={save} disabled={saving}>
              {saving ? 'Saving…' : 'Record return'}
            </button>
          </>
        }
      >
        <FormError error={saveError} />

        <Field label="Product" required htmlFor="r-product">
          <Select
            id="r-product"
            value={form.product_id}
            onChange={(e) => {
              const p = productsById[e.target.value];
              const qty = parseFloat(form.quantity);
              setForm({
                ...form,
                product_id: e.target.value,
                // Offer the obvious refund — selling price times quantity — as a
                // starting point rather than making someone do it on paper.
                amount:
                  form.amount || !p?.sell_price || !Number.isFinite(qty) || qty <= 0
                    ? form.amount
                    : String(p.sell_price * qty),
              });
            }}
            placeholder="Choose a product"
            options={products.map((p) => ({ value: p.id, label: p.name }))}
          />
        </Field>

        <FormGrid>
          <Field
            label={selectedUnit ? `Quantity (${selectedUnit})` : 'Quantity'}
            required
            hint={
              selectedProduct?.sell_price
                ? `Sells at ${formatMoney(selectedProduct.sell_price)} per ${selectedUnit || 'unit'}`
                : undefined
            }
            htmlFor="r-qty"
          >
            <NumberInput
              id="r-qty"
              value={form.quantity}
              min={0}
              onChange={(e) => {
                const qty = parseFloat(e.target.value);
                setForm({
                  ...form,
                  quantity: e.target.value,
                  amount:
                    !form.amount && selectedProduct?.sell_price && Number.isFinite(qty) && qty > 0
                      ? String(selectedProduct.sell_price * qty)
                      : form.amount,
                });
              }}
            />
          </Field>
          <Field label="Refund amount" hint="Zero is fine for an exchange or a goodwill return." htmlFor="r-amt">
            <NumberInput
              id="r-amt"
              value={form.amount}
              min={0}
              onChange={(e) => setForm({ ...form, amount: e.target.value })}
            />
          </Field>
        </FormGrid>

        <FormGrid>
          <Field label="Reason" required hint="Why the goods came back." htmlFor="r-reason">
            <TextInput
              id="r-reason"
              value={form.reason}
              onChange={(e) => setForm({ ...form, reason: e.target.value })}
            />
          </Field>
          <Field label="Refund mode" htmlFor="r-mode">
            <Select
              id="r-mode"
              value={form.refund_mode}
              onChange={(e) => setForm({ ...form, refund_mode: e.target.value })}
              options={REFUND_MODES}
            />
          </Field>
        </FormGrid>
      </Modal>

      <Modal
        open={!!viewing}
        title={viewing ? (productsById[viewing.product_id]?.name ?? 'Return') : ''}
        subtitle={
          viewing
            ? `${formatQty(viewing.quantity, unitOf(viewing.product_id))} returned on ${formatDate(viewing.created_at)}`
            : undefined
        }
        onClose={() => setViewing(null)}
        width="34rem"
        footer={
          viewing?.status === 'pending' ? (
            <>
              <button style={ui.button} onClick={() => setViewing(null)}>
                Close
              </button>
              <button
                style={ui.buttonPrimary}
                disabled={approvingId === viewing.id}
                onClick={() => approve(viewing)}
              >
                {approvingId === viewing.id ? 'Approving…' : 'Approve and restock'}
              </button>
            </>
          ) : (
            <button style={ui.button} onClick={() => setViewing(null)}>
              Close
            </button>
          )
        }
      >
        {viewing && (
          <>
            <div style={styles.detailGrid}>
              <div>
                <div style={styles.statLabel}>Status</div>
                <div style={{ marginTop: space.xs }}>
                  <span style={badge(viewing.status === 'completed' ? 'success' : 'warning')}>
                    {viewing.status === 'completed' ? 'Approved' : 'Awaiting approval'}
                  </span>
                </div>
              </div>
              <div>
                <div style={styles.statLabel}>Refund</div>
                <div style={styles.detailValue}>{formatMoney(viewing.amount)}</div>
              </div>
              <div>
                <div style={styles.statLabel}>Mode</div>
                <div style={styles.detailValue}>
                  {REFUND_MODES.find((m) => m.value === viewing.refund_mode)?.label ?? viewing.refund_mode ?? '—'}
                </div>
              </div>
            </div>
            <div>
              <div style={styles.statLabel}>Reason</div>
              <div>{viewing.reason || '—'}</div>
            </div>
            {viewing.invoice_id && (
              <div>
                <div style={styles.statLabel}>Against invoice</div>
                <div style={{ fontFamily: font.mono, fontSize: font.size.sm }}>{viewing.invoice_id}</div>
              </div>
            )}
            {viewing.status === 'pending' && (
              <div style={styles.warn}>
                Approving puts {formatQty(viewing.quantity, unitOf(viewing.product_id))} back into stock and cannot be
                undone from this screen.
              </div>
            )}
          </>
        )}
      </Modal>
    </div>
  );
}

function Stat({ label, value, note, tone }: { label: string; value: string; note?: string; tone?: 'warning' }) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 10rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div style={{ ...styles.statValue, color: tone === 'warning' ? color.warning : color.text }}>{value}</div>
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
  notice: {
    padding: space.md,
    marginBottom: space.lg,
    background: color.successSoft,
    border: `1px solid ${color.success}33`,
    color: color.success,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
  detailGrid: { display: 'flex', gap: space.xl, flexWrap: 'wrap' },
  detailValue: { marginTop: space.xs, fontSize: font.size.lg, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
  warn: {
    padding: space.md,
    background: color.warningSoft,
    border: `1px solid ${color.warning}33`,
    color: color.warning,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
};
