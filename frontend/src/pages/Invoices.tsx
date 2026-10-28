import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { invoiceApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { FormError, Select } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatDate, formatMoney, formatQty } from '../utils/format';

interface InvoiceRow {
  id: string;
  invoice_no: string;
  invoice_date: string;
  customer_id: string | null;
  payment_mode: string;
  status: string;
  sub_total: number;
  cgst: number;
  sgst: number;
  igst: number;
  discount: number;
  round_off: number;
  total: number;
}

interface InvoiceLine {
  id: string;
  product_id: string;
  product_name: string;
  // Snapshotted on the line when the bill was raised, so this view shows the
  // document as issued rather than the catalogue as it stands now.
  hsn_code: string;
  gst_rate: number;
  batch_no: string;
  product_unit_id: string;
  unit_qty: number;
  unit_label: string;
  quantity: number;
  rate: number;
  discount: number;
  cgst: number;
  sgst: number;
  igst: number;
  total: number;
}

/**
 * Bills raised.
 *
 * The screen this replaces printed `invoice_date` straight into the cell, so
 * every row read "2026-09-10T00:00:00Z" — a machine's idea of a date on the one
 * screen a shopkeeper opens to answer "what did I sell last Tuesday". Money
 * came through ungrouped for the same reason.
 *
 * The detail is where the units work has to show. A bill that records two sacks
 * must say two sacks; printing the fifty kilos it came to is arithmetic the
 * customer never agreed to.
 */
