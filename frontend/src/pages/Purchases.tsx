import React, { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { purchaseApi, vendorApi, productApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { Product, Vendor } from '../types';
import {
  IconPurchases,
  IconPlus,
  IconClose,
  IconCheck,
  IconPrinter,
  IconRefresh,
} from '../components/Icons';

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

interface POLine {
  product_id: string;
  quantity: string;
  rate: string;
  discount: string;
}

interface POItemDetail {
  id: string;
  product_id: string;
  name: string;
  quantity: number;
  received_qty: number;
  rate: number;
  discount: number;
  total: number;
}

export default function Purchases() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [orders, setOrders] = useState<PurchaseOrder[]>([]);
  const [vendors, setVendors] = useState<Vendor[]>([]);
  const [products, setProducts] = useState<Product[]>([]);
  const [loading, setLoading] = useState(false);
  const [statusFilter, setStatusFilter] = useState('');

  // Create PO Modal
  const [showCreate, setShowCreate] = useState(false);
  const [createVendor, setCreateVendor] = useState('');
  const [createExpected, setCreateExpected] = useState('');
  const [createLines, setCreateLines] = useState<POLine[]>([
    { product_id: '', quantity: '10', rate: '0', discount: '0' },
  ]);
  const [createSaving, setCreateSaving] = useState(false);

  // Detail View Modal
  const [detailPO, setDetailPO] = useState<PurchaseOrder | null>(null);
  const [detailItems, setDetailItems] = useState<POItemDetail[]>([]);
  const [detailLoading, setDetailLoading] = useState(false);

  const loadOrders = useCallback(async () => {
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
  }, [store, statusFilter]);

  const loadVendorsAndProducts = useCallback(async () => {
    if (!store) return;
    try {
      const [vRes, pRes] = await Promise.all([
        vendorApi.list(),
        productApi.list({ store_id: store.id }),
      ]);
      setVendors(vRes.data || []);
      setProducts(pRes.data || []);
    } catch (err) {
      console.warn('Failed to load vendors/products:', err);
    }
  }, [store]);

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    if (store) {
      loadOrders();
      loadVendorsAndProducts();
    }
  }, [user, store?.id, loadOrders, loadVendorsAndProducts]);

  const openDetail = async (po: PurchaseOrder) => {
    setDetailPO(po);
    setDetailLoading(true);
    try {
      const { data } = await purchaseApi.get(po.id);
      setDetailItems(data.items || []);
    } catch (err) {
      console.error('Failed to load PO details:', err);
    } finally {
      setDetailLoading(false);
    }
  };

  const handleAddLine = () => {
    setCreateLines((prev) => [...prev, { product_id: '', quantity: '1', rate: '0', discount: '0' }]);
  };

  const handleRemoveLine = (idx: number) => {
    setCreateLines((prev) => prev.filter((_, i) => i !== idx));
  };

  const handleLineProductChange = (idx: number, productId: string) => {
    const prod = products.find((p) => p.id === productId);
    const cost = (prod as any)?.cost_price || prod?.sell_price || 0;
    setCreateLines((prev) =>
      prev.map((l, i) => (i === idx ? { ...l, product_id: productId, rate: String(cost) } : l))
    );
  };

  const handleCreateSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!createVendor || !store || createSaving) return;

    const validLines = createLines.filter(
      (l) => l.product_id && parseFloat(l.quantity) > 0 && parseFloat(l.rate) >= 0
    );
    if (validLines.length === 0) {
      alert('Please add at least one valid product line with quantity > 0.');
      return;
    }

    setCreateSaving(true);
    try {
      await purchaseApi.create({
        store_id: store.id,
        vendor_id: createVendor,
        order_date: new Date().toISOString().slice(0, 10),
        expected_date: createExpected || undefined,
        items: validLines.map((l) => {
          const prod = products.find((p) => p.id === l.product_id);
          const qty = parseFloat(l.quantity) || 1;
          return {
            product_id: l.product_id,
            unit_qty: qty,
            quantity: qty,
            rate: parseFloat(l.rate),
            discount: parseFloat(l.discount) || 0,
            uom_id: prod?.uom_id,
          };
        }),
      });

      setShowCreate(false);
      setCreateVendor('');
      setCreateExpected('');
      setCreateLines([{ product_id: '', quantity: '10', rate: '0', discount: '0' }]);
      loadOrders();
      alert('Purchase order created successfully!');
    } catch (err: any) {
      alert(err.response?.data?.error || 'Failed to create purchase order.');
    } finally {
      setCreateSaving(false);
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
            Purchase Orders & Procurement
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Supplier purchase orders, expected delivery dates, and Goods Receiving Notes (GRN)
          </p>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => navigate('/grn')}
          >
            <span>Goods Receipt (GRN)</span>
          </button>

          <button
            type="button"
            className="btn btn-primary btn-sm"
            onClick={() => {
              setShowCreate(true);
              if (vendors.length > 0) setCreateVendor(vendors[0].id);
            }}
          >
            <IconPlus size={16} />
            <span>Create Purchase Order</span>
          </button>
        </div>
      </div>

      {/* Filter Bar */}
      <div
        className="erp-card"
        style={{ padding: '0.75rem 1rem', marginBottom: '1.25rem', backgroundColor: '#fff' }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', flexWrap: 'wrap' }}>
          <div style={{ minWidth: 180 }}>
            <select
              className="form-select"
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value)}
            >
              <option value="">All Orders ({orders.length})</option>
              <option value="pending">Pending Receipt</option>
              <option value="partial">Partially Received</option>
              <option value="completed">Completed / Received</option>
              <option value="cancelled">Cancelled</option>
            </select>
          </div>

          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={loadOrders}
            disabled={loading}
          >
            <IconRefresh size={16} />
            <span>Refresh</span>
          </button>
        </div>
      </div>

      {/* Orders Table */}
      <div className="erp-table-container">
        <table className="erp-table">
          <thead>
            <tr>
              <th>PO Number</th>
              <th>Vendor / Supplier</th>
              <th>Order Date</th>
              <th>Expected Date</th>
              <th className="text-right">Order Value (₹)</th>
              <th className="text-center">Status</th>
              <th className="text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={7} style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-muted)' }}>
                  Loading purchase orders...
                </td>
              </tr>
            ) : orders.length === 0 ? (
              <tr>
                <td colSpan={7} style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
                  No purchase orders found matching this filter.
                </td>
              </tr>
            ) : (
              orders.map((po) => {
                const isPending = po.status === 'pending';
                const isPartial = po.status === 'partial';

                return (
                  <tr key={po.id}>
                    <td className="text-mono font-bold" style={{ color: 'var(--primary-600)' }}>
                      {po.po_no}
                    </td>
                    <td style={{ fontWeight: 600 }}>{po.vendor_name || 'Vendor'}</td>
                    <td className="text-mono">{po.order_date ? po.order_date.slice(0, 10) : '—'}</td>
                    <td className="text-mono">{po.expected_date ? po.expected_date.slice(0, 10) : '—'}</td>
                    <td className="text-right text-mono font-bold">
                      ₹{(po.total || 0).toLocaleString('en-IN', { minimumFractionDigits: 2 })}
                    </td>
                    <td className="text-center">
                      {po.status === 'completed' ? (
                        <span className="badge badge-success">Received</span>
                      ) : isPartial ? (
                        <span className="badge badge-warning">Partial</span>
                      ) : po.status === 'cancelled' ? (
                        <span className="badge badge-neutral">Cancelled</span>
                      ) : (
                        <span className="badge badge-info">Pending</span>
                      )}
                    </td>
                    <td className="text-right">
                      <div style={{ display: 'inline-flex', gap: '0.25rem' }}>
                        <button
                          type="button"
                          className="btn btn-outline btn-sm"
                          onClick={() => openDetail(po)}
                        >
                          View
                        </button>
                        {(isPending || isPartial) && (
                          <button
                            type="button"
                            className="btn btn-primary btn-sm"
                            onClick={() => navigate('/grn')}
                          >
                            Receive Goods
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>

      {/* ===================================================================
          Create Purchase Order Modal
          =================================================================== */}
      {showCreate && (
        <div className="erp-modal-backdrop" onClick={() => setShowCreate(false)}>
          <div className="erp-modal" style={{ maxWidth: 740 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 600 }}>
                Create Purchase Order (PO)
              </h3>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setShowCreate(false)}
              >
                <IconClose size={18} />
              </button>
            </div>

            <form onSubmit={handleCreateSubmit}>
              <div className="erp-modal-body">
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.875rem', marginBottom: '1rem' }}>
                  <div className="form-group">
                    <label className="form-label">Vendor / Supplier *</label>
                    <select
                      required
                      className="form-select"
                      value={createVendor}
                      onChange={(e) => setCreateVendor(e.target.value)}
                    >
                      <option value="">Select Vendor</option>
                      {vendors.map((v) => (
                        <option key={v.id} value={v.id}>
                          {v.name} {v.gstin ? `(GST: ${v.gstin})` : ''}
                        </option>
                      ))}
                    </select>
                  </div>

                  <div className="form-group">
                    <label className="form-label">Expected Delivery Date</label>
                    <input
                      type="date"
                      className="form-input text-mono"
                      value={createExpected}
                      onChange={(e) => setCreateExpected(e.target.value)}
                    />
                  </div>
                </div>

                {/* Items Line Editor */}
                <div>
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.4rem' }}>
                    <label className="form-label" style={{ margin: 0 }}>Order Items</label>
                    <button
                      type="button"
                      className="btn btn-outline btn-sm"
                      onClick={handleAddLine}
                    >
                      + Add Line
                    </button>
                  </div>

                  <table className="erp-table">
                    <thead>
                      <tr>
                        <th>Product (Brand)</th>
                        <th style={{ width: 100 }}>Quantity</th>
                        <th style={{ width: 110 }}>Rate (₹)</th>
                        <th style={{ width: 90 }}>Discount</th>
                        <th style={{ width: 50 }}></th>
                      </tr>
                    </thead>
                    <tbody>
                      {createLines.map((line, idx) => (
                        <tr key={idx}>
                          <td>
                            <select
                              required
                              className="form-select"
                              value={line.product_id}
                              onChange={(e) => handleLineProductChange(idx, e.target.value)}
                            >
                              <option value="">Select Product</option>
                              {products.map((p) => (
                                <option key={p.id} value={p.id}>
                                  {p.name} {p.brand ? `[${p.brand}]` : ''}
                                </option>
                              ))}
                            </select>
                          </td>
                          <td>
                            <input
                              type="number"
                              required
                              min="1"
                              className="form-input text-mono"
                              value={line.quantity}
                              onChange={(e) =>
                                setCreateLines((prev) =>
                                  prev.map((l, i) => (i === idx ? { ...l, quantity: e.target.value } : l))
                                )
                              }
                            />
                          </td>
                          <td>
                            <input
                              type="number"
                              step="0.01"
                              required
                              className="form-input text-mono"
                              value={line.rate}
                              onChange={(e) =>
                                setCreateLines((prev) =>
                                  prev.map((l, i) => (i === idx ? { ...l, rate: e.target.value } : l))
                                )
                              }
                            />
                          </td>
                          <td>
                            <input
                              type="number"
                              step="0.01"
                              className="form-input text-mono"
                              value={line.discount}
                              onChange={(e) =>
                                setCreateLines((prev) =>
                                  prev.map((l, i) => (i === idx ? { ...l, discount: e.target.value } : l))
                                )
                              }
                            />
                          </td>
                          <td className="text-center">
                            {createLines.length > 1 && (
                              <button
                                type="button"
                                style={{ background: 'none', border: 'none', color: '#dc2626', cursor: 'pointer' }}
                                onClick={() => handleRemoveLine(idx)}
                              >
                                ✕
                              </button>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>

              <div className="erp-modal-footer">
                <button
                  type="button"
                  className="btn btn-outline"
                  onClick={() => setShowCreate(false)}
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="btn btn-primary"
                  disabled={createSaving}
                >
                  {createSaving ? 'Creating PO...' : 'Submit Purchase Order'}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* ===================================================================
          PO Details View Modal
          =================================================================== */}
      {detailPO && (
        <div className="erp-modal-backdrop" onClick={() => setDetailPO(null)}>
          <div className="erp-modal" style={{ maxWidth: 680 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <div>
                <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 700 }}>
                  Purchase Order: #{detailPO.po_no}
                </h3>
                <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                  Vendor: <strong>{detailPO.vendor_name}</strong> &middot; Status: {detailPO.status}
                </div>
              </div>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setDetailPO(null)}
              >
                <IconClose size={18} />
              </button>
            </div>

            <div className="erp-modal-body">
              {detailLoading ? (
                <p style={{ textAlign: 'center', padding: '1.5rem', color: 'var(--text-muted)' }}>
                  Loading line items...
                </p>
              ) : (
                <table className="erp-table">
                  <thead>
                    <tr>
                      <th>Product</th>
                      <th className="text-center">Ordered</th>
                      <th className="text-center">Received</th>
                      <th className="text-right">Unit Rate</th>
                      <th className="text-right">Total (₹)</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detailItems.map((item, i) => (
                      <tr key={item.id || i}>
                        <td style={{ fontWeight: 600 }}>{item.name}</td>
                        <td className="text-center text-mono font-bold">{item.quantity}</td>
                        <td className="text-center text-mono" style={{ color: item.received_qty > 0 ? '#16a34a' : 'inherit' }}>
                          {item.received_qty}
                        </td>
                        <td className="text-right text-mono">₹{item.rate.toFixed(2)}</td>
                        <td className="text-right text-mono font-bold">₹{item.total.toFixed(2)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>

            <div className="erp-modal-footer">
              <button
                type="button"
                className="btn btn-outline"
                onClick={() => window.print()}
              >
                <IconPrinter size={16} />
                <span>Print PO</span>
              </button>
              <button
                type="button"
                className="btn btn-secondary"
                onClick={() => setDetailPO(null)}
              >
                Close
              </button>
            </div>
          </div>
        </div>
      )}
    </AppLayout>
  );
}
