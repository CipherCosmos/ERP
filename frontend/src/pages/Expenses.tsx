import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { expenseApi } from '../services/api';

export default function Expenses() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [expenses, setExpenses] = useState<any[]>([]);
  const [categories, setCategories] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [showForm, setShowForm] = useState(false);
  const [summary, setSummary] = useState<any>(null);
  const [dateRange, setDateRange] = useState({ start: '', end: '' });
  const [form, setForm] = useState({ category_id: '', amount: '', description: '', expense_date: new Date().toISOString().split('T')[0] });

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadCategories();
    loadExpenses();
    loadSummary();
  }, [user, store, dateRange]);

  const loadCategories = async () => {
    try {
      const { data } = await expenseApi.getCategories();
      setCategories(data);
    } catch (err) {
      console.error('Failed to load categories:', err);
    }
  };

  const loadExpenses = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await expenseApi.list({ store_id: store.id, ...dateRange });
      setExpenses(data);
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

  const handleSubmit = async () => {
    if (!store || !user || !form.category_id || !form.amount) {
      alert('Please fill required fields');
      return;
    }
    try {
      await expenseApi.create({
        store_id: store.id,
        category_id: form.category_id,
        amount: parseFloat(form.amount),
        description: form.description,
        expense_date: form.expense_date,
        user_id: user.id,
      });
      setShowForm(false);
      setForm({ category_id: '', amount: '', description: '', expense_date: new Date().toISOString().split('T')[0] });
      loadExpenses();
      loadSummary();
      alert('Expense added');
    } catch (err) {
      console.error('Failed to add expense:', err);
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
        <h1>Expenses</h1>
        <button onClick={() => setShowForm(true)} style={styles.addBtn}>+ Add Expense</button>
      </div>

      <div style={styles.dateFilter}>
        <input type="date" value={dateRange.start} onChange={(e) => setDateRange({ ...dateRange, start: e.target.value })} style={styles.input} />
        <span>to</span>
        <input type="date" value={dateRange.end} onChange={(e) => setDateRange({ ...dateRange, end: e.target.value })} style={styles.input} />
      </div>

      {summary && (
        <div style={styles.summaryCard}>
          <h3>Expense Summary</h3>
          <div style={styles.summaryGrid}>
            {summary.summary?.map((s: any) => (
              <div key={s.category} style={styles.summaryItem}>
                <span style={styles.categoryName}>{s.category}</span>
                <span style={styles.categoryTotal}>₹{s.total?.toFixed(2)}</span>
              </div>
            ))}
          </div>
          <div style={styles.grandTotal}>
            <span>Grand Total</span>
            <span>₹{summary.grand_total?.toFixed(2)}</span>
          </div>
        </div>
      )}

      {showForm && (
        <div style={styles.formCard}>
          <h3>Add Expense</h3>
          <div style={styles.formGrid}>
            <select value={form.category_id} onChange={(e) => setForm({ ...form, category_id: e.target.value })} style={styles.select}>
              <option value="">Select Category</option>
              {categories.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </select>
            <input type="number" placeholder="Amount" value={form.amount} onChange={(e) => setForm({ ...form, amount: e.target.value })} style={styles.input} />
            <input type="date" value={form.expense_date} onChange={(e) => setForm({ ...form, expense_date: e.target.value })} style={styles.input} />
            <input type="text" placeholder="Description (optional)" value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} style={styles.input} />
          </div>
          <div style={styles.formActions}>
            <button onClick={handleSubmit} style={styles.submitBtn}>Submit</button>
            <button onClick={() => setShowForm(false)} style={styles.cancelBtn}>Cancel</button>
          </div>
        </div>
      )}

      <div style={styles.listCard}>
        <table style={styles.table}>
          <thead>
            <tr>
              <th>Date</th>
              <th>Category</th>
              <th>Description</th>
              <th>Amount</th>
            </tr>
          </thead>
          <tbody>
            {expenses.map((e) => (
              <tr key={e.id}>
                <td>{new Date(e.expense_date).toLocaleDateString()}</td>
                <td>{e.category_name}</td>
                <td>{e.description || '-'}</td>
                <td style={{ fontWeight: 'bold' }}>₹{e.amount?.toFixed(2)}</td>
              </tr>
            ))}
            {expenses.length === 0 && <tr><td colSpan={4} style={{ textAlign: 'center', padding: '2rem' }}>No expenses</td></tr>}
          </tbody>
        </table>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', maxWidth: '1000px', margin: '0 auto' },
  header: { display: 'flex', alignItems: 'center', gap: '1rem', marginBottom: '1rem' },
  backBtn: { padding: '0.5rem 1rem', background: '#f5f5f5', border: '1px solid #ddd', borderRadius: '4px', cursor: 'pointer' },
  addBtn: { marginLeft: 'auto', padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  dateFilter: { display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '1rem' },
  input: { padding: '0.5rem', fontSize: '1rem', border: '1px solid #ddd', borderRadius: '4px' },
  summaryCard: { background: '#fff3e0', padding: '1rem', borderRadius: '8px', marginBottom: '1rem' },
  summaryGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))', gap: '0.5rem', marginBottom: '1rem' },
  summaryItem: { display: 'flex', justifyContent: 'space-between', padding: '0.5rem', background: 'white', borderRadius: '4px' },
  categoryName: { color: '#666' },
  categoryTotal: { fontWeight: 'bold' },
  grandTotal: { display: 'flex', justifyContent: 'space-between', padding: '1rem', background: '#ff9800', color: 'white', borderRadius: '4px', fontWeight: 'bold', fontSize: '1.1rem' },
  formCard: { background: 'white', padding: '1.5rem', borderRadius: '8px', boxShadow: '0 2px 4px rgba(0,0,0,0.1)', marginBottom: '1rem' },
  formGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(150px, 1fr))', gap: '0.5rem', marginBottom: '1rem' },
  formActions: { display: 'flex', gap: '0.5rem' },
  submitBtn: { padding: '0.5rem 1rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  cancelBtn: { padding: '0.5rem 1rem', background: '#f5f5f5', border: '1px solid #ddd', borderRadius: '4px', cursor: 'pointer' },
  select: { padding: '0.5rem', fontSize: '1rem', border: '1px solid #ddd', borderRadius: '4px' },
  listCard: { background: 'white', padding: '1rem', borderRadius: '8px', boxShadow: '0 2px 4px rgba(0,0,0,0.1)' },
  table: { width: '100%', borderCollapse: 'collapse' },
};