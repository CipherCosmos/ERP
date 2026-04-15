import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { analyticsApi, storeApi } from '../services/api';
import { Store } from '../types';

interface MultiStoreMetrics {
  total_revenue: number;
  total_cgst: number;
  total_sgst: number;
  total_igst: number;
  total_invoices: number;
  stores: Array<{
    store_id: string;
    store_name: string;
    revenue: number;
    cgst: number;
    sgst: number;
    igst: number;
    invoice_count: number;
  }>;
}

export default function MultiStoreDashboard() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [metrics, setMetrics] = useState<MultiStoreMetrics | null>(null);
  const [stores, setStores] = useState<Store[]>([]);
  const [loading, setLoading] = useState(false);
  const [selectedStores, setSelectedStores] = useState<string[]>([]);
  const [range, setRange] = useState('today');

  useEffect(() => {
    if (!user || user.role !== 'owner') {
      navigate('/login');
    }
  }, [user, navigate]);

  const loadData = async () => {
    setLoading(true);
    try {
      const [storesRes, metricsRes] = await Promise.all([
        storeApi.list(),
        analyticsApi.multiStore({
          store_ids: selectedStores.join(','),
          range,
        }),
      ]);
      setStores(storesRes.data || []);
      setMetrics(metricsRes.data);
    } catch (err) {
      console.error('Failed to load data:', err);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadData();
  }, [selectedStores, range]);

  if (!user || user.role !== 'owner') {
    return (
      <div style={styles.container}>
        <p style={styles.error}>Access Denied. Only owners can view multi-store analytics.</p>
        <button type="button" onClick={() => navigate('/')} style={styles.backBtn}>
          ← Back to Menu
        </button>
      </div>
    );
  }

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Multi-Store Dashboard</h1>
        <button type="button" onClick={() => navigate('/')} style={styles.backBtn}>
          ← Back to Menu
        </button>
      </div>

      <div style={styles.controls}>
        <div style={styles.controlGroup}>
          <label>Date Range:</label>
          <select value={range} onChange={(e) => setRange(e.target.value)} style={styles.input}>
            <option value="today">Today</option>
            <option value="week">This Week</option>
            <option value="month">This Month</option>
            <option value="year">This Year</option>
          </select>
        </div>

        <div style={styles.controlGroup}>
          <label>Select Stores:</label>
          <div style={styles.storeCheckboxes}>
            <label style={styles.checkbox}>
              <input
                type="checkbox"
                checked={selectedStores.length === 0}
                onChange={() => setSelectedStores([])}
              />
              All Stores
            </label>
            {stores.map((s) => (
              <label key={s.id} style={styles.checkbox}>
                <input
                  type="checkbox"
                  checked={selectedStores.length === 0 || selectedStores.includes(s.id)}
                  onChange={(e) => {
                    if (selectedStores.length === 0) {
                      setSelectedStores(stores.map((st) => st.id).filter((id) => id !== s.id));
                    } else if (e.target.checked) {
                      setSelectedStores([...selectedStores, s.id]);
                    } else {
                      setSelectedStores(selectedStores.filter((id) => id !== s.id));
                    }
                  }}
                />
                {s.name}
              </label>
            ))}
          </div>
        </div>
      </div>

      {loading ? (
        <p>Loading...</p>
      ) : metrics ? (
        <>
          <div style={styles.summaryCards}>
            <div style={styles.card}>
              <div style={styles.cardLabel}>Total Revenue</div>
              <div style={styles.cardValue}>₹{Number(metrics.total_revenue).toFixed(2)}</div>
            </div>
            <div style={styles.card}>
              <div style={styles.cardLabel}>Total GST</div>
              <div style={styles.cardValue}>
                ₹{(Number(metrics.total_cgst) + Number(metrics.total_sgst) + Number(metrics.total_igst)).toFixed(2)}
              </div>
            </div>
            <div style={styles.card}>
              <div style={styles.cardLabel}>Total Invoices</div>
              <div style={styles.cardValue}>{metrics.total_invoices}</div>
            </div>
          </div>

          <table style={styles.table}>
            <thead>
              <tr>
                <th style={styles.th}>Store</th>
                <th style={styles.th}>Revenue</th>
                <th style={styles.th}>CGST</th>
                <th style={styles.th}>SGST</th>
                <th style={styles.th}>IGST</th>
                <th style={styles.th}>Invoices</th>
              </tr>
            </thead>
            <tbody>
              {metrics.stores.map((storeMetric) => (
                <tr key={storeMetric.store_id}>
                  <td style={styles.td}>{storeMetric.store_name}</td>
                  <td style={styles.td}>₹{Number(storeMetric.revenue).toFixed(2)}</td>
                  <td style={styles.td}>₹{Number(storeMetric.cgst).toFixed(2)}</td>
                  <td style={styles.td}>₹{Number(storeMetric.sgst).toFixed(2)}</td>
                  <td style={styles.td}>₹{Number(storeMetric.igst).toFixed(2)}</td>
                  <td style={styles.td}>{storeMetric.invoice_count}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      ) : (
        <p style={styles.empty}>No data available</p>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', background: '#f5f5f5', minHeight: '100vh' },
  header: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' },
  backBtn: { padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  error: { color: '#f44336', marginTop: '2rem', textAlign: 'center' },
  controls: { background: 'white', padding: '1.5rem', borderRadius: '8px', marginBottom: '1.5rem', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  controlGroup: { marginBottom: '1rem' },
  input: { padding: '0.5rem', border: '1px solid #ddd', borderRadius: '4px', marginLeft: '0.5rem' },
  storeCheckboxes: { display: 'flex', gap: '1rem', flexWrap: 'wrap', marginTop: '0.5rem' },
  checkbox: { display: 'flex', alignItems: 'center', gap: '0.5rem', cursor: 'pointer' },
  summaryCards: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: '1rem', marginBottom: '1.5rem' },
  card: { background: 'white', padding: '1.5rem', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  cardLabel: { color: '#666', fontSize: '0.9rem', marginBottom: '0.5rem' },
  cardValue: { fontSize: '1.8rem', fontWeight: 'bold', color: '#1976d2' },
  table: { width: '100%', background: 'white', borderCollapse: 'collapse', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9', fontWeight: 'bold' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  empty: { textAlign: 'center', color: '#999', marginTop: '2rem' },
};
