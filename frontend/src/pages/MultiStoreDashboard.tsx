import React, { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { analyticsApi, storeApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { Store } from '../types';
import { formatMoney } from '../utils/format';

/**
 * The shape the multi-store endpoint actually returns. Note that it reports one
 * combined GST figure per store, not a CGST/SGST/IGST split — the split lives on
 * the GST reports, not here.
 */
interface MultiStoreMetrics {
  date_range?: { start: string; end: string };
  totals: {
    revenue: number;
    gst: number;
    invoice_count: number;
  };
  stores: Array<{
    store_id: string;
    store_name: string;
    revenue: number;
    gst: number;
    invoice_count: number;
  }>;
}

export default function MultiStoreDashboard() {
  const navigate = useNavigate();
  // This screen reports across every store, so it never reads the till's own
  // store from the app store — only the owner check and the store list matter.
  const { user } = useAppStore();

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

  const loadData = useCallback(async () => {
    setLoading(true);
    try {
      const storesRes = await storeApi.list();
      const allStores = storesRes.data || [];
      setStores(allStores);

      // "All Stores" is an empty selection here, but the server will not accept
      // an empty store_ids — it asks to be told which stores to include, and
      // answers a blank one with a 400. So spell out every store instead.
      const ids = selectedStores.length > 0 ? selectedStores : allStores.map((s) => s.id);
      if (ids.length === 0) {
        setMetrics(null);
        return;
      }

      const metricsRes = await analyticsApi.multiStore({
        store_ids: ids.join(','),
        range,
      });
      setMetrics(metricsRes.data);
    } catch (err) {
      console.error('Failed to load data:', err);
    } finally {
      setLoading(false);
    }
    // `stores` is written by this loader, never read from state here, so it must
    // stay out of the dependency list or the effect below would re-fire forever.
  }, [selectedStores, range]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  if (!user || user.role !== 'owner') {
    return (
      <AppLayout>
        <div style={{ padding: '3rem 1rem', textAlign: 'center' }}>
          <p style={{ color: 'var(--danger)', fontSize: '1.1rem' }}>Access Denied. Only owners can view multi-store analytics.</p>
        </div>
      </AppLayout>
    );
  }

  return (
    <AppLayout>
      <div style={{ maxWidth: '1200px', margin: '0 auto' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.25rem' }}>
          <h1 style={{ margin: 0, fontSize: '1.5rem', fontWeight: 700 }}>Multi-Store Enterprise Dashboard</h1>
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
              <div style={styles.cardValue}>{formatMoney(Number(metrics.totals?.revenue ?? 0))}</div>
            </div>
            <div style={styles.card}>
              <div style={styles.cardLabel}>Total GST</div>
              <div style={styles.cardValue}>{formatMoney(Number(metrics.totals?.gst ?? 0))}</div>
            </div>
            <div style={styles.card}>
              <div style={styles.cardLabel}>Total Invoices</div>
              <div style={styles.cardValue}>{metrics.totals?.invoice_count ?? 0}</div>
            </div>
          </div>

          <table style={styles.table}>
            <thead>
              <tr>
                <th style={styles.th}>Store</th>
                <th style={styles.th}>Revenue</th>
                <th style={styles.th}>GST</th>
                <th style={styles.th}>Invoices</th>
              </tr>
            </thead>
            <tbody>
              {(metrics.stores || []).map((storeMetric) => (
                <tr key={storeMetric.store_id}>
                  <td style={styles.td}>{storeMetric.store_name}</td>
                  <td style={styles.td}>{formatMoney(Number(storeMetric.revenue ?? 0))}</td>
                  <td style={styles.td}>{formatMoney(Number(storeMetric.gst ?? 0))}</td>
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
    </AppLayout>
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
