import React, { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { invoiceApi } from '../services/api';
import { Invoice, InvoiceItem } from '../types';
import jsPDF from 'jspdf';

interface InvoiceDetail {
  invoice: Invoice;
  items: (InvoiceItem & { product_name?: string })[];
}

export default function Invoices() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [invoices, setInvoices] = useState<Invoice[]>([]);
  const [loading, setLoading] = useState(false);
  const [startDate, setStartDate] = useState('');
  const [endDate, setEndDate] = useState('');

  const [detail, setDetail] = useState<InvoiceDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);

  useEffect(() => {
    if (!user) {
      navigate('/login');
    }
  }, [user, navigate]);

  const loadInvoices = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await invoiceApi.list({
        store_id: store.id,
        ...(startDate ? { start_date: startDate } : {}),
        ...(endDate ? { end_date: endDate } : {}),
      });
      setInvoices(data || []);
    } catch (err) {
      console.error('Failed to load invoices:', err);
    } finally {
      setLoading(false);
    }
  }, [store, startDate, endDate]);

  useEffect(() => {
    if (store) {
      loadInvoices();
    }
  }, [store, loadInvoices]);

  const openDetail = async (inv: Invoice) => {
    setDetailLoading(true);
    setDetail(null);
    try {
      const { data } = await invoiceApi.get(inv.id);
      const body = data as { invoice?: Invoice; items?: (InvoiceItem & { product_name?: string })[] };
      setDetail({
        invoice: body.invoice || inv,
        items: body.items || [],
      });
    } catch (err) {
      console.error(err);
      alert('Failed to load invoice details');
    } finally {
      setDetailLoading(false);
    }
  };

  const handleVoid = async () => {
    if (!detail) return;
    if (!window.confirm(`Are you sure you want to void invoice ${detail.invoice.invoice_no}? This will restore stock.`)) {
      return;
    }
    try {
      await invoiceApi.void(detail.invoice.id);
      alert('Invoice voided successfully');
      setDetail(null);
      loadInvoices();
    } catch (err) {
      console.error('Failed to void invoice:', err);
      alert('Failed to void invoice');
    }
  };

  const generatePDF = () => {
    if (!detail) return;
    const { invoice, items } = detail;
    const doc = new jsPDF({ unit: 'mm', format: [80, 200] });
    const pageWidth = 80;
    let y = 10;

    // Store name header
    doc.setFontSize(12);
    doc.setFont('helvetica', 'bold');
    const storeName = store?.name || 'Store';
    doc.text(storeName, pageWidth / 2, y, { align: 'center' });
    y += 5;

    if (store?.address) {
      doc.setFontSize(7);
      doc.setFont('helvetica', 'normal');
      doc.text(store.address, pageWidth / 2, y, { align: 'center' });
      y += 4;
    }
    if (store?.phone) {
      doc.setFontSize(7);
      doc.text('Ph: ' + store.phone, pageWidth / 2, y, { align: 'center' });
      y += 4;
    }
    if (store?.gstin) {
      doc.setFontSize(7);
      doc.text('GSTIN: ' + store.gstin, pageWidth / 2, y, { align: 'center' });
      y += 4;
    }

    // Divider
    doc.setLineWidth(0.3);
    doc.line(4, y, pageWidth - 4, y);
    y += 4;

    // Invoice info
    doc.setFontSize(8);
    doc.setFont('helvetica', 'bold');
    doc.text('Invoice: ' + invoice.invoice_no, 4, y);
    y += 4;
    doc.setFont('helvetica', 'normal');
    doc.text('Date: ' + invoice.invoice_date, 4, y);
    y += 4;
    doc.text('Payment: ' + invoice.payment_mode, 4, y);
    y += 4;
    doc.text('Status: ' + invoice.status, 4, y);
    y += 5;

    // Divider
    doc.line(4, y, pageWidth - 4, y);
    y += 4;

    // Items header
    doc.setFontSize(7);
    doc.setFont('helvetica', 'bold');
    doc.text('Item', 4, y);
    doc.text('Qty', 38, y);
    doc.text('Rate', 50, y);
    doc.text('Total', 64, y);
    y += 3;
    doc.line(4, y, pageWidth - 4, y);
    y += 3;

    // Items
    doc.setFont('helvetica', 'normal');
    for (const item of items) {
      const name = item.product_name || item.product_id.slice(0, 12);
      doc.text(name.slice(0, 16), 4, y);
      doc.text(String(item.quantity), 38, y);
      doc.text(Number(item.rate).toFixed(2), 50, y);
      doc.text(Number(item.total).toFixed(2), 64, y);
      y += 4;

      // Add page if running out of space
      if (y > 190) {
        doc.addPage([80, 200]);
        y = 10;
      }
    }

    // Divider
    doc.line(4, y, pageWidth - 4, y);
    y += 4;

    // Totals
    doc.setFontSize(7);
    doc.text('Sub Total:', 4, y);
    doc.text(Number(invoice.sub_total).toFixed(2), 64, y);
    y += 4;

    if (Number(invoice.discount) > 0) {
      doc.text('Discount:', 4, y);
      doc.text('-' + Number(invoice.discount).toFixed(2), 64, y);
      y += 4;
    }

    if (Number(invoice.cgst) > 0) {
      doc.text('CGST:', 4, y);
      doc.text(Number(invoice.cgst).toFixed(2), 64, y);
      y += 4;
    }
    if (Number(invoice.sgst) > 0) {
      doc.text('SGST:', 4, y);
      doc.text(Number(invoice.sgst).toFixed(2), 64, y);
      y += 4;
    }
    if (Number(invoice.igst) > 0) {
      doc.text('IGST:', 4, y);
      doc.text(Number(invoice.igst).toFixed(2), 64, y);
      y += 4;
    }

    if (Number(invoice.round_off) !== 0) {
      doc.text('Round Off:', 4, y);
      doc.text(Number(invoice.round_off).toFixed(2), 64, y);
      y += 4;
    }

    doc.setFont('helvetica', 'bold');
    doc.setFontSize(9);
    doc.text('Total:', 4, y);
    doc.text(Number(invoice.total).toFixed(2), 64, y);
    y += 6;

    // Footer
    doc.setFont('helvetica', 'normal');
    doc.setFontSize(7);
    doc.line(4, y, pageWidth - 4, y);
    y += 4;
    doc.text('Thank you!', pageWidth / 2, y, { align: 'center' });

    doc.save(`invoice_${invoice.invoice_no}.pdf`);
  };

  if (!user) return null;

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Invoices</h1>
        <button type="button" onClick={() => navigate('/')} style={styles.backBtn}>
          ← Back to Menu
        </button>
      </div>

      <div style={styles.filters}>
        <div style={styles.filterGroup}>
          <label style={styles.filterLabel}>Start Date</label>
          <input
            type="date"
            value={startDate}
            onChange={(e) => setStartDate(e.target.value)}
            style={styles.input}
          />
        </div>
        <div style={styles.filterGroup}>
          <label style={styles.filterLabel}>End Date</label>
          <input
            type="date"
            value={endDate}
            onChange={(e) => setEndDate(e.target.value)}
            style={styles.input}
          />
        </div>
        <button type="button" style={styles.searchBtn} onClick={loadInvoices}>
          Search
        </button>
        {(startDate || endDate) && (
          <button
            type="button"
            style={styles.clearBtn}
            onClick={() => {
              setStartDate('');
              setEndDate('');
            }}
          >
            Clear
          </button>
        )}
      </div>

      {loading ? (
        <p>Loading...</p>
      ) : invoices.length === 0 ? (
        <p style={styles.empty}>No invoices found.</p>
      ) : (
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Invoice No</th>
              <th style={styles.th}>Date</th>
              <th style={styles.th}>Total</th>
              <th style={styles.th}>Status</th>
              <th style={styles.th}>Payment</th>
            </tr>
          </thead>
          <tbody>
            {invoices.map((inv) => (
              <tr
                key={inv.id}
                style={styles.trClickable}
                onClick={() => openDetail(inv)}
              >
                <td style={styles.td}>{inv.invoice_no}</td>
                <td style={styles.td}>{inv.invoice_date}</td>
                <td style={styles.td}>₹{Number(inv.total).toFixed(2)}</td>
                <td style={styles.td}>
                  <span
                    style={{
                      ...styles.statusBadge,
                      background:
                        inv.status === 'paid'
                          ? '#e8f5e9'
                          : inv.status === 'cancelled'
                            ? '#ffebee'
                            : '#fff3e0',
                    }}
                  >
                    {inv.status}
                  </span>
                </td>
                <td style={styles.td}>{inv.payment_mode}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {/* Detail modal */}
      {(detail || detailLoading) && (
        <div style={styles.modalBackdrop} onClick={() => { if (!detailLoading) setDetail(null); }}>
          <div style={styles.modal} onClick={(e) => e.stopPropagation()}>
            {detailLoading ? (
              <p>Loading invoice details...</p>
            ) : detail ? (
              <>
                <div style={styles.modalHeader}>
                  <h3 style={{ margin: 0 }}>Invoice {detail.invoice.invoice_no}</h3>
                  <button
                    type="button"
                    style={styles.closeBtn}
                    onClick={() => setDetail(null)}
                  >
                    ×
                  </button>
                </div>

                <div style={styles.detailInfo}>
                  <p><strong>Date:</strong> {detail.invoice.invoice_date}</p>
                  <p><strong>Status:</strong> {detail.invoice.status}</p>
                  <p><strong>Payment:</strong> {detail.invoice.payment_mode}</p>
                </div>

                <table style={{ ...styles.table, marginTop: '0.5rem' }}>
                  <thead>
                    <tr>
                      <th style={styles.th}>Product</th>
                      <th style={styles.th}>Qty</th>
                      <th style={styles.th}>Rate</th>
                      <th style={styles.th}>Disc</th>
                      <th style={styles.th}>Total</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detail.items.map((item) => (
                      <tr key={item.id}>
                        <td style={styles.td}>{item.product_name || item.product_id}</td>
                        <td style={styles.td}>{item.quantity}</td>
                        <td style={styles.td}>₹{Number(item.rate).toFixed(2)}</td>
                        <td style={styles.td}>₹{Number(item.discount).toFixed(2)}</td>
                        <td style={styles.td}>₹{Number(item.total).toFixed(2)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>

                <div style={styles.totalsSection}>
                  <div style={styles.totalRow}>
                    <span>Sub Total</span>
                    <span>₹{Number(detail.invoice.sub_total).toFixed(2)}</span>
                  </div>
                  {Number(detail.invoice.discount) > 0 && (
                    <div style={styles.totalRow}>
                      <span>Discount</span>
                      <span>-₹{Number(detail.invoice.discount).toFixed(2)}</span>
                    </div>
                  )}
                  {Number(detail.invoice.cgst) > 0 && (
                    <div style={styles.totalRow}>
                      <span>CGST</span>
                      <span>₹{Number(detail.invoice.cgst).toFixed(2)}</span>
                    </div>
                  )}
                  {Number(detail.invoice.sgst) > 0 && (
                    <div style={styles.totalRow}>
                      <span>SGST</span>
                      <span>₹{Number(detail.invoice.sgst).toFixed(2)}</span>
                    </div>
                  )}
                  {Number(detail.invoice.igst) > 0 && (
                    <div style={styles.totalRow}>
                      <span>IGST</span>
                      <span>₹{Number(detail.invoice.igst).toFixed(2)}</span>
                    </div>
                  )}
                  {Number(detail.invoice.round_off) !== 0 && (
                    <div style={styles.totalRow}>
                      <span>Round Off</span>
                      <span>₹{Number(detail.invoice.round_off).toFixed(2)}</span>
                    </div>
                  )}
                  <div style={{ ...styles.totalRow, fontWeight: 'bold', fontSize: '1.1rem' }}>
                    <span>Total</span>
                    <span>₹{Number(detail.invoice.total).toFixed(2)}</span>
                  </div>
                </div>

                <div style={styles.modalActions}>
                  <button type="button" style={styles.printBtn} onClick={generatePDF}>
                    Print PDF
                  </button>
                  {detail.invoice.status !== 'voided' && detail.invoice.status !== 'cancelled' && (
                    <button type="button" style={styles.voidBtn} onClick={handleVoid}>
                      Void Invoice
                    </button>
                  )}
                  <button type="button" style={styles.backBtn} onClick={() => setDetail(null)}>
                    Close
                  </button>
                </div>
              </>
            ) : null}
          </div>
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', background: '#f5f5f5', minHeight: '100vh' },
  header: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' },
  backBtn: { padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  filters: { display: 'flex', gap: '1rem', marginBottom: '1rem', flexWrap: 'wrap', alignItems: 'flex-end' },
  filterGroup: { display: 'flex', flexDirection: 'column', gap: '0.25rem' },
  filterLabel: { fontSize: '0.875rem', color: '#666' },
  input: { padding: '0.5rem', border: '1px solid #ddd', borderRadius: '4px' },
  searchBtn: { padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer', height: 'fit-content' },
  clearBtn: { padding: '0.5rem 1rem', background: '#757575', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer', height: 'fit-content' },
  empty: { textAlign: 'center', color: '#999', marginTop: '2rem' },
  table: { width: '100%', background: 'white', borderCollapse: 'collapse', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  trClickable: { cursor: 'pointer', borderBottom: '1px solid #eee' },
  statusBadge: { padding: '0.25rem 0.5rem', borderRadius: '4px', fontSize: '0.75rem', textTransform: 'capitalize' },
  modalBackdrop: {
    position: 'fixed',
    inset: 0,
    background: 'rgba(0,0,0,0.4)',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    zIndex: 1000,
    padding: '1rem',
  },
  modal: {
    background: 'white',
    borderRadius: '8px',
    padding: '1.25rem',
    maxWidth: '640px',
    width: '100%',
    maxHeight: '90vh',
    overflow: 'auto',
    boxShadow: '0 4px 24px rgba(0,0,0,0.15)',
  },
  modalHeader: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.75rem' },
  closeBtn: { background: 'none', border: 'none', fontSize: '1.5rem', cursor: 'pointer', color: '#666', padding: '0.25rem 0.5rem' },
  detailInfo: { display: 'flex', gap: '1.5rem', flexWrap: 'wrap', marginBottom: '0.5rem', fontSize: '0.9rem' },
  totalsSection: { marginTop: '0.75rem', borderTop: '1px solid #eee', paddingTop: '0.5rem' },
  totalRow: { display: 'flex', justifyContent: 'space-between', padding: '0.25rem 0', fontSize: '0.9rem' },
  modalActions: { display: 'flex', gap: '0.5rem', marginTop: '1rem', flexWrap: 'wrap' },
  printBtn: { padding: '0.5rem 1rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  voidBtn: { padding: '0.5rem 1rem', background: '#f44336', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
};