export default function Invoices() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [invoices, setInvoices] = useState<InvoiceRow[]>([]);
  const [loading, setLoading] = useState(false);
  const [startDate, setStartDate] = useState('');
  const [endDate, setEndDate] = useState('');
  const [statusFilter, setStatusFilter] = useState<'all' | 'completed' | 'voided'>('all');

  const [detail, setDetail] = useState<{ invoice: InvoiceRow; items: InvoiceLine[] } | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [voiding, setVoiding] = useState(false);

  const loadInvoices = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await invoiceApi.list({
        store_id: store.id,
        start_date: startDate || undefined,
        end_date: endDate || undefined,
      });
      setInvoices(data as unknown as InvoiceRow[]);
    } catch (err) {
      console.error('Failed to load invoices:', err);
    } finally {
      setLoading(false);
    }
  }, [store, startDate, endDate]);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadInvoices();
  }, [user, store, navigate, loadInvoices]);

  const openDetail = async (inv: InvoiceRow) => {
    setDetailLoading(true);
    setDetailError(null);
    setDetail({ invoice: inv, items: [] });
    try {
      const { data } = await invoiceApi.get(inv.id);
      setDetail({ invoice: (data as any).invoice ?? inv, items: ((data as any).items ?? []) as InvoiceLine[] });
    } catch (err: any) {
      setDetailError(err?.response?.data?.error || 'Could not load this bill.');
    } finally {
      setDetailLoading(false);
    }
  };

  const voidInvoice = async () => {
    if (!detail) return;
    if (
      !window.confirm(
        `Void ${detail.invoice.invoice_no}? The stock goes back on the shelf and the sale leaves your revenue and your GST return. The bill stays in the list, marked voided.`
      )
    ) {
      return;
    }
    setVoiding(true);
    setDetailError(null);
    try {
      await invoiceApi.void(detail.invoice.id);
      setDetail(null);
      await loadInvoices();
    } catch (err: any) {
      setDetailError(err?.response?.data?.error || 'Could not void this bill.');
    } finally {
      setVoiding(false);
    }
  };

  const rows = useMemo(() => {
    if (statusFilter === 'all') return invoices;
    return invoices.filter((i) => i.status === statusFilter);
  }, [invoices, statusFilter]);

  const totals = useMemo(() => {
    // Voided bills are excluded from every figure. A voided sale is not revenue,
    // and it is left out of the GST return for the same reason.
    const live = invoices.filter((i) => i.status !== 'voided');
    return {
      count: live.length,
      revenue: live.reduce((s, i) => s + (i.total || 0), 0),
      tax: live.reduce((s, i) => s + (i.cgst || 0) + (i.sgst || 0) + (i.igst || 0), 0),
      voided: invoices.length - live.length,
    };
  }, [invoices]);

  const columns: Column<InvoiceRow>[] = [
    {
      key: 'invoice_no',
      header: 'Bill',
      render: (i) => <span style={{ fontFamily: font.mono, fontWeight: 600 }}>{i.invoice_no}</span>,
      value: (i) => i.invoice_no,
    },
    { key: 'invoice_date', header: 'Date', render: (i) => formatDate(i.invoice_date), value: (i) => i.invoice_date },
    {
      key: 'payment_mode',
      header: 'Paid by',
      render: (i) => <span style={badge(i.payment_mode === 'credit' ? 'warning' : 'neutral')}>{i.payment_mode}</span>,
      value: (i) => i.payment_mode,
    },
    { key: 'sub_total', header: 'Taxable', numeric: true, render: (i) => formatMoney(i.sub_total), value: (i) => i.sub_total },
    {
      key: 'tax',
      header: 'GST',
      numeric: true,
      render: (i) => formatMoney((i.cgst || 0) + (i.sgst || 0) + (i.igst || 0)),
      value: (i) => (i.cgst || 0) + (i.sgst || 0) + (i.igst || 0),
    },
    {
      key: 'total',
      header: 'Total',
      numeric: true,
      render: (i) => (
        <span style={{ fontWeight: 700, textDecoration: i.status === 'voided' ? 'line-through' : undefined, color: i.status === 'voided' ? color.textFaint : color.text }}>
          {formatMoney(i.total)}
        </span>
      ),
      value: (i) => i.total,
    },
    {
      key: 'status',
      header: 'Status',
      render: (i) => <span style={badge(i.status === 'voided' ? 'danger' : 'success')}>{i.status}</span>,
      value: (i) => i.status,
    },
  ];

  return (
    <AppLayout>
      <div style={{ ...ui.page, padding: 0 }}>
        <div style={styles.header}>
          <h1 style={styles.h1}>Sales Invoices & Billing History</h1>
        </div>

        <div style={styles.summary}>
        <Stat label="Bills" value={String(totals.count)} />
        <Stat label="Revenue" value={formatMoney(totals.revenue)} />
        <Stat label="GST collected" value={formatMoney(totals.tax)} />
        <Stat label="Voided" value={String(totals.voided)} tone={totals.voided ? 'warning' : undefined} />
      </div>

      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(i) => i.id}
        loading={loading}
        onRowClick={openDetail}
        searchPlaceholder="Search bill number or payment mode…"
        empty={
          statusFilter === 'voided'
            ? 'No bills have been voided.'
            : startDate || endDate
              ? 'No bills in this period.'
              : 'No bills raised yet.'
        }
        rowTone={(i) => (i.status === 'voided' ? 'danger' : undefined)}
        toolbar={
          <>
            <input
              type="date"
              value={startDate}
              onChange={(e) => setStartDate(e.target.value)}
              style={{ ...ui.input, width: 'auto' }}
              aria-label="From date"
            />
            <span style={{ color: color.textMuted }}>to</span>
            <input
              type="date"
              value={endDate}
              onChange={(e) => setEndDate(e.target.value)}
              style={{ ...ui.input, width: 'auto' }}
              aria-label="To date"
            />
            <Select
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value as typeof statusFilter)}
              style={{ width: 'auto' }}
              options={[
                { value: 'all', label: 'All bills' },
                { value: 'completed', label: 'Completed' },
                { value: 'voided', label: 'Voided' },
              ]}
            />
          </>
        }
      />

      <Modal
        open={!!detail}
        title={detail?.invoice.invoice_no ?? ''}
        subtitle={detail ? `${formatDate(detail.invoice.invoice_date)} · paid by ${detail.invoice.payment_mode}` : undefined}
        width="46rem"
        onClose={() => setDetail(null)}
        footer={
          <>
            <button style={ui.button} onClick={() => setDetail(null)}>Close</button>
            {detail?.invoice.status !== 'voided' && (
              <button style={ui.buttonDanger} onClick={voidInvoice} disabled={voiding}>
                {voiding ? 'Voiding…' : 'Void this bill'}
              </button>
            )}
          </>
        }
      >
        <FormError error={detailError} />

        {detail?.invoice.status === 'voided' && (
          <div style={styles.voidedNote}>
            This bill was voided. Its stock went back on the shelf and it counts towards neither revenue nor GST.
          </div>
        )}

        {detailLoading ? (
          <div style={{ padding: space.xl, textAlign: 'center', color: color.textMuted }}>Loading…</div>
        ) : (
          <>
            <table style={styles.lineTable}>
              <thead>
                <tr>
                  <th style={styles.lth}>Item</th>
                  <th style={{ ...styles.lth, textAlign: 'right' }}>Qty</th>
                  <th style={{ ...styles.lth, textAlign: 'right' }}>Rate</th>
                  <th style={{ ...styles.lth, textAlign: 'right' }}>GST</th>
                  <th style={{ ...styles.lth, textAlign: 'right' }}>Rate %</th>
                  <th style={{ ...styles.lth, textAlign: 'right' }}>Amount</th>
                </tr>
              </thead>
              <tbody>
                {(detail?.items ?? []).map((it) => (
                  <tr key={it.id} style={styles.ltr}>
                    <td style={styles.ltd}>
                      <div style={{ fontWeight: 600 }}>{it.product_name || it.product_id.slice(0, 8)}</div>
                      {/* HSN belongs on a tax invoice, so it belongs on the
                          copy of one. A line with none is flagged rather than
                          left blank: the gap is in the product record, and
                          somebody has to know to go and fill it in. */}
                      <div style={styles.sub}>
                        {it.hsn_code ? `HSN ${it.hsn_code}` : 'HSN missing'}
                      </div>
                      {it.batch_no && <div style={styles.sub}>batch {it.batch_no}</div>}
                    </td>
                    <td style={{ ...styles.ltd, ...ui.numeric }}>
                      {/* What the customer asked for, in their words. */}
                      <div>{formatQty(it.unit_qty)} × {it.unit_label || 'unit'}</div>
                      {/* And what it came to, when the two differ. */}
                      {it.unit_qty !== it.quantity && (
                        <div style={styles.sub}>{formatQty(it.quantity)} base</div>
                      )}
                    </td>
                    <td style={{ ...styles.ltd, ...ui.numeric }}>{formatMoney(it.rate)}</td>
                    <td style={{ ...styles.ltd, ...ui.numeric }}>
                      {formatMoney((it.cgst || 0) + (it.sgst || 0) + (it.igst || 0))}
                    </td>
                    {/* The rate charged on this document, not the product's
                        rate today — GST rates move by notification. */}
                    <td style={{ ...styles.ltd, ...ui.numeric }}>{it.gst_rate ?? 0}%</td>
                    <td style={{ ...styles.ltd, ...ui.numeric, fontWeight: 600 }}>{formatMoney(it.total)}</td>
                  </tr>
                ))}
                {!detailLoading && (detail?.items?.length ?? 0) === 0 && (
                  <tr>
                    <td colSpan={6} style={{ padding: space.xl, textAlign: 'center', color: color.textMuted }}>
                      This bill has no lines.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>

            {detail && (
              <div style={styles.totals}>
                <Line label="Taxable value" value={formatMoney(detail.invoice.sub_total)} />
                {!!detail.invoice.discount && <Line label="Discount" value={`− ${formatMoney(detail.invoice.discount)}`} />}
                {!!detail.invoice.cgst && <Line label="CGST" value={formatMoney(detail.invoice.cgst)} />}
                {!!detail.invoice.sgst && <Line label="SGST" value={formatMoney(detail.invoice.sgst)} />}
                {!!detail.invoice.igst && <Line label="IGST" value={formatMoney(detail.invoice.igst)} />}
                {!!detail.invoice.round_off && <Line label="Round off" value={formatMoney(detail.invoice.round_off)} />}
                <Line label="Total" value={formatMoney(detail.invoice.total)} strong />
              </div>
            )}
          </>
        )}
      </Modal>
      </div>
    </AppLayout>
  );
}

