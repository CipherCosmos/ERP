import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { productApi, stockApi } from '../services/api';

interface StockItem {
  id: string;
  product_id: string;
  product_name?: string;
  batch_no: string;
  quantity: number;
  reserved_qty: number;
  expiry_date: string | null;
  cost_price: number;
}

export default function Inventory() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [stock, setStock] = useState<StockItem[]>([]);
  const [lowStock, setLowStock] = useState<any[]>([]);
  const [adjustments, setAdjustments] = useState<any[]>([]);
  const [products, setProducts] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [activeTab, setActiveTab] = useState<'stock' | 'low' | 'adjust' | 'history'>('stock');
  const [showAdjustForm, setShowAdjustForm] = useState(false);
  const [adjustForm, setAdjustForm] = useState({
    product_id: '', quantity: '', type: 'add', reason: '',
  });

  const reasonOptions = [
    { value: 'received', label: 'Stock Received' },
    { value: 'returned', label: 'Customer Return' },
    { value: 'damage', label: 'Damaged' },
    { value: 'expiry', label: 'Expired' },
    { value: 'error', label: 'Entry Error' },
    { value: 'count_variance', label: 'Count Variance' },
  ];

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user]);

  useEffect(() => {
    if (store) {
      loadStock();
      loadLowStock();
      loadAdjustments();
      loadProducts();
    }
  }, [store, activeTab]);

  const loadStock = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await stockApi.list(store.id);
      setStock(data || []);
    } catch (err) {
      console.error('Failed to load stock:', err);
    } finally {
      setLoading(false);
    }
  };

  const loadLowStock = async () => {
    if (!store) return;
    try {
      const { data } = await stockApi.low(store.id);
      setLowStock(data || []);
    } catch (err) {
      console.error('Failed to load low stock:', err);
    }
  };

  const loadAdjustments = async () => {
    if (!store) return;
    try {
      const { data } = await stockApi.adjustments(store.id);
      setAdjustments(data || []);
    } catch (err) {
      console.error('Failed to load adjustments:', err);
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

  const handleAdjust = async () => {
    if (!store || !user || !adjustForm.product_id || !adjustForm.quantity) {
      alert('Please fill required fields');
      return;
    }

    const reason = adjustForm.type === 'remove' && adjustForm.reason 
      ? adjustForm.reason 
      : (adjustForm.type === 'add' ? 'received' : 'count_variance');

    try {
      await stockApi.adjust({
        product_id: adjustForm.product_id,
        store_id: store.id,
        quantity: parseFloat(adjustForm.quantity),
        type: adjustForm.type,
        reason,
      });
      setShowAdjustForm(false);
      setAdjustForm({ product_id: '', quantity: '', type: 'add', reason: '' });
      loadStock();
      loadAdjustments();
      alert('Stock adjusted');
    } catch (err) {
      console.error('Failed to adjust stock:', err);
      alert('Failed to adjust stock');
    }
  };

  if (!user || user.role === 'cashier') return null;

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Inventory Management</h1>
        <div style={styles.headerActions}>
          <button onClick={() => setShowAdjustForm(true)} style={styles.adjustBtn}>
            + Adjust Stock
          </button>
          <button onClick={() => navigate('/')} style={styles.backBtn}>
            ← Back
          </button>
        </div>
      </div>

      {showAdjustForm && (
        <div style={styles.modal}>
          <div style={styles.modalContent}>
            <h3>Adjust Stock</h3>
            <div style={styles.formGrid}>
              <select
                value={adjustForm.product_id}
                onChange={(e) => setAdjustForm({ ...adjustForm, product_id: e.target.value })}
                style={styles.select}
              >
                <option value="">Select Product</option>
                {products.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </select>
              <select
                value={adjustForm.type}
                onChange={(e) => setAdjustForm({ ...adjustForm, type: e.target.value })}
                style={styles.select}
              >
                <option value="add">Add Stock</option>
                <option value="remove">Remove Stock</option>
              </select>
              <input
                type="number"
                placeholder="Quantity"
                value={adjustForm.quantity}
                onChange={(e) => setAdjustForm({ ...adjustForm, quantity: e.target.value })}
                style={styles.input}
              />
              {adjustForm.type === 'remove' && (
                <select
                  value={adjustForm.reason}
                  onChange={(e) => setAdjustForm({ ...adjustForm, reason: e.target.value })}
                  style={styles.select}
                >
                  <option value="">Select Reason</option>
                  {reasonOptions.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
                </select>
              )}
            </div>
            <div style={styles.modalActions}>
              <button onClick={handleAdjust} style={styles.submitBtn}>Submit</button>
              <button onClick={() => setShowAdjustForm(false)} style={styles.cancelBtn}>Cancel</button>
            </div>
          </div>
        </div>
      )}

      <div style={styles.tabs}>
        <button style={activeTab === 'stock' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('stock')}>
          Current Stock
        </button>
        <button style={activeTab === 'low' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('low')}>
          Low Stock Alert
        </button>
        <button style={activeTab === 'history' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('history')}>
          Adjustment History
        </button>
      </div>

      {loading ? (
        <p>Loading...</p>
      ) : activeTab === 'stock' ? (
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Product</th>
              <th style={styles.th}>Batch</th>
              <th style={styles.th}>Qty</th>
              <th style={styles.th}>Reserved</th>
              <th style={styles.th}>Expiry</th>
              <th style={styles.th}>Cost</th>
            </tr>
          </thead>
          <tbody>
            {stock.map((item) => (
              <tr key={item.id} style={styles.tr}>
                <td style={styles.td}>{item.product_name || item.product_id}</td>
                <td style={styles.td}>{item.batch_no}</td>
                <td style={styles.td}>{item.quantity.toFixed(2)}</td>
                <td style={styles.td}>{item.reserved_qty.toFixed(2)}</td>
                <td style={styles.td}>{item.expiry_date || '-'}</td>
                <td style={styles.td}>₹{item.cost_price.toFixed(2)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : activeTab === 'low' ? (
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Product</th>
              <th style={styles.th}>Current Stock</th>
              <th style={styles.th}>Min Stock</th>
              <th style={styles.th}>Action</th>
            </tr>
          </thead>
          <tbody>
            {lowStock.map((item) => (
              <tr key={item.product_id} style={{...styles.tr, background: '#ffebee'}}>
                <td style={styles.td}>{item.name}</td>
                <td style={styles.td}>{item.current_stock}</td>
                <td style={styles.td}>{item.min_stock}</td>
                <td style={styles.td}>
                  <button style={styles.actionBtn} onClick={() => navigate('/purchases')}>Order</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Date</th>
              <th style={styles.th}>Product</th>
              <th style={styles.th}>Type</th>
              <th style={styles.th}>Qty</th>
              <th style={styles.th}>Reason</th>
            </tr>
          </thead>
          <tbody>
            {adjustments.map((adj) => (
              <tr key={adj.id} style={styles.tr}>
                <td style={styles.td}>{new Date(adj.created_at).toLocaleString()}</td>
                <td style={styles.td}>{adj.product_id?.slice(0, 8)}</td>
                <td style={styles.td}>{adj.adjustment_type}</td>
                <td style={styles.td}>{adj.quantity}</td>
                <td style={styles.td}>{adj.reason || '-'}</td>
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
  backBtn: { padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  adjustBtn: { padding: '0.5rem 1rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  tabs: { display: 'flex', gap: '0.5rem', marginBottom: '1rem' },
  tab: { padding: '0.75rem 1.5rem', background: 'white', border: '1px solid #ddd', borderRadius: '4px', cursor: 'pointer' },
  tabActive: { padding: '0.75rem 1.5rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  table: { width: '100%', background: 'white', borderCollapse: 'collapse', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  tr: { borderBottom: '1px solid #eee' },
  actionBtn: { padding: '0.25rem 0.75rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  modal: { position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.5)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000 },
  modalContent: { background: 'white', padding: '1.5rem', borderRadius: '8px', maxWidth: '500px', width: '90%' },
  formGrid: { display: 'grid', gap: '0.5rem', marginBottom: '1rem' },
  select: { padding: '0.5rem', fontSize: '1rem', border: '1px solid #ddd', borderRadius: '4px' },
  input: { padding: '0.5rem', fontSize: '1rem', border: '1px solid #ddd', borderRadius: '4px' },
  modalActions: { display: 'flex', gap: '0.5rem' },
  submitBtn: { padding: '0.5rem 1rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  cancelBtn: { padding: '0.5rem 1rem', background: '#f5f5f5', border: '1px solid #ddd', borderRadius: '4px', cursor: 'pointer' },
};