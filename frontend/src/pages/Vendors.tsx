import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { vendorApi } from '../services/api';

interface Vendor {
  id: string;
  name: string;
  phone: string;
  address: string;
  gstin: string;
  state: string;
  credit_days?: number;
  credit_limit?: number;
  total_purchase?: number;
}

export default function Vendors() {
  const navigate = useNavigate();
  const { user } = useAppStore();
  const [vendors, setVendors] = useState<Vendor[]>([]);
  const [loading, setLoading] = useState(false);
  const [search, setSearch] = useState('');
  const [showModal, setShowModal] = useState(false);
  const [form, setForm] = useState({
    name: '',
    phone: '',
    address: '',
    gstin: '',
    state: '',
    credit_days: 0,
    credit_limit: 0,
  });

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user]);

  useEffect(() => {
    loadVendors();
  }, [search]);

  const loadVendors = async () => {
    setLoading(true);
    try {
      const { data } = await vendorApi.list(search || undefined);
      setVendors((data as Vendor[]) || []);
    } catch (err) {
      console.error('Failed to load vendors:', err);
    } finally {
      setLoading(false);
    }
  };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await vendorApi.create({
        name: form.name,
        phone: form.phone || undefined,
        address: form.address || undefined,
        gstin: form.gstin || undefined,
        state: form.state || undefined,
        credit_days: form.credit_days || 0,
        credit_limit: form.credit_limit || 0,
      });
      setShowModal(false);
      setForm({ name: '', phone: '', address: '', gstin: '', state: '', credit_days: 0, credit_limit: 0 });
      loadVendors();
    } catch (err) {
      console.error('Failed to create vendor:', err);
    }
  };

  if (!user || user.role === 'cashier') return null;

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Vendors</h1>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
      </div>

      <div style={styles.toolbar}>
        <input type="text" placeholder="Search vendors..." value={search} onChange={e => setSearch(e.target.value)} style={styles.searchInput} />
        <button onClick={() => setShowModal(true)} style={styles.addBtn}>+ Add Vendor</button>
      </div>

      {loading ? (
        <p>Loading...</p>
      ) : (
        <div style={styles.grid}>
          {vendors.map((v) => (
            <div key={v.id} style={styles.card}>
              <h3 style={styles.cardTitle}>{v.name}</h3>
              <p style={styles.cardText}>Phone: {v.phone || '-'}</p>
              <p style={styles.cardText}>GSTIN: {v.gstin || '-'}</p>
              <p style={styles.cardText}>State: {v.state || '-'}</p>
              <p style={styles.cardText}>Credit: {v.credit_days ?? 0} days · limit ₹{(v.credit_limit ?? 0).toFixed(2)}</p>
              <p style={styles.cardText}>Address: {v.address || '-'}</p>
              {v.total_purchase !== undefined && v.total_purchase > 0 && (
                <p style={styles.cardValue}>Total Purchase: ₹{v.total_purchase.toFixed(2)}</p>
              )}
            </div>
          ))}
        </div>
      )}

      {showModal && (
        <div style={styles.modal}>
          <div style={styles.modalContent}>
            <h2>Add Vendor</h2>
            <form onSubmit={handleCreate}>
              <input type="text" placeholder="Name" value={form.name} onChange={e => setForm({...form, name: e.target.value})} style={styles.input} required />
              <input type="text" placeholder="Phone" value={form.phone} onChange={e => setForm({...form, phone: e.target.value})} style={styles.input} />
              <input type="text" placeholder="GSTIN" value={form.gstin} onChange={e => setForm({...form, gstin: e.target.value})} style={styles.input} />
              <input type="text" placeholder="State" value={form.state} onChange={e => setForm({...form, state: e.target.value})} style={styles.input} />
              <input type="text" placeholder="Address" value={form.address} onChange={e => setForm({...form, address: e.target.value})} style={styles.input} />
              <input type="number" placeholder="Credit days (0 = cash)" value={form.credit_days} onChange={e => setForm({ ...form, credit_days: parseInt(e.target.value, 10) || 0 })} style={styles.input} />
              <input type="number" placeholder="Credit limit ₹" value={form.credit_limit} onChange={e => setForm({ ...form, credit_limit: parseFloat(e.target.value) || 0 })} style={styles.input} />
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
  grid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: '1rem' },
  card: { background: 'white', padding: '1rem', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  cardTitle: { margin: '0 0 0.5rem', color: '#1976d2' },
  cardText: { margin: '0.25rem 0', color: '#666', fontSize: '0.875rem' },
  cardValue: { margin: '0.5rem 0 0', fontWeight: 'bold', color: '#388e3c' },
  modal: { position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.5)', display: 'flex', alignItems: 'center', justifyContent: 'center' },
  modalContent: { background: 'white', padding: '2rem', borderRadius: '8px', width: '400px' },
  input: { width: '100%', padding: '0.75rem', marginBottom: '0.5rem', border: '1px solid #ddd', borderRadius: '4px' },
  modalActions: { display: 'flex', gap: '1rem', justifyContent: 'flex-end', marginTop: '1rem' },
  cancelBtn: { padding: '0.75rem 1.5rem', background: '#ddd', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  submitBtn: { padding: '0.75rem 1.5rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
};