function Line({ label, value, strong }: { label: string; value: string; strong?: boolean }) {
  return (
    <div style={{ ...styles.totalRow, ...(strong ? styles.totalRowStrong : null) }}>
      <span>{label}</span>
      <span style={{ fontVariantNumeric: 'tabular-nums' }}>{value}</span>
    </div>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'warning' }) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 10rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div style={{ ...styles.statValue, color: tone === 'warning' ? color.warning : color.text }}>{value}</div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  header: { display: 'flex', alignItems: 'center', gap: space.lg, marginBottom: space.lg },
  h1: { margin: 0, fontSize: font.size.xxl },
  summary: { display: 'flex', gap: space.lg, marginBottom: space.lg, flexWrap: 'wrap' },
  statLabel: { fontSize: font.size.xs, textTransform: 'uppercase', letterSpacing: '0.04em', color: color.textMuted, fontWeight: 700 },
  statValue: { marginTop: space.xs, fontSize: font.size.xl, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
  sub: { fontSize: font.size.xs, color: color.textMuted },
  lineTable: { width: '100%', borderCollapse: 'collapse', fontSize: font.size.md },
  lth: {
    textAlign: 'left',
    padding: `${space.sm} ${space.md}`,
    fontSize: font.size.xs,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    fontWeight: 700,
    color: color.textMuted,
    borderBottom: `1px solid ${color.border}`,
  },
  ltr: { borderBottom: `1px solid ${color.border}` },
  ltd: { padding: `${space.sm} ${space.md}`, verticalAlign: 'top' },
  totals: { display: 'grid', gap: space.xs, justifyContent: 'end' },
  totalRow: { display: 'flex', gap: space.xl, justifyContent: 'space-between', minWidth: '16rem', fontSize: font.size.md },
  totalRowStrong: {
    fontWeight: 700,
    fontSize: font.size.lg,
    paddingTop: space.sm,
    borderTop: `2px solid ${color.text}`,
  },
  voidedNote: {
    padding: space.md,
    background: color.dangerSoft,
    border: `1px solid ${color.danger}33`,
    borderRadius: '6px',
    color: color.danger,
    fontSize: font.size.md,
  },
};
