import React, { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { analyticsApi } from '../services/api';
import { BarChart, LineChart, PieChart } from '../components/Charts';
import { todayISO } from '../utils/format';

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
  /**
   * Which tab the rows in reportData were fetched for. Clicking a tab re-renders
   * before the effect can refetch, so without this the new tab reads the previous
   * tab's rows — and a category row has no `product`/`hour`/`mode`, so the chart
   * label comes back undefined and the whole page throws on `label.length`.
   */
  const [dataTab, setDataTab] = useState<'category' | 'products' | 'hourly' | 'payment' | null>(null);
  const [loading, setLoading] = useState(false);
  const [startDate, setStartDate] = useState(todayISO());
  const [endDate, setEndDate] = useState(todayISO());

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user, navigate]);

  // Memoised so the load effect can list it. It re-creates when the tab or the
  // date range changes — that is what refetches, exactly as before. The params
  // object is built inside the callback, so it is not a render-time reference.
  const loadReport = useCallback(async () => {
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
      setDataTab(activeTab);
    } catch (err) {
      console.error('Failed to load report:', err);
    } finally {
      setLoading(false);
    }
  }, [store, activeTab, startDate, endDate]);

  useEffect(() => {
    if (store) loadReport();
  }, [store, loadReport]);

  if (!user || user.role === 'cashier') return null;

  // Only read the rows when they belong to the tab being drawn.
  const shownData = dataTab === activeTab ? reportData : null;
  const categoryData = shownData as SalesReport[] | null;
  const productsData = shownData as TopProduct[] | null;
  const hourlyData = shownData as { hour: string; count: number; sales: number }[] | null;
  const paymentData = shownData as { mode: string; count: number; total: number }[] | null;

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Reports</h1>
        <button onClick={() => navigate('/')} style={styles.backBtn}>&larr; Back</button>
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
        <>
          {/* ---- By Category ---- */}
          {activeTab === 'category' && (
            <>
              <div style={styles.chartCard}>
                <h3 style={styles.chartTitle}>Category-wise Sales</h3>
                <BarChart
                  data={(categoryData || []).map(r => ({ label: r.category, value: r.sales }))}
                  height={230}
                  width={560}
                />
              </div>
              <div style={styles.tableContainer}>
                <table style={styles.table}>
                  <thead>
                    <tr>
                      <th style={styles.th}>Category</th>
                      <th style={styles.th}>Sales</th>
                      <th style={styles.th}>Items Sold</th>
                    </tr>
                  </thead>
                  <tbody>
                    {categoryData?.map((r, i) => (
                      <tr key={i} style={styles.tr}>
                        <td style={styles.td}>{r.category}</td>
                        <td style={styles.td}>{'\u20B9'}{r.sales.toFixed(2)}</td>
                        <td style={styles.td}>{r.item_count}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}

          {/* ---- Top Products ---- */}
          {activeTab === 'products' && (
            <>
              <div style={styles.chartCard}>
                <h3 style={styles.chartTitle}>Top Products by Sales</h3>
                <BarChart
                  data={(productsData || []).map(r => ({ label: r.product, value: r.sales }))}
                  height={230}
                  width={560}
                />
              </div>
              <div style={styles.tableContainer}>
                <table style={styles.table}>
                  <thead>
                    <tr>
                      <th style={styles.th}>Product</th>
                      <th style={styles.th}>Quantity</th>
                      <th style={styles.th}>Sales</th>
                    </tr>
                  </thead>
                  <tbody>
                    {productsData?.map((r, i) => (
                      <tr key={i} style={styles.tr}>
                        <td style={styles.td}>{r.product}</td>
                        <td style={styles.td}>{r.quantity.toFixed(2)}</td>
                        <td style={styles.td}>{'\u20B9'}{r.sales.toFixed(2)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}

          {/* ---- Hourly ---- */}
          {activeTab === 'hourly' && (
            <>
              <div style={styles.chartCard}>
                <h3 style={styles.chartTitle}>Hourly Sales Pattern</h3>
                <LineChart
                  data={(hourlyData || []).map(r => ({ label: r.hour, value: r.sales }))}
                  height={230}
                  width={560}
                  color="#1976d2"
                />
              </div>
              <div style={styles.tableContainer}>
                <table style={styles.table}>
                  <thead>
                    <tr>
                      <th style={styles.th}>Hour</th>
                      <th style={styles.th}>Transactions</th>
                      <th style={styles.th}>Sales</th>
                    </tr>
                  </thead>
                  <tbody>
                    {hourlyData?.map((r, i) => (
                      <tr key={i} style={styles.tr}>
                        <td style={styles.td}>{r.hour}</td>
                        <td style={styles.td}>{r.count}</td>
                        <td style={styles.td}>{'\u20B9'}{r.sales.toFixed(2)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}

          {/* ---- Payment Mode ---- */}
          {activeTab === 'payment' && (
            <>
              <div style={styles.chartCard}>
                <h3 style={styles.chartTitle}>Payment Mode Breakdown</h3>
                <PieChart
                  data={(paymentData || []).map(r => ({ label: r.mode, value: r.total }))}
                  size={200}
                />
              </div>
              <div style={styles.tableContainer}>
                <table style={styles.table}>
                  <thead>
                    <tr>
                      <th style={styles.th}>Mode</th>
                      <th style={styles.th}>Count</th>
                      <th style={styles.th}>Total</th>
                    </tr>
                  </thead>
                  <tbody>
                    {paymentData?.map((r, i) => (
                      <tr key={i} style={styles.tr}>
                        <td style={styles.td}>{r.mode}</td>
                        <td style={styles.td}>{r.count}</td>
                        <td style={styles.td}>{'\u20B9'}{r.total.toFixed(2)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </>
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
  chartCard: { background: 'white', padding: '1rem', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)', marginBottom: '1rem' },
  chartTitle: { margin: '0 0 0.75rem 0', fontSize: '1rem', color: '#333' },
  tableContainer: { background: 'white', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)', overflow: 'hidden' },
  table: { width: '100%', borderCollapse: 'collapse' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  tr: { borderBottom: '1px solid #eee' },
};
