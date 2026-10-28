import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { analyticsApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { IconReports, IconPrinter, IconRefresh } from '../components/Icons';

export default function Reports() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [activeTab, setActiveTab] = useState<'category' | 'products' | 'hourly' | 'payment'>('category');
  const [reportData, setReportData] = useState<any>(null);
  const [loading, setLoading] = useState(false);
  const [startDate, setStartDate] = useState(new Date().toISOString().slice(0, 10));
  const [endDate, setEndDate] = useState(new Date().toISOString().slice(0, 10));

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
  }, [user]);

  useEffect(() => {
    if (store) loadReport();
  }, [store?.id, activeTab, startDate, endDate]);

  const loadReport = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const params = { store_id: store.id, start_date: startDate, end_date: endDate };
      let res;
      switch (activeTab) {
        case 'category':
          res = await analyticsApi.salesByCategory(params);
          break;
        case 'products':
          res = await analyticsApi.topProducts(params);
          break;
        case 'hourly':
          res = await analyticsApi.hourlySales({ store_id: store.id, date: startDate });
          break;
        case 'payment':
          res = await analyticsApi.paymentBreakup(params);
          break;
        default:
          res = { data: null };
      }
      setReportData(res.data);
    } catch (err) {
      console.error('Failed to load report:', err);
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
            Operational Sales & Commercial Reports
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Store: <strong>{store?.name}</strong> &middot; Category performance, sales volume, peak hours, and payment modes
          </p>
        </div>

        <div style={{ display: 'flex', gap: '0.5rem' }}>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => navigate('/profit-loss')}
          >
            <span>P&L Statement</span>
          </button>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => window.print()}
          >
            <IconPrinter size={16} />
            <span>Print Report</span>
          </button>
        </div>
      </div>

      {/* Date Filter & Tabs Bar */}
      <div className="erp-card" style={{ padding: '0.75rem 1rem', marginBottom: '1.25rem', backgroundColor: '#fff' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '0.75rem' }}>
          <div style={{ display: 'flex', gap: '0.25rem' }}>
            {[
              { id: 'category', label: 'Sales by Category' },
              { id: 'products', label: 'Top Products' },
              { id: 'payment', label: 'Payment Modes' },
              { id: 'hourly', label: 'Hourly Peak Trend' },
            ].map((t) => (
              <button
                key={t.id}
                type="button"
                className={activeTab === t.id ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
                onClick={() => setActiveTab(t.id as any)}
              >
                {t.label}
              </button>
            ))}
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
            <input
              type="date"
              className="form-input text-mono"
              style={{ width: 'auto' }}
              value={startDate}
              onChange={(e) => setStartDate(e.target.value)}
            />
            {activeTab !== 'hourly' && (
              <>
                <span style={{ color: 'var(--text-muted)' }}>to</span>
                <input
                  type="date"
                  className="form-input text-mono"
                  style={{ width: 'auto' }}
                  value={endDate}
                  onChange={(e) => setEndDate(e.target.value)}
                />
              </>
            )}
            <button
              type="button"
              className="btn btn-outline btn-sm"
              onClick={loadReport}
              disabled={loading}
            >
              <IconRefresh size={16} />
            </button>
          </div>
        </div>
      </div>

      {/* Report Tables */}
      <div className="erp-table-container">
        {loading ? (
          <p style={{ textAlign: 'center', padding: '2.5rem', color: 'var(--text-muted)' }}>
            Loading report data...
          </p>
        ) : !reportData || (Array.isArray(reportData) && reportData.length === 0) ? (
          <p style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
            No sales data recorded for the selected date range.
          </p>
        ) : activeTab === 'category' ? (
          <table className="erp-table">
            <thead>
              <tr>
                <th>Category Name</th>
                <th className="text-center">Items Sold Count</th>
                <th className="text-right">Total Revenue (₹)</th>
              </tr>
            </thead>
            <tbody>
              {reportData.map((row: any, i: number) => (
                <tr key={row.name || i}>
                  <td style={{ fontWeight: 600 }}>{row.name}</td>
                  <td className="text-center text-mono">{row.item_count || 0}</td>
                  <td className="text-right text-mono font-bold">
                    ₹{(row.sales || 0).toLocaleString('en-IN', { minimumFractionDigits: 2 })}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : activeTab === 'products' ? (
          <table className="erp-table">
            <thead>
              <tr>
                <th>Product Name</th>
                <th className="text-center">Units Sold</th>
                <th className="text-right">Total Revenue (₹)</th>
              </tr>
            </thead>
            <tbody>
              {reportData.map((row: any, i: number) => (
                <tr key={row.product || i}>
                  <td style={{ fontWeight: 600 }}>{row.product}</td>
                  <td className="text-center text-mono font-bold">{row.quantity}</td>
                  <td className="text-right text-mono font-bold">
                    ₹{(row.sales || 0).toLocaleString('en-IN', { minimumFractionDigits: 2 })}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : activeTab === 'payment' ? (
          <table className="erp-table">
            <thead>
              <tr>
                <th>Payment Tender</th>
                <th className="text-center">Bill Count</th>
                <th className="text-right">Total Amount (₹)</th>
              </tr>
            </thead>
            <tbody>
              {reportData.map((row: any, i: number) => (
                <tr key={row.mode || i}>
                  <td style={{ fontWeight: 600, textTransform: 'capitalize' }}>{row.mode}</td>
                  <td className="text-center text-mono">{row.count}</td>
                  <td className="text-right text-mono font-bold">
                    ₹{(row.total || 0).toLocaleString('en-IN', { minimumFractionDigits: 2 })}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : activeTab === 'hourly' ? (
          <table className="erp-table">
            <thead>
              <tr>
                <th>Hour Window</th>
                <th className="text-center">Transactions</th>
                <th className="text-right">Sales Volume (₹)</th>
              </tr>
            </thead>
            <tbody>
              {reportData.map((row: any, i: number) => (
                <tr key={row.hour || i}>
                  <td className="text-mono font-bold">{row.hour}:00 - {row.hour}:59</td>
                  <td className="text-center text-mono">{row.count}</td>
                  <td className="text-right text-mono font-bold">
                    ₹{(row.sales || 0).toLocaleString('en-IN', { minimumFractionDigits: 2 })}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : null}
      </div>
    </AppLayout>
  );
}
