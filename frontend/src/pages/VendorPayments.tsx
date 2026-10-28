import React, { useState, useEffect, useCallback, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { vendorPaymentApi, vendorApi, purchaseApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { VendorPayment, Vendor } from '../types';
import {
  IconVendors,
  IconPlus,
  IconPrinter,
  IconClose,
  IconCheck,
  IconRefresh,
} from '../components/Icons';

export default function VendorPayments() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [payments, setPayments] = useState<VendorPayment[]>([]);
  const [vendors, setVendors] = useState<Vendor[]>([]);
  const [loading, setLoading] = useState(false);
  const [showForm, setShowForm] = useState(false);

  const [formData, setFormData] = useState({
    vendor_id: '',
    po_id: '',
    amount: '',
    payment_mode: 'bank',
    reference: '',
    payment_date: new Date().toISOString().split('T')[0],
    notes: '',
  });

  const [vendorPOs, setVendorPOs] = useState<any[]>([]);

  useEffect(() => {
    if (!user) {
      navigate('/login');
    }
  }, [user, navigate]);

  const loadData = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const [paymentsRes, vendorsRes] = await Promise.all([
        vendorPaymentApi.list({ store_id: store.id }),
        vendorApi.list(),
      ]);
      setPayments(paymentsRes.data || []);
      setVendors(vendorsRes.data || []);
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

  // Load unpaid POs when vendor selected
  useEffect(() => {
    if (formData.vendor_id && store) {
      purchaseApi
        .list({ store_id: store.id })
        .then((res) => {
          const list = (res.data || []).filter(
            (p: any) => p.vendor_id === formData.vendor_id && p.status !== 'cancelled'
          );
          setVendorPOs(list);
        })
        .catch(() => setVendorPOs([]));
    } else {
      setVendorPOs([]);
    }
  }, [formData.vendor_id, store?.id]);

  const totalPaymentsMade = useMemo(() => {
    return payments.reduce((sum, p) => sum + (p.amount || 0), 0);
  }, [payments]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!store) return;

    const amt = parseFloat(formData.amount);
    if (!formData.vendor_id || !amt || amt <= 0) {
      alert('Please select a supplier and enter a valid settlement amount.');
      return;
    }

    try {
      await vendorPaymentApi.create({
        ...formData,
        store_id: store.id,
        amount: amt,
        po_id: formData.po_id || undefined,
      });

      setFormData({
        vendor_id: '',
        po_id: '',
        amount: '',
        payment_mode: 'bank',
        reference: '',
        payment_date: new Date().toISOString().split('T')[0],
        notes: '',
      });
      setShowForm(false);
      loadData();
      alert(`Supplier payment of ₹${amt.toFixed(2)} recorded successfully.`);
    } catch (err: any) {
      alert(err.response?.data?.error || 'Failed to record vendor payment.');
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
            Vendor Payments & Accounts Payable
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Supplier disbursements, bank transfer references, and PO settlement tracking
          </p>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => navigate('/vendors')}
          >
            <span>Supplier Directory</span>
          </button>

          <button
            type="button"
            className="btn btn-primary btn-sm"
            onClick={() => {
              setShowForm(true);
              if (vendors.length > 0) setFormData((p) => ({ ...p, vendor_id: vendors[0].id }));
            }}
          >
            <IconPlus size={16} />
            <span>Record Supplier Payment</span>
          </button>
        </div>
      </div>

      {/* KPI Card */}
      <div className="erp-card" style={{ borderLeft: '4px solid #16a34a', marginBottom: '1.25rem' }}>
        <div className="erp-card-body" style={{ padding: '1rem 1.25rem' }}>
          <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-muted)', textTransform: 'uppercase' }}>
            Total Disbursements Settled
          </div>
          <div style={{ fontSize: '1.75rem', fontWeight: 700, color: '#16a34a', margin: '0.25rem 0' }}>
            ₹{totalPaymentsMade.toLocaleString('en-IN', { minimumFractionDigits: 2 })}
          </div>
          <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
            Across {payments.length} supplier payments recorded
          </div>
        </div>
      </div>

      {/* Payments Table */}
      <div className="erp-table-container">
        <table className="erp-table">
          <thead>
            <tr>
              <th>Date</th>
              <th>Vendor / Supplier</th>
              <th>PO Link</th>
              <th>Mode</th>
              <th>UTR / Reference</th>
              <th className="text-right">Amount Paid (₹)</th>
              <th>Notes</th>
              <th className="text-right">Action</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={8} style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-muted)' }}>
                  Loading payments...
                </td>
              </tr>
            ) : payments.length === 0 ? (
              <tr>
                <td colSpan={8} style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
                  No vendor payments recorded yet. Click "Record Supplier Payment" to log a disbursement.
                </td>
              </tr>
            ) : (
              payments.map((p) => {
                const v = vendors.find((vend) => vend.id === p.vendor_id);
                return (
                  <tr key={p.id}>
                    <td className="text-mono">{p.payment_date?.slice(0, 10)}</td>
                    <td style={{ fontWeight: 600 }}>{v?.name || p.vendor_id}</td>
                    <td className="text-mono" style={{ fontSize: '0.8rem' }}>
                      {p.po_id ? `#${p.po_id.slice(-6)}` : 'On Account'}
                    </td>
                    <td>
                      <span className="badge badge-neutral" style={{ textTransform: 'uppercase' }}>
                        {p.payment_mode}
                      </span>
                    </td>
                    <td className="text-mono" style={{ fontSize: '0.8rem' }}>
                      {p.reference || '—'}
                    </td>
                    <td className="text-right text-mono font-bold" style={{ color: '#16a34a' }}>
                      ₹{p.amount.toFixed(2)}
                    </td>
                    <td style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{p.notes || '—'}</td>
                    <td className="text-right">
                      <button
                        type="button"
                        className="btn btn-outline btn-sm"
                        onClick={() => window.print()}
                      >
                        <IconPrinter size={14} />
                        <span>Voucher</span>
                      </button>
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>

      {/* ===================================================================
          Record Vendor Payment Modal
          =================================================================== */}
      {showForm && (
        <div className="erp-modal-backdrop" onClick={() => setShowForm(false)}>
          <div className="erp-modal" style={{ maxWidth: 520 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 600 }}>
                Record Supplier Disbursement
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
                  <label className="form-label">Vendor / Supplier *</label>
                  <select
                    required
                    className="form-select"
                    value={formData.vendor_id}
                    onChange={(e) => setFormData({ ...formData, vendor_id: e.target.value })}
                  >
                    <option value="">Select Vendor</option>
                    {vendors.map((v) => (
                      <option key={v.id} value={v.id}>
                        {v.name} {v.gstin ? `(${v.gstin})` : ''}
                      </option>
                    ))}
                  </select>
                </div>

                <div className="form-group">
                  <label className="form-label">Link to Purchase Order (Optional)</label>
                  <select
                    className="form-select"
                    value={formData.po_id}
                    onChange={(e) => setFormData({ ...formData, po_id: e.target.value })}
                  >
                    <option value="">General On-Account Payment</option>
                    {vendorPOs.map((po) => (
                      <option key={po.id} value={po.id}>
                        PO #{po.po_no} — ₹{po.total} [{po.status}]
                      </option>
                    ))}
                  </select>
                </div>

                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem' }}>
                  <div className="form-group">
                    <label className="form-label">Payment Amount (₹) *</label>
                    <input
                      type="number"
                      step="0.01"
                      required
                      className="form-input text-mono font-bold"
                      value={formData.amount}
                      placeholder="0.00"
                      onChange={(e) => setFormData({ ...formData, amount: e.target.value })}
                    />
                  </div>

                  <div className="form-group">
                    <label className="form-label">Payment Mode</label>
                    <select
                      className="form-select"
                      value={formData.payment_mode}
                      onChange={(e) => setFormData({ ...formData, payment_mode: e.target.value })}
                    >
                      <option value="bank">Bank Transfer / NEFT / RTGS</option>
                      <option value="upi">UPI / QR</option>
                      <option value="cash">Cash Out of Drawer</option>
                      <option value="cheque">Cheque</option>
                    </select>
                  </div>
                </div>

                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem' }}>
                  <div className="form-group">
                    <label className="form-label">UTR / Check / Reference</label>
                    <input
                      type="text"
                      className="form-input text-mono"
                      placeholder="Transaction reference ID"
                      value={formData.reference}
                      onChange={(e) => setFormData({ ...formData, reference: e.target.value })}
                    />
                  </div>

                  <div className="form-group">
                    <label className="form-label">Payment Date</label>
                    <input
                      type="date"
                      className="form-input text-mono"
                      value={formData.payment_date}
                      onChange={(e) => setFormData({ ...formData, payment_date: e.target.value })}
                    />
                  </div>
                </div>

                <div className="form-group">
                  <label className="form-label">Remarks / Notes</label>
                  <input
                    type="text"
                    className="form-input"
                    placeholder="Disbursement details, ledger note..."
                    value={formData.notes}
                    onChange={(e) => setFormData({ ...formData, notes: e.target.value })}
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
                  Save Payment & Update Payables
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </AppLayout>
  );
}
