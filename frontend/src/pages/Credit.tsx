import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';

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
}

export default function Credit() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [entries, setEntries] = useState<CreditEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [activeTab, setActiveTab] = useState<'all' | 'pending' | 'overdue'>('all');
  const [showPaymentModal, setShowPaymentModal] = useState(false);
  const [selectedCustomer, setSelectedCustomer] = useState<string | null>(null);
  const [paymentAmount, setPaymentAmount] = useState('');

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user]);

  useEffect(() => {
    if (store) loadCreditEntries();
  }, [store, activeTab]);

  const loadCreditEntries = async () => {
    if (!store) return;
    setLoading(true);
    try {
      let url = `/api/v1/credit?store_id=${store.id}`;
      if (activeTab === 'overdue') url += '&status=overdue';
      
      const { data } = await fetch(url).then(r => r.json());
      setEntries(data || []);
    } catch (err) {
      console.error('Failed to load credit entries:', err);
    } finally {
      setLoading(false);
    }
  };

  const handlePayment = async () => {
    if (!selectedCustomer || !paymentAmount) return;
    try {
      await fetch('/api/v1/credit/payment', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ customer_id: selectedCustomer, amount: parseFloat(paymentAmount), method: 'cash' }),
      });
      setShowPaymentModal(false);
      setPaymentAmount('');
      loadCreditEntries();
    } catch (err) {
      console.error('Payment failed:', err);
    }
  };

  if (!user || user.role === 'cashier') return null;

  const totalOutstanding = entries.filter(e => e.status !== 'paid').reduce((sum, e) => sum + (e.amount - e.paid_amount), 0);

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Credit Management</h1>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
      </div>

      <div style={styles.summary}>
        <div style={styles.summaryCard}>
          <div style={styles.summaryLabel}>Total Outstanding</div>
          <div style={styles.summaryValue}>₹{totalOutstanding.toFixed(2)}</div>
        </div>
        <div style={styles.summaryCard}>
          <div style={styles.summaryLabel}>Pending Entries</div>
          <div style={styles.summaryValue}>{entries.filter(e => e.status === 'pending' || e.status === 'partial').length}</div>
        </div>
      </div>

      <div style={styles.tabs}>
        <button style={activeTab === 'all' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('all')}>All</button>
        <button style={activeTab === 'pending' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('pending')}>Pending</button>
        <button style={activeTab === 'overdue' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('overdue')}>Overdue</button>
      </div>

      {loading ? (
        <p>Loading...</p>
      ) : (
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Customer</th>
              <th style={styles.th}>Type</th>
              <th style={styles.th}>Amount</th>
              <th style={styles.th}>Paid</th>
              <th style={styles.th}>Balance</th>
              <th style={styles.th}>Due Date</th>
              <th style={styles.th}>Status</th>
              <th style={styles.th}>Action</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e) => (
              <tr key={e.id} style={styles.tr}>
                <td style={styles.td}>{e.customer_name}</td>
                <td style={styles.td}>
                  <span style={{...styles.badge, background: e.type === 'issue' ? '#ffebee' : '#e8f5e9', color: e.type === 'issue' ? '#d32f2f' : '#388e3c'}}>
                    {e.type === 'issue' ? 'Given' : 'Payment'}
                  </span>
                </td>
                <td style={styles.td}>₹{e.amount.toFixed(2)}</td>
                <td style={styles.td}>₹{e.paid_amount.toFixed(2)}</td>
                <td style={styles.td}>₹{(e.amount - e.paid_amount).toFixed(2)}</td>
                <td style={styles.td}>{e.due_date || '-'}</td>
                <td style={styles.td}>
                  <span style={{...styles.statusBadge, background: e.status === 'paid' ? '#e8f5e9' : e.status === 'partial' ? '#fff3e0' : '#ffebee'}}>
                    {e.status}
                  </span>
                </td>
                <td style={styles.td}>
                  {e.status !== 'paid' && e.type === 'issue' && (
                    <button onClick={() => { setSelectedCustomer(e.customer_id); setShowPaymentModal(true); }} style={styles.payBtn}>
                      Pay
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {showPaymentModal && (
        <div style={styles.modal}>
          <div style={styles.modalContent}>
            <h2>Record Payment</h2>
            <input type="number" placeholder="Amount" value={paymentAmount} onChange={e => setPaymentAmount(e.target.value)} style={styles.input} />
            <div style={styles.modalActions}>
              <button onClick={() => setShowPaymentModal(false)} style={styles.cancelBtn}>Cancel</button>
              <button onClick={handlePayment} style={styles.submitBtn}>Submit</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', background: '#f5f5f5', minHeight: '100vh' },
  header: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' },
  backBtn: { padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  summary: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(150px, 1fr))', gap: '1rem', marginBottom: '1rem' },
  summaryCard: { background: 'white', padding: '1rem', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  summaryLabel: { fontSize: '0.875rem', color: '#666', marginBottom: '0.25rem' },
  summaryValue: { fontSize: '1.5rem', fontWeight: 'bold', color: '#1976d2' },
  tabs: { display: 'flex', gap: '0.5rem', marginBottom: '1rem' },
  tab: { padding: '0.75rem 1.5rem', background: 'white', border: '1px solid #ddd', borderRadius: '4px', cursor: 'pointer' },
  tabActive: { padding: '0.75rem 1.5rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  table: { width: '100%', background: 'white', borderCollapse: 'collapse', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  tr: { borderBottom: '1px solid #eee' },
  badge: { padding: '0.25rem 0.5rem', borderRadius: '4px', fontSize: '0.75rem' },
  statusBadge: { padding: '0.25rem 0.5rem', borderRadius: '4px', fontSize: '0.75rem' },
  payBtn: { padding: '0.25rem 0.75rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  modal: { position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.5)', display: 'flex', alignItems: 'center', justifyContent: 'center' },
  modalContent: { background: 'white', padding: '2rem', borderRadius: '8px', width: '300px' },
  input: { width: '100%', padding: '0.75rem', marginBottom: '1rem', border: '1px solid #ddd', borderRadius: '4px' },
  modalActions: { display: 'flex', gap: '1rem', justifyContent: 'flex-end' },
  cancelBtn: { padding: '0.75rem 1rem', background: '#ddd', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  submitBtn: { padding: '0.75rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
};