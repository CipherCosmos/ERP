import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';

interface DashboardData {
  revenue: number;
  gst_collected: number;
  discount_given: number;
  invoice_count: number;
  transaction_count: number;
  avg_bill_value: number;
  low_stock_count: number;
  credit_outstanding: number;
}

export default function Dashboard() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [data, setData] = useState<DashboardData | null>(null);
  const [loading, setLoading] = useState(false);
  const [range, setRange] = useState('today');

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user]);

  useEffect(() => {
    if (store) loadDashboard();
  }, [store, range]);

  const loadDashboard = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data: dashboardData } = await fetch(`/api/v1/analytics/dashboard?store_id=${store.id}&range=${range}`).then(r => r.json());
      setData(dashboardData);
    } catch (err) {
      console.error('Failed to load dashboard:', err);
    } finally {
      setLoading(false);
    }
  };

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
          <div style={styles.statsGrid}>
            <div style={styles.statCard}>
              <div style={styles.statLabel}>Revenue</div>
              <div style={styles.statValue}>₹{data?.revenue?.toFixed(2) || '0.00'}</div>
            </div>
            <div style={styles.statCard}>
              <div style={styles.statLabel}>GST Collected</div>
              <div style={styles.statValue}>₹{data?.gst_collected?.toFixed(2) || '0.00'}</div>
            </div>
            <div style={styles.statCard}>
              <div style={styles.statLabel}>Invoices</div>
              <div style={styles.statValue}>{data?.invoice_count || 0}</div>
            </div>
            <div style={styles.statCard}>
              <div style={styles.statLabel}>Avg Bill Value</div>
              <div style={styles.statValue}>₹{data?.avg_bill_value?.toFixed(2) || '0.00'}</div>
            </div>
          </div>

          <div style={styles.alertsGrid}>
            <div style={{...styles.alertCard, background: data?.low_stock_count > 0 ? '#fff3e0' : '#e8f5e9'}}>
              <div style={styles.alertLabel}>Low Stock Items</div>
              <div style={styles.alertValue}>{data?.low_stock_count || 0}</div>
              {data?.low_stock_count > 0 && (
                <button style={styles.alertBtn} onClick={() => navigate('/inventory')}>View</button>
              )}
            </div>
            <div style={{...styles.alertCard, background: data?.credit_outstanding > 0 ? '#ffebee' : '#e8f5e9'}}>
              <div style={styles.alertLabel}>Credit Outstanding</div>
              <div style={styles.alertValue}>₹{data?.credit_outstanding?.toFixed(2) || '0.00'}</div>
              {data?.credit_outstanding > 0 && (
                <button style={styles.alertBtn} onClick={() => navigate('/credit')}>View</button>
              )}
            </div>
          </div>

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
  statCard: { background: 'white', padding: '1.5rem', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  statLabel: { fontSize: '0.875rem', color: '#666', marginBottom: '0.5rem' },
  statValue: { fontSize: '1.5rem', fontWeight: 'bold', color: '#1976d2' },
  alertsGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: '1rem', marginBottom: '1rem' },
  alertCard: { padding: '1rem', borderRadius: '8px' },
  alertLabel: { fontSize: '0.875rem', color: '#666', marginBottom: '0.25rem' },
  alertValue: { fontSize: '1.25rem', fontWeight: 'bold', marginBottom: '0.5rem' },
  alertBtn: { padding: '0.25rem 0.75rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  quickActions: { background: 'white', padding: '1rem', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  actionButtons: { display: 'flex', gap: '0.5rem', flexWrap: 'wrap', marginTop: '0.5rem' },
  actionBtn: { padding: '0.75rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
};