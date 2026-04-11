import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { lossApi, productApi } from '../services/api';

export default function Losses() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [losses, setLosses] = useState<any[]>([]);
  const [products, setProducts] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [showForm, setShowForm] = useState(false);
  const [filterType, setFilterType] = useState('');
  const [form, setForm] = useState({ product_id: '', quantity: '', loss_type: 'damaged', reason: '' });

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadLosses();
    loadProducts();
  }, [user, store, filterType]);

  const loadLosses = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await lossApi.list({ store_id: store.id, loss_type: filterType || undefined });
      setLosses(data);
    } catch (err) {
      console.error('Failed to load losses:', err);
    } finally {
      setLoading(false);
    }
  };

  const loadProducts = async () => {
    if (!store) return;
    try {
      const { data } = await productApi.list({ store_id: store.id });
      setProducts(data);
    } catch (err) {
      console.error('Failed to load products:', err);
    }
  };

  const handleSubmit = async () => {
    if (!store || !user || !form.product_id || !form.quantity || !form.loss_type) {
      alert('Please fill all required fields');
      return;
    }
    try {
      await lossApi.create({
        product_id: form.product_id,
        store_id: store.id,
        quantity: parseFloat(form.quantity),
        loss_type: form.loss_type,
        reason: form.reason,
      });
      setShowForm(false);
      setForm({ product_id: '', quantity: '', loss_type: 'damaged', reason: '' });
      loadLosses();
      alert('Loss recorded');
    } catch (err) {
      console.error('Failed to record loss:', err);
      alert('Failed to record loss');
    }
  };

  const getTypeColor = (type: string) => {
    switch (type) {
      case 'damaged': return '#d32f2f';
      case 'expired': return '#ed6c02';
      case 'stolen': return '#9c27b0';
      case 'adjustment': return '#666';
      default: return '#666';
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
        <h1>Inventory Losses</h1>
        <button onClick={() => setShowForm(true)} style={styles.addBtn}>+ Record Loss</button>
      </div>

      <div style={styles.filters}>
        <select value={filterType} onChange={(e) => setFilterType(e.target.value)} style={styles.select}>
          <option value="">All Types</option>
          <option value="damaged">Damaged</option>
          <option value="expired">Expired</option>
          <option value="stolen">Stolen</option>
          <option value="adjustment">Adjustment</option>
        </select>
      </div>

      {showForm && (
        <div style={styles.formCard}>
          <h3>Record Inventory Loss</h3>
          <div style={styles.formGrid}>
            <select value={form.product_id} onChange={(e) => setForm({ ...form, product_id: e.target.value })} style={styles.select}>
              <option value="">Select Product</option>
              {products.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
            <input type="number" placeholder="Quantity" value={form.quantity} onChange={(e) => setForm({ ...form, quantity: e.target.value })} style={styles.input} />
            <select value={form.loss_type} onChange={(e) => setForm({ ...form, loss_type: e.target.value })} style={styles.select}>
              <option value="damaged">Damaged</option>
              <option value="expired">Expired</option>
              <option value="stolen">Stolen</option>
              <option value="adjustment">Adjustment</option>
            </select>
            <input type="text" placeholder="Reason (optional)" value={form.reason} onChange={(e) => setForm({ ...form, reason: e.target.value })} style={styles.input} />
          </div>
          <div style={styles.formActions}>
            <button onClick={handleSubmit} style={styles.submitBtn}>Submit</button>
            <button onClick={() => setShowForm(false)} style={styles.cancelBtn}>Cancel</button>
          </div>
        </div>
      )}

      <div style={styles.listCard}>
        <table style={styles.table}>
          <thead>
            <tr>
              <th>Date</th>
              <th>Product</th>
              <th>Type</th>
              <th>Qty</th>
              <th>Reason</th>
              <th>Recorded By</th>
            </tr>
          </thead>
          <tbody>
            {losses.map((l) => (
              <tr key={l.id}>
                <td>{new Date(l.created_at).toLocaleDateString()}</td>
                <td>{l.product_id?.slice(0, 8)}</td>
                <td style={{ color: getTypeColor(l.loss_type), textTransform: 'capitalize' }}>{l.loss_type}</td>
                <td>{l.quantity}</td>
                <td>{l.reason || '-'}</td>
                <td>{l.user_id?.slice(0, 8)}</td>
              </tr>
            ))}
            {losses.length === 0 && <tr><td colSpan={6} style={{ textAlign: 'center', padding: '2rem' }}>No losses recorded</td></tr>}
          </tbody>
        </table>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', maxWidth: '1000px', margin: '0 auto' },
  header: { display: 'flex', alignItems: 'center', gap: '1rem', marginBottom: '1rem' },
  backBtn: { padding: '0.5rem 1rem', background: '#f5f5f5', border: '1px solid #ddd', borderRadius: '4px', cursor: 'pointer' },
  addBtn: { marginLeft: 'auto', padding: '0.5rem 1rem', background: '#d32f2f', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  filters: { marginBottom: '1rem' },
  select: { padding: '0.5rem', fontSize: '1rem', border: '1px solid #ddd', borderRadius: '4px' },
  formCard: { background: 'white', padding: '1.5rem', borderRadius: '8px', boxShadow: '0 2px 4px rgba(0,0,0,0.1)', marginBottom: '1rem' },
  formGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(150px, 1fr))', gap: '0.5rem', marginBottom: '1rem' },
  formActions: { display: 'flex', gap: '0.5rem' },
  submitBtn: { padding: '0.5rem 1rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  cancelBtn: { padding: '0.5rem 1rem', background: '#f5f5f5', border: '1px solid #ddd', borderRadius: '4px', cursor: 'pointer' },
  input: { padding: '0.5rem', fontSize: '1rem', border: '1px solid #ddd', borderRadius: '4px' },
  listCard: { background: 'white', padding: '1rem', borderRadius: '8px', boxShadow: '0 2px 4px rgba(0,0,0,0.1)' },
  table: { width: '100%', borderCollapse: 'collapse' },
};