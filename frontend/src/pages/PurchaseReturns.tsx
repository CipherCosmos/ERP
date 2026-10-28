import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { productApi, purchaseApi, purchaseReturnApi, uomApi, vendorApi } from '../services/api';
import { PurchaseReturn, Vendor } from '../types';
import { AppLayout } from '../components/Layout/AppLayout';
import { Column, DataTable } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, NumberInput, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatDate, formatMoney, formatQty, todayISO } from '../utils/format';

/**
 * Goods going back to the vendor.
 *
 * The screen this replaces was a form permanently half-unfolded above a bare
 * table: no search, no sorting, no way to tell twelve returns apart, and a
 * "PO ID (Optional)" box that asked a human to type a uuid. Failures arrived as
 * `alert('Failed to create return')` — the server's actual reason, which is
 * usually the one thing worth reading, was thrown away.
 *
 * On units, this screen is deliberately the odd one out. A purchase order line
 * and a goods receipt line both name a `product_unit_id`; a purchase return
 * line does not — the API takes `{product_id, quantity, rate}` and nothing
 * else, the quantity is in the product's base stocking unit, and the rate is
 * per base unit rather than per pack. So rather than offer a unit picker that
 * would quietly send the wrong number, the quantity field says which unit it is
 * counted in and the screen says so out loud.
 */

interface ProductRow {
  id: string;
  name: string;
  uom_id?: string;
  cost_price?: number;
  sell_price?: number;
  is_active?: boolean;
}

interface POOption {
  id: string;
  po_no: string;
  vendor_id: string;
  order_date: string;
  status: string;
}

interface DraftLine {
  product_id: string;
  quantity: string;
  rate: string;
}

const BLANK_LINE: DraftLine = { product_id: '', quantity: '1', rate: '0' };

