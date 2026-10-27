import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { reportApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { IconPrinter, IconRefresh, IconCredit } from '../components/Icons';

export default function CreditAging() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [report, setReport] = useState<any[]>([]);
  const [totalOutstanding, setTotalOutstanding] = useState(0);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadReport();
  }, [user, store]);

  const loadReport = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await reportApi.getCreditAging({ store_id: store.id });
      setReport(data.report || []);
      setTotalOutstanding(data.total_outstanding || 0);
    } catch (err) {
      console.error('Failed to load aging report:', err);
    } finally {
      setLoading(false);
    }
  };

  return (
    <AppLayout>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          marginBottom: '1rem',
          flexWrap: 'wrap',
          gap: '0.75rem',
        }}
      >
        <div>
          <h1 style={{ margin: 0, fontSize: '1.4rem', fontWeight: 700 }}>
            Customer Credit Aging Analysis (Khata Dues)
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Aging buckets (Current 0–30 days, 31–60 days, 61–90 days, 90+ days) for overdue debt recovery
          </p>
        </div>

        <div style={{ display: 'flex', gap: '0.5rem' }}>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={loadReport}
            disabled={loading}
          >
            <IconRefresh size={16} />
            <span>Refresh</span>
          </button>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => window.print()}
          >
            <IconPrinter size={16} />
            <span>Print Report</span>
          </button>
          <button
            type="button"
            className="btn btn-primary btn-sm"
            onClick={() => navigate('/credit')}
          >
            <IconCredit size={16} />
            <span>Open Khata Ledger</span>
          </button>
        </div>
      </div>

      {/* Summary KPI Card */}
      <div className="erp-card" style={{ borderLeft: '4px solid #ea580c', marginBottom: '1.25rem' }}>
        <div className="erp-card-body" style={{ padding: '1rem 1.25rem' }}>
          <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-muted)', textTransform: 'uppercase' }}>
            Total Outstanding Accounts Receivable
          </div>
          <div style={{ fontSize: '1.75rem', fontWeight: 700, color: '#ea580c', margin: '0.25rem 0' }}>
            ₹{totalOutstanding.toLocaleString('en-IN', { minimumFractionDigits: 2 })}
          </div>
          <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
            Across {report.length} customer credit accounts
          </div>
        </div>
      </div>

      {/* Aging Table */}
      <div className="erp-table-container">
        <table className="erp-table">
          <thead>
            <tr>
              <th>Customer Name</th>
              <th className="text-right">Total Due (₹)</th>
              <th className="text-right">Current (0–30 Days)</th>
              <th className="text-right">31–60 Days</th>
              <th className="text-right">61–90 Days</th>
              <th className="text-right">Over 90 Days</th>
              <th className="text-center">Action</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={7} style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-muted)' }}>
                  Calculating aging buckets...
                </td>
              </tr>
            ) : report.length === 0 ? (
              <tr>
                <td colSpan={7} style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
                  No outstanding receivables currently. All customer accounts are settled!
                </td>
              </tr>
            ) : (
              report.map((r) => (
                <tr key={r.customer_id}>
                  <td style={{ fontWeight: 600 }}>{r.customer_name}</td>
                  <td className="text-right text-mono font-bold">
                    ₹{(r.total_due || 0).toFixed(2)}
                  </td>
                  <td className="text-right text-mono" style={{ color: r.current > 0 ? '#16a34a' : 'inherit' }}>
                    ₹{(r.current || 0).toFixed(2)}
                  </td>
                  <td className="text-right text-mono" style={{ color: r.days_30 > 0 ? '#b45309' : 'inherit', fontWeight: r.days_30 > 0 ? 600 : 400 }}>
                    ₹{(r.days_30 || 0).toFixed(2)}
                  </td>
                  <td className="text-right text-mono" style={{ color: r.days_60 > 0 ? '#ea580c' : 'inherit', fontWeight: r.days_60 > 0 ? 600 : 400 }}>
                    ₹{(r.days_60 || 0).toFixed(2)}
                  </td>
                  <td className="text-right text-mono font-bold" style={{ color: r.over_90 > 0 ? '#dc2626' : 'inherit' }}>
                    ₹{(r.over_90 || 0).toFixed(2)}
                  </td>
                  <td className="text-center">
                    <button
                      type="button"
                      className="btn btn-outline btn-sm"
                      onClick={() => navigate('/credit')}
                    >
                      Collect
                    </button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </AppLayout>
  );
}