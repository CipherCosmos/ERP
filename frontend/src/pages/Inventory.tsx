import React, { useState, useEffect, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { productApi, stockApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import {
  IconInventory,
  IconPlus,
  IconAlert,
  IconRefresh,
  IconClose,
  IconSearch,
  IconWarehouse,
} from '../components/Icons';

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
  const [search, setSearch] = useState('');
  const [activeTab, setActiveTab] = useState<'stock' | 'low' | 'history'>('stock');

  // Adjustment Modal
  const [showAdjustModal, setShowAdjustModal] = useState(false);
  const [adjustForm, setAdjustForm] = useState({
    product_id: '',
    batch_no: '',
    quantity: '',
    type: 'add',
    reason: 'count_variance',
    cost_price: 0,
    expiry_date: '',
  });

  const reasonOptions = [
    { value: 'count_variance', label: 'Physical Audit Count Variance' },
    { value: 'received', label: 'Manual Goods Inward' },
    { value: 'damage', label: 'Damaged / Broken in Warehouse' },
    { value: 'expiry', label: 'Expired Stock Write-off' },
    { value: 'returned', label: 'Customer Return Return-to-Stock' },
    { value: 'error', label: 'Data Correction / Clerical Error' },
  ];

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
  }, [user]);

  useEffect(() => {
    if (store) {
      loadAll();
    }
  }, [store?.id]);

  const loadAll = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const [sRes, lRes, aRes, pRes] = await Promise.all([
        stockApi.list(store.id),
        stockApi.low(store.id),
        stockApi.adjustments(store.id),
        productApi.list({ store_id: store.id }),
      ]);
      setStock(sRes.data || []);
      setLowStock(lRes.data || []);
      setAdjustments(aRes.data || []);
      setProducts(pRes.data || []);
    } catch (err) {
      console.error('Failed to load inventory data:', err);
    } finally {
      setLoading(false);
    }
  };

  const productsMap = useMemo(() => {
    const map: Record<string, any> = {};
    products.forEach((p) => {
      map[p.id] = p;
    });
    return map;
  }, [products]);

  // Total valuation
  const inventoryValuation = useMemo(() => {
    return stock.reduce((sum, item) => sum + (item.quantity * (item.cost_price || 0)), 0);
  }, [stock]);

  // Filtered Stock list
  const filteredStock = useMemo(() => {
    return stock.filter((item) => {
      const prod = productsMap[item.product_id];
      const name = prod?.name || item.product_name || '';
      const brand = prod?.brand || '';
      const batch = item.batch_no || '';
      const q = search.toLowerCase();
      return (
        name.toLowerCase().includes(q) ||
        brand.toLowerCase().includes(q) ||
        batch.toLowerCase().includes(q)
      );
    });
  }, [stock, productsMap, search]);

  const handleAdjustSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const qty = parseFloat(adjustForm.quantity);
    if (!store || !adjustForm.product_id || !qty || qty <= 0) {
      alert('Please select a product and enter a positive quantity.');
      return;
    }

    try {
      await stockApi.adjust({
        product_id: adjustForm.product_id,
        store_id: store.id,
        batch_no: adjustForm.batch_no || undefined,
        quantity: qty,
        type: adjustForm.type,
        reason: adjustForm.reason,
        cost_price: Number(adjustForm.cost_price) || 0,
        expiry_date: adjustForm.expiry_date || undefined,
      });

      setShowAdjustModal(false);
      setAdjustForm({
        product_id: '',
        batch_no: '',
        quantity: '',
        type: 'add',
        reason: 'count_variance',
        cost_price: 0,
        expiry_date: '',
      });
      loadAll();
      alert('Stock adjustment recorded successfully.');
    } catch (err: any) {
      alert(err.response?.data?.error || 'Failed to record stock adjustment.');
    }
  };

  return (
    <AppLayout>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          marginBottom: '1rem',
          flexWrap: 'wrap',
          gap: '0.75rem',
        }}
      >
        <div>
          <h1 style={{ margin: 0, fontSize: '1.4rem', fontWeight: 700 }}>
            Inventory Levels & Batch Tracking
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Location: <strong>{store?.name}</strong> &middot; Multi-batch FEFO/FIFO inventory valuation and physical count audits
          </p>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => navigate('/stock-transfer')}
          >
            <IconWarehouse size={16} />
            <span>Warehouse Transfers</span>
          </button>

          <button
            type="button"
            className="btn btn-primary btn-sm"
            onClick={() => {
              setShowAdjustModal(true);
              if (products.length > 0) setAdjustForm((p) => ({ ...p, product_id: products[0].id }));
            }}
          >
            <IconPlus size={16} />
            <span>Stock Adjustment / Count</span>
          </button>
        </div>
      </div>

      {/* KPI Cards */}
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fit, minmax(260px, 1fr))',
          gap: '1rem',
          marginBottom: '1.25rem',
        }}
      >
        <div className="erp-card" style={{ borderLeft: '4px solid var(--primary-600)' }}>
          <div className="erp-card-body" style={{ padding: '1rem 1.25rem' }}>
            <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-muted)', textTransform: 'uppercase' }}>
              Total Stock Valuation (Cost Basis)
            </div>
            <div style={{ fontSize: '1.75rem', fontWeight: 700, color: 'var(--text-main)', margin: '0.25rem 0' }}>
              ₹{inventoryValuation.toLocaleString('en-IN', { minimumFractionDigits: 2 })}
            </div>
            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
              Across {stock.length} unique batches on hand
            </div>
          </div>
        </div>

        <div
          className="erp-card"
          style={{ borderLeft: '4px solid #b45309', cursor: 'pointer' }}
          onClick={() => setActiveTab('low')}
        >
          <div className="erp-card-body" style={{ padding: '1rem 1.25rem' }}>
            <div style={{ fontSize: '0.75rem', fontWeight: 600, color: '#b45309', textTransform: 'uppercase' }}>
              Low Stock Warnings
            </div>
            <div style={{ fontSize: '1.75rem', fontWeight: 700, color: '#b45309', margin: '0.25rem 0' }}>
              {lowStock.length} Products
            </div>
            <div style={{ fontSize: '0.75rem', color: '#92400e' }}>
              Stock below configured reorder threshold
            </div>
          </div>
        </div>
      </div>

      {/* Navigation Tabs and Search */}
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: '1rem',
          flexWrap: 'wrap',
          gap: '0.5rem',
        }}
      >
        <div style={{ display: 'flex', gap: '0.25rem' }}>
          <button
            type="button"
            className={activeTab === 'stock' ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
            onClick={() => setActiveTab('stock')}
          >
            All Stock Batches ({stock.length})
          </button>
          <button
            type="button"
            className={activeTab === 'low' ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
            onClick={() => setActiveTab('low')}
          >
            Low Stock Alerts ({lowStock.length})
          </button>
          <button
            type="button"
            className={activeTab === 'history' ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
            onClick={() => setActiveTab('history')}
          >
            Adjustment History ({adjustments.length})
          </button>
        </div>

        {activeTab === 'stock' && (
          <div style={{ width: 280 }}>
            <input
              type="text"
              className="form-input"
              placeholder="Search product, brand, batch..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>
        )}
      </div>

      {/* Tab 1: All Stock Batches Table */}
      {activeTab === 'stock' && (
        <div className="erp-table-container">
          <table className="erp-table">
            <thead>
              <tr>
                <th>Product & Brand</th>
                <th>Batch Number</th>
                <th className="text-center">Expiry Date</th>
                <th className="text-right">Cost Price (₹)</th>
                <th className="text-right">Available Qty</th>
                <th className="text-right">Reserved Qty</th>
                <th className="text-right">Total Batch Value (₹)</th>
              </tr>
            </thead>
            <tbody>
              {loading ? (
                <tr>
                  <td colSpan={7} style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-muted)' }}>
                    Loading inventory batches...
                  </td>
                </tr>
              ) : filteredStock.length === 0 ? (
                <tr>
                  <td colSpan={7} style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
                    No stock batches found. Record initial stock or receive goods via Purchase Orders.
                  </td>
                </tr>
              ) : (
                filteredStock.map((item) => {
                  const prod = productsMap[item.product_id];
                  const batchVal = item.quantity * (item.cost_price || 0);

                  return (
                    <tr key={item.id}>
                      <td>
                        <div style={{ fontWeight: 600 }}>{prod?.name || item.product_name || item.product_id}</div>
                        <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
                          Brand: <strong>{prod?.brand || 'Unbranded'}</strong> &middot; Barcode: {prod?.barcode || '—'}
                        </div>
                      </td>
                      <td className="text-mono font-bold" style={{ color: 'var(--primary-600)' }}>
                        {item.batch_no || 'DEFAULT'}
                      </td>
                      <td className="text-center text-mono">
                        {item.expiry_date ? item.expiry_date.slice(0, 10) : 'None'}
                      </td>
                      <td className="text-right text-mono">₹{(item.cost_price || 0).toFixed(2)}</td>
                      <td className="text-right text-mono font-bold" style={{ color: '#16a34a' }}>
                        {item.quantity}
                      </td>
                      <td className="text-right text-mono" style={{ color: item.reserved_qty > 0 ? '#ea580c' : 'inherit' }}>
                        {item.reserved_qty}
                      </td>
                      <td className="text-right text-mono font-bold">
                        ₹{batchVal.toLocaleString('en-IN', { minimumFractionDigits: 2 })}
                      </td>
                    </tr>
                  );
                })
              )}
            </tbody>
          </table>
        </div>
      )}

      {/* Tab 2: Low Stock Alerts Table */}
      {activeTab === 'low' && (
        <div className="erp-table-container">
          <table className="erp-table">
            <thead>
              <tr>
                <th>Product Name</th>
                <th>Brand</th>
                <th>Barcode</th>
                <th className="text-center">Current Stock</th>
                <th className="text-center">Min Threshold</th>
                <th className="text-right">Action</th>
              </tr>
            </thead>
            <tbody>
              {lowStock.length === 0 ? (
                <tr>
                  <td colSpan={6} style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
                    All products have healthy inventory levels above their reorder thresholds!
                  </td>
                </tr>
              ) : (
                lowStock.map((item) => (
                  <tr key={item.id}>
                    <td style={{ fontWeight: 600 }}>{item.name}</td>
                    <td>{item.brand || 'Unbranded'}</td>
                    <td className="text-mono">{item.barcode}</td>
                    <td className="text-center text-mono font-bold" style={{ color: '#dc2626' }}>
                      {item.stock ?? 0}
                    </td>
                    <td className="text-center text-mono">{item.min_stock}</td>
                    <td className="text-right">
                      <button
                        type="button"
                        className="btn btn-primary btn-sm"
                        onClick={() => navigate('/purchases')}
                      >
                        Create PO
                      </button>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      )}

      {/* Tab 3: Adjustment Audit Log Table */}
      {activeTab === 'history' && (
        <div className="erp-table-container">
          <table className="erp-table">
            <thead>
              <tr>
                <th>Date</th>
                <th>Product</th>
                <th>Type</th>
                <th>Reason</th>
                <th className="text-right">Qty</th>
                <th className="text-mono">Batch</th>
              </tr>
            </thead>
            <tbody>
              {adjustments.length === 0 ? (
                <tr>
                  <td colSpan={6} style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
                    No manual stock adjustments recorded.
                  </td>
                </tr>
              ) : (
                adjustments.map((adj, i) => (
                  <tr key={adj.id || i}>
                    <td className="text-mono">{adj.created_at ? adj.created_at.slice(0, 16) : '—'}</td>
                    <td style={{ fontWeight: 600 }}>{adj.product_name || adj.product_id}</td>
                    <td>
                      <span className={adj.type === 'add' ? 'badge badge-success' : 'badge badge-danger'}>
                        {adj.type}
                      </span>
                    </td>
                    <td>{adj.reason || 'Variance'}</td>
                    <td className="text-right text-mono font-bold">{adj.quantity}</td>
                    <td className="text-mono" style={{ fontSize: '0.8rem' }}>{adj.batch_no || '—'}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      )}

      {/* ===================================================================
          Record Stock Adjustment Modal
          =================================================================== */}
      {showAdjustModal && (
        <div className="erp-modal-backdrop" onClick={() => setShowAdjustModal(false)}>
          <div className="erp-modal" style={{ maxWidth: 520 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 600 }}>
                Record Stock Count Adjustment
              </h3>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setShowAdjustModal(false)}
              >
                <IconClose size={18} />
              </button>
            </div>

            <form onSubmit={handleAdjustSubmit}>
              <div className="erp-modal-body">
                <div className="form-group">
                  <label className="form-label">Product *</label>
                  <select
                    required
                    className="form-select"
                    value={adjustForm.product_id}
                    onChange={(e) => setAdjustForm({ ...adjustForm, product_id: e.target.value })}
                  >
                    <option value="">Select Product</option>
                    {products.map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name} {p.brand ? `[${p.brand}]` : ''} ({p.barcode})
                      </option>
                    ))}
                  </select>
                </div>

                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem' }}>
                  <div className="form-group">
                    <label className="form-label">Adjustment Type *</label>
                    <select
                      className="form-select"
                      value={adjustForm.type}
                      onChange={(e) => setAdjustForm({ ...adjustForm, type: e.target.value })}
                    >
                      <option value="add">Add Stock (+)</option>
                      <option value="remove">Reduce Stock (-)</option>
                    </select>
                  </div>

                  <div className="form-group">
                    <label className="form-label">Quantity *</label>
                    <input
                      type="number"
                      required
                      min="0.001"
                      step="any"
                      className="form-input text-mono font-bold"
                      value={adjustForm.quantity}
                      onChange={(e) => setAdjustForm({ ...adjustForm, quantity: e.target.value })}
                    />
                  </div>
                </div>

                <div className="form-group">
                  <label className="form-label">Reason / Audit Category *</label>
                  <select
                    className="form-select"
                    value={adjustForm.reason}
                    onChange={(e) => setAdjustForm({ ...adjustForm, reason: e.target.value })}
                  >
                    {reasonOptions.map((opt) => (
                      <option key={opt.value} value={opt.value}>{opt.label}</option>
                    ))}
                  </select>
                </div>

                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem' }}>
                  <div className="form-group">
                    <label className="form-label">Batch Number</label>
                    <input
                      type="text"
                      className="form-input text-mono"
                      placeholder="e.g. AUDIT-01"
                      value={adjustForm.batch_no}
                      onChange={(e) => setAdjustForm({ ...adjustForm, batch_no: e.target.value })}
                    />
                  </div>

                  <div className="form-group">
                    <label className="form-label">Expiry Date (Optional)</label>
                    <input
                      type="date"
                      className="form-input text-mono"
                      value={adjustForm.expiry_date}
                      onChange={(e) => setAdjustForm({ ...adjustForm, expiry_date: e.target.value })}
                    />
                  </div>
                </div>
              </div>

              <div className="erp-modal-footer">
                <button
                  type="button"
                  className="btn btn-outline"
                  onClick={() => setShowAdjustModal(false)}
                >
                  Cancel
                </button>
                <button type="submit" className="btn btn-primary">
                  Save Adjustment & Audit Log
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </AppLayout>
  );
}