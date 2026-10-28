import React, { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { analyticsApi } from '../services/api';
import { StatCard, BarChart, PieChart } from '../components/Charts';
import { addDaysISO, toISODate, todayISO } from '../utils/format';

interface DashboardData {
  revenue: number;
  gst_collected: number;
  discount_given: number;
  invoice_count: number;
  transaction_count: number;
  avg_bill_value: number;
  low_stock_count: number;
  credit_outstanding: number;
  inventory_valuation?: number;
  expiring_count?: number;
  pending_po_count?: number;
  vendor_payables?: number;
}

interface TopProduct {
  product: string;
  quantity: number;
  sales: number;
}

interface PaymentMode {
  mode: string;
  count: number;
  total: number;
}

export default function Dashboard() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [data, setData] = useState<DashboardData | null>(null);
  const [loading, setLoading] = useState(false);
  const [range, setRange] = useState('today');
  const [topProducts, setTopProducts] = useState<TopProduct[]>([]);
  const [paymentBreakup, setPaymentBreakup] = useState<PaymentMode[]>([]);

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user, navigate]);

  // Calendar dates, computed as a calendar does. The version this replaces
  // rendered each one with toISOString, which converts to UTC first: in India,
  // five and a half hours ahead, every range between midnight and half past
  // five in the morning asked the server for the wrong day.
  const getRangeDates = useCallback(() => {
    const end = todayISO();
    const now = new Date();
    let start = end;
    if (range === 'week') {
      start = addDaysISO(end, -7);
    } else if (range === 'month') {
      const d = new Date(now);
      d.setMonth(d.getMonth() - 1);
      start = toISODate(d);
    } else if (range === 'year') {
      const d = new Date(now);
      d.setFullYear(d.getFullYear() - 1);
      start = toISODate(d);
    }
    return { start, end };
  }, [range]);

  const loadDashboard = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data: dashboardData } = await analyticsApi.dashboard({ store_id: store.id, range });
      setData(dashboardData);

      // Load charts data
      const { start, end } = getRangeDates();
      const [topRes, payRes] = await Promise.allSettled([
        analyticsApi.topProducts({ store_id: store.id, start_date: start, end_date: end, limit: 5 }),
        analyticsApi.paymentBreakup({ store_id: store.id, start_date: start, end_date: end }),
      ]);
      if (topRes.status === 'fulfilled') setTopProducts(topRes.value.data || []);
      if (payRes.status === 'fulfilled') setPaymentBreakup(payRes.value.data || []);
    } catch (err) {
      console.error('Failed to load dashboard:', err);
    } finally {
      setLoading(false);
    }
  }, [store, range, getRangeDates]);

  // Re-runs when the range dropdown changes, because loadDashboard is rebuilt
  // with it — the same behaviour the old [store, range] dependency list had.
  useEffect(() => {
    if (store) loadDashboard();
  }, [store, loadDashboard]);

  if (!user || user.role === 'cashier') return null;

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Dashboard</h1>
        <div style={styles.rangeSelect}>
          <select value={range} onChange={(e) => setRange(e.target.value)} style={styles.select}>
            <option value="today">Today</option>
            <option value="week">This Week</option>
            <option value="month">This Month</option>
            <option value="year">This Year</option>
          </select>
        </div>
      </div>

      {loading ? (
        <p>Loading...</p>
      ) : (
        <>
          {/* KPI Stat Cards */}
          <div style={styles.statsGrid}>
            <StatCard
              title="Revenue"
              value={`\u20B9${data?.revenue?.toFixed(2) || '0.00'}`}
              color="#1976d2"
            />
            <StatCard
              title="GST Collected"
              value={`\u20B9${data?.gst_collected?.toFixed(2) || '0.00'}`}
              color="#4caf50"
            />
            <StatCard
              title="Invoices"
              value={data?.invoice_count || 0}
              color="#ff9800"
            />
            <StatCard
              title="Avg Bill Value"
              value={`\u20B9${data?.avg_bill_value?.toFixed(2) || '0.00'}`}
              color="#9c27b0"
            />
          </div>

          {/* Alerts & Operational Stats row */}
          <div style={styles.alertsGrid}>
            <StatCard
              title="Stock Valuation"
              value={`\u20B9${data?.inventory_valuation != null ? data.inventory_valuation.toFixed(2) : '0.00'}`}
              color="#0284c7"
            />
            <StatCard
              title="Low Stock Items"
              value={data?.low_stock_count || 0}
              subtitle={(data?.low_stock_count || 0) > 0 ? 'Click Inventory to review' : 'All good'}
              color={(data?.low_stock_count || 0) > 0 ? '#ff5722' : '#4caf50'}
            />
            <StatCard
              title="Expiring (30 Days)"
              value={data?.expiring_count || 0}
              subtitle={(data?.expiring_count || 0) > 0 ? 'Expiring soon' : 'None expiring'}
              color={(data?.expiring_count || 0) > 0 ? '#d97706' : '#4caf50'}
            />
            <StatCard
              title="Credit Outstanding"
              value={`\u20B9${data?.credit_outstanding?.toFixed(2) || '0.00'}`}
              subtitle={(data?.credit_outstanding || 0) > 0 ? 'Click Credit to review' : 'All clear'}
              color={(data?.credit_outstanding || 0) > 0 ? '#e91e63' : '#4caf50'}
            />
            <StatCard
              title="Vendor Payables"
              value={`\u20B9${data?.vendor_payables != null ? data.vendor_payables.toFixed(2) : '0.00'}`}
              color="#7c3aed"
            />
            <StatCard
              title="Pending POs"
              value={data?.pending_po_count || 0}
              color="#0d9488"
            />
          </div>

          {/* Alert action buttons */}
          <div style={styles.alertActions}>
            {(data?.low_stock_count || 0) > 0 && (
              <button style={styles.alertBtn} onClick={() => navigate('/inventory')}>View Low Stock</button>
            )}
            {(data?.expiring_count || 0) > 0 && (
              <button style={styles.alertBtn} onClick={() => navigate('/expiry')}>View Expiring</button>
            )}
            {(data?.pending_po_count || 0) > 0 && (
              <button style={styles.alertBtn} onClick={() => navigate('/purchases')}>View Purchases</button>
            )}
            {(data?.credit_outstanding || 0) > 0 && (
              <button style={styles.alertBtn} onClick={() => navigate('/credit')}>View Credit</button>
            )}
          </div>

          {/* Charts row */}
          <div style={styles.chartsGrid}>
            <div style={styles.chartCard}>
              <h3 style={styles.chartTitle}>Top 5 Products by Sales</h3>
              <BarChart
                data={topProducts.map(p => ({ label: p.product, value: p.sales }))}
                height={230}
                width={460}
              />
            </div>
            <div style={styles.chartCard}>
              <h3 style={styles.chartTitle}>Payment Mode Breakdown</h3>
              <PieChart
                data={paymentBreakup.map(p => ({ label: p.mode, value: p.total }))}
                size={190}
              />
            </div>
          </div>

          {/* Quick Actions */}
          <div style={styles.quickActions}>
            <h3>Quick Actions</h3>
            <div style={styles.actionButtons}>
              <button style={styles.actionBtn} onClick={() => navigate('/billing')}>New Sale</button>
              <button style={styles.actionBtn} onClick={() => navigate('/inventory')}>Inventory</button>
              <button style={styles.actionBtn} onClick={() => navigate('/purchases')}>Purchase</button>
              <button style={styles.actionBtn} onClick={() => navigate('/vendors')}>Vendors</button>
            </div>
          </div>
        </>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', background: '#f5f5f5', minHeight: '100vh' },
  header: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' },
  rangeSelect: {},
  select: { padding: '0.5rem', fontSize: '1rem', borderRadius: '4px', border: '1px solid #ddd' },
  statsGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: '1rem', marginBottom: '1rem' },
  alertsGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: '1rem', marginBottom: '0.5rem' },
  alertActions: { display: 'flex', gap: '0.5rem', marginBottom: '1rem' },
  alertBtn: { padding: '0.25rem 0.75rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer', fontSize: '0.85rem' },
  chartsGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))', gap: '1rem', marginBottom: '1rem' },
  chartCard: { background: 'white', padding: '1rem', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  chartTitle: { margin: '0 0 0.75rem 0', fontSize: '1rem', color: '#333' },
  quickActions: { background: 'white', padding: '1rem', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  actionButtons: { display: 'flex', gap: '0.5rem', flexWrap: 'wrap', marginTop: '0.5rem' },
  actionBtn: { padding: '0.75rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
};
