import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { analyticsApi, stockApi, creditApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import {
  IconPOS,
  IconProducts,
  IconInventory,
  IconWarehouse,
  IconPurchases,
  IconCredit,
  IconAlert,
  IconRefresh,
  IconArrowRight,
} from '../components/Icons';

interface DashboardMetrics {
  revenue: number;
  collected_payments: number;
  gst_collected: number;
  discount_given: number;
  invoice_count: number;
  transaction_count: number;
  avg_bill_value: number;
  low_stock_count: number;
  credit_outstanding: number;
  inventory_valuation: number;
  expiring_count: number;
  pending_po_count: number;
  vendor_payables: number;
  date_range?: { start: string; end: string };
}

interface PaymentModeBreakup {
  mode: string;
  count: number;
  total: number;
}

interface TopProduct {
  product: string;
  quantity: number;
  sales: number;
}

export default function Dashboard() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [metrics, setMetrics] = useState<DashboardMetrics | null>(null);
  const [loading, setLoading] = useState(false);
  const [range, setRange] = useState('today');
  const [paymentBreakup, setPaymentBreakup] = useState<PaymentModeBreakup[]>([]);
  const [topProducts, setTopProducts] = useState<TopProduct[]>([]);
  const [lowStockItems, setLowStockItems] = useState<any[]>([]);

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    if (user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user]);

  useEffect(() => {
    if (store) {
      loadDashboard();
    }
  }, [store, range]);

  const getRangeDates = () => {
    const now = new Date();
    const end = now.toISOString().slice(0, 10);
    let start = end;
    if (range === 'week') {
      const d = new Date(now);
      d.setDate(d.getDate() - 7);
      start = d.toISOString().slice(0, 10);
    } else if (range === 'month') {
      const d = new Date(now);
      d.setMonth(d.getMonth() - 1);
      start = d.toISOString().slice(0, 10);
    } else if (range === 'year') {
      const d = new Date(now);
      d.setFullYear(d.getFullYear() - 1);
      start = d.toISOString().slice(0, 10);
    }
    return { start, end };
  };

  const loadDashboard = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await analyticsApi.dashboard({ store_id: store.id, range });
      setMetrics(data);

      const { start, end } = getRangeDates();
      const [payRes, topRes, lowRes] = await Promise.allSettled([
        analyticsApi.paymentBreakup({ store_id: store.id, start_date: start, end_date: end }),
        analyticsApi.topProducts({ store_id: store.id, start_date: start, end_date: end, limit: 5 }),
        stockApi.low(store.id),
      ]);

      if (payRes.status === 'fulfilled') setPaymentBreakup(payRes.value.data || []);
      if (topRes.status === 'fulfilled') setTopProducts(topRes.value.data || []);
      if (lowRes.status === 'fulfilled') setLowStockItems(lowRes.value.data || []);
    } catch (err) {
      console.error('Failed to load dashboard:', err);
    } finally {
      setLoading(false);
    }
  };

  return (
    <AppLayout>
      {/* Top Controls Bar */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          marginBottom: '1.25rem',
          flexWrap: 'wrap',
          gap: '0.75rem',
        }}
      >
        <div>
          <h1 style={{ margin: 0, fontSize: '1.4rem', fontWeight: 700 }}>Business Overview & Operational KPIs</h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Store: <strong>{store?.name}</strong> &middot; Real-time data from database
          </p>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <select
            className="form-select"
            value={range}
            onChange={(e) => setRange(e.target.value)}
            style={{ width: 'auto', minWidth: 140 }}
          >
            <option value="today">Today</option>
            <option value="week">Past 7 Days</option>
            <option value="month">This Month</option>
            <option value="year">Past Year</option>
          </select>

          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={loadDashboard}
            disabled={loading}
            title="Refresh Metrics"
          >
            <IconRefresh size={16} />
            <span>Refresh</span>
          </button>

          <button
            type="button"
            className="btn btn-primary btn-sm"
            onClick={() => navigate('/billing')}
          >
            <IconPOS size={16} />
            <span>Open POS Terminal</span>
          </button>
        </div>
      </div>

      {/* 4 Main Core Financial Cards */}
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fit, minmax(260px, 1fr))',
          gap: '1rem',
          marginBottom: '1.25rem',
        }}
      >
        {/* Revenue */}
        <div className="erp-card" style={{ borderLeft: '4px solid var(--primary-600)' }}>
          <div className="erp-card-body" style={{ padding: '1rem 1.25rem' }}>
            <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-muted)', textTransform: 'uppercase' }}>
              Sales Revenue ({range})
            </div>
            <div style={{ fontSize: '1.75rem', fontWeight: 700, color: 'var(--text-main)', margin: '0.25rem 0' }}>
              ₹{(metrics?.revenue || 0).toLocaleString('en-IN', { minimumFractionDigits: 2 })}
            </div>
            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
              Invoices: <strong>{metrics?.invoice_count || 0}</strong> &middot; Avg Bill: ₹{(metrics?.avg_bill_value || 0).toFixed(2)}
            </div>
          </div>
        </div>

        {/* Collected Payments */}
        <div className="erp-card" style={{ borderLeft: '4px solid #16a34a' }}>
          <div className="erp-card-body" style={{ padding: '1rem 1.25rem' }}>
            <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-muted)', textTransform: 'uppercase' }}>
              Payments Collected
            </div>
            <div style={{ fontSize: '1.75rem', fontWeight: 700, color: '#16a34a', margin: '0.25rem 0' }}>
              ₹{(metrics?.collected_payments || metrics?.revenue || 0).toLocaleString('en-IN', { minimumFractionDigits: 2 })}
            </div>
            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
              Cash, Card & UPI collected into register
            </div>
          </div>
        </div>

        {/* Customer Receivables / Khata */}
        <div
          className="erp-card"
          style={{ borderLeft: '4px solid #ea580c', cursor: 'pointer' }}
          onClick={() => navigate('/credit')}
        >
          <div className="erp-card-body" style={{ padding: '1rem 1.25rem' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-muted)', textTransform: 'uppercase' }}>
                Customer Credit (Khata)
              </div>
              <IconArrowRight size={14} color="#ea580c" />
            </div>
            <div style={{ fontSize: '1.75rem', fontWeight: 700, color: '#ea580c', margin: '0.25rem 0' }}>
              ₹{(metrics?.credit_outstanding || 0).toLocaleString('en-IN', { minimumFractionDigits: 2 })}
            </div>
            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
              Total outstanding receivables due
            </div>
          </div>
        </div>

        {/* Supplier / Vendor Payables */}
        <div
          className="erp-card"
          style={{ borderLeft: '4px solid #9333ea', cursor: 'pointer' }}
          onClick={() => navigate('/vendor-payments')}
        >
          <div className="erp-card-body" style={{ padding: '1rem 1.25rem' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-muted)', textTransform: 'uppercase' }}>
                Vendor Payables Due
              </div>
              <IconArrowRight size={14} color="#9333ea" />
            </div>
            <div style={{ fontSize: '1.75rem', fontWeight: 700, color: '#9333ea', margin: '0.25rem 0' }}>
              ₹{(metrics?.vendor_payables || 0).toLocaleString('en-IN', { minimumFractionDigits: 2 })}
            </div>
            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
              Pending PO liabilities &middot; {metrics?.pending_po_count || 0} POs pending
            </div>
          </div>
        </div>
      </div>

      {/* Secondary Operational Status Strips */}
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))',
          gap: '1rem',
          marginBottom: '1.25rem',
        }}
      >
        {/* Inventory Valuation */}
        <div
          className="erp-card"
          style={{ cursor: 'pointer' }}
          onClick={() => navigate('/inventory')}
        >
          <div className="erp-card-body" style={{ padding: '0.85rem 1rem' }}>
            <div style={{ color: 'var(--text-muted)', fontSize: '0.75rem', fontWeight: 600 }}>
              INVENTORY VALUATION
            </div>
            <div style={{ fontSize: '1.35rem', fontWeight: 700, color: 'var(--text-main)', marginTop: '0.2rem' }}>
              ₹{(metrics?.inventory_valuation || 0).toLocaleString('en-IN', { minimumFractionDigits: 2 })}
            </div>
            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginTop: '0.2rem' }}>
              Total stock on hand at cost price
            </div>
          </div>
        </div>

        {/* Low Stock Alerts */}
        <div
          className="erp-card"
          style={{ cursor: 'pointer', backgroundColor: (metrics?.low_stock_count || 0) > 0 ? '#fffbeb' : '#fff' }}
          onClick={() => navigate('/inventory')}
        >
          <div className="erp-card-body" style={{ padding: '0.85rem 1rem' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', color: '#b45309', fontSize: '0.75rem', fontWeight: 600 }}>
              <IconAlert size={14} />
              <span>LOW STOCK ALERT</span>
            </div>
            <div style={{ fontSize: '1.35rem', fontWeight: 700, color: '#b45309', marginTop: '0.2rem' }}>
              {metrics?.low_stock_count || 0} Products
            </div>
            <div style={{ fontSize: '0.75rem', color: '#92400e', marginTop: '0.2rem' }}>
              Items below minimum reorder point
            </div>
          </div>
        </div>

        {/* Expiry Alerts */}
        <div
          className="erp-card"
          style={{ cursor: 'pointer', backgroundColor: (metrics?.expiring_count || 0) > 0 ? '#fef2f2' : '#fff' }}
          onClick={() => navigate('/expiry')}
        >
          <div className="erp-card-body" style={{ padding: '0.85rem 1rem' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', color: '#dc2626', fontSize: '0.75rem', fontWeight: 600 }}>
              <IconAlert size={14} />
              <span>EXPIRING BATCHES</span>
            </div>
            <div style={{ fontSize: '1.35rem', fontWeight: 700, color: '#dc2626', marginTop: '0.2rem' }}>
              {metrics?.expiring_count || 0} Batches
            </div>
            <div style={{ fontSize: '0.75rem', color: '#991b1b', marginTop: '0.2rem' }}>
              Expiring within next 30 days
            </div>
          </div>
        </div>

        {/* Pending POs */}
        <div
          className="erp-card"
          style={{ cursor: 'pointer' }}
          onClick={() => navigate('/purchases')}
        >
          <div className="erp-card-body" style={{ padding: '0.85rem 1rem' }}>
            <div style={{ color: 'var(--text-muted)', fontSize: '0.75rem', fontWeight: 600 }}>
              PENDING PURCHASE ORDERS
            </div>
            <div style={{ fontSize: '1.35rem', fontWeight: 700, color: 'var(--text-main)', marginTop: '0.2rem' }}>
              {metrics?.pending_po_count || 0} Orders
            </div>
            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginTop: '0.2rem' }}>
              Awaiting supplier goods receipt
            </div>
          </div>
        </div>
      </div>

      {/* Two Column Layout: Payment Modes Breakdown & Top Selling Products */}
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fit, minmax(400px, 1fr))',
          gap: '1.25rem',
          marginBottom: '1.25rem',
        }}
      >
        {/* Payment Breakup */}
        <div className="erp-card">
          <div className="erp-card-header">
            <h3 style={{ margin: 0, fontSize: '0.95rem', fontWeight: 600 }}>Payment Method Breakdown</h3>
            <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>Registered sales tender</span>
          </div>
          <div className="erp-card-body">
            {paymentBreakup.length === 0 ? (
              <div style={{ textAlign: 'center', padding: '1.5rem', color: 'var(--text-muted)' }}>
                No payment data for this period
              </div>
            ) : (
              <table className="erp-table">
                <thead>
                  <tr>
                    <th>Payment Mode</th>
                    <th className="text-center">Bills</th>
                    <th className="text-right">Total Amount</th>
                    <th className="text-right">Share</th>
                  </tr>
                </thead>
                <tbody>
                  {paymentBreakup.map((p) => {
                    const totalSales = metrics?.revenue || 1;
                    const share = ((p.total / totalSales) * 100).toFixed(1);
                    return (
                      <tr key={p.mode}>
                        <td style={{ textTransform: 'capitalize', fontWeight: 600 }}>{p.mode}</td>
                        <td className="text-center">{p.count}</td>
                        <td className="text-right text-mono font-bold">
                          ₹{p.total.toLocaleString('en-IN', { minimumFractionDigits: 2 })}
                        </td>
                        <td className="text-right text-mono">{share}%</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            )}
          </div>
        </div>

        {/* Top Products */}
        <div className="erp-card">
          <div className="erp-card-header">
            <h3 style={{ margin: 0, fontSize: '0.95rem', fontWeight: 600 }}>Top Selling Products</h3>
            <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>Highest sales volume</span>
          </div>
          <div className="erp-card-body">
            {topProducts.length === 0 ? (
              <div style={{ textAlign: 'center', padding: '1.5rem', color: 'var(--text-muted)' }}>
                No sales recorded for this period
              </div>
            ) : (
              <table className="erp-table">
                <thead>
                  <tr>
                    <th>Product</th>
                    <th className="text-center">Units Sold</th>
                    <th className="text-right">Revenue</th>
                  </tr>
                </thead>
                <tbody>
                  {topProducts.map((p, idx) => (
                    <tr key={p.product + idx}>
                      <td style={{ fontWeight: 500 }}>{p.product}</td>
                      <td className="text-center">{p.quantity}</td>
                      <td className="text-right text-mono font-bold">
                        ₹{p.sales.toLocaleString('en-IN', { minimumFractionDigits: 2 })}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>
      </div>

      {/* Low Stock Watchlist */}
      {lowStockItems.length > 0 && (
        <div className="erp-card">
          <div className="erp-card-header" style={{ backgroundColor: '#fffbeb' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
              <IconAlert size={18} color="#b45309" />
              <h3 style={{ margin: 0, fontSize: '0.95rem', fontWeight: 600, color: '#92400e' }}>
                Low Stock Watchlist ({lowStockItems.length} items requiring replenishment)
              </h3>
            </div>
            <button
              type="button"
              className="btn btn-outline btn-sm"
              onClick={() => navigate('/purchases')}
            >
              <IconPurchases size={14} />
              <span>Create Purchase Order</span>
            </button>
          </div>
          <div className="erp-card-body" style={{ padding: 0 }}>
            <table className="erp-table">
              <thead>
                <tr>
                  <th>Product Name</th>
                  <th>Barcode</th>
                  <th className="text-center">Current Stock</th>
                  <th className="text-center">Min Threshold</th>
                  <th className="text-center">Status</th>
                  <th className="text-right">Action</th>
                </tr>
              </thead>
              <tbody>
                {lowStockItems.slice(0, 8).map((item) => (
                  <tr key={item.id}>
                    <td style={{ fontWeight: 600 }}>{item.name}</td>
                    <td className="text-mono" style={{ fontSize: '0.8rem' }}>{item.barcode}</td>
                    <td className="text-center text-mono font-bold" style={{ color: '#dc2626' }}>
                      {item.stock ?? 0}
                    </td>
                    <td className="text-center text-mono">{item.min_stock}</td>
                    <td className="text-center">
                      <span className="badge badge-danger">Reorder Required</span>
                    </td>
                    <td className="text-right">
                      <button
                        type="button"
                        className="btn btn-outline btn-sm"
                        onClick={() => navigate('/purchases')}
                      >
                        Order
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </AppLayout>
  );
}
