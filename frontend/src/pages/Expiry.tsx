import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { expiryApi } from '../services/api';

interface ExpiryItem {
  id: string;
  product_name: string;
  batch_no: string;
  quantity: number;
  expiry_date: string;
  days_until_expiry: number;
}

export default function Expiry() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [expiring, setExpiring] = useState<ExpiryItem[]>([]);
  const [expired, setExpired] = useState<ExpiryItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [activeTab, setActiveTab] = useState<'expiring' | 'expired'>('expiring');
  const [days, setDays] = useState(30);

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user]);

  useEffect(() => {
    if (store) {
      loadExpiring();
      loadExpired();
    }
  }, [store, days]);

  const loadExpiring = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await expiryApi.expiring({ store_id: store.id, days: String(days) });
      setExpiring(data || []);
    } catch (err) {
      console.error('Failed to load expiring:', err);
    } finally {
      setLoading(false);
    }
  };

  const loadExpired = async () => {
    if (!store) return;
    try {
      const { data } = await expiryApi.expired({ store_id: store.id });
      setExpired(data || []);
    } catch (err) {
      console.error('Failed to load expired:', err);
    }
  };

  if (!user || user.role === 'cashier') return null;

  const items = activeTab === 'expiring' ? expiring : expired;
  const isExpired = activeTab === 'expired';

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Expiry Tracking</h1>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
      </div>

      <div style={styles.controls}>
        <div style={styles.filterGroup}>
          <label>Alert before (days):</label>
          <select value={days} onChange={e => setDays(parseInt(e.target.value))} style={styles.select}>
            <option value={7}>7 days</option>
            <option value={14}>14 days</option>
            <option value={30}>30 days</option>
            <option value={60}>60 days</option>
            <option value={90}>90 days</option>
          </select>
        </div>
      </div>

      <div style={styles.tabs}>
        <button style={activeTab === 'expiring' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('expiring')}>
          Expiring Soon ({expiring.length})
        </button>
        <button style={activeTab === 'expired' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('expired')}>
          Expired ({expired.length})
        </button>
      </div>

      {loading ? (
        <p>Loading...</p>
      ) : items.length === 0 ? (
        <div style={styles.empty}>
          <p>No items {isExpired ? 'expired' : 'expiring within ' + days + ' days'}</p>
        </div>
      ) : (
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Product</th>
              <th style={styles.th}>Batch No</th>
              <th style={styles.th}>Quantity</th>
              <th style={styles.th}>Expiry Date</th>
              {!isExpired && <th style={styles.th}>Days Left</th>}
            </tr>
          </thead>
          <tbody>
            {items.map((item) => (
              <tr key={item.id} style={{...styles.tr, background: isExpired ? '#ffebee' : item.days_until_expiry <= 7 ? '#fff3e0' : 'white'}}>
                <td style={styles.td}>{item.product_name}</td>
                <td style={styles.td}>{item.batch_no}</td>
                <td style={styles.td}>{item.quantity.toFixed(2)}</td>
                <td style={styles.td}>{item.expiry_date}</td>
                {!isExpired && (
                  <td style={styles.td}>
                    <span style={{...styles.badge, color: item.days_until_expiry <= 7 ? '#d32f2f' : '#666'}}>
                      {item.days_until_expiry} days
                    </span>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', background: '#f5f5f5', minHeight: '100vh' },
  header: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' },
  backBtn: { padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  controls: { display: 'flex', gap: '1rem', marginBottom: '1rem' },
  filterGroup: { display: 'flex', alignItems: 'center', gap: '0.5rem' },
  select: { padding: '0.5rem', border: '1px solid #ddd', borderRadius: '4px' },
  tabs: { display: 'flex', gap: '0.5rem', marginBottom: '1rem' },
  tab: { padding: '0.75rem 1.5rem', background: 'white', border: '1px solid #ddd', borderRadius: '4px', cursor: 'pointer' },
  tabActive: { padding: '0.75rem 1.5rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  empty: { padding: '2rem', textAlign: 'center', background: 'white', borderRadius: '8px' },
  table: { width: '100%', background: 'white', borderCollapse: 'collapse', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  tr: { borderBottom: '1px solid #eee' },
  badge: { fontWeight: 'bold' },
};