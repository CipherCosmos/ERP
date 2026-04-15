import React, { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { vendorPaymentApi, vendorApi } from '../services/api';
import { VendorPayment, Vendor } from '../types';

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
    amount: 0,
    payment_mode: 'cash',
    reference: '',
    payment_date: new Date().toISOString().split('T')[0],
    notes: '',
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

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!store) return;

    try {
      await vendorPaymentApi.create({
        ...formData,
        store_id: store.id,
        amount: Number(formData.amount),
      });
      alert('Payment recorded successfully');
      setFormData({
        vendor_id: '',
        po_id: '',
        amount: 0,
        payment_mode: 'cash',
        reference: '',
        payment_date: new Date().toISOString().split('T')[0],
        notes: '',
      });
      setShowForm(false);
      loadData();
    } catch (err) {
      console.error('Failed to record payment:', err);
      alert('Failed to record payment');
    }
  };

  if (!user) return null;

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Vendor Payments</h1>
        <div style={styles.headerActions}>
          <button
            type="button"
            onClick={() => setShowForm(!showForm)}
            style={styles.addBtn}
          >
            {showForm ? 'Cancel' : '+ New Payment'}
          </button>
          <button type="button" onClick={() => navigate('/')} style={styles.backBtn}>
            ← Back to Menu
          </button>
        </div>
      </div>

      {showForm && (
        <div style={styles.formContainer}>
          <h3>Record New Payment</h3>
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
                <label>Amount *</label>
                <input
                  type="number"
                  step="0.01"
                  min="0"
                  value={formData.amount}
                  onChange={(e) => setFormData({ ...formData, amount: parseFloat(e.target.value) || 0 })}
                  style={styles.input}
                  required
                />
              </div>
            </div>

            <div style={styles.formRow}>
              <div style={styles.formGroup}>
                <label>Payment Mode *</label>
                <select
                  value={formData.payment_mode}
                  onChange={(e) => setFormData({ ...formData, payment_mode: e.target.value })}
                  style={styles.input}
                >
                  <option value="cash">Cash</option>
                  <option value="cheque">Cheque</option>
                  <option value="bank_transfer">Bank Transfer</option>
                  <option value="upi">UPI</option>
                </select>
              </div>
              <div style={styles.formGroup}>
                <label>Payment Date *</label>
                <input
                  type="date"
                  value={formData.payment_date}
                  onChange={(e) => setFormData({ ...formData, payment_date: e.target.value })}
                  style={styles.input}
                  required
                />
              </div>
            </div>

            <div style={styles.formRow}>
              <div style={styles.formGroup}>
                <label>Reference</label>
                <input
                  type="text"
                  placeholder="Cheque/Receipt number"
                  value={formData.reference}
                  onChange={(e) => setFormData({ ...formData, reference: e.target.value })}
                  style={styles.input}
                />
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
            </div>

            <div style={styles.formGroup}>
              <label>Notes</label>
              <textarea
                value={formData.notes}
                onChange={(e) => setFormData({ ...formData, notes: e.target.value })}
                style={{ ...styles.input, minHeight: '80px' }}
                placeholder="Additional notes..."
              />
            </div>

            <button type="submit" style={styles.submitBtn}>
              Record Payment
            </button>
          </form>
        </div>
      )}

      {loading ? (
        <p>Loading...</p>
      ) : payments.length === 0 ? (
        <p style={styles.empty}>No payments recorded.</p>
      ) : (
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Vendor</th>
              <th style={styles.th}>Amount</th>
              <th style={styles.th}>Mode</th>
              <th style={styles.th}>Reference</th>
              <th style={styles.th}>Date</th>
              <th style={styles.th}>Notes</th>
            </tr>
          </thead>
          <tbody>
            {payments.map((payment) => (
              <tr key={payment.id}>
                <td style={styles.td}>{payment.vendor_name || payment.vendor_id}</td>
                <td style={styles.td}>₹{Number(payment.amount).toFixed(2)}</td>
                <td style={styles.td}>{payment.payment_mode}</td>
                <td style={styles.td}>{payment.reference || '-'}</td>
                <td style={styles.td}>{payment.payment_date}</td>
                <td style={styles.td}>{payment.notes || '-'}</td>
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
  submitBtn: { padding: '0.75rem 1.5rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer', fontWeight: 'bold' },
  empty: { textAlign: 'center', color: '#999', marginTop: '2rem' },
  table: { width: '100%', background: 'white', borderCollapse: 'collapse', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9', fontWeight: 'bold' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
};
