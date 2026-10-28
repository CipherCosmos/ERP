import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { vendorApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { Vendor } from '../types';
import {
  IconVendors,
  IconPlus,
  IconClose,
  IconPurchases,
} from '../components/Icons';

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
    credit_days: 30,
    credit_limit: 50000,
  });

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    loadVendors();
  }, [user, search]);

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
    if (!form.name) return;

    try {
      await vendorApi.create({
        name: form.name,
        phone: form.phone || undefined,
        address: form.address || undefined,
        gstin: form.gstin || undefined,
        state: form.state || undefined,
        credit_days: Number(form.credit_days) || 0,
        credit_limit: Number(form.credit_limit) || 0,
      });
      setShowModal(false);
      setForm({
        name: '',
        phone: '',
        address: '',
        gstin: '',
        state: '',
        credit_days: 30,
        credit_limit: 50000,
      });
      loadVendors();
      alert('Vendor profile saved successfully.');
    } catch (err: any) {
      alert(err.response?.data?.error || 'Failed to create vendor.');
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
            Vendors & Suppliers Directory
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Supplier profiles, agreed payment terms, credit days, and purchase history
          </p>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => navigate('/vendor-payments')}
          >
            <span>Accounts Payable</span>
          </button>

          <button
            type="button"
            className="btn btn-primary btn-sm"
            onClick={() => setShowModal(true)}
          >
            <IconPlus size={16} />
            <span>Add Supplier</span>
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
            placeholder="Search suppliers by name..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
      </div>

      {/* Vendors Table */}
      <div className="erp-table-container">
        <table className="erp-table">
          <thead>
            <tr>
              <th>Vendor / Supplier Name</th>
              <th>Phone</th>
              <th>Address</th>
              <th>GSTIN</th>
              <th>State</th>
              <th className="text-center">Credit Days</th>
              <th className="text-right">Credit Limit (₹)</th>
              <th className="text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={8} style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-muted)' }}>
                  Loading suppliers...
                </td>
              </tr>
            ) : vendors.length === 0 ? (
              <tr>
                <td colSpan={8} style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
                  No vendor records found. Click "+ Add Supplier" to add a supplier.
                </td>
              </tr>
            ) : (
              vendors.map((v) => (
                <tr key={v.id}>
                  <td style={{ fontWeight: 600 }}>{v.name}</td>
                  <td className="text-mono">{v.phone || '—'}</td>
                  <td style={{ color: 'var(--text-secondary)' }}>{v.address || '—'}</td>
                  <td className="text-mono" style={{ fontSize: '0.8rem' }}>{v.gstin || '—'}</td>
                  <td>{v.state || '—'}</td>
                  <td className="text-center text-mono font-bold">
                    {v.credit_days ? `${v.credit_days} d` : 'Immediate'}
                  </td>
                  <td className="text-right text-mono font-bold">
                    ₹{(v.credit_limit || 0).toLocaleString('en-IN', { minimumFractionDigits: 2 })}
                  </td>
                  <td className="text-right">
                    <button
                      type="button"
                      className="btn btn-outline btn-sm"
                      onClick={() => navigate('/purchases')}
                    >
                      New PO
                    </button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      {/* Add Vendor Modal */}
      {showModal && (
        <div className="erp-modal-backdrop" onClick={() => setShowModal(false)}>
          <div className="erp-modal" style={{ maxWidth: 540 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 600 }}>
                Add New Supplier Profile
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
                  <label className="form-label">Vendor / Company Name *</label>
                  <input
                    type="text"
                    required
                    className="form-input"
                    placeholder="e.g. Adani Wilmar Distributors"
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
                      placeholder="Contact number"
                      value={form.phone}
                      onChange={(e) => setForm({ ...form, phone: e.target.value })}
                    />
                  </div>

                  <div className="form-group">
                    <label className="form-label">GSTIN</label>
                    <input
                      type="text"
                      className="form-input text-mono"
                      placeholder="15-digit GSTIN"
                      value={form.gstin}
                      onChange={(e) => setForm({ ...form, gstin: e.target.value })}
                    />
                  </div>
                </div>

                <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr', gap: '0.75rem' }}>
                  <div className="form-group">
                    <label className="form-label">Address</label>
                    <input
                      type="text"
                      className="form-input"
                      placeholder="Warehouse or office location"
                      value={form.address}
                      onChange={(e) => setForm({ ...form, address: e.target.value })}
                    />
                  </div>

                  <div className="form-group">
                    <label className="form-label">State</label>
                    <input
                      type="text"
                      className="form-input"
                      placeholder="e.g. KA, MH"
                      value={form.state}
                      onChange={(e) => setForm({ ...form, state: e.target.value })}
                    />
                  </div>
                </div>

                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem' }}>
                  <div className="form-group">
                    <label className="form-label">Credit Period (Days)</label>
                    <input
                      type="number"
                      className="form-input text-mono font-bold"
                      value={form.credit_days}
                      onChange={(e) => setForm({ ...form, credit_days: parseInt(e.target.value) || 0 })}
                    />
                  </div>

                  <div className="form-group">
                    <label className="form-label">Agreed Credit Limit (₹)</label>
                    <input
                      type="number"
                      className="form-input text-mono font-bold"
                      value={form.credit_limit}
                      onChange={(e) => setForm({ ...form, credit_limit: parseFloat(e.target.value) || 0 })}
                    />
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
                  Save Supplier Profile
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </AppLayout>
  );
}