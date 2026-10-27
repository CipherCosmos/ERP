import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { reportApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { IconPrinter, IconRefresh } from '../components/Icons';

export default function ProfitLoss() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [report, setReport] = useState<any>(null);
  const [startDate, setStartDate] = useState(() => {
    const d = new Date();
    d.setMonth(d.getMonth() - 1);
    return d.toISOString().split('T')[0];
  });
  const [endDate, setEndDate] = useState(() => new Date().toISOString().split('T')[0]);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadReport();
  }, [user, store?.id, startDate, endDate]);

  const loadReport = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await reportApi.getProfitLoss({
        store_id: store.id,
        start_date: startDate,
        end_date: endDate,
      });
      setReport(data);
    } catch (err) {
      console.error('Failed to load profit & loss report:', err);
    } finally {
      setLoading(false);
    }
  };

  const netProfit = report?.net_profit ?? 0;
  const isProfitable = netProfit >= 0;

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
            Profit & Loss Financial Statement (COGS Model)
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Store: <strong>{store?.name}</strong> &middot; Realized revenue, batch cost of goods sold, inventory write-offs, and operating net
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
            <span>Print P&L Statement</span>
          </button>
        </div>
      </div>

      {/* Date Range Bar */}
      <div className="erp-card" style={{ padding: '0.75rem 1rem', marginBottom: '1.25rem', backgroundColor: '#fff' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', flexWrap: 'wrap' }}>
          <label style={{ fontSize: '0.8rem', fontWeight: 600 }}>Period:</label>
          <input
            type="date"
            className="form-input text-mono"
            style={{ width: 'auto' }}
            value={startDate}
            onChange={(e) => setStartDate(e.target.value)}
          />
          <span style={{ color: 'var(--text-muted)' }}>to</span>
          <input
            type="date"
            className="form-input text-mono"
            style={{ width: 'auto' }}
            value={endDate}
            onChange={(e) => setEndDate(e.target.value)}
          />
        </div>
      </div>

      {loading || !report ? (
        <p style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
          Computing financial statement...
        </p>
      ) : (
        <div style={{ maxWidth: 780, margin: '0 auto' }}>
          <div className="erp-card">
            <div className="erp-card-header">
              <h3 style={{ margin: 0, fontSize: '1rem', fontWeight: 700 }}>
                Income Statement ({report.start_date} to {report.end_date})
              </h3>
              <span className="badge badge-neutral">Audited</span>
            </div>

            <div className="erp-card-body" style={{ padding: '1.5rem' }}>
              {/* Section 1: Operating Revenue */}
              <div style={{ marginBottom: '1.25rem' }}>
                <h4 style={{ textTransform: 'uppercase', fontSize: '0.75rem', letterSpacing: '0.05em', color: 'var(--text-muted)', marginBottom: '0.5rem' }}>
                  Operating Revenue
                </h4>
                <div style={{ display: 'flex', justifyContent: 'space-between', padding: '0.35rem 0', borderBottom: '1px solid var(--border-light)' }}>
                  <span>Gross Sales Billed</span>
                  <span className="text-mono font-bold">₹{(report.total_revenue || 0).toFixed(2)}</span>
                </div>
                <div style={{ display: 'flex', justifyContent: 'space-between', padding: '0.35rem 0', borderBottom: '1px solid var(--border-light)', color: '#dc2626' }}>
                  <span>Less: Customer Sales Returns & Refunds</span>
                  <span className="text-mono">-₹{(report.total_returns || 0).toFixed(2)}</span>
                </div>
                <div style={{ display: 'flex', justifyContent: 'space-between', padding: '0.5rem 0', fontWeight: 700, backgroundColor: '#f8fafc', borderRadius: 4 }}>
                  <span>Net Revenue</span>
                  <span className="text-mono">₹{(report.net_revenue || 0).toFixed(2)}</span>
                </div>
              </div>

              {/* Section 2: COGS */}
              <div style={{ marginBottom: '1.25rem' }}>
                <h4 style={{ textTransform: 'uppercase', fontSize: '0.75rem', letterSpacing: '0.05em', color: 'var(--text-muted)', marginBottom: '0.5rem' }}>
                  Cost of Goods Sold (COGS)
                </h4>
                <div style={{ display: 'flex', justifyContent: 'space-between', padding: '0.35rem 0', borderBottom: '1px solid var(--border-light)', color: '#dc2626' }}>
                  <span>Historical Purchase Cost of Sold Batches</span>
                  <span className="text-mono">-₹{(report.total_cogs || 0).toFixed(2)}</span>
                </div>
                <div style={{ display: 'flex', justifyContent: 'space-between', padding: '0.5rem 0', fontWeight: 700, backgroundColor: '#f8fafc', borderRadius: 4 }}>
                  <span>Gross Operating Profit</span>
                  <span className="text-mono font-bold" style={{ color: (report.gross_profit || 0) >= 0 ? '#16a34a' : '#dc2626' }}>
                    ₹{(report.gross_profit || 0).toFixed(2)}
                  </span>
                </div>
              </div>

              {/* Section 3: Losses & Operating Expenses */}
              <div style={{ marginBottom: '1.5rem' }}>
                <h4 style={{ textTransform: 'uppercase', fontSize: '0.75rem', letterSpacing: '0.05em', color: 'var(--text-muted)', marginBottom: '0.5rem' }}>
                  Losses & Operating Expenditures
                </h4>
                <div style={{ display: 'flex', justifyContent: 'space-between', padding: '0.35rem 0', borderBottom: '1px solid var(--border-light)', color: '#ea580c' }}>
                  <span>Inventory Shrinkage (Damaged & Expired Stock)</span>
                  <span className="text-mono">-₹{(report.total_losses || 0).toFixed(2)}</span>
                </div>
                <div style={{ display: 'flex', justifyContent: 'space-between', padding: '0.35rem 0', borderBottom: '1px solid var(--border-light)', color: '#dc2626' }}>
                  <span>Operating Overhead Expenses (Rent, Utilities, Wages, etc.)</span>
                  <span className="text-mono">-₹{(report.total_expenses || 0).toFixed(2)}</span>
                </div>
              </div>

              {/* Net Profit Banner */}
              <div
                style={{
                  padding: '1rem 1.25rem',
                  borderRadius: 'var(--radius-md)',
                  backgroundColor: isProfitable ? '#f0fdf4' : '#fef2f2',
                  border: `2px solid ${isProfitable ? '#22c55e' : '#ef4444'}`,
                  display: 'flex',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                }}
              >
                <div>
                  <div style={{ fontWeight: 700, fontSize: '1.1rem', color: isProfitable ? '#15803d' : '#991b1b' }}>
                    {isProfitable ? 'NET PROFIT' : 'NET OPERATING LOSS'}
                  </div>
                  <div style={{ fontSize: '0.75rem', color: isProfitable ? '#166534' : '#7f1d1d' }}>
                    Calculated strictly from real transaction data, COGS and expenses
                  </div>
                </div>
                <div className="text-mono" style={{ fontSize: '1.75rem', fontWeight: 800, color: isProfitable ? '#15803d' : '#991b1b' }}>
                  ₹{Math.abs(netProfit).toLocaleString('en-IN', { minimumFractionDigits: 2 })}
                </div>
              </div>
            </div>
          </div>
        </div>
      )}
    </AppLayout>
  );
}