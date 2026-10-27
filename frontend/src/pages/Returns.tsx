import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { returnApi, productApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { IconReturns, IconPlus, IconClose, IconCheck, IconAlert } from '../components/Icons';

export default function Returns() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [returns, setReturns] = useState<any[]>([]);
  const [products, setProducts] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [showForm, setShowForm] = useState(false);

  const [form, setForm] = useState({
    product_id: '',
    quantity: '1',
    amount: '',
    reason: 'Customer exchange / Defective item',
    refund_mode: 'cash',
  });

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
  }, [user]);

  useEffect(() => {
    if (store) {
      loadReturns();
      loadProducts();
    }
  }, [store?.id]);

  const loadReturns = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await returnApi.list(store.id);
      setReturns(data || []);
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
      setProducts(data || []);
      if (data && data.length > 0 && !form.product_id) {
        setForm((prev) => ({
          ...prev,
          product_id: data[0].id,
          amount: String(data[0].sell_price || 0),
        }));
      }
    } catch (err) {
      console.error('Failed to load products:', err);
    }
  };

  const handleProductSelect = (productId: string) => {
    const prod = products.find((p) => p.id === productId);
    setForm((prev) => ({
      ...prev,
      product_id: productId,
      amount: prod ? String(prod.sell_price * (parseFloat(prev.quantity) || 1)) : prev.amount,
    }));
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const qty = parseFloat(form.quantity);
    const amt = parseFloat(form.amount);

    if (!store || !user || !form.product_id || !qty || qty <= 0) {
      alert('Please select a product and valid return quantity.');
      return;
    }

    try {
      await returnApi.create({
        product_id: form.product_id,
        quantity: qty,
        amount: amt,
        reason: form.reason,
        refund_mode: form.refund_mode,
        store_id: store.id,
      });
      setShowForm(false);
      setForm({
        product_id: products.length > 0 ? products[0].id : '',
        quantity: '1',
        amount: '',
        reason: 'Customer exchange / Defective item',
        refund_mode: 'cash',
      });
      loadReturns();
      alert('Return request recorded.');
    } catch (err: any) {
      alert(err.response?.data?.error || 'Failed to create return.');
    }
  };

  const handleApprove = async (id: string) => {
    if (!store) return;
    if (!window.confirm('Approve return and restore inventory stock?')) return;

    try {
      await returnApi.approve(id, store.id);
      loadReturns();
      alert('Return approved and stock restored to inventory.');
    } catch (err: any) {
      alert(err.response?.data?.error || 'Failed to approve return.');
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
            Sales Returns & Customer Exchanges
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Store: <strong>{store?.name}</strong> &middot; Track returned goods, refunds, and restock approvals
          </p>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => navigate('/losses')}
          >
            <span>Damaged Stock Losses</span>
          </button>

          <button
            type="button"
            className="btn btn-primary btn-sm"
            onClick={() => setShowForm(true)}
          >
            <IconPlus size={16} />
            <span>Record Sales Return</span>
          </button>
        </div>
      </div>

      {/* Returns Table */}
      <div className="erp-table-container">
        <table className="erp-table">
          <thead>
            <tr>
              <th>Date</th>
              <th>Product Name</th>
              <th className="text-center">Returned Qty</th>
              <th className="text-right">Refund Amount (₹)</th>
              <th>Refund Mode</th>
              <th>Reason</th>
              <th className="text-center">Status</th>
              <th className="text-right">Action</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={8} style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-muted)' }}>
                  Loading sales returns...
                </td>
              </tr>
            ) : returns.length === 0 ? (
              <tr>
                <td colSpan={8} style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
                  No customer returns recorded.
                </td>
              </tr>
            ) : (
              returns.map((r) => {
                const isPending = r.status === 'pending';
                return (
                  <tr key={r.id}>
                    <td className="text-mono">{r.created_at ? r.created_at.slice(0, 10) : '—'}</td>
                    <td style={{ fontWeight: 600 }}>{r.product_name || r.product_id}</td>
                    <td className="text-center text-mono font-bold">{r.quantity}</td>
                    <td className="text-right text-mono font-bold" style={{ color: '#dc2626' }}>
                      ₹{(r.amount || 0).toFixed(2)}
                    </td>
                    <td>
                      <span className="badge badge-neutral" style={{ textTransform: 'uppercase' }}>
                        {r.refund_mode}
                      </span>
                    </td>
                    <td style={{ color: 'var(--text-secondary)' }}>{r.reason}</td>
                    <td className="text-center">
                      {r.status === 'completed' || r.status === 'approved' ? (
                        <span className="badge badge-success">Restocked</span>
                      ) : (
                        <span className="badge badge-warning">Pending Approval</span>
                      )}
                    </td>
                    <td className="text-right">
                      {isPending && (
                        <button
                          type="button"
                          className="btn btn-primary btn-sm"
                          onClick={() => handleApprove(r.id)}
                        >
                          Approve & Restock
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>

      {/* Add Return Modal */}
      {showForm && (
        <div className="erp-modal-backdrop" onClick={() => setShowForm(false)}>
          <div className="erp-modal" style={{ maxWidth: 520 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 600 }}>
                Record Customer Sales Return
              </h3>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setShowForm(false)}
              >
                <IconClose size={18} />
              </button>
            </div>

            <form onSubmit={handleSubmit}>
              <div className="erp-modal-body">
                <div className="form-group">
                  <label className="form-label">Returned Product *</label>
                  <select
                    required
                    className="form-select"
                    value={form.product_id}
                    onChange={(e) => handleProductSelect(e.target.value)}
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
                    <label className="form-label">Quantity Returned *</label>
                    <input
                      type="number"
                      required
                      min="1"
                      className="form-input text-mono font-bold"
                      value={form.quantity}
                      onChange={(e) => setForm({ ...form, quantity: e.target.value })}
                    />
                  </div>

                  <div className="form-group">
                    <label className="form-label">Refund Amount (₹) *</label>
                    <input
                      type="number"
                      step="0.01"
                      required
                      className="form-input text-mono font-bold"
                      value={form.amount}
                      onChange={(e) => setForm({ ...form, amount: e.target.value })}
                    />
                  </div>
                </div>

                <div className="form-group">
                  <label className="form-label">Refund Mode</label>
                  <select
                    className="form-select"
                    value={form.refund_mode}
                    onChange={(e) => setForm({ ...form, refund_mode: e.target.value })}
                  >
                    <option value="cash">Cash Refund</option>
                    <option value="upi">UPI / Online Refund</option>
                    <option value="credit">Store Credit / Khata Credit</option>
                  </select>
                </div>

                <div className="form-group">
                  <label className="form-label">Reason for Return</label>
                  <input
                    type="text"
                    required
                    className="form-input"
                    value={form.reason}
                    onChange={(e) => setForm({ ...form, reason: e.target.value })}
                  />
                </div>
              </div>

              <div className="erp-modal-footer">
                <button
                  type="button"
                  className="btn btn-outline"
                  onClick={() => setShowForm(false)}
                >
                  Cancel
                </button>
                <button type="submit" className="btn btn-primary">
                  Save Return Request
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </AppLayout>
  );
}