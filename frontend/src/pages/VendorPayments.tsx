import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { purchaseApi, vendorApi, vendorPaymentApi } from '../services/api';
import { VendorPayment, Vendor } from '../types';
import { Column, DataTable } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, NumberInput, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatDate, formatMoney, thisMonthISO, todayISO } from '../utils/format';

/**
 * Money paid out to vendors.
 *
 * The screen this replaces listed payments in a bare table with no search, no
 * sorting and no totals, behind a form that pushed the list down the page
 * whenever it was open. It asked for a "PO ID (Optional)" as free text — a uuid,
 * typed by hand — and reported every failure as `alert('Failed to record
 * payment')`, discarding the server's reason.
 *
 * What it never showed is the one number a person recording a payment wants:
 * how much this vendor is still owed. That is `ordered − paid` across every
 * non-cancelled order, and the API has always returned it from GET /vendors/:id.
 * It is fetched when a vendor is chosen — on the click, not in an effect, so
 * the screen cannot fall into a request loop.
 */

interface POOption {
  id: string;
  po_no: string;
  vendor_id: string;
  order_date: string;
  total: number;
  status: string;
}

interface Exposure {
  ordered: number;
  paid: number;
  outstanding: number;
}

const MODES = [
  { value: 'cash', label: 'Cash' },
  { value: 'cheque', label: 'Cheque' },
  { value: 'bank_transfer', label: 'Bank transfer' },
  { value: 'upi', label: 'UPI' },
];

const modeLabel = (mode: string) => MODES.find((m) => m.value === mode)?.label ?? mode;

