import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { storeApi, stockApi } from '../services/api';
import { Store } from '../types';

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

export default function StockTransfer() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [stores, setStores] = useState<Store[]>([]);
  const [fromStoreId, setFromStoreId] = useState('');
  const [toStoreId, setToStoreId] = useState('');
  const [stockItems, setStockItems] = useState<StockItem[]>([]);
  const [selectedStockItem, setSelectedStockItem] = useState('');
  const [quantity, setQuantity] = useState('');
  const [loading, setLoading] = useState(false);
  const [stockLoading, setStockLoading] = useState(false);
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null);

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    loadStores();
  }, [user]);

  useEffect(() => {
    if (fromStoreId) {
      loadStock(fromStoreId);
    } else {
      setStockItems([]);
      setSelectedStockItem('');
    }
  }, [fromStoreId]);

  const loadStores = async () => {
    try {
      const { data } = await storeApi.list();
      setStores(data || []);
      if (store) {
        setFromStoreId(store.id);
      }
    } catch (err) {
      console.error('Failed to load stores:', err);
      setMessage({ type: 'error', text: 'Failed to load stores' });
    }
  };

  const loadStock = async (storeId: string) => {
    setStockLoading(true);
    setSelectedStockItem('');
    setQuantity('');
    try {
      const { data } = await stockApi.list(storeId);
      setStockItems((data || []).filter((item: StockItem) => item.quantity > 0));
    } catch (err) {
      console.error('Failed to load stock:', err);
      setMessage({ type: 'error', text: 'Failed to load stock for selected store' });
      setStockItems([]);
    } finally {
      setStockLoading(false);
    }
  };

  const getSelectedItem = (): StockItem | undefined => {
    return stockItems.find(
      (item) => `${item.product_id}|${item.batch_no}` === selectedStockItem
    );
  };

  const handleTransfer = async () => {
    setMessage(null);

    if (!fromStoreId || !toStoreId) {
      setMessage({ type: 'error', text: 'Please select both From and To stores' });
      return;
    }
    if (fromStoreId === toStoreId) {
      setMessage({ type: 'error', text: 'From and To stores must be different' });
      return;
    }
    if (!selectedStockItem) {
      setMessage({ type: 'error', text: 'Please select a product/batch to transfer' });
      return;
    }
    const qty = parseFloat(quantity);
    if (!qty || qty <= 0) {
      setMessage({ type: 'error', text: 'Please enter a valid quantity' });
      return;
    }
    const item = getSelectedItem();
    if (!item) {
      setMessage({ type: 'error', text: 'Selected stock item not found' });
      return;
    }
    if (qty > item.quantity) {
      setMessage({ type: 'error', text: `Insufficient stock. Available: ${item.quantity}` });
      return;
    }

    setLoading(true);
    try {
      await stockApi.transfer({
        product_id: item.product_id,
        from_store_id: fromStoreId,
        to_store_id: toStoreId,
        quantity: qty,
        batch_no: item.batch_no || undefined,
      });
      setMessage({ type: 'success', text: 'Stock transferred successfully!' });
      setSelectedStockItem('');
      setQuantity('');
      loadStock(fromStoreId);
    } catch (err: any) {
      const errMsg = err?.response?.data?.error || 'Failed to transfer stock';
      setMessage({ type: 'error', text: errMsg });
    } finally {
      setLoading(false);
    }
  };

  if (!user) return null;

  const selectedItem = getSelectedItem();
  const fromStoreName = stores.find((s) => s.id === fromStoreId)?.name || '';
  const toStoreName = stores.find((s) => s.id === toStoreId)?.name || '';

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1 style={styles.title}>Stock Transfer</h1>
        <button onClick={() => navigate('/')} style={styles.backBtn}>
          ← Back to Menu
        </button>
      </div>

      {message && (
        <div
          style={{
            ...styles.message,
            background: message.type === 'success' ? '#e8f5e9' : '#ffebee',
            color: message.type === 'success' ? '#2e7d32' : '#c62828',
            border: `1px solid ${message.type === 'success' ? '#a5d6a7' : '#ef9a9a'}`,
          }}
        >
          {message.text}
        </div>
      )}

      <div style={styles.card}>
        <h3 style={styles.sectionTitle}>Select Stores</h3>
        <div style={styles.storeRow}>
          <div style={styles.fieldGroup}>
            <label style={styles.label}>From Store</label>
            <select
              value={fromStoreId}
              onChange={(e) => setFromStoreId(e.target.value)}
              style={styles.select}
            >
              <option value="">-- Select Source Store --</option>
              {stores.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </select>
          </div>

          <div style={styles.arrow}>→</div>

          <div style={styles.fieldGroup}>
            <label style={styles.label}>To Store</label>
            <select
              value={toStoreId}
              onChange={(e) => setToStoreId(e.target.value)}
              style={styles.select}
            >
              <option value="">-- Select Destination Store --</option>
              {stores
                .filter((s) => s.id !== fromStoreId)
                .map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
            </select>
          </div>
        </div>
      </div>

      {fromStoreId && (
        <div style={styles.card}>
          <h3 style={styles.sectionTitle}>
            Stock at {fromStoreName}
            {stockLoading && <span style={styles.loadingText}> Loading...</span>}
          </h3>

          {!stockLoading && stockItems.length === 0 && (
            <p style={styles.emptyText}>No stock available at this store.</p>
          )}

          {stockItems.length > 0 && (
            <>
              <div style={styles.fieldGroup}>
                <label style={styles.label}>Select Product / Batch</label>
                <select
                  value={selectedStockItem}
                  onChange={(e) => {
                    setSelectedStockItem(e.target.value);
                    setQuantity('');
                  }}
                  style={styles.select}
                >
                  <option value="">-- Select Item --</option>
                  {stockItems.map((item) => (
                    <option
                      key={`${item.product_id}|${item.batch_no}`}
                      value={`${item.product_id}|${item.batch_no}`}
                    >
                      {item.product_name || item.product_id.slice(0, 8)}
                      {item.batch_no ? ` | Batch: ${item.batch_no}` : ''}
                      {` | Qty: ${item.quantity}`}
                    </option>
                  ))}
                </select>
              </div>

              {selectedItem && (
                <div style={styles.detailRow}>
                  <div style={styles.detailItem}>
                    <span style={styles.detailLabel}>Product</span>
                    <span style={styles.detailValue}>
                      {selectedItem.product_name || selectedItem.product_id.slice(0, 8)}
                    </span>
                  </div>
                  {selectedItem.batch_no && (
                    <div style={styles.detailItem}>
                      <span style={styles.detailLabel}>Batch</span>
                      <span style={styles.detailValue}>{selectedItem.batch_no}</span>
                    </div>
                  )}
                  <div style={styles.detailItem}>
                    <span style={styles.detailLabel}>Available</span>
                    <span style={styles.detailValue}>{selectedItem.quantity}</span>
                  </div>
                  {selectedItem.expiry_date && (
                    <div style={styles.detailItem}>
                      <span style={styles.detailLabel}>Expiry</span>
                      <span style={styles.detailValue}>{selectedItem.expiry_date}</span>
                    </div>
                  )}
                </div>
              )}
            </>
          )}
        </div>
      )}

      {selectedItem && (
        <div style={styles.card}>
          <h3 style={styles.sectionTitle}>Transfer Details</h3>
          <div style={styles.transferRow}>
            <div style={styles.fieldGroup}>
              <label style={styles.label}>
                Quantity to Transfer (max: {selectedItem.quantity})
              </label>
              <input
                type="number"
                value={quantity}
                onChange={(e) => setQuantity(e.target.value)}
                placeholder="Enter quantity"
                min={1}
                max={selectedItem.quantity}
                style={styles.input}
              />
            </div>
          </div>

          {fromStoreName && toStoreName && quantity && (
            <div style={styles.summary}>
              Transferring <strong>{quantity}</strong> units of{' '}
              <strong>{selectedItem.product_name || selectedItem.product_id.slice(0, 8)}</strong>
              {selectedItem.batch_no ? ` (Batch: ${selectedItem.batch_no})` : ''} from{' '}
              <strong>{fromStoreName}</strong> to <strong>{toStoreName}</strong>.
            </div>
          )}

          <button
            onClick={handleTransfer}
            disabled={loading}
            style={{
              ...styles.transferBtn,
              opacity: loading ? 0.7 : 1,
              cursor: loading ? 'not-allowed' : 'pointer',
            }}
          >
            {loading ? 'Transferring...' : 'Transfer Stock'}
          </button>
        </div>
      )}

      {fromStoreId && stockItems.length > 0 && (
        <div style={styles.card}>
          <h3 style={styles.sectionTitle}>All Stock at {fromStoreName}</h3>
          <div style={styles.tableWrapper}>
            <table style={styles.table}>
              <thead>
                <tr>
                  <th style={styles.th}>Product</th>
                  <th style={styles.th}>Batch</th>
                  <th style={styles.th}>Quantity</th>
                  <th style={styles.th}>Expiry</th>
                  <th style={styles.th}>Cost Price</th>
                </tr>
              </thead>
              <tbody>
                {stockItems.map((item) => (
                  <tr
                    key={`${item.product_id}|${item.batch_no}`}
                    style={{
                      ...styles.tr,
                      background:
                        `${item.product_id}|${item.batch_no}` === selectedStockItem
                          ? '#e3f2fd'
                          : 'transparent',
                    }}
                  >
                    <td style={styles.td}>
                      {item.product_name || item.product_id.slice(0, 8)}
                    </td>
                    <td style={styles.td}>{item.batch_no || '-'}</td>
                    <td style={styles.td}>{item.quantity}</td>
                    <td style={styles.td}>{item.expiry_date || '-'}</td>
                    <td style={styles.td}>
                      {'\u20B9'}
                      {item.cost_price?.toFixed(2) || '0.00'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', background: '#f5f5f5', minHeight: '100vh', maxWidth: '1000px', margin: '0 auto' },
  header: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' },
  title: { margin: 0, fontSize: '1.5rem' },
  backBtn: { padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  message: { padding: '0.75rem 1rem', borderRadius: '4px', marginBottom: '1rem', fontSize: '0.95rem' },
  card: { background: 'white', padding: '1.5rem', borderRadius: '8px', boxShadow: '0 2px 4px rgba(0,0,0,0.1)', marginBottom: '1rem' },
  sectionTitle: { margin: '0 0 1rem 0', fontSize: '1.1rem', color: '#333' },
  storeRow: { display: 'flex', alignItems: 'flex-end', gap: '1rem', flexWrap: 'wrap' as const },
  fieldGroup: { flex: 1, minWidth: '200px', marginBottom: '0.5rem' },
  label: { display: 'block', marginBottom: '0.25rem', fontSize: '0.875rem', color: '#555', fontWeight: 500 },
  select: { width: '100%', padding: '0.5rem', fontSize: '1rem', border: '1px solid #ddd', borderRadius: '4px', background: 'white' },
  input: { width: '100%', padding: '0.5rem', fontSize: '1rem', border: '1px solid #ddd', borderRadius: '4px', boxSizing: 'border-box' as const },
  arrow: { fontSize: '1.5rem', color: '#1976d2', fontWeight: 'bold', paddingBottom: '0.75rem', alignSelf: 'flex-end' },
  loadingText: { fontSize: '0.85rem', color: '#888', fontWeight: 'normal' },
  emptyText: { color: '#888', fontStyle: 'italic' },
  detailRow: { display: 'flex', gap: '1.5rem', flexWrap: 'wrap' as const, marginTop: '1rem', padding: '0.75rem', background: '#f9f9f9', borderRadius: '4px' },
  detailItem: { display: 'flex', flexDirection: 'column' as const },
  detailLabel: { fontSize: '0.75rem', color: '#888', textTransform: 'uppercase' as const },
  detailValue: { fontSize: '0.95rem', fontWeight: 500, color: '#333' },
  transferRow: { marginBottom: '1rem' },
  summary: { padding: '0.75rem', background: '#e3f2fd', borderRadius: '4px', marginBottom: '1rem', fontSize: '0.95rem', lineHeight: 1.5, color: '#1565c0' },
  transferBtn: { padding: '0.75rem 2rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', fontSize: '1rem', fontWeight: 'bold' },
  tableWrapper: { overflowX: 'auto' as const },
  table: { width: '100%', borderCollapse: 'collapse' as const },
  th: { padding: '0.75rem', textAlign: 'left' as const, borderBottom: '2px solid #ddd', background: '#f9f9f9', fontSize: '0.85rem', color: '#555' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  tr: { borderBottom: '1px solid #eee' },
};
