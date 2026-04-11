import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { purchaseApi } from '../services/api';

interface PurchaseOrder {
  id: string;
  po_no: string;
  vendor_id: string;
  vendor_name: string;
  order_date: string;
  expected_date: string | null;
  total: number;
  status: string;
}

export default function Purchases() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [orders, setOrders] = useState<PurchaseOrder[]>([]);
  const [loading, setLoading] = useState(false);
  const [statusFilter, setStatusFilter] = useState('');

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user]);

  useEffect(() => {
    if (store) loadOrders();
  }, [store, statusFilter]);

  const loadOrders = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await purchaseApi.list({
        store_id: store.id,
        ...(statusFilter ? { status: statusFilter } : {}),
      });
      setOrders(data || []);
    } catch (err) {
      console.error('Failed to load purchases:', err);
    } finally {
      setLoading(false);
    }
  };

  if (!user || user.role === 'cashier') return null;

  const pendingCount = orders.filter(o => o.status === 'pending').length;
  const receivedCount = orders.filter(o => o.status === 'received').length;
  const partialCount = orders.filter(o => o.status === 'partial').length;

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Purchase Orders</h1>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
      </div>

      <div style={styles.summary}>
        <div style={styles.summaryCard}>
          <div style={styles.summaryLabel}>Pending</div>
          <div style={styles.summaryValue}>{pendingCount}</div>
        </div>
        <div style={styles.summaryCard}>
          <div style={styles.summaryLabel}>Partial</div>
          <div style={styles.summaryValue}>{partialCount}</div>
        </div>
        <div style={styles.summaryCard}>
          <div style={styles.summaryLabel}>Received</div>
          <div style={styles.summaryValue}>{receivedCount}</div>
        </div>
      </div>

      <div style={styles.filters}>
        <select value={statusFilter} onChange={e => setStatusFilter(e.target.value)} style={styles.select}>
          <option value="">All Status</option>
          <option value="pending">Pending</option>
          <option value="partial">Partial</option>
          <option value="received">Received</option>
          <option value="cancelled">Cancelled</option>
        </select>
        <button style={styles.createBtn}>+ New Purchase Order</button>
      </div>

      {loading ? (
        <p>Loading...</p>
      ) : (
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>PO No</th>
              <th style={styles.th}>Vendor</th>
              <th style={styles.th}>Order Date</th>
              <th style={styles.th}>Expected</th>
              <th style={styles.th}>Total</th>
              <th style={styles.th}>Status</th>
              <th style={styles.th}>Action</th>
            </tr>
          </thead>
          <tbody>
            {orders.map((po) => (
              <tr key={po.id} style={styles.tr}>
                <td style={styles.td}>{po.po_no}</td>
                <td style={styles.td}>{po.vendor_name}</td>
                <td style={styles.td}>{po.order_date}</td>
                <td style={styles.td}>{po.expected_date || '-'}</td>
                <td style={styles.td}>₹{po.total.toFixed(2)}</td>
                <td style={styles.td}>
                  <span style={{...styles.statusBadge, background: po.status === 'received' ? '#e8f5e9' : po.status === 'pending' ? '#fff3e0' : '#ffebee'}}>
                    {po.status}
                  </span>
                </td>
                <td style={styles.td}>
                  <button style={styles.viewBtn}>View</button>
                  {po.status === 'pending' && <button style={styles.receiveBtn}>Receive</button>}
                </td>
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
  summary: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(120px, 1fr))', gap: '1rem', marginBottom: '1rem' },
  summaryCard: { background: 'white', padding: '1rem', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  summaryLabel: { fontSize: '0.875rem', color: '#666' },
  summaryValue: { fontSize: '1.5rem', fontWeight: 'bold', color: '#1976d2' },
  filters: { display: 'flex', gap: '1rem', marginBottom: '1rem' },
  select: { padding: '0.75rem', border: '1px solid #ddd', borderRadius: '4px' },
  createBtn: { padding: '0.75rem 1.5rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  table: { width: '100%', background: 'white', borderCollapse: 'collapse', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  tr: { borderBottom: '1px solid #eee' },
  statusBadge: { padding: '0.25rem 0.5rem', borderRadius: '4px', fontSize: '0.75rem', textTransform: 'capitalize' },
  viewBtn: { padding: '0.25rem 0.75rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer', marginRight: '0.25rem' },
  receiveBtn: { padding: '0.25rem 0.75rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
};