import React, { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { reportApi } from '../services/api';
import { formatMoney } from '../utils/format';

export default function CreditAging() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [report, setReport] = useState<any[]>([]);
  const [totalOutstanding, setTotalOutstanding] = useState(0);
  const [loading, setLoading] = useState(false);

  const loadReport = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await reportApi.getCreditAging({ store_id: store.id });
      setReport(data.report || []);
      setTotalOutstanding(data.total_outstanding || 0);
    } catch (err) {
      console.error('Failed to load report:', err);
    } finally {
      setLoading(false);
    }
  }, [store]);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadReport();
  }, [user, store, navigate, loadReport]);

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
        <h1>Credit Aging Report</h1>
      </div>

      <div style={styles.summaryCard}>
        <div style={styles.summaryRow}>
          <span style={styles.summaryLabel}>Total Outstanding</span>
          <span style={styles.summaryValue}>{formatMoney(totalOutstanding)}</span>
        </div>
      </div>

      <div style={styles.tableCard}>
        <table style={styles.table}>
          <thead>
            <tr>
              <th>Customer</th>
              <th>Total Due</th>
              <th>Current (0-30)</th>
              <th>31-60 Days</th>
              <th>61-90 Days</th>
              <th>Over 90 Days</th>
            </tr>
          </thead>
          <tbody>
            {report.map((r) => (
              <tr key={r.customer_id}>
                <td>{r.customer_name}</td>
                <td style={{ fontWeight: 'bold' }}>{formatMoney(r.total_due)}</td>
                <td style={{ color: r.current > 0 ? '#2e7d32' : '#666' }}>{formatMoney(r.current)}</td>
                <td style={{ color: r.days_30 > 0 ? '#ed6c02' : '#666' }}>{formatMoney(r.days_30)}</td>
                <td style={{ color: r.days_60 > 0 ? '#d32f2f' : '#666' }}>{formatMoney(r.days_60)}</td>
                <td style={{ color: r.over_90 > 0 ? '#9c27b0' : '#666' }}>{formatMoney(r.over_90)}</td>
              </tr>
            ))}
            {loading && (
              <tr>
                <td colSpan={6} style={{ textAlign: 'center', padding: '2rem' }}>
                  Loading...
                </td>
              </tr>
            )}
            {!loading && report.length === 0 && (
              <tr>
                <td colSpan={6} style={{ textAlign: 'center', padding: '2rem' }}>
                  No outstanding credit
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', maxWidth: '1200px', margin: '0 auto' },
  header: { display: 'flex', alignItems: 'center', gap: '1rem', marginBottom: '1rem' },
  backBtn: { padding: '0.5rem 1rem', background: '#f5f5f5', border: '1px solid #ddd', borderRadius: '4px', cursor: 'pointer' },
  summaryCard: { background: '#1976d2', color: 'white', padding: '1.5rem', borderRadius: '8px', marginBottom: '1rem' },
  summaryRow: { display: 'flex', justifyContent: 'space-between', alignItems: 'center' },
  summaryLabel: { fontSize: '1.25rem' },
  summaryValue: { fontSize: '2rem', fontWeight: 'bold' },
  tableCard: { background: 'white', borderRadius: '8px', boxShadow: '0 2px 4px rgba(0,0,0,0.1)', overflow: 'hidden' },
  table: { width: '100%', borderCollapse: 'collapse' },
};