import React, { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { purchaseReturnApi, vendorApi, productApi } from '../services/api';
import { PurchaseReturn, Vendor, Product } from '../types';

interface ReturnItem {
  product_id: string;
  product_name?: string;
  quantity: number;
  rate: number;
}

export default function PurchaseReturns() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [returns, setReturns] = useState<PurchaseReturn[]>([]);
  const [vendors, setVendors] = useState<Vendor[]>([]);
  const [products, setProducts] = useState<Product[]>([]);
  const [loading, setLoading] = useState(false);
  const [showForm, setShowForm] = useState(false);

  const [formData, setFormData] = useState({
    vendor_id: '',
    po_id: '',
    return_date: new Date().toISOString().split('T')[0],
    reason: '',
    items: [] as ReturnItem[],
  });

  useEffect(() => {
    if (!user) {
      navigate('/login');
    }
  }, [user, navigate]);

  const loadData = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const [returnsRes, vendorsRes, productsRes] = await Promise.all([
        purchaseReturnApi.list({ store_id: store.id }),
        vendorApi.list(),
        productApi.list({ store_id: store.id }),
      ]);
      setReturns(returnsRes.data || []);
      setVendors(vendorsRes.data || []);
      setProducts(productsRes.data || []);
    } catch (err) {
      console.error('Failed to load data:', err);
    } finally {
      setLoading(false);
    }
  }, [store]);

  useEffect(() => {
    if (store) {
      loadData();
    }
  }, [store, loadData]);

  const addItem = () => {
    setFormData({
      ...formData,
      items: [...formData.items, { product_id: '', quantity: 1, rate: 0 }],
    });
  };

  const removeItem = (index: number) => {
    setFormData({
      ...formData,
      items: formData.items.filter((_, i) => i !== index),
    });
  };

  const updateItem = (index: number, field: string, value: unknown) => {
    const newItems = [...formData.items];
    (newItems[index] as Record<string, unknown>)[field] = value;
    setFormData({ ...formData, items: newItems });
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!store || formData.items.length === 0) {
      alert('Please add at least one item');
      return;
    }

    try {
      await purchaseReturnApi.create({
        ...formData,
        store_id: store.id,
        vendor_id: formData.vendor_id,
        items: formData.items.map((item) => ({
          product_id: item.product_id,
          quantity: Number(item.quantity),
          rate: Number(item.rate),
        })),
      });
      alert('Return created successfully');
      setFormData({
        vendor_id: '',
        po_id: '',
        return_date: new Date().toISOString().split('T')[0],
        reason: '',
        items: [],
      });
      setShowForm(false);
      loadData();
    } catch (err) {
      console.error('Failed to create return:', err);
      alert('Failed to create return');
    }
  };

  const handleApprove = async (returnId: string) => {
    if (!window.confirm('Approve this return and restore stock?')) return;
    try {
      await purchaseReturnApi.approve(returnId, store!.id);
      alert('Return approved successfully');
      loadData();
    } catch (err) {
      console.error('Failed to approve return:', err);
      alert('Failed to approve return');
    }
  };

  if (!user) return null;

  const calculateTotal = () =>
    formData.items.reduce((sum, item) => sum + item.quantity * item.rate, 0);

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Purchase Returns</h1>
        <div style={styles.headerActions}>
          <button
            type="button"
            onClick={() => setShowForm(!showForm)}
            style={styles.addBtn}
          >
            {showForm ? 'Cancel' : '+ New Return'}
          </button>
          <button type="button" onClick={() => navigate('/')} style={styles.backBtn}>
            ← Back to Menu
          </button>
        </div>
      </div>

      {showForm && (
        <div style={styles.formContainer}>
          <h3>Create New Return</h3>
          <form onSubmit={handleSubmit} style={styles.form}>
            <div style={styles.formRow}>
              <div style={styles.formGroup}>
                <label>Vendor *</label>
                <select
                  value={formData.vendor_id}
                  onChange={(e) => setFormData({ ...formData, vendor_id: e.target.value })}
                  style={styles.input}
                  required
                >
                  <option value="">Select Vendor</option>
                  {vendors.map((v) => (
                    <option key={v.id} value={v.id}>
                      {v.name}
                    </option>
                  ))}
                </select>
              </div>
              <div style={styles.formGroup}>
                <label>PO ID (Optional)</label>
                <input
                  type="text"
                  value={formData.po_id}
                  onChange={(e) => setFormData({ ...formData, po_id: e.target.value })}
                  style={styles.input}
                />
              </div>
              <div style={styles.formGroup}>
                <label>Return Date *</label>
                <input
                  type="date"
                  value={formData.return_date}
                  onChange={(e) => setFormData({ ...formData, return_date: e.target.value })}
                  style={styles.input}
                  required
                />
              </div>
            </div>

            <div style={styles.formGroup}>
              <label>Reason</label>
              <textarea
                value={formData.reason}
                onChange={(e) => setFormData({ ...formData, reason: e.target.value })}
                style={{ ...styles.input, minHeight: '60px' }}
                placeholder="Reason for return..."
              />
            </div>

            <div style={styles.itemsContainer}>
              <h4>Items to Return</h4>
              {formData.items.map((item, idx) => (
                <div key={idx} style={styles.itemRow}>
                  <select
                    value={item.product_id}
                    onChange={(e) => updateItem(idx, 'product_id', e.target.value)}
                    style={{ ...styles.input, flex: 1 }}
                    required
                  >
                    <option value="">Select Product</option>
                    {products.map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name}
                      </option>
                    ))}
                  </select>
                  <input
                    type="number"
                    placeholder="Qty"
                    value={item.quantity}
                    onChange={(e) => updateItem(idx, 'quantity', parseFloat(e.target.value) || 0)}
                    style={{ ...styles.input, width: '80px' }}
                    min="0"
                    step="0.01"
                  />
                  <input
                    type="number"
                    placeholder="Rate"
                    value={item.rate}
                    onChange={(e) => updateItem(idx, 'rate', parseFloat(e.target.value) || 0)}
                    style={{ ...styles.input, width: '100px' }}
                    min="0"
                    step="0.01"
                  />
                  <span style={styles.itemTotal}>
                    ₹{(item.quantity * item.rate).toFixed(2)}
                  </span>
                  <button
                    type="button"
                    onClick={() => removeItem(idx)}
                    style={styles.removeBtn}
                  >
                    ✕
                  </button>
                </div>
              ))}
              <button
                type="button"
                onClick={addItem}
                style={styles.addItemBtn}
              >
                + Add Item
              </button>
            </div>

            <div style={styles.totalDisplay}>
              <strong>Total: ₹{calculateTotal().toFixed(2)}</strong>
            </div>

            <button type="submit" style={styles.submitBtn}>
              Create Return
            </button>
          </form>
        </div>
      )}

      {loading ? (
        <p>Loading...</p>
      ) : returns.length === 0 ? (
        <p style={styles.empty}>No returns recorded.</p>
      ) : (
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Vendor</th>
              <th style={styles.th}>Return Date</th>
              <th style={styles.th}>Total</th>
              <th style={styles.th}>Status</th>
              <th style={styles.th}>Reason</th>
              <th style={styles.th}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {returns.map((ret) => (
              <tr key={ret.id}>
                <td style={styles.td}>{ret.vendor_name || ret.vendor_id}</td>
                <td style={styles.td}>{ret.return_date}</td>
                <td style={styles.td}>₹{Number(ret.total).toFixed(2)}</td>
                <td style={styles.td}>
                  <span
                    style={{
                      ...styles.statusBadge,
                      background:
                        ret.status === 'approved' ? '#e8f5e9' : '#fff3e0',
                    }}
                  >
                    {ret.status}
                  </span>
                </td>
                <td style={styles.td}>{ret.reason || '-'}</td>
                <td style={styles.td}>
                  {ret.status === 'pending' && (
                    <button
                      type="button"
                      onClick={() => handleApprove(ret.id)}
                      style={styles.approveBtn}
                    >
                      Approve
                    </button>
                  )}
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
  headerActions: { display: 'flex', gap: '0.5rem' },
  addBtn: { padding: '0.5rem 1rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  backBtn: { padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  formContainer: { background: 'white', padding: '1.5rem', borderRadius: '8px', marginBottom: '1.5rem', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  form: { display: 'flex', flexDirection: 'column', gap: '1rem' },
  formRow: { display: 'flex', gap: '1rem', flexWrap: 'wrap' },
  formGroup: { display: 'flex', flexDirection: 'column', gap: '0.25rem', flex: 1, minWidth: '200px' },
  input: { padding: '0.5rem', border: '1px solid #ddd', borderRadius: '4px', fontFamily: 'inherit' },
  itemsContainer: { border: '1px solid #ddd', padding: '1rem', borderRadius: '4px', background: '#fafafa' },
  itemRow: { display: 'flex', gap: '0.5rem', marginBottom: '0.5rem', alignItems: 'center' },
  itemTotal: { minWidth: '80px', textAlign: 'right', fontWeight: 'bold' },
  removeBtn: { padding: '0.25rem 0.5rem', background: '#f44336', color: 'white', border: 'none', borderRadius: '3px', cursor: 'pointer' },
  addItemBtn: { padding: '0.5rem 1rem', background: '#2196f3', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer', marginTop: '0.5rem' },
  totalDisplay: { padding: '0.75rem', background: '#e3f2fd', borderRadius: '4px', textAlign: 'right' },
  submitBtn: { padding: '0.75rem 1.5rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer', fontWeight: 'bold' },
  empty: { textAlign: 'center', color: '#999', marginTop: '2rem' },
  table: { width: '100%', background: 'white', borderCollapse: 'collapse', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9', fontWeight: 'bold' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  statusBadge: { padding: '0.25rem 0.5rem', borderRadius: '4px', fontSize: '0.75rem', textTransform: 'capitalize' },
  approveBtn: { padding: '0.25rem 0.75rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '3px', cursor: 'pointer', fontSize: '0.85rem' },
};