export default function PurchaseReturns() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [returns, setReturns] = useState<PurchaseReturn[]>([]);
  const [vendors, setVendors] = useState<Vendor[]>([]);
  const [products, setProducts] = useState<ProductRow[]>([]);
  const [orders, setOrders] = useState<POOption[]>([]);
  const [uoms, setUoms] = useState<{ id: string; code: string }[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState<'all' | 'pending' | 'approved'>('all');

  const [showForm, setShowForm] = useState(false);
  const [vendorId, setVendorId] = useState('');
  const [poId, setPoId] = useState('');
  const [returnDate, setReturnDate] = useState(todayISO);
  const [reason, setReason] = useState('');
  const [lines, setLines] = useState<DraftLine[]>([BLANK_LINE]);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const [confirming, setConfirming] = useState<PurchaseReturn | null>(null);
  const [approving, setApproving] = useState(false);
  const [approveError, setApproveError] = useState<string | null>(null);

  const loadData = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    setLoadError(null);
    try {
      const [returnsRes, vendorsRes, productsRes, ordersRes, uomsRes] = await Promise.all([
        purchaseReturnApi.list({ store_id: store.id }),
        vendorApi.list(),
        productApi.list({ store_id: store.id }),
        purchaseApi.list({ store_id: store.id }),
        uomApi.list(),
      ]);
      setReturns((returnsRes.data as PurchaseReturn[]) || []);
      setVendors((vendorsRes.data as Vendor[]) || []);
      setProducts((productsRes.data as unknown as ProductRow[]) || []);
      setOrders((ordersRes.data as POOption[]) || []);
      setUoms((uomsRes.data as { id: string; code: string }[]) || []);
    } catch (err: any) {
      console.error('Failed to load purchase returns:', err);
      setLoadError(err?.response?.data?.error || 'Could not load purchase returns.');
    } finally {
      setLoading(false);
    }
  }, [store]);

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
  }, [user, navigate]);

  useEffect(() => {
    if (store) loadData();
  }, [store, loadData]);

  const productById = useCallback((id: string) => products.find((p) => p.id === id), [products]);

  /** The unit a return is counted in — the product's own stocking unit. */
  const baseCode = useCallback(
    (productId: string): string => {
      const p = productById(productId);
      if (!p?.uom_id) return '';
      return uoms.find((u) => u.id === p.uom_id)?.code ?? '';
    },
    [productById, uoms]
  );

  // Create -------------------------------------------------------------------

  const openForm = () => {
    setVendorId('');
    setPoId('');
    setReturnDate(todayISO());
    setReason('');
    setLines([{ ...BLANK_LINE }]);
    setFormError(null);
    setShowForm(true);
  };

  const patchLine = (idx: number, patch: Partial<DraftLine>) => {
    setLines((prev) => prev.map((ln, i) => (i === idx ? { ...ln, ...patch } : ln)));
  };

  /** Picking the product fills the rate with what the shop paid for one base unit. */
  const chooseProduct = (idx: number, productId: string) => {
    const p = productById(productId);
    patchLine(idx, {
      product_id: productId,
      rate: p?.cost_price && p.cost_price > 0 ? String(p.cost_price) : '0',
    });
  };

  /** Only this vendor's orders: a return quotes the order the goods came in on. */
  const vendorOrders = useMemo(
    () => (vendorId ? orders.filter((o) => o.vendor_id === vendorId && o.status !== 'cancelled') : []),
    [orders, vendorId]
  );

  const formTotal = useMemo(
    () =>
      lines.reduce((sum, ln) => {
        if (!ln.product_id) return sum;
        return sum + (parseFloat(ln.quantity) || 0) * (parseFloat(ln.rate) || 0);
      }, 0),
    [lines]
  );

  const submit = async () => {
    if (!store) return;
    if (!vendorId) {
      setFormError('Choose the vendor these goods go back to.');
      return;
    }
    const items: { product_id: string; quantity: number; rate: number }[] = [];
    for (const [i, ln] of lines.entries()) {
      if (!ln.product_id) continue;
      const quantity = parseFloat(ln.quantity) || 0;
      const rate = parseFloat(ln.rate) || 0;
      if (quantity <= 0) {
        setFormError(`Line ${i + 1} needs a quantity greater than zero.`);
        return;
      }
      if (rate < 0) {
        setFormError(`Line ${i + 1} cannot have a negative rate.`);
        return;
      }
      items.push({ product_id: ln.product_id, quantity, rate });
    }
    if (items.length === 0) {
      setFormError('Add at least one product to return.');
      return;
    }

    setSaving(true);
    setFormError(null);
    try {
      await purchaseReturnApi.create({
        store_id: store.id,
        vendor_id: vendorId,
        po_id: poId || undefined,
        return_date: returnDate,
        reason: reason.trim() || undefined,
        items,
      });
      setShowForm(false);
      await loadData();
    } catch (err: any) {
      // The axios layer has already flattened the server envelope to a string.
      // Putting the object itself into JSX unmounts the page, which is how this
      // screen used to disappear on exactly the validation it exists to show.
      setFormError(err?.response?.data?.error || 'Could not raise this return.');
    } finally {
      setSaving(false);
    }
  };

  /**
   * Approving takes the goods out of stock, once and irreversibly, so it asks
   * first — in a dialog rather than a native confirm(), because what is being
   * approved (whose goods, how much, and why) is worth showing, and because the
   * server's refusal — "insufficient stock" is the common one — has to land
   * somewhere the reader is already looking.
   */
  const approve = async () => {
    if (!store || !confirming) return;
    setApproving(true);
    setApproveError(null);
    try {
      await purchaseReturnApi.approve(confirming.id, store.id);
      setConfirming(null);
      await loadData();
    } catch (err: any) {
      setApproveError(err?.response?.data?.error || 'Could not approve this return.');
    } finally {
      setApproving(false);
    }
  };

  // Rows ---------------------------------------------------------------------

  const rows = useMemo(
    () => (statusFilter === 'all' ? returns : returns.filter((r) => r.status === statusFilter)),
    [returns, statusFilter]
  );

  const totals = useMemo(
    () => ({
      pending: returns.filter((r) => r.status === 'pending').length,
      approved: returns.filter((r) => r.status === 'approved').length,
      awaiting: returns
        .filter((r) => r.status === 'pending')
        .reduce((sum, r) => sum + (Number(r.total) || 0), 0),
      credited: returns
        .filter((r) => r.status === 'approved')
        .reduce((sum, r) => sum + (Number(r.total) || 0), 0),
    }),
    [returns]
  );

  const vendorName = useCallback(
    (r: PurchaseReturn) => r.vendor_name || vendors.find((v) => v.id === r.vendor_id)?.name || r.vendor_id.slice(0, 8),
    [vendors]
  );

  const poNumber = useCallback(
    (r: PurchaseReturn) => (r.po_id ? orders.find((o) => o.id === r.po_id)?.po_no || r.po_id.slice(0, 8) : ''),
    [orders]
  );

  const columns: Column<PurchaseReturn>[] = [
    {
      key: 'vendor_name',
      header: 'Vendor',
      render: (r) => <span style={{ fontWeight: 600 }}>{vendorName(r)}</span>,
      value: (r) => vendorName(r),
    },
    {
      key: 'return_date',
      header: 'Returned',
      render: (r) => formatDate(r.return_date),
      // Sort on the ISO string: the rendered DD-MM-YYYY sorts by day of month.
      value: (r) => r.return_date,
    },
    {
      key: 'po_id',
      header: 'Against order',
      render: (r) =>
        r.po_id ? (
          <span style={{ fontFamily: font.mono, fontSize: font.size.sm }}>{poNumber(r)}</span>
        ) : (
          <span style={styles.muted}>—</span>
        ),
      value: (r) => poNumber(r),
    },
    {
      key: 'total',
      header: 'Value',
      numeric: true,
      render: (r) => formatMoney(r.total),
      value: (r) => Number(r.total) || 0,
    },
    {
      key: 'status',
      header: 'Status',
      render: (r) => (
        <span style={badge(r.status === 'approved' ? 'success' : 'warning')}>
          {r.status === 'approved' ? 'approved' : 'awaiting approval'}
        </span>
      ),
      value: (r) => r.status,
    },
    { key: 'reason', header: 'Reason', render: (r) => r.reason || <span style={styles.muted}>—</span>, value: (r) => r.reason },
    {
      key: 'actions',
      header: '',
      sortable: false,
      width: '7rem',
      render: (r) =>
        r.status === 'pending' ? (
          <button
            style={{ ...ui.button, padding: '0.25rem 0.625rem', fontSize: font.size.sm }}
            onClick={(e) => {
              e.stopPropagation();
              setApproveError(null);
              setConfirming(r);
            }}
          >
            Approve
          </button>
        ) : null,
    },
  ];

  if (!user) return null;

  return (
    <AppLayout>
      <div style={{ ...ui.page, padding: 0 }}>
        <div style={styles.header}>
          <h1 style={styles.h1}>Vendor Purchase Returns (Debit Notes)</h1>
        </div>

      {loadError && (
        <div style={{ marginBottom: space.lg }}>
          <FormError error={loadError} />
        </div>
      )}

      <div style={styles.summary}>
        <Stat label="Awaiting approval" value={String(totals.pending)} tone={totals.pending ? 'warning' : undefined} />
        <Stat label="Approved" value={String(totals.approved)} />
        <Stat label="Value awaiting approval" value={formatMoney(totals.awaiting)} />
        <Stat label="Value returned" value={formatMoney(totals.credited)} />
      </div>

      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(r) => r.id}
        loading={loading}
        searchPlaceholder="Search vendor, order number or reason…"
        // The filter narrows the rows before the table sees them, so the table
        // cannot tell "none exist" from "none match". Saying the first while
        // eight approved returns sit behind the filter is a lie worth avoiding.
        empty={
          statusFilter === 'pending'
            ? 'Nothing is awaiting approval.'
            : statusFilter === 'approved'
              ? 'No return has been approved yet.'
              : 'No purchase returns yet. Raise one when goods go back to a vendor.'
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
                { value: 'approved', label: 'Approved' },
              ]}
            />
            <button style={ui.buttonPrimary} onClick={openForm}>
              + New return
            </button>
          </>
        }
      />

      {returns.length >= 100 && (
        <div style={styles.capNote}>Showing the most recent 100 returns — older ones are not on this screen.</div>
      )}

      <Modal
        open={!!confirming}
        title="Approve this return?"
        subtitle="The goods leave stock when you do, and a return cannot be un-approved."
        width="34rem"
        onClose={() => setConfirming(null)}
        footer={
          <>
            <button style={ui.button} onClick={() => setConfirming(null)} disabled={approving}>
              Keep it pending
            </button>
            <button style={ui.buttonPrimary} onClick={approve} disabled={approving}>
              {approving ? 'Approving…' : 'Approve and remove stock'}
            </button>
          </>
        }
      >
        <FormError error={approveError} />
        {confirming && (
          <div style={styles.lineCard}>
            <div style={{ display: 'flex', gap: space.xl, flexWrap: 'wrap' }}>
              <div>
                <div style={styles.statLabel}>Vendor</div>
                <div style={styles.confirmValue}>{vendorName(confirming)}</div>
              </div>
              <div>
                <div style={styles.statLabel}>Value</div>
                <div style={styles.confirmValue}>{formatMoney(confirming.total)}</div>
              </div>
              <div>
                <div style={styles.statLabel}>Returned</div>
                <div style={styles.confirmValue}>{formatDate(confirming.return_date)}</div>
              </div>
            </div>
            {confirming.reason && <div style={{ fontSize: font.size.md }}>{confirming.reason}</div>}
          </div>
        )}
      </Modal>

      <Modal
        open={showForm}
        title="New purchase return"
        subtitle="Raised as pending. Stock moves only when it is approved, and only once."
        width="52rem"
        onClose={() => setShowForm(false)}
        footer={
          <>
            <button style={ui.button} onClick={() => setShowForm(false)} disabled={saving}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={submit} disabled={saving}>
              {saving ? 'Saving…' : 'Raise return'}
            </button>
          </>
        }
      >
        <FormError error={formError} />

        <FormGrid>
          <Field label="Vendor" required htmlFor="pr-vendor">
            <Select
              id="pr-vendor"
              value={vendorId}
              onChange={(e) => {
                setVendorId(e.target.value);
                // The order list is this vendor's; keeping another vendor's
                // order selected would file the return against the wrong one.
                setPoId('');
              }}
              placeholder="Choose…"
              options={vendors.map((v) => ({ value: v.id, label: v.name }))}
            />
          </Field>
          <Field
            label="Against order"
            hint={
              !vendorId
                ? 'Choose a vendor first.'
                : vendorOrders.length === 0
                  ? 'This vendor has no orders on file.'
                  : 'Optional — which order these goods arrived on.'
            }
            htmlFor="pr-po"
          >
            <Select
              id="pr-po"
              value={poId}
              disabled={!vendorId || vendorOrders.length === 0}
              onChange={(e) => setPoId(e.target.value)}
              placeholder="No particular order"
              options={vendorOrders.map((o) => ({
                value: o.id,
                label: `${o.po_no} · ${formatDate(o.order_date)}`,
              }))}
            />
          </Field>
          <Field label="Return date" required htmlFor="pr-date">
            <TextInput id="pr-date" type="date" value={returnDate} onChange={(e) => setReturnDate(e.target.value)} />
          </Field>
        </FormGrid>

        <Field label="Reason" hint="What the vendor is being told: short weight, damaged, wrong goods." htmlFor="pr-reason">
          <TextInput
            id="pr-reason"
            value={reason}
            placeholder="e.g. Two sacks water-damaged in transit"
            onChange={(e) => setReason(e.target.value)}
          />
        </Field>

        <div style={styles.divider}>Goods going back</div>

        {lines.map((ln, idx) => {
          const code = baseCode(ln.product_id);
          const quantity = parseFloat(ln.quantity) || 0;
          const rate = parseFloat(ln.rate) || 0;
          return (
            <div key={idx} style={styles.lineCard}>
              <div style={styles.lineHead}>
                <div style={{ flex: 1, minWidth: '12rem' }}>
                  <Field label={`Line ${idx + 1}`} htmlFor={`pr-p-${idx}`}>
                    <Select
                      id={`pr-p-${idx}`}
                      value={ln.product_id}
                      onChange={(e) => chooseProduct(idx, e.target.value)}
                      placeholder="Choose a product…"
                      options={products.map((p) => ({ value: p.id, label: p.name }))}
                    />
                  </Field>
                </div>
                {lines.length > 1 && (
                  <button
                    style={styles.removeBtn}
                    onClick={() => setLines(lines.filter((_, i) => i !== idx))}
                    aria-label={`Remove line ${idx + 1}`}
                  >
                    ×
                  </button>
                )}
              </div>

              <div style={styles.lineGrid}>
                <Field
                  label={code ? `Quantity in ${code}` : 'Quantity'}
                  required
                  // Not a pack count. A return line carries no unit, so a "2"
                  // here is two kilos, not two sacks.
                  hint={code ? `Counted in ${code} — the unit stock is held in.` : 'Counted in the stocking unit.'}
                  htmlFor={`pr-q-${idx}`}
                >
                  <NumberInput
                    id={`pr-q-${idx}`}
                    min={0}
                    value={ln.quantity}
                    onChange={(e) => patchLine(idx, { quantity: e.target.value })}
                  />
                </Field>
                <Field
                  label={code ? `Rate per ${code}` : 'Rate'}
                  hint="What the vendor credits for one."
                  htmlFor={`pr-r-${idx}`}
                >
                  <NumberInput
                    id={`pr-r-${idx}`}
                    min={0}
                    value={ln.rate}
                    onChange={(e) => patchLine(idx, { rate: e.target.value })}
                  />
                </Field>
                <Field label="Line value">
                  <div style={styles.readout}>{formatMoney(quantity * rate)}</div>
                </Field>
              </div>

              {ln.product_id && (
                <div style={styles.lineFoot}>
                  <span>
                    <strong>{formatQty(quantity, code)}</strong> at {formatMoney(rate)} each ={' '}
                    <strong>{formatMoney(quantity * rate)}</strong>
                  </span>
                </div>
              )}
            </div>
          );
        })}

        <div style={{ display: 'flex', alignItems: 'center', gap: space.lg, flexWrap: 'wrap' }}>
          <button style={ui.button} onClick={() => setLines([...lines, { ...BLANK_LINE }])}>
            + Add a line
          </button>
          <div style={{ marginLeft: 'auto', ...ui.numeric, fontSize: font.size.md }}>
            <span style={{ color: color.textMuted }}>Return value </span>
            <strong style={{ fontSize: font.size.lg }}>{formatMoney(formTotal)}</strong>
          </div>
        </div>

        <div style={styles.note}>
          A return is counted in the product's <strong>stocking unit</strong>, not in packs. The API takes only a
          product, a quantity and a rate here — there is no unit on a return line — so two 25 kg sacks going back is a
          quantity of 50, at the rate for one kg.
        </div>
      </Modal>
      </div>
    </AppLayout>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'warning' | 'danger' }) {
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
  statLabel: {
    fontSize: font.size.xs,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    color: color.textMuted,
    fontWeight: 700,
  },
  statValue: { marginTop: space.xs, fontSize: font.size.xl, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
  muted: { color: color.textFaint, fontSize: font.size.sm },
  confirmValue: { marginTop: space.xs, fontSize: font.size.lg, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
  capNote: { marginTop: space.md, fontSize: font.size.sm, color: color.textMuted },
  divider: {
    paddingTop: space.md,
    borderTop: `1px solid ${color.border}`,
    fontSize: font.size.xs,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    fontWeight: 700,
    color: color.textMuted,
  },
  lineCard: {
    border: `1px solid ${color.border}`,
    borderRadius: '10px',
    padding: space.lg,
    background: color.surfaceAlt,
    display: 'grid',
    gap: space.md,
  },
  lineHead: { display: 'flex', gap: space.md, alignItems: 'flex-end' },
  lineGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(9rem, 1fr))', gap: space.md },
  lineFoot: {
    display: 'flex',
    gap: space.md,
    flexWrap: 'wrap',
    alignItems: 'baseline',
    fontSize: font.size.sm,
    color: color.textMuted,
    paddingTop: space.sm,
    borderTop: `1px solid ${color.border}`,
  },
  readout: {
    ...ui.input,
    background: color.primarySoft,
    borderColor: `${color.primary}44`,
    fontWeight: 700,
    fontFamily: font.mono,
    textAlign: 'right',
  },
  removeBtn: {
    ...ui.buttonDanger,
    padding: '0.25rem 0.625rem',
    fontSize: font.size.lg,
    lineHeight: 1,
    marginBottom: '0.35rem',
  },
  note: {
    padding: space.md,
    background: color.primarySoft,
    border: `1px solid ${color.primary}22`,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
};
