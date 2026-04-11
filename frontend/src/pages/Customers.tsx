import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { customerApi } from '../services/api';

interface Customer {
  id: string;
  name: string;
  name_hi: string;
  phone: string;
  address: string;
  gstin: string;
  credit_limit: number;
  is_wholesale?: boolean;
  outstanding?: number;
}

export default function Customers() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [customers, setCustomers] = useState<Customer[]>([]);
  const [loading, setLoading] = useState(false);
  const [search, setSearch] = useState('');
  const [showModal, setShowModal] = useState(false);
  const [form, setForm] = useState({
    name: '',
    name_hi: '',
    phone: '',
    address: '',
    gstin: '',
    credit_limit: 0,
    is_wholesale: false,
  });

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user]);

  useEffect(() => {
    if (store) loadCustomers();
  }, [store, search]);

  const loadCustomers = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await customerApi.list(store.id, search);
      setCustomers(data || []);
    } catch (err) {
      console.error('Failed to load customers:', err);
    } finally {
      setLoading(false);
    }
  };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await customerApi.create({ ...form, store_id: store.id });
      setShowModal(false);
      setForm({ name: '', name_hi: '', phone: '', address: '', gstin: '', credit_limit: 0, is_wholesale: false });
      loadCustomers();
    } catch (err) {
      console.error('Failed to create customer:', err);
    }
  };

  if (!user || user.role === 'cashier') return null;

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Customers</h1>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
      </div>

      <div style={styles.toolbar}>
        <input
          type="text"
          placeholder="Search by name or phone..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          style={styles.searchInput}
        />
        <button onClick={() => setShowModal(true)} style={styles.addBtn}>+ Add Customer</button>
      </div>

      {loading ? (
        <p>Loading...</p>
      ) : (
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Name</th>
              <th style={styles.th}>Phone</th>
              <th style={styles.th}>Address</th>
              <th style={styles.th}>GSTIN</th>
              <th style={styles.th}>Wholesale</th>
              <th style={styles.th}>Credit Limit</th>
              <th style={styles.th}>Outstanding</th>
            </tr>
          </thead>
          <tbody>
            {customers.map((c) => (
              <tr key={c.id} style={styles.tr}>
                <td style={styles.td}>{c.name}</td>
                <td style={styles.td}>{c.phone || '-'}</td>
                <td style={styles.td}>{c.address || '-'}</td>
                <td style={styles.td}>{c.gstin || '-'}</td>
                <td style={styles.td}>{c.is_wholesale ? 'Yes' : '—'}</td>
                <td style={styles.td}>₹{c.credit_limit.toFixed(2)}</td>
                <td style={styles.td}>₹{(c.outstanding || 0).toFixed(2)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {showModal && (
        <div style={styles.modal}>
          <div style={styles.modalContent}>
            <h2>Add Customer</h2>
            <form onSubmit={handleCreate}>
              <input type="text" placeholder="Name" value={form.name} onChange={e => setForm({...form, name: e.target.value})} style={styles.input} required />
              <input type="text" placeholder="Name (Hindi)" value={form.name_hi} onChange={e => setForm({...form, name_hi: e.target.value})} style={styles.input} />
              <input type="text" placeholder="Phone" value={form.phone} onChange={e => setForm({...form, phone: e.target.value})} style={styles.input} />
              <input type="text" placeholder="Address" value={form.address} onChange={e => setForm({...form, address: e.target.value})} style={styles.input} />
              <input type="text" placeholder="GSTIN" value={form.gstin} onChange={e => setForm({...form, gstin: e.target.value})} style={styles.input} />
              <input type="number" placeholder="Credit Limit" value={form.credit_limit} onChange={e => setForm({...form, credit_limit: parseFloat(e.target.value) || 0})} style={styles.input} />
              <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: '0.5rem' }}>
                <input type="checkbox" checked={form.is_wholesale} onChange={e => setForm({ ...form, is_wholesale: e.target.checked })} />
                Wholesale customer (default wholesale price on POS when enabled)
              </label>
              <div style={styles.modalActions}>
                <button type="button" onClick={() => setShowModal(false)} style={styles.cancelBtn}>Cancel</button>
                <button type="submit" style={styles.submitBtn}>Save</button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', background: '#f5f5f5', minHeight: '100vh' },
  header: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' },
  backBtn: { padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  toolbar: { display: 'flex', gap: '1rem', marginBottom: '1rem' },
  searchInput: { flex: 1, padding: '0.75rem', border: '1px solid #ddd', borderRadius: '4px' },
  addBtn: { padding: '0.75rem 1.5rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  table: { width: '100%', background: 'white', borderCollapse: 'collapse', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  tr: { borderBottom: '1px solid #eee' },
  modal: { position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.5)', display: 'flex', alignItems: 'center', justifyContent: 'center' },
  modalContent: { background: 'white', padding: '2rem', borderRadius: '8px', width: '400px', maxWidth: '90%' },
  input: { width: '100%', padding: '0.75rem', marginBottom: '0.5rem', border: '1px solid #ddd', borderRadius: '4px' },
  modalActions: { display: 'flex', gap: '1rem', justifyContent: 'flex-end', marginTop: '1rem' },
  cancelBtn: { padding: '0.75rem 1.5rem', background: '#ddd', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  submitBtn: { padding: '0.75rem 1.5rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
};