export default function VendorPayments() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [payments, setPayments] = useState<VendorPayment[]>([]);
  const [vendors, setVendors] = useState<Vendor[]>([]);
  const [orders, setOrders] = useState<POOption[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [vendorFilter, setVendorFilter] = useState('');
  const [modeFilter, setModeFilter] = useState('');

  const [showForm, setShowForm] = useState(false);
  const [vendorId, setVendorId] = useState('');
  const [poId, setPoId] = useState('');
  const [amount, setAmount] = useState('0');
  const [paymentMode, setPaymentMode] = useState('cash');
  const [reference, setReference] = useState('');
  const [paymentDate, setPaymentDate] = useState(todayISO);
  const [notes, setNotes] = useState('');
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [exposure, setExposure] = useState<Exposure | null>(null);
  const [exposureLoading, setExposureLoading] = useState(false);

  const loadData = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    setLoadError(null);
    try {
      const [paymentsRes, vendorsRes, ordersRes] = await Promise.all([
        vendorPaymentApi.list({ store_id: store.id }),
        vendorApi.list(),
        purchaseApi.list({ store_id: store.id }),
      ]);
      setPayments((paymentsRes.data as VendorPayment[]) || []);
      setVendors((vendorsRes.data as Vendor[]) || []);
      setOrders((ordersRes.data as POOption[]) || []);
    } catch (err: any) {
      console.error('Failed to load vendor payments:', err);
      setLoadError(err?.response?.data?.error || 'Could not load vendor payments.');
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

  // Create -------------------------------------------------------------------

  const openForm = () => {
    setVendorId('');
    setPoId('');
    setAmount('0');
    setPaymentMode('cash');
    setReference('');
    setPaymentDate(todayISO());
    setNotes('');
    setExposure(null);
    setFormError(null);
    setShowForm(true);
  };

  /**
   * What this vendor is still owed, fetched when they are chosen.
   *
   * A payment recorded blind is how a shop pays an invoice twice. The figure is
   * ordered less paid across every non-cancelled order, which is the same sum
   * the server checks a new order's credit limit against.
   */
  const chooseVendor = async (id: string) => {
    setVendorId(id);
    // Another vendor's order would file this payment against the wrong one.
    setPoId('');
    setExposure(null);
    if (!id) return;
    setExposureLoading(true);
    try {
      const { data } = await vendorApi.get(id);
      const body = data as { exposure?: { ordered?: number; paid?: number }; outstanding?: number };
      setExposure({
        ordered: Number(body.exposure?.ordered) || 0,
        paid: Number(body.exposure?.paid) || 0,
        outstanding: Number(body.outstanding) || 0,
      });
    } catch (err) {
      // A missing balance is not a reason to block the payment: leave it unsaid
      // rather than showing a zero that reads as "nothing owed".
      console.error('Failed to load vendor balance:', err);
      setExposure(null);
    } finally {
      setExposureLoading(false);
    }
  };

  const vendorOrders = useMemo(
    () => (vendorId ? orders.filter((o) => o.vendor_id === vendorId && o.status !== 'cancelled') : []),
    [orders, vendorId]
  );

  const submit = async () => {
    if (!store) return;
    if (!vendorId) {
      setFormError('Choose the vendor being paid.');
      return;
    }
    const value = parseFloat(amount) || 0;
    if (value <= 0) {
      setFormError('A payment needs an amount greater than zero.');
      return;
    }
    setSaving(true);
    setFormError(null);
    try {
      await vendorPaymentApi.create({
        store_id: store.id,
        vendor_id: vendorId,
        po_id: poId || undefined,
        amount: value,
        payment_mode: paymentMode,
        reference: reference.trim() || undefined,
        payment_date: paymentDate,
        notes: notes.trim() || undefined,
      });
      setShowForm(false);
      await loadData();
    } catch (err: any) {
      // Already flattened to a string by the axios layer. An object here is
      // what React refuses outright, taking the page down with it.
      setFormError(err?.response?.data?.error || 'Could not record this payment.');
    } finally {
      setSaving(false);
    }
  };

  // Rows ---------------------------------------------------------------------

  const vendorName = useCallback(
    (p: VendorPayment) =>
      p.vendor_name || vendors.find((v) => v.id === p.vendor_id)?.name || p.vendor_id.slice(0, 8),
    [vendors]
  );

  const poNumber = useCallback(
    (p: VendorPayment) => (p.po_id ? orders.find((o) => o.id === p.po_id)?.po_no || p.po_id.slice(0, 8) : ''),
    [orders]
  );

  const rows = useMemo(
    () =>
      payments.filter(
        (p) => (!vendorFilter || p.vendor_id === vendorFilter) && (!modeFilter || p.payment_mode === modeFilter)
      ),
    [payments, vendorFilter, modeFilter]
  );

  const totals = useMemo(() => {
    const month = thisMonthISO();
    return {
      count: payments.length,
      paid: payments.reduce((sum, p) => sum + (Number(p.amount) || 0), 0),
      vendors: new Set(payments.map((p) => p.vendor_id)).size,
      thisMonth: payments
        .filter((p) => String(p.payment_date).slice(0, 7) === month)
        .reduce((sum, p) => sum + (Number(p.amount) || 0), 0),
    };
  }, [payments]);

  const columns: Column<VendorPayment>[] = [
    {
      key: 'vendor_name',
      header: 'Vendor',
      render: (p) => <span style={{ fontWeight: 600 }}>{vendorName(p)}</span>,
      value: (p) => vendorName(p),
    },
    {
      key: 'payment_date',
      header: 'Paid on',
      render: (p) => formatDate(p.payment_date),
      // The ISO string sorts chronologically; DD-MM-YYYY sorts by day of month.
      value: (p) => p.payment_date,
    },
    {
      key: 'amount',
      header: 'Amount',
      numeric: true,
      render: (p) => <span style={{ fontWeight: 600 }}>{formatMoney(p.amount)}</span>,
      value: (p) => Number(p.amount) || 0,
    },
    {
      key: 'payment_mode',
      header: 'Mode',
      render: (p) => <span style={badge(p.payment_mode === 'cash' ? 'neutral' : 'info')}>{modeLabel(p.payment_mode)}</span>,
      value: (p) => modeLabel(p.payment_mode),
    },
    {
      key: 'reference',
      header: 'Reference',
      render: (p) =>
        p.reference ? (
          <span style={{ fontFamily: font.mono, fontSize: font.size.sm }}>{p.reference}</span>
        ) : (
          <span style={styles.muted}>—</span>
        ),
      value: (p) => p.reference,
    },
    {
      key: 'po_id',
      header: 'Against order',
      render: (p) =>
        p.po_id ? (
          <span style={{ fontFamily: font.mono, fontSize: font.size.sm }}>{poNumber(p)}</span>
        ) : (
          <span style={styles.muted}>—</span>
        ),
      value: (p) => poNumber(p),
    },
    { key: 'notes', header: 'Notes', render: (p) => p.notes || <span style={styles.muted}>—</span>, value: (p) => p.notes },
  ];

  if (!user) return null;

  const filtered = !!vendorFilter || !!modeFilter;
  const filterName = vendorFilter ? vendors.find((v) => v.id === vendorFilter)?.name : null;

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>Vendor payments</h1>
      </div>

      {loadError && (
        <div style={{ marginBottom: space.lg }}>
          <FormError error={loadError} />
        </div>
      )}

      <div style={styles.summary}>
        <Stat label="Payments" value={String(totals.count)} />
        <Stat label="Total paid out" value={formatMoney(totals.paid)} />
        <Stat label="Vendors paid" value={String(totals.vendors)} />
        <Stat label="Paid this month" value={formatMoney(totals.thisMonth)} />
      </div>

      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(p) => p.id}
        loading={loading}
        searchPlaceholder="Search vendor, reference, order or notes…"
        // "No payments to this vendor" and "no payments at all" send a person
        // in different directions, and only one of them is true at a time.
        empty={
          filterName
            ? `No payments to ${filterName}${modeFilter ? ` by ${modeLabel(modeFilter)}` : ''} yet.`
            : filtered
              ? `No payments by ${modeLabel(modeFilter)} yet.`
              : 'No payments recorded yet. Record one when money goes out to a vendor.'
        }
        toolbar={
          <>
            <Select
              value={vendorFilter}
              onChange={(e) => setVendorFilter(e.target.value)}
              style={{ width: 'auto' }}
              placeholder="All vendors"
              options={vendors.map((v) => ({ value: v.id, label: v.name }))}
            />
            <Select
              value={modeFilter}
              onChange={(e) => setModeFilter(e.target.value)}
              style={{ width: 'auto' }}
              placeholder="All modes"
              options={MODES}
            />
            <button style={ui.buttonPrimary} onClick={openForm}>
              + Record a payment
            </button>
          </>
        }
      />

      {payments.length >= 100 && (
        <div style={styles.capNote}>Showing the most recent 100 payments — older ones are not on this screen.</div>
      )}

      <Modal
        open={showForm}
        title="Record a payment"
        subtitle="Money going out. It reduces what the vendor is owed straight away."
        width="46rem"
        onClose={() => setShowForm(false)}
        footer={
          <>
            <button style={ui.button} onClick={() => setShowForm(false)} disabled={saving}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={submit} disabled={saving}>
              {saving ? 'Saving…' : 'Record payment'}
            </button>
          </>
        }
      >
        <FormError error={formError} />

        <FormGrid>
          <Field label="Vendor" required htmlFor="vp-vendor">
            <Select
              id="vp-vendor"
              value={vendorId}
              onChange={(e) => chooseVendor(e.target.value)}
              placeholder="Choose…"
              options={vendors.map((v) => ({ value: v.id, label: v.name }))}
            />
          </Field>
          <Field label="Amount" required htmlFor="vp-amount">
            <NumberInput id="vp-amount" min={0} value={amount} onChange={(e) => setAmount(e.target.value)} />
          </Field>
        </FormGrid>

        {/* What this vendor is owed, so the amount above is not typed blind. */}
        {vendorId && (
          <div style={styles.ledger}>
            {exposureLoading ? (
              <span style={styles.muted}>Looking up what this vendor is owed…</span>
            ) : exposure ? (
              <>
                <div>
                  <div style={styles.statLabel}>Ordered</div>
                  <div style={styles.ledgerValue}>{formatMoney(exposure.ordered)}</div>
                </div>
                <div>
                  <div style={styles.statLabel}>Already paid</div>
                  <div style={styles.ledgerValue}>{formatMoney(exposure.paid)}</div>
                </div>
                <div>
                  <div style={styles.statLabel}>Still owed</div>
                  <div
                    style={{
                      ...styles.ledgerValue,
                      color:
                        exposure.outstanding > 0
                          ? color.danger
                          : exposure.outstanding < 0
                            ? color.success
                            : color.text,
                    }}
                  >
                    {formatMoney(exposure.outstanding)}
                  </div>
                </div>
                {parseFloat(amount) > 0 && (
                  <div style={{ marginLeft: 'auto', textAlign: 'right' }}>
                    <div style={styles.statLabel}>Owed after this</div>
                    <div style={styles.ledgerValue}>
                      {formatMoney(exposure.outstanding - (parseFloat(amount) || 0))}
                    </div>
                  </div>
                )}
              </>
            ) : (
              <span style={styles.muted}>Could not read this vendor's balance — the payment can still be recorded.</span>
            )}
          </div>
        )}

        <FormGrid>
          <Field label="Paid by" required htmlFor="vp-mode">
            <Select
              id="vp-mode"
              value={paymentMode}
              onChange={(e) => setPaymentMode(e.target.value)}
              options={MODES}
            />
          </Field>
          <Field label="Paid on" required htmlFor="vp-date">
            <TextInput id="vp-date" type="date" value={paymentDate} onChange={(e) => setPaymentDate(e.target.value)} />
          </Field>
          <Field
            label="Reference"
            hint={paymentMode === 'cheque' ? 'The cheque number.' : 'Transaction or receipt number.'}
            htmlFor="vp-ref"
          >
            <TextInput id="vp-ref" value={reference} onChange={(e) => setReference(e.target.value)} />
          </Field>
          <Field
            label="Against order"
            hint={
              !vendorId
                ? 'Choose a vendor first.'
                : vendorOrders.length === 0
                  ? 'This vendor has no orders on file.'
                  : 'Optional — which order this settles.'
            }
            htmlFor="vp-po"
          >
            <Select
              id="vp-po"
              value={poId}
              disabled={!vendorId || vendorOrders.length === 0}
              onChange={(e) => setPoId(e.target.value)}
              placeholder="No particular order"
              options={vendorOrders.map((o) => ({
                value: o.id,
                label: `${o.po_no} · ${formatDate(o.order_date)} · ${formatMoney(o.total)}`,
              }))}
            />
          </Field>
        </FormGrid>

        <Field label="Notes" htmlFor="vp-notes">
          <TextInput
            id="vp-notes"
            value={notes}
            placeholder="e.g. Part payment, balance on delivery"
            onChange={(e) => setNotes(e.target.value)}
          />
        </Field>
      </Modal>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 10rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div style={styles.statValue}>{value}</div>
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
  capNote: { marginTop: space.md, fontSize: font.size.sm, color: color.textMuted },
  ledger: {
    display: 'flex',
    gap: space.xl,
    alignItems: 'center',
    flexWrap: 'wrap',
    padding: space.lg,
    background: color.surfaceAlt,
    borderRadius: '10px',
    border: `1px solid ${color.border}`,
  },
  ledgerValue: { marginTop: space.xs, fontSize: font.size.lg, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
};
