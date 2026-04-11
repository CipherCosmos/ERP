import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { analyticsApi } from '../services/api';

interface SalesReport {
  category: string;
  sales: number;
  item_count: number;
}

interface TopProduct {
  product: string;
  quantity: number;
  sales: number;
}

export default function Reports() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [activeTab, setActiveTab] = useState<'category' | 'products' | 'hourly' | 'payment'>('category');
  const [reportData, setReportData] = useState<any>(null);
  const [loading, setLoading] = useState(false);
  const [startDate, setStartDate] = useState(new Date().toISOString().slice(0, 10));
  const [endDate, setEndDate] = useState(new Date().toISOString().slice(0, 10));

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user]);

  useEffect(() => {
    if (store) loadReport();
  }, [store, activeTab, startDate, endDate]);

  const loadReport = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const params = { store_id: store.id, start_date: startDate, end_date: endDate };
      let res;
      switch (activeTab) {
        case 'category':
          res = await analyticsApi.salesByCategory(params);
          break;
        case 'products':
          res = await analyticsApi.topProducts(params);
          break;
        case 'hourly':
          res = await analyticsApi.hourlySales({ store_id: store.id, date: startDate });
          break;
        case 'payment':
          res = await analyticsApi.paymentBreakup(params);
          break;
        default:
          res = { data: null };
      }
      setReportData(res.data);
    } catch (err) {
      console.error('Failed to load report:', err);
    } finally {
      setLoading(false);
    }
  };

  if (!user || user.role === 'cashier') return null;

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Reports</h1>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
      </div>

      <div style={styles.filters}>
        <input type="date" value={startDate} onChange={e => setStartDate(e.target.value)} style={styles.input} />
        <span>to</span>
        <input type="date" value={endDate} onChange={e => setEndDate(e.target.value)} style={styles.input} />
      </div>

      <div style={styles.tabs}>
        <button style={activeTab === 'category' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('category')}>By Category</button>
        <button style={activeTab === 'products' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('products')}>Top Products</button>
        <button style={activeTab === 'hourly' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('hourly')}>Hourly</button>
        <button style={activeTab === 'payment' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('payment')}>Payment Mode</button>
      </div>

      {loading ? (
        <p>Loading...</p>
      ) : (
        <div style={styles.tableContainer}>
          {activeTab === 'category' && (
            <table style={styles.table}>
              <thead>
                <tr>
                  <th style={styles.th}>Category</th>
                  <th style={styles.th}>Sales</th>
                  <th style={styles.th}>Items Sold</th>
                </tr>
              </thead>
              <tbody>
                {(reportData as SalesReport[])?.map((r, i) => (
                  <tr key={i} style={styles.tr}>
                    <td style={styles.td}>{r.category}</td>
                    <td style={styles.td}>₹{r.sales.toFixed(2)}</td>
                    <td style={styles.td}>{r.item_count}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          {activeTab === 'products' && (
            <table style={styles.table}>
              <thead>
                <tr>
                  <th style={styles.th}>Product</th>
                  <th style={styles.th}>Quantity</th>
                  <th style={styles.th}>Sales</th>
                </tr>
              </thead>
              <tbody>
                {(reportData as TopProduct[])?.map((r, i) => (
                  <tr key={i} style={styles.tr}>
                    <td style={styles.td}>{r.product}</td>
                    <td style={styles.td}>{r.quantity.toFixed(2)}</td>
                    <td style={styles.td}>₹{r.sales.toFixed(2)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          {activeTab === 'hourly' && (
            <table style={styles.table}>
              <thead>
                <tr>
                  <th style={styles.th}>Hour</th>
                  <th style={styles.th}>Transactions</th>
                  <th style={styles.th}>Sales</th>
                </tr>
              </thead>
              <tbody>
                {(reportData as any[])?.map((r, i) => (
                  <tr key={i} style={styles.tr}>
                    <td style={styles.td}>{r.hour}</td>
                    <td style={styles.td}>{r.count}</td>
                    <td style={styles.td}>₹{r.sales.toFixed(2)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          {activeTab === 'payment' && (
            <table style={styles.table}>
              <thead>
                <tr>
                  <th style={styles.th}>Mode</th>
                  <th style={styles.th}>Count</th>
                  <th style={styles.th}>Total</th>
                </tr>
              </thead>
              <tbody>
                {(reportData as any[])?.map((r, i) => (
                  <tr key={i} style={styles.tr}>
                    <td style={styles.td}>{r.mode}</td>
                    <td style={styles.td}>{r.count}</td>
                    <td style={styles.td}>₹{r.total.toFixed(2)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', background: '#f5f5f5', minHeight: '100vh' },
  header: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' },
  backBtn: { padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  filters: { display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '1rem' },
  input: { padding: '0.5rem', border: '1px solid #ddd', borderRadius: '4px' },
  tabs: { display: 'flex', gap: '0.5rem', marginBottom: '1rem', flexWrap: 'wrap' },
  tab: { padding: '0.5rem 1rem', background: 'white', border: '1px solid #ddd', borderRadius: '4px', cursor: 'pointer' },
  tabActive: { padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  tableContainer: { background: 'white', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)', overflow: 'hidden' },
  table: { width: '100%', borderCollapse: 'collapse' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  tr: { borderBottom: '1px solid #eee' },
};