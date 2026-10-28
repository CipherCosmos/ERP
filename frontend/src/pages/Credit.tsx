import React, { useState, useEffect, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { creditApi, customerApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { Customer } from '../types';
import {
  IconCredit,
  IconSearch,
  IconPrinter,
  IconClose,
  IconCheck,
  IconAlert,
  IconRefresh,
} from '../components/Icons';

interface CreditEntry {
  id: string;
  customer_id: string;
  customer_name: string;
  invoice_id: string | null;
  amount: number;
  type: string;
  due_date: string | null;
  paid_amount: number;
  status: string;
  created_at: string;
}

export default function Credit() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [entries, setEntries] = useState<CreditEntry[]>([]);
  const [customers, setCustomers] = useState<Customer[]>([]);
  const [loading, setLoading] = useState(false);
  const [activeTab, setActiveTab] = useState<'all' | 'pending' | 'overdue'>('pending');
  const [search, setSearch] = useState('');

  // Payment Collection Modal
  const [showPaymentModal, setShowPaymentModal] = useState(false);
  const [selectedCustomerId, setSelectedCustomerId] = useState('');
  const [paymentAmount, setPaymentAmount] = useState('');
  const [paymentMethod, setPaymentMethod] = useState<'cash' | 'upi' | 'bank'>('cash');
  const [paymentNotes, setPaymentNotes] = useState('');

  // Customer Account Statement Modal
  const [statementCustomer, setStatementCustomer] = useState<Customer | null>(null);

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    if (store) {
      loadCreditEntries();
      loadCustomers();
    }
  }, [user, store?.id]);

  const loadCreditEntries = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await creditApi.list({ store_id: store.id });
      setEntries(Array.isArray(data) ? data : []);
    } catch (err) {
      console.error('Failed to load credit entries:', err);
    } finally {
      setLoading(false);
    }
  };

  const loadCustomers = async () => {
    if (!store) return;
    try {
      const { data } = await customerApi.list(store.id);
      setCustomers(data || []);
    } catch (err) {
      console.warn('Failed to load customers:', err);
    }
  };

  const todayISO = new Date().toISOString().slice(0, 10);

  // Filtered entries
  const filteredEntries = useMemo(() => {
    return entries.filter((e) => {
      // Tab filter
      if (activeTab === 'pending') {
        if (e.status === 'paid' || e.type !== 'issue') return false;
      } else if (activeTab === 'overdue') {
        if (e.status === 'paid' || e.type !== 'issue') return false;
        if (!e.due_date || e.due_date.slice(0, 10) >= todayISO) return false;
      }
      // Text search
      if (search) {
        const query = search.toLowerCase();
        if (
          !e.customer_name?.toLowerCase().includes(query) &&
          !e.invoice_id?.toLowerCase().includes(query)
        ) {
          return false;
        }
      }
      return true;
    });
  }, [entries, activeTab, search, todayISO]);

  // Aggregate metrics
  const totalOutstanding = useMemo(() => {
    return entries
      .filter((e) => e.status !== 'paid' && e.type === 'issue')
      .reduce((sum, e) => sum + (e.amount - e.paid_amount), 0);
  }, [entries]);

  const overdueBalance = useMemo(() => {
    return entries
      .filter(
        (e) =>
          e.status !== 'paid' &&
          e.type === 'issue' &&
          e.due_date &&
          e.due_date.slice(0, 10) < todayISO
      )
      .reduce((sum, e) => sum + (e.amount - e.paid_amount), 0);
  }, [entries, todayISO]);

  const handleRecordPayment = async (e: React.FormEvent) => {
    e.preventDefault();
    const amt = parseFloat(paymentAmount);
    if (!selectedCustomerId || !amt || amt <= 0) {
      alert('Please select a customer and enter a valid collection amount.');
      return;
    }

    try {
      await creditApi.payment({
        customer_id: selectedCustomerId,
        amount: amt,
        method: paymentMethod,
      });
      setShowPaymentModal(false);
      setPaymentAmount('');
      setPaymentNotes('');
      loadCreditEntries();
      alert(`Payment of ₹${amt.toFixed(2)} recorded to Khata.`);
    } catch (err: any) {
      alert(err.response?.data?.error || 'Failed to record credit payment.');
    }
  };

  // Statement entries for specific customer
  const customerStatementEntries = useMemo(() => {
    if (!statementCustomer) return [];
    return entries.filter((e) => e.customer_id === statementCustomer.id);
  }, [entries, statementCustomer]);

  return (
    <AppLayout>
      {/* Top Header */}
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
            Customer Credit & Khata Ledger
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Receivables tracking, customer khata statements, collections, and overdue aging
          </p>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => navigate('/credit-aging')}
          >
            <span>View Aging Report</span>
          </button>

          <button
            type="button"
            className="btn btn-primary btn-sm"
            onClick={() => {
              setShowPaymentModal(true);
              if (customers.length > 0) setSelectedCustomerId(customers[0].id);
            }}
          >
            <IconCredit size={16} />
            <span>Record Khata Payment</span>
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
        <div className="erp-card" style={{ borderLeft: '4px solid #ea580c' }}>
          <div className="erp-card-body" style={{ padding: '1rem 1.25rem' }}>
            <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-muted)', textTransform: 'uppercase' }}>
              Total Credit Outstanding
            </div>
            <div style={{ fontSize: '1.75rem', fontWeight: 700, color: '#ea580c', margin: '0.25rem 0' }}>
              ₹{totalOutstanding.toLocaleString('en-IN', { minimumFractionDigits: 2 })}
            </div>
            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
              Active customer khata receivables
            </div>
          </div>
        </div>

        <div className="erp-card" style={{ borderLeft: '4px solid #dc2626' }}>
          <div className="erp-card-body" style={{ padding: '1rem 1.25rem' }}>
            <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-muted)', textTransform: 'uppercase' }}>
              Overdue Balance Due
            </div>
            <div style={{ fontSize: '1.75rem', fontWeight: 700, color: '#dc2626', margin: '0.25rem 0' }}>
              ₹{overdueBalance.toLocaleString('en-IN', { minimumFractionDigits: 2 })}
            </div>
            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
              Exceeded payment credit due date
            </div>
          </div>
        </div>
      </div>

      {/* Tabs and Filter Bar */}
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
          {[
            { id: 'pending', label: 'Pending Dues' },
            { id: 'overdue', label: 'Overdue Only' },
            { id: 'all', label: 'Full Ledger History' },
          ].map((tab) => (
            <button
              key={tab.id}
              type="button"
              className={activeTab === tab.id ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
              onClick={() => setActiveTab(tab.id as any)}
            >
              {tab.label}
            </button>
          ))}
        </div>

        <div style={{ width: 260 }}>
          <input
            type="text"
            className="form-input"
            placeholder="Search customer or bill..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
      </div>

      {/* Khata Ledger Table */}
      <div className="erp-table-container">
        <table className="erp-table">
          <thead>
            <tr>
              <th>Customer</th>
              <th>Bill / Ref</th>
              <th>Issue Date</th>
              <th>Due Date</th>
              <th className="text-right">Bill Amount</th>
              <th className="text-right">Paid</th>
              <th className="text-right">Balance Due</th>
              <th className="text-center">Status</th>
              <th className="text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={9} style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-muted)' }}>
                  Loading Khata ledger...
                </td>
              </tr>
            ) : filteredEntries.length === 0 ? (
              <tr>
                <td colSpan={9} style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
                  No credit records found in this view.
                </td>
              </tr>
            ) : (
              filteredEntries.map((e) => {
                const balance = e.amount - e.paid_amount;
                const isOverdue = e.due_date && e.due_date.slice(0, 10) < todayISO && balance > 0;

                return (
                  <tr key={e.id}>
                    <td style={{ fontWeight: 600 }}>{e.customer_name}</td>
                    <td className="text-mono" style={{ fontSize: '0.8rem' }}>
                      {e.invoice_id ? `#${e.invoice_id.slice(-6)}` : 'Manual Entry'}
                    </td>
                    <td className="text-mono">{e.created_at ? e.created_at.slice(0, 10) : '—'}</td>
                    <td className="text-mono">
                      {e.due_date ? (
                        <span style={{ color: isOverdue ? '#dc2626' : 'inherit', fontWeight: isOverdue ? 700 : 400 }}>
                          {e.due_date.slice(0, 10)}
                        </span>
                      ) : (
                        'None'
                      )}
                    </td>
                    <td className="text-right text-mono">₹{e.amount.toFixed(2)}</td>
                    <td className="text-right text-mono" style={{ color: '#16a34a' }}>
                      ₹{e.paid_amount.toFixed(2)}
                    </td>
                    <td className="text-right text-mono font-bold" style={{ color: balance > 0 ? '#ea580c' : '#16a34a' }}>
                      ₹{balance.toFixed(2)}
                    </td>
                    <td className="text-center">
                      {balance <= 0 ? (
                        <span className="badge badge-success">Settled</span>
                      ) : isOverdue ? (
                        <span className="badge badge-danger">Overdue</span>
                      ) : (
                        <span className="badge badge-warning">Pending</span>
                      )}
                    </td>
                    <td className="text-right">
                      <div style={{ display: 'inline-flex', gap: '0.25rem' }}>
                        <button
                          type="button"
                          className="btn btn-outline btn-sm"
                          onClick={() => {
                            const cust = customers.find((c) => c.id === e.customer_id);
                            if (cust) setStatementCustomer(cust);
                          }}
                        >
                          Statement
                        </button>
                        {balance > 0 && (
                          <button
                            type="button"
                            className="btn btn-primary btn-sm"
                            onClick={() => {
                              setSelectedCustomerId(e.customer_id);
                              setPaymentAmount(balance.toFixed(2));
                              setShowPaymentModal(true);
                            }}
                          >
                            Collect
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
          Record Payment Modal
          =================================================================== */}
      {showPaymentModal && (
        <div className="erp-modal-backdrop" onClick={() => setShowPaymentModal(false)}>
          <div className="erp-modal" style={{ maxWidth: 460 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 600 }}>
                Record Khata Payment Collection
              </h3>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setShowPaymentModal(false)}
              >
                <IconClose size={18} />
              </button>
            </div>
            <form onSubmit={handleRecordPayment}>
              <div className="erp-modal-body">
                <div className="form-group">
                  <label className="form-label">Customer Account *</label>
                  <select
                    required
                    className="form-select"
                    value={selectedCustomerId}
                    onChange={(e) => setSelectedCustomerId(e.target.value)}
                  >
                    <option value="">Select Customer</option>
                    {customers.map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name} {c.phone ? `(${c.phone})` : ''}
                      </option>
                    ))}
                  </select>
                </div>

                <div className="form-group">
                  <label className="form-label">Collection Amount (₹) *</label>
                  <input
                    type="number"
                    step="0.01"
                    required
                    className="form-input text-mono font-bold"
                    placeholder="0.00"
                    value={paymentAmount}
                    onChange={(e) => setPaymentAmount(e.target.value)}
                  />
                </div>

                <div className="form-group">
                  <label className="form-label">Payment Mode</label>
                  <select
                    className="form-select"
                    value={paymentMethod}
                    onChange={(e) => setPaymentMethod(e.target.value as any)}
                  >
                    <option value="cash">Cash Received</option>
                    <option value="upi">UPI / Online QR</option>
                    <option value="bank">Bank Transfer / NEFT</option>
                  </select>
                </div>
              </div>
              <div className="erp-modal-footer">
                <button
                  type="button"
                  className="btn btn-outline"
                  onClick={() => setShowPaymentModal(false)}
                >
                  Cancel
                </button>
                <button type="submit" className="btn btn-primary">
                  Save Receipt & Update Khata
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* ===================================================================
          Customer Account Statement Modal (Printable)
          =================================================================== */}
      {statementCustomer && (
        <div className="erp-modal-backdrop" onClick={() => setStatementCustomer(null)}>
          <div className="erp-modal" style={{ maxWidth: 650 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <div>
                <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 700 }}>
                  Customer Khata Statement: {statementCustomer.name}
                </h3>
                <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                  Phone: {statementCustomer.phone || '—'} &middot; Credit Limit: ₹{statementCustomer.credit_limit}
                </div>
              </div>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setStatementCustomer(null)}
              >
                <IconClose size={18} />
              </button>
            </div>

            <div className="erp-modal-body">
              <table className="erp-table">
                <thead>
                  <tr>
                    <th>Date</th>
                    <th>Type</th>
                    <th className="text-right">Debits (Bill)</th>
                    <th className="text-right">Credits (Paid)</th>
                    <th className="text-right">Net Due</th>
                  </tr>
                </thead>
                <tbody>
                  {customerStatementEntries.length === 0 ? (
                    <tr>
                      <td colSpan={5} style={{ textAlign: 'center', padding: '1.5rem', color: 'var(--text-muted)' }}>
                        No historical entries found for this customer.
                      </td>
                    </tr>
                  ) : (
                    customerStatementEntries.map((item) => (
                      <tr key={item.id}>
                        <td className="text-mono">{item.created_at ? item.created_at.slice(0, 10) : '—'}</td>
                        <td>{item.type}</td>
                        <td className="text-right text-mono">₹{item.amount.toFixed(2)}</td>
                        <td className="text-right text-mono" style={{ color: '#16a34a' }}>
                          ₹{item.paid_amount.toFixed(2)}
                        </td>
                        <td className="text-right text-mono font-bold" style={{ color: '#ea580c' }}>
                          ₹{(item.amount - item.paid_amount).toFixed(2)}
                        </td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>

            <div className="erp-modal-footer">
              <button
                type="button"
                className="btn btn-outline"
                onClick={() => window.print()}
              >
                <IconPrinter size={16} />
                <span>Print Statement</span>
              </button>
              <button
                type="button"
                className="btn btn-secondary"
                onClick={() => setStatementCustomer(null)}
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