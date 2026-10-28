import React, { useState, useEffect, useMemo, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { invoiceApi, reportApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { jsPDF } from 'jspdf';
import 'jspdf-autotable';
import { formatDate, formatMoney, thisMonthISO } from '../utils/format';

/** One GSTR-1 row exactly as the server sends it. */
interface GSTR1Entry {
  invoice_no: string;
  invoice_date: string;
  customer_name: string;
  customer_gstin: string;
  taxable_value: number;
  cgst: number;
  sgst: number;
  igst: number;
  total: number;
}

/**
 * The GSTR-3B buckets carry the tax split, not a total, so the outward and
 * inward figures are summed here rather than read off a field.
 */
function bucketTax(bucket?: { cgst?: number; sgst?: number; igst?: number }): number {
  return Number(bucket?.cgst ?? 0) + Number(bucket?.sgst ?? 0) + Number(bucket?.igst ?? 0);
}

export default function GSTR1Reports() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [gstr1Data, setGstr1Data] = useState<{ invoices: GSTR1Entry[] } | null>(null);
  const [gstr3b, setGstr3b] = useState<any>(null);
  const [loading, setLoading] = useState(false);
  const [month, setMonth] = useState(thisMonthISO);

  /**
   * The endpoint returns the rows and nothing else, so the three headline
   * figures are added up here from the same rows the table shows. That also
   * keeps the cards and the table from ever disagreeing.
   */
  const summary = useMemo(() => {
    const invoices = gstr1Data?.invoices ?? [];
    return invoices.reduce(
      (acc, inv) => ({
        total_invoices: acc.total_invoices + 1,
        total_tax: acc.total_tax + Number(inv.cgst ?? 0) + Number(inv.sgst ?? 0) + Number(inv.igst ?? 0),
        total_value: acc.total_value + Number(inv.total ?? 0),
      }),
      { total_invoices: 0, total_tax: 0, total_value: 0 },
    );
  }, [gstr1Data]);

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user, navigate]);

  const loadGSTR1 = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await invoiceApi.exportGSTR1(store.id, month);
      setGstr1Data(data);
    } catch (err) {
      console.error('Failed to load GSTR1:', err);
    } finally {
      setLoading(false);
    }
  }, [store, month]);

  useEffect(() => {
    if (store && month) {
      loadGSTR1();
      reportApi.getGSTR3B({ store_id: store.id, month }).then((r) => setGstr3b(r.data)).catch(() => setGstr3b(null));
    }
  }, [store, month, loadGSTR1]);

  const exportPDF = () => {
    if (!gstr1Data) return;
    
    const doc = new jsPDF();
    doc.setFontSize(16);
    doc.text('GSTR-1 Report', 14, 20);
    doc.setFontSize(10);
    doc.text(`Month: ${month}`, 14, 28);
    doc.text(`Store: ${store?.name || ''}`, 14, 34);
    
    const tableData = gstr1Data.invoices.map(inv => [
      inv.invoice_no, formatDate(inv.invoice_date, ''), inv.customer_name, inv.customer_gstin,
      inv.taxable_value.toFixed(2), inv.cgst.toFixed(2), inv.sgst.toFixed(2),
      inv.igst.toFixed(2), inv.total.toFixed(2)
    ]);
    
    (doc as any).autoTable({
      startY: 40,
      head: [['Invoice', 'Date', 'Customer', 'GSTIN', 'Taxable', 'CGST', 'SGST', 'IGST', 'Total']],
      body: tableData,
      foot: [['', '', '', '', 
        gstr1Data.invoices.reduce((s, i) => s + i.taxable_value, 0).toFixed(2),
        gstr1Data.invoices.reduce((s, i) => s + i.cgst, 0).toFixed(2),
        gstr1Data.invoices.reduce((s, i) => s + i.sgst, 0).toFixed(2),
        gstr1Data.invoices.reduce((s, i) => s + i.igst, 0).toFixed(2),
        gstr1Data.invoices.reduce((s, i) => s + i.total, 0).toFixed(2)
      ]],
    });
    
    doc.save(`GSTR1-${month}.pdf`);
  };

  const exportCSV = () => {
    if (!gstr1Data) return;
    
    const headers = ['Invoice No', 'Date', 'Customer', 'GSTIN', 'Taxable Value', 'CGST', 'SGST', 'IGST', 'Total'];
    const rows = gstr1Data.invoices.map(inv => [
      inv.invoice_no, formatDate(inv.invoice_date, ''), inv.customer_name, inv.customer_gstin,
      inv.taxable_value, inv.cgst, inv.sgst, inv.igst, inv.total
    ]);
    
    const csv = [headers.join(','), ...rows.map(r => r.join(','))].join('\n');
    const blob = new Blob([csv], { type: 'text/csv' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `GSTR1-${month}.csv`;
    a.click();
  };

  if (!user || user.role === 'cashier') return null;

  return (
    <AppLayout>
      <div style={{ maxWidth: '1200px', margin: '0 auto' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.25rem' }}>
          <h1 style={{ margin: 0, fontSize: '1.5rem', fontWeight: 700 }}>GST Statutory Returns (GSTR-1 & GSTR-3B)</h1>
        </div>

      <div style={styles.controls}>
        <input type="month" value={month} onChange={e => setMonth(e.target.value)} style={styles.input} />
        <button onClick={loadGSTR1} style={styles.refreshBtn}>Refresh</button>
      </div>

      {gstr3b && (
        <div style={styles.gstr3bBox}>
          <h2 style={{ margin: '0 0 0.5rem', fontSize: '1.1rem' }}>GSTR-3B summary (internal)</h2>
          <p style={{ fontSize: '0.8rem', color: '#666', margin: '0 0 0.75rem' }}>{gstr3b.note}</p>
          <div style={styles.summary}>
            <div style={styles.summaryCard}>
              <div style={styles.summaryLabel}>Outward tax</div>
              <div style={styles.summaryValue}>{formatMoney(bucketTax(gstr3b.outward_supplies))}</div>
            </div>
            <div style={styles.summaryCard}>
              <div style={styles.summaryLabel}>Inward (ITC hint)</div>
              <div style={styles.summaryValue}>{formatMoney(bucketTax(gstr3b.inward_supplies))}</div>
            </div>
            <div style={styles.summaryCard}>
              <div style={styles.summaryLabel}>Net payable hint</div>
              <div style={styles.summaryValue}>{formatMoney(Number(gstr3b.net_tax_payable_hint ?? 0))}</div>
            </div>
          </div>
        </div>
      )}

      {loading ? (
        <p>Loading...</p>
      ) : gstr1Data ? (
        <>
          <div style={styles.summary}>
            <div style={styles.summaryCard}>
              <div style={styles.summaryLabel}>Total Invoices</div>
              <div style={styles.summaryValue}>{summary.total_invoices}</div>
            </div>
            <div style={styles.summaryCard}>
              <div style={styles.summaryLabel}>Total Tax</div>
              <div style={styles.summaryValue}>{formatMoney(summary.total_tax)}</div>
            </div>
            <div style={styles.summaryCard}>
              <div style={styles.summaryLabel}>Total Value</div>
              <div style={styles.summaryValue}>{formatMoney(summary.total_value)}</div>
            </div>
          </div>

          <div style={styles.actions}>
            <button onClick={exportPDF} style={styles.exportBtn}>Export PDF</button>
            <button onClick={exportCSV} style={styles.exportBtn}>Export CSV</button>
          </div>

          <table style={styles.table}>
            <thead>
              <tr>
                <th style={styles.th}>Invoice</th>
                <th style={styles.th}>Date</th>
                <th style={styles.th}>Customer</th>
                <th style={styles.th}>GSTIN</th>
                <th style={styles.th}>Taxable</th>
                <th style={styles.th}>CGST</th>
                <th style={styles.th}>SGST</th>
                <th style={styles.th}>IGST</th>
                <th style={styles.th}>Total</th>
              </tr>
            </thead>
            <tbody>
              {gstr1Data.invoices.map((inv, i) => (
                <tr key={i} style={styles.tr}>
                  <td style={styles.td}>{inv.invoice_no}</td>
                  <td style={styles.td}>{formatDate(inv.invoice_date)}</td>
                  <td style={styles.td}>{inv.customer_name}</td>
                  <td style={styles.td}>{inv.customer_gstin}</td>
                  <td style={styles.td}>{formatMoney(inv.taxable_value)}</td>
                  <td style={styles.td}>{formatMoney(inv.cgst)}</td>
                  <td style={styles.td}>{formatMoney(inv.sgst)}</td>
                  <td style={styles.td}>{formatMoney(inv.igst)}</td>
                  <td style={styles.td}>{formatMoney(inv.total)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      ) : (
        <p>No data for selected month</p>
      )}
      </div>
    </AppLayout>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', background: '#f5f5f5', minHeight: '100vh' },
  header: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' },
  backBtn: { padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  controls: { display: 'flex', gap: '1rem', marginBottom: '1rem' },
  input: { padding: '0.75rem', border: '1px solid #ddd', borderRadius: '4px' },
  refreshBtn: { padding: '0.75rem 1.5rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  summary: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(150px, 1fr))', gap: '1rem', marginBottom: '1rem' },
  summaryCard: { background: 'white', padding: '1rem', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  summaryLabel: { fontSize: '0.875rem', color: '#666' },
  summaryValue: { fontSize: '1.5rem', fontWeight: 'bold', color: '#1976d2' },
  actions: { display: 'flex', gap: '1rem', marginBottom: '1rem' },
  exportBtn: { padding: '0.75rem 1.5rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  table: { width: '100%', background: 'white', borderCollapse: 'collapse', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9', fontSize: '0.875rem' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee', fontSize: '0.875rem' },
  tr: { borderBottom: '1px solid #eee' },
  gstr3bBox: { background: 'white', padding: '1rem', borderRadius: '8px', marginBottom: '1rem', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
};