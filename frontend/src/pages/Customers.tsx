import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { customerApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { Customer } from '../types';
import {
  IconCustomers,
  IconPlus,
  IconClose,
  IconCredit,
  IconCheck,
  IconSearch,
} from '../components/Icons';

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
    credit_limit: 5000,
    is_wholesale: false,
  });

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
  }, [user]);

  useEffect(() => {
    if (store) loadCustomers();
  }, [store?.id, search]);

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
    if (!form.name || !store) return;

    try {
      await customerApi.create({
        ...form,
        credit_limit: Number(form.credit_limit) || 0,
        store_id: store.id,
      });
      setShowModal(false);
      setForm({
        name: '',
        name_hi: '',
        phone: '',
        address: '',
        gstin: '',
        credit_limit: 5000,
        is_wholesale: false,
      });
      loadCustomers();
      alert('Customer account created successfully.');
    } catch (err: any) {
      alert(err.response?.data?.error || 'Failed to create customer.');
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
            Customers & Receivables Directory
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Customer profiles, credit limits, Khata accounts, and wholesale price eligibility
          </p>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => navigate('/credit')}
          >
            <IconCredit size={16} />
            <span>Khata Ledger</span>
          </button>

          <button
            type="button"
            className="btn btn-primary btn-sm"
            onClick={() => setShowModal(true)}
          >
            <IconPlus size={16} />
            <span>Add Customer</span>
          </button>
        </div>
      </div>

      {/* Search Toolbar */}
      <div
        className="erp-card"
        style={{ padding: '0.75rem 1rem', marginBottom: '1.25rem', backgroundColor: '#fff' }}
      >
        <div style={{ maxWidth: 360 }}>
          <input
            type="text"
            className="form-input"
            placeholder="Search customer by name or phone..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
      </div>

      {/* Customers Table */}
      <div className="erp-table-container">
        <table className="erp-table">
          <thead>
            <tr>
              <th>Customer Name</th>
              <th>Phone</th>
              <th>Address</th>
              <th>GSTIN</th>
              <th className="text-right">Credit Limit (₹)</th>
              <th className="text-center">Account Type</th>
              <th className="text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={7} style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-muted)' }}>
                  Loading customer directory...
                </td>
              </tr>
            ) : customers.length === 0 ? (
              <tr>
                <td colSpan={7} style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
                  No customer records found. Click "+ Add Customer" to create an account.
                </td>
              </tr>
            ) : (
              customers.map((c) => (
                <tr key={c.id}>
                  <td style={{ fontWeight: 600 }}>{c.name}</td>
                  <td className="text-mono">{c.phone || '—'}</td>
                  <td style={{ color: 'var(--text-secondary)' }}>{c.address || '—'}</td>
                  <td className="text-mono" style={{ fontSize: '0.8rem' }}>{c.gstin || '—'}</td>
                  <td className="text-right text-mono font-bold">
                    ₹{(c.credit_limit || 0).toLocaleString('en-IN', { minimumFractionDigits: 2 })}
                  </td>
                  <td className="text-center">
                    {c.is_wholesale ? (
                      <span className="badge badge-info">Wholesale</span>
                    ) : (
                      <span className="badge badge-neutral">Retail</span>
                    )}
                  </td>
                  <td className="text-right">
                    <button
                      type="button"
                      className="btn btn-outline btn-sm"
                      onClick={() => navigate('/credit')}
                    >
                      Khata Dues
                    </button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      {/* Add Customer Modal */}
      {showModal && (
        <div className="erp-modal-backdrop" onClick={() => setShowModal(false)}>
          <div className="erp-modal" style={{ maxWidth: 520 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 600 }}>
                Add New Customer Account
              </h3>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setShowModal(false)}
              >
                <IconClose size={18} />
              </button>
            </div>

            <form onSubmit={handleCreate}>
              <div className="erp-modal-body">
                <div className="form-group">
                  <label className="form-label">Customer Name *</label>
                  <input
                    type="text"
                    required
                    className="form-input"
                    placeholder="Full name or business name"
                    value={form.name}
                    onChange={(e) => setForm({ ...form, name: e.target.value })}
                  />
                </div>

                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem' }}>
                  <div className="form-group">
                    <label className="form-label">Phone Number</label>
                    <input
                      type="tel"
                      className="form-input text-mono"
                      placeholder="10-digit mobile"
                      value={form.phone}
                      onChange={(e) => setForm({ ...form, phone: e.target.value })}
                    />
                  </div>

                  <div className="form-group">
                    <label className="form-label">GSTIN (Optional)</label>
                    <input
                      type="text"
                      className="form-input text-mono"
                      placeholder="15-digit GST Number"
                      value={form.gstin}
                      onChange={(e) => setForm({ ...form, gstin: e.target.value })}
                    />
                  </div>
                </div>

                <div className="form-group">
                  <label className="form-label">Address</label>
                  <input
                    type="text"
                    className="form-input"
                    placeholder="Street, locality, city"
                    value={form.address}
                    onChange={(e) => setForm({ ...form, address: e.target.value })}
                  />
                </div>

                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem', alignItems: 'center' }}>
                  <div className="form-group">
                    <label className="form-label">Credit Limit (₹)</label>
                    <input
                      type="number"
                      className="form-input text-mono font-bold"
                      value={form.credit_limit}
                      onChange={(e) => setForm({ ...form, credit_limit: parseFloat(e.target.value) || 0 })}
                    />
                  </div>

                  <div style={{ paddingTop: '1rem' }}>
                    <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', cursor: 'pointer', fontSize: '0.85rem' }}>
                      <input
                        type="checkbox"
                        checked={form.is_wholesale}
                        onChange={(e) => setForm({ ...form, is_wholesale: e.target.checked })}
                      />
                      <span>Enable Wholesale Pricing</span>
                    </label>
                  </div>
                </div>
              </div>

              <div className="erp-modal-footer">
                <button
                  type="button"
                  className="btn btn-outline"
                  onClick={() => setShowModal(false)}
                >
                  Cancel
                </button>
                <button type="submit" className="btn btn-primary">
                  Save Customer
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </AppLayout>
  );
}