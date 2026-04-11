import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { returnApi, productApi } from '../services/api';

export default function Returns() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [returns, setReturns] = useState<any[]>([]);
  const [products, setProducts] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState({ product_id: '', quantity: '', amount: '', reason: '', refund_mode: 'cash' });

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadReturns();
    loadProducts();
  }, [user, store]);

  const loadReturns = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await returnApi.list(store.id);
      setReturns(data);
    } catch (err) {
      console.error('Failed to load returns:', err);
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
    if (!store || !user || !form.product_id || !form.quantity || !form.amount || !form.reason) {
      alert('Please fill all required fields');
      return;
    }
    try {
      await returnApi.create({
        product_id: form.product_id,
        quantity: parseFloat(form.quantity),
        amount: parseFloat(form.amount),
        reason: form.reason,
        refund_mode: form.refund_mode,
        store_id: store.id,
        user_id: user.id,
      });
      setShowForm(false);
      setForm({ product_id: '', quantity: '', amount: '', reason: '', refund_mode: 'cash' });
      loadReturns();
      alert('Return request created');
    } catch (err) {
      console.error('Failed to create return:', err);
      alert('Failed to create return');
    }
  };

  const handleApprove = async (id: string) => {
    if (!store) return;
    try {
      await returnApi.approve(id, store.id);
      loadReturns();
      alert('Return approved and stock restored');
    } catch (err) {
      console.error('Failed to approve return:', err);
    }
  };

  const getStatusColor = (status: string) => {
    switch (status) {
      case 'completed': return '#2e7d32';
      case 'pending': return '#ed6c02';
      default: return '#666';
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
        <h1>Returns Management</h1>
        <button onClick={() => setShowForm(true)} style={styles.addBtn}>+ New Return</button>
      </div>

      {showForm && (
        <div style={styles.formCard}>
          <h3>New Return Request</h3>
          <div style={styles.formGrid}>
            <select value={form.product_id} onChange={(e) => setForm({ ...form, product_id: e.target.value })} style={styles.select}>
              <option value="">Select Product</option>
              {products.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
            <input type="number" placeholder="Quantity" value={form.quantity} onChange={(e) => setForm({ ...form, quantity: e.target.value })} style={styles.input} />
            <input type="number" placeholder="Amount" value={form.amount} onChange={(e) => setForm({ ...form, amount: e.target.value })} style={styles.input} />
            <input type="text" placeholder="Reason" value={form.reason} onChange={(e) => setForm({ ...form, reason: e.target.value })} style={styles.input} />
            <select value={form.refund_mode} onChange={(e) => setForm({ ...form, refund_mode: e.target.value })} style={styles.select}>
              <option value="cash">Cash</option>
              <option value="upi">UPI</option>
              <option value="card">Card</option>
            </select>
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
              <th>ID</th>
              <th>Product</th>
              <th>Qty</th>
              <th>Amount</th>
              <th>Reason</th>
              <th>Status</th>
              <th>Action</th>
            </tr>
          </thead>
          <tbody>
            {returns.map((r) => (
              <tr key={r.id}>
                <td>{r.id.slice(0, 8)}</td>
                <td>{r.product_id?.slice(0, 8)}</td>
                <td>{r.quantity}</td>
                <td>₹{r.amount?.toFixed(2)}</td>
                <td>{r.reason}</td>
                <td style={{ color: getStatusColor(r.status) }}>{r.status}</td>
                <td>
                  {r.status === 'pending' && <button onClick={() => handleApprove(r.id)} style={styles.approveBtn}>Approve</button>}
                </td>
              </tr>
            ))}
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
  addBtn: { marginLeft: 'auto', padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  formCard: { background: 'white', padding: '1.5rem', borderRadius: '8px', boxShadow: '0 2px 4px rgba(0,0,0,0.1)', marginBottom: '1rem' },
  formGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(150px, 1fr))', gap: '0.5rem', marginBottom: '1rem' },
  formActions: { display: 'flex', gap: '0.5rem' },
  submitBtn: { padding: '0.5rem 1rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  cancelBtn: { padding: '0.5rem 1rem', background: '#f5f5f5', border: '1px solid #ddd', borderRadius: '4px', cursor: 'pointer' },
  input: { padding: '0.5rem', fontSize: '1rem', border: '1px solid #ddd', borderRadius: '4px' },
  select: { padding: '0.5rem', fontSize: '1rem', border: '1px solid #ddd', borderRadius: '4px' },
  listCard: { background: 'white', padding: '1rem', borderRadius: '8px', boxShadow: '0 2px 4px rgba(0,0,0,0.1)' },
  table: { width: '100%', borderCollapse: 'collapse' },
  approveBtn: { padding: '0.25rem 0.5rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer', fontSize: '0.875rem' },
};