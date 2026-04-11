import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { reportApi } from '../services/api';

export default function ProfitLoss() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [report, setReport] = useState<any>(null);
  const [startDate, setStartDate] = useState(() => {
    const d = new Date();
    d.setMonth(d.getMonth() - 1);
    return d.toISOString().split('T')[0];
  });
  const [endDate, setEndDate] = useState(() => new Date().toISOString().split('T')[0]);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadReport();
  }, [user, store, startDate, endDate]);

  const loadReport = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await reportApi.getProfitLoss({
        store_id: store.id,
        start_date: startDate,
        end_date: endDate,
      });
      setReport(data);
    } catch (err) {
      console.error('Failed to load report:', err);
    } finally {
      setLoading(false);
    }
  };

  if (!report) return <div style={{ padding: '2rem' }}>Loading...</div>;

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
        <h1>Profit & Loss Report</h1>
      </div>

      <div style={styles.dateRange}>
        <label>From: </label>
        <input type="date" value={startDate} onChange={(e) => setStartDate(e.target.value)} style={styles.input} />
        <label style={{ marginLeft: '1rem' }}>To: </label>
        <input type="date" value={endDate} onChange={(e) => setEndDate(e.target.value)} style={styles.input} />
      </div>

      <div style={styles.card}>
        <h2 style={styles.period}>Period: {report.start_date} to {report.end_date}</h2>
        
        <div style={styles.section}>
          <h3 style={styles.sectionTitle}>Revenue</h3>
          <div style={styles.row}>
            <span>Total Sales</span>
            <span>₹{report.total_revenue?.toFixed(2)}</span>
          </div>
          <div style={styles.row}>
            <span>Less: Returns</span>
            <span style={styles.negative}>-₹{report.total_returns?.toFixed(2)}</span>
          </div>
          <div style={styles.rowHighlight}>
            <span>Net Revenue</span>
            <span>₹{report.net_revenue?.toFixed(2)}</span>
          </div>
        </div>

        <div style={styles.section}>
          <h3 style={styles.sectionTitle}>Cost of Goods Sold</h3>
          <div style={styles.row}>
            <span>COGS</span>
            <span style={styles.negative}>-₹{report.total_cogs?.toFixed(2)}</span>
          </div>
        </div>

        <div style={styles.section}>
          <h3 style={styles.sectionTitle}>Inventory Losses</h3>
          <div style={styles.row}>
            <span>Losses (Damaged/Expired)</span>
            <span style={styles.negative}>-₹{report.total_losses?.toFixed(2)}</span>
          </div>
        </div>

        <div style={styles.section}>
          <h3 style={styles.sectionTitle}>Operating Expenses</h3>
          <div style={styles.row}>
            <span>Expenses</span>
            <span style={styles.negative}>-₹{report.total_expenses?.toFixed(2)}</span>
          </div>
        </div>

        <div style={styles.resultSection}>
          <div style={styles.row}>
            <span>Gross Profit</span>
            <span>₹{report.gross_profit?.toFixed(2)}</span>
          </div>
          <div style={styles.row}>
            <span>Gross Margin</span>
            <span>{report.gross_margin_percent?.toFixed(1)}%</span>
          </div>
          <div style={styles.finalRow}>
            <span>Net Profit</span>
            <span style={report.net_profit >= 0 ? styles.positive : styles.negative}>
              ₹{report.net_profit?.toFixed(2)}
            </span>
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
  dateRange: { marginBottom: '1rem', display: 'flex', alignItems: 'center' },
  input: { padding: '0.5rem', fontSize: '1rem', border: '1px solid #ddd', borderRadius: '4px' },
  card: { background: 'white', padding: '1.5rem', borderRadius: '8px', boxShadow: '0 2px 4px rgba(0,0,0,0.1)' },
  period: { margin: '0 0 1.5rem', fontSize: '1.25rem' },
  section: { marginBottom: '1.5rem', paddingBottom: '1rem', borderBottom: '1px solid #eee' },
  sectionTitle: { margin: '0 0 1rem', fontSize: '1rem', color: '#666' },
  row: { display: 'flex', justifyContent: 'space-between', padding: '0.5rem 0' },
  rowHighlight: { display: 'flex', justifyContent: 'space-between', padding: '0.75rem', fontWeight: 'bold', background: '#f5f5f5', marginTop: '0.5rem', borderRadius: '4px' },
  resultSection: { marginTop: '1rem', paddingTop: '1rem', borderTop: '2px solid #333' },
  finalRow: { display: 'flex', justifyContent: 'space-between', padding: '1rem 0', fontSize: '1.25rem', fontWeight: 'bold' },
  negative: { color: '#d32f2f' },
  positive: { color: '#2e7d32' },
};