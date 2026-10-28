import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { expenseApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { IconExpenses, IconPlus, IconClose, IconRefresh } from '../components/Icons';

export default function Expenses() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [expenses, setExpenses] = useState<any[]>([]);
  const [categories, setCategories] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [showForm, setShowForm] = useState(false);
  const [summary, setSummary] = useState<any>(null);
  const [dateRange, setDateRange] = useState({ start: '', end: '' });

  const [form, setForm] = useState({
    category_id: '',
    amount: '',
    description: '',
    expense_date: new Date().toISOString().split('T')[0],
  });

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    loadCategories();
  }, [user]);

  useEffect(() => {
    if (store) {
      loadExpenses();
      loadSummary();
    }
  }, [store?.id, dateRange]);

  const loadCategories = async () => {
    try {
      const { data } = await expenseApi.getCategories();
      setCategories(data || []);
      if (data && data.length > 0 && !form.category_id) {
        setForm((prev) => ({ ...prev, category_id: data[0].id }));
      }
    } catch (err) {
      console.error('Failed to load categories:', err);
    }
  };

  const loadExpenses = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await expenseApi.list({ store_id: store.id, ...dateRange });
      setExpenses(data || []);
    } catch (err) {
      console.error('Failed to load expenses:', err);
    } finally {
      setLoading(false);
    }
  };

  const loadSummary = async () => {
    if (!store) return;
    try {
      const { data } = await expenseApi.getSummary({ store_id: store.id, ...dateRange });
      setSummary(data);
    } catch (err) {
      console.error('Failed to load summary:', err);
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const amt = parseFloat(form.amount);
    if (!store || !user || !form.category_id || !amt || amt <= 0) {
      alert('Please select a category and enter a valid positive amount.');
      return;
    }

    try {
      await expenseApi.create({
        store_id: store.id,
        category_id: form.category_id,
        amount: amt,
        description: form.description,
        expense_date: form.expense_date,
      });
      setShowForm(false);
      setForm({
        category_id: categories.length > 0 ? categories[0].id : '',
        amount: '',
        description: '',
        expense_date: new Date().toISOString().split('T')[0],
      });
      loadExpenses();
      loadSummary();
      alert('Expense recorded successfully.');
    } catch (err) {
      console.error('Failed to add expense:', err);
    }
  };

  const totalExpense = summary?.total_expense ?? expenses.reduce((s, e) => s + (e.amount || 0), 0);

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
            Operating Expenses & Overhead Ledger
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Store: <strong>{store?.name}</strong> &middot; Track rent, electricity, wages, freight, packaging, and store overheads
          </p>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <button
            type="button"
            className="btn btn-primary btn-sm"
            onClick={() => setShowForm(true)}
          >
            <IconPlus size={16} />
            <span>Record Expense</span>
          </button>
        </div>
      </div>

      {/* KPI Card */}
      <div className="erp-card" style={{ borderLeft: '4px solid #dc2626', marginBottom: '1.25rem' }}>
        <div className="erp-card-body" style={{ padding: '1rem 1.25rem' }}>
          <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-muted)', textTransform: 'uppercase' }}>
            Total Operating Overhead
          </div>
          <div style={{ fontSize: '1.75rem', fontWeight: 700, color: '#dc2626', margin: '0.25rem 0' }}>
            ₹{totalExpense.toLocaleString('en-IN', { minimumFractionDigits: 2 })}
          </div>
          <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
            Across {expenses.length} expense line items logged
          </div>
        </div>
      </div>

      {/* Expenses Table */}
      <div className="erp-table-container">
        <table className="erp-table">
          <thead>
            <tr>
              <th>Date</th>
              <th>Category</th>
              <th>Description / Remarks</th>
              <th className="text-right">Amount (₹)</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={4} style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-muted)' }}>
                  Loading expenses...
                </td>
              </tr>
            ) : expenses.length === 0 ? (
              <tr>
                <td colSpan={4} style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
                  No expenses recorded for this period.
                </td>
              </tr>
            ) : (
              expenses.map((e) => (
                <tr key={e.id}>
                  <td className="text-mono">{e.expense_date ? e.expense_date.slice(0, 10) : '—'}</td>
                  <td style={{ fontWeight: 600 }}>{e.category_name || 'General'}</td>
                  <td style={{ color: 'var(--text-secondary)' }}>{e.description || '—'}</td>
                  <td className="text-right text-mono font-bold" style={{ color: '#dc2626' }}>
                    ₹{(e.amount || 0).toFixed(2)}
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      {/* Add Expense Modal */}
      {showForm && (
        <div className="erp-modal-backdrop" onClick={() => setShowForm(false)}>
          <div className="erp-modal" style={{ maxWidth: 480 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 600 }}>Record Store Expense</h3>
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
                  <label className="form-label">Expense Category *</label>
                  <select
                    required
                    className="form-select"
                    value={form.category_id}
                    onChange={(e) => setForm({ ...form, category_id: e.target.value })}
                  >
                    <option value="">Select Category</option>
                    {categories.map((c) => (
                      <option key={c.id} value={c.id}>{c.name}</option>
                    ))}
                  </select>
                </div>

                <div className="form-group">
                  <label className="form-label">Amount (₹) *</label>
                  <input
                    type="number"
                    step="0.01"
                    required
                    className="form-input text-mono font-bold"
                    placeholder="0.00"
                    value={form.amount}
                    onChange={(e) => setForm({ ...form, amount: e.target.value })}
                  />
                </div>

                <div className="form-group">
                  <label className="form-label">Expense Date</label>
                  <input
                    type="date"
                    className="form-input text-mono"
                    value={form.expense_date}
                    onChange={(e) => setForm({ ...form, expense_date: e.target.value })}
                  />
                </div>

                <div className="form-group">
                  <label className="form-label">Description / Remarks</label>
                  <input
                    type="text"
                    className="form-input"
                    placeholder="e.g. Monthly shop electricity bill"
                    value={form.description}
                    onChange={(e) => setForm({ ...form, description: e.target.value })}
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
                  Record Expense
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </AppLayout>
  );
}