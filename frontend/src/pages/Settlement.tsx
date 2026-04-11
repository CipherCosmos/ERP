import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { paymentApi } from '../services/api';

export default function Settlement() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [report, setReport] = useState<any>(null);
  const [date, setDate] = useState(new Date().toISOString().split('T')[0]);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadReport();
  }, [user, store, date]);

  const loadReport = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await paymentApi.getSettlement({ store_id: store.id, date });
      setReport(data);
    } catch (err) {
      console.error('Failed to load settlement:', err);
    } finally {
      setLoading(false);
    }
  };

  if (!report) return <div style={{ padding: '2rem' }}>Loading...</div>;

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
        <h1>Daily Settlement Report</h1>
      </div>

      <div style={styles.dateSelect}>
        <label>Date: </label>
        <input type="date" value={date} onChange={(e) => setDate(e.target.value)} style={styles.input} />
      </div>

      <div style={styles.summaryCard}>
        <h2 style={styles.cardTitle}>{report.date}</h2>
        
        <div style={styles.statGrid}>
          <div style={styles.statBox}>
            <span style={styles.statLabel}>Total Sales</span>
            <span style={styles.statValue}>₹{report.total_sales?.toFixed(2)}</span>
          </div>
          <div style={styles.statBox}>
            <span style={styles.statLabel}>Total Invoices</span>
            <span style={styles.statValue}>{report.total_invoices}</span>
          </div>
        </div>

        <h3 style={styles.sectionTitle}>Payment Mode Breakup</h3>
        {report.split_breakdown_used && (
          <p style={styles.hint}>Includes split-tender lines from invoice payments where recorded.</p>
        )}

        <div style={styles.breakupTable}>
          <div style={styles.tableHeader}>
            <span>Mode</span>
            <span>Count</span>
            <span>Amount</span>
          </div>
          <div style={styles.tableRow}>
            <span>Cash</span>
            <span>{report.cash_count}</span>
            <span>₹{report.cash_total?.toFixed(2)}</span>
          </div>
          <div style={styles.tableRow}>
            <span>UPI</span>
            <span>{report.upi_count}</span>
            <span>₹{report.upi_total?.toFixed(2)}</span>
          </div>
          <div style={styles.tableRow}>
            <span>Card</span>
            <span>{report.card_count}</span>
            <span>₹{report.card_total?.toFixed(2)}</span>
          </div>
          <div style={styles.tableRow}>
            <span>Credit</span>
            <span>{report.credit_count}</span>
            <span>₹{report.credit_total?.toFixed(2)}</span>
          </div>
          <div style={styles.tableRow}>
            <span>Bank / transfer</span>
            <span>{report.bank_count ?? 0}</span>
            <span>₹{(report.bank_total ?? 0).toFixed(2)}</span>
          </div>
        </div>

        <div style={styles.finalSection}>
          <div style={styles.finalRow}>
            <span>Returns</span>
            <span style={styles.negative}>-₹{report.returns_total?.toFixed(2)}</span>
          </div>
          <div style={styles.finalRow}>
            <span>Net Collections</span>
            <span style={styles.highlight}>₹{report.net_collections?.toFixed(2)}</span>
          </div>
        </div>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', maxWidth: '800px', margin: '0 auto' },
  header: { display: 'flex', alignItems: 'center', gap: '1rem', marginBottom: '1rem' },
  backBtn: { padding: '0.5rem 1rem', background: '#f5f5f5', border: '1px solid #ddd', borderRadius: '4px', cursor: 'pointer' },
  dateSelect: { marginBottom: '1rem', display: 'flex', alignItems: 'center', gap: '0.5rem' },
  input: { padding: '0.5rem', fontSize: '1rem', border: '1px solid #ddd', borderRadius: '4px' },
  summaryCard: { background: 'white', padding: '1.5rem', borderRadius: '8px', boxShadow: '0 2px 4px rgba(0,0,0,0.1)' },
  cardTitle: { margin: '0 0 1rem', fontSize: '1.25rem' },
  statGrid: { display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: '1rem', marginBottom: '1.5rem' },
  statBox: { background: '#f5f5f5', padding: '1rem', borderRadius: '8px', textAlign: 'center' },
  statLabel: { display: 'block', color: '#666', marginBottom: '0.5rem' },
  statValue: { display: 'block', fontSize: '1.5rem', fontWeight: 'bold', color: '#1976d2' },
  sectionTitle: { margin: '1rem 0', fontSize: '1rem' },
  breakupTable: { border: '1px solid #eee', borderRadius: '4px', overflow: 'hidden' },
  tableHeader: { display: 'grid', gridTemplateColumns: '2fr 1fr 1fr', background: '#f5f5f5', padding: '0.75rem', fontWeight: 'bold' },
  tableRow: { display: 'grid', gridTemplateColumns: '2fr 1fr 1fr', padding: '0.75rem', borderTop: '1px solid #eee' },
  finalSection: { marginTop: '1.5rem', paddingTop: '1rem', borderTop: '2px solid #333' },
  finalRow: { display: 'flex', justifyContent: 'space-between', padding: '0.5rem 0', fontSize: '1.1rem' },
  negative: { color: '#d32f2f' },
  highlight: { fontWeight: 'bold', color: '#2e7d32', fontSize: '1.25rem' },
  hint: { fontSize: '0.8rem', color: '#666', marginBottom: '0.5rem' },
};