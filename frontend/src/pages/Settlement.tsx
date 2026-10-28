import React, { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { paymentApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { formatDate, formatMoney, todayISO } from '../utils/format';

/**
 * The settlement endpoint reports one row per payment mode rather than a fixed
 * set of columns, so the screen renders whatever modes come back. These labels
 * only make the server's lowercase keys presentable; an unknown mode still
 * shows, capitalised.
 */
interface SettlementMode {
  payment_mode: string;
  amount: number;
  count: number;
}

const MODE_LABELS: Record<string, string> = {
  cash: 'Cash',
  upi: 'UPI',
  card: 'Card',
  credit: 'Credit',
  bank: 'Bank / transfer',
  cheque: 'Cheque',
};

const modeLabel = (mode: string) =>
  MODE_LABELS[mode] || mode.charAt(0).toUpperCase() + mode.slice(1);

export default function Settlement() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [report, setReport] = useState<any>(null);
  const [date, setDate] = useState(todayISO());
  const [loading, setLoading] = useState(false);

  // Memoised so the effect can depend on it. It re-creates when the selected
  // date changes, which is what re-runs the effect and refetches the report.
  const loadReport = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await paymentApi.getSettlement({ store_id: store.id, date });
      setReport(data);
    } catch (err) {
      console.error('Failed to load settlement:', err);
    } finally {
      setLoading(false);
    }
  }, [store, date]);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadReport();
  }, [user, store, navigate, loadReport]);

  if (!report) {
    return (
      <AppLayout>
        <div style={{ padding: '2rem', textAlign: 'center', color: 'var(--text-muted)' }}>Loading settlement report...</div>
      </AppLayout>
    );
  }

  return (
    <AppLayout>
      <div style={{ maxWidth: '900px', margin: '0 auto' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.25rem' }}>
          <h1 style={{ margin: 0, fontSize: '1.5rem', fontWeight: 700 }}>Daily Cash Settlement</h1>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
            <label style={{ fontSize: '0.9rem', fontWeight: 500 }}>Date:</label>
            <input type="date" value={date} onChange={(e) => setDate(e.target.value)} className="form-input" style={{ width: 'auto' }} />
            {loading && <span style={{ fontSize: '0.8rem', color: 'var(--text-muted)' }}>Refreshing…</span>}
          </div>
        </div>

        <div className="card" style={{ padding: '1.5rem', marginBottom: '1.5rem' }}>
          <h2 style={{ margin: '0 0 1rem', fontSize: '1.15rem', color: 'var(--text-main)', borderBottom: '1px solid var(--border-subtle)', paddingBottom: '0.5rem' }}>
            Settlement Summary for {formatDate(report.date)}
          </h2>

          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: '1rem', marginBottom: '1.5rem' }}>
            <div style={{ background: 'var(--bg-app)', padding: '1rem', borderRadius: '8px', textAlign: 'center', border: '1px solid var(--border-subtle)' }}>
              <span style={{ display: 'block', color: 'var(--text-muted)', fontSize: '0.85rem', marginBottom: '0.25rem' }}>Total Sales</span>
              <span style={{ display: 'block', fontSize: '1.6rem', fontWeight: 'bold', color: 'var(--primary)' }}>{formatMoney(Number(report.total_sales ?? 0))}</span>
            </div>
            <div style={{ background: 'var(--bg-app)', padding: '1rem', borderRadius: '8px', textAlign: 'center', border: '1px solid var(--border-subtle)' }}>
              <span style={{ display: 'block', color: 'var(--text-muted)', fontSize: '0.85rem', marginBottom: '0.25rem' }}>Total Invoices</span>
              <span style={{ display: 'block', fontSize: '1.6rem', fontWeight: 'bold', color: 'var(--text-main)' }}>{report.total_invoices}</span>
            </div>
          </div>

          <h3 style={{ margin: '1rem 0 0.75rem', fontSize: '1rem', fontWeight: 600 }}>Payment Mode Breakup</h3>

          <div style={{ border: '1px solid var(--border-subtle)', borderRadius: '6px', overflow: 'hidden' }}>
            <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr 1fr', background: 'var(--bg-app)', padding: '0.75rem 1rem', fontWeight: 600, fontSize: '0.85rem' }}>
              <span>Mode</span>
              <span>Count</span>
              <span style={{ textAlign: 'right' }}>Amount</span>
            </div>
            {((report.modes || []) as SettlementMode[]).length === 0 ? (
              <div style={{ padding: '1rem', textAlign: 'center', color: 'var(--text-muted)' }}>
                No payments recorded for this date.
              </div>
            ) : (
              ((report.modes || []) as SettlementMode[]).map((m) => (
                <div key={m.payment_mode} style={{ display: 'grid', gridTemplateColumns: '2fr 1fr 1fr', padding: '0.75rem 1rem', borderTop: '1px solid var(--border-subtle)', fontSize: '0.9rem' }}>
                  <span style={{ fontWeight: 500 }}>{modeLabel(m.payment_mode)}</span>
                  <span>{m.count ?? 0}</span>
                  <span style={{ textAlign: 'right', fontVariantNumeric: 'tabular-nums', fontWeight: 600 }}>{formatMoney(Number(m.amount ?? 0))}</span>
                </div>
              ))
            )}
          </div>

          <div style={{ marginTop: '1.5rem', paddingTop: '1rem', borderTop: '2px solid var(--border-subtle)' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', padding: '0.4rem 0', fontSize: '1rem' }}>
              <span>Customer Returns & Refunds</span>
              <span style={{ color: 'var(--danger)', fontWeight: 600 }}>-{formatMoney(Number(report.refunds_total ?? 0))}</span>
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between', padding: '0.5rem 0', fontSize: '1.2rem', fontWeight: 700, borderTop: '1px dashed var(--border-subtle)', marginTop: '0.5rem' }}>
              <span>Net Drawer Collections</span>
              <span style={{ color: 'var(--success)' }}>{formatMoney(Number(report.net_collections ?? 0))}</span>
            </div>
          </div>
        </div>
      </div>
    </AppLayout>
  );
}