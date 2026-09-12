import React, { useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { menuT } from '../i18n/menu';
import { getPendingInvoiceCount } from '../services/offlineInvoiceQueue';

export default function Menu() {
  const navigate = useNavigate();
  const { user, store, language, isOnline, syncPending, setSyncPending, permissions } = useAppStore();

  useEffect(() => {
    setSyncPending(getPendingInvoiceCount());
  }, [setSyncPending]);

  if (!user) {
    navigate('/login');
    return null;
  }

  const lang = language || 'en';

  // Each screen names the permission its own API calls need, so this list and
  // the server cannot disagree. It used to name roles instead, duplicating the
  // matrix, and the copies had drifted in both directions: a warehouse hand who
  // may write off damage had no entry for it, and a helper whose job is reading
  // the reports could not reach them.
  //
  // Hiding an entry is a courtesy. The API refuses on its own account, so a
  // stale list here cannot grant anything.
  const menuItems = [
    { key: 'billing', labelKey: 'billing', icon: '💰', path: '/billing', permission: 'invoice.create' },
    { key: 'products', labelKey: 'products', icon: '🏷️', path: '/products', permission: 'catalog.read' },
    { key: 'inventory', labelKey: 'inventory', icon: '📦', path: '/inventory', permission: 'catalog.read' },
    { key: 'expiry', labelKey: 'expiry', icon: '⏰', path: '/expiry', permission: 'expiry.read' },
    { key: 'purchases', labelKey: 'purchases', icon: '🛒', path: '/purchases', permission: 'purchase.read' },
    { key: 'vendors', labelKey: 'vendors', icon: '🏭', path: '/vendors', permission: 'catalog.read' },
    { key: 'customers', labelKey: 'customers', icon: '👥', path: '/customers', permission: 'catalog.read' },
    { key: 'credit', labelKey: 'credit', icon: '💳', path: '/credit', permission: 'credit.view' },
    { key: 'settlement', labelKey: 'settlement', icon: '📝', path: '/settlement', permission: 'payment.read' },
    { key: 'returns', labelKey: 'returns', icon: '↩️', path: '/returns', permission: 'return.read' },
    { key: 'losses', labelKey: 'losses', icon: '📉', path: '/losses', permission: 'loss.read' },
    { key: 'expenses', labelKey: 'expenses', icon: '💸', path: '/expenses', permission: 'expense.read' },
    { key: 'dashboard', labelKey: 'dashboard', icon: '📊', path: '/dashboard', permission: 'analytics.view' },
    { key: 'reports', labelKey: 'reports', icon: '📈', path: '/reports', permission: 'analytics.view' },
    { key: 'profit-loss', labelKey: 'profitLoss', icon: '💵', path: '/profit-loss', permission: 'report.view' },
    { key: 'credit-aging', labelKey: 'creditAging', icon: '📅', path: '/credit-aging', permission: 'report.view' },
    { key: 'gstr1', labelKey: 'gstr1', icon: '📋', path: '/gstr1', permission: 'report.view' },
    { key: 'invoices', labelKey: 'invoices', icon: '🧾', path: '/invoices', permission: 'invoice.read' },
    { key: 'stock-transfer', labelKey: 'stockTransfer', icon: '🔄', path: '/stock-transfer', permission: 'stock.move' },
    { key: 'grn', labelKey: 'grn', icon: '📥', path: '/grn', permission: 'grn.read' },
    { key: 'vendor-payments', labelKey: 'vendorPayments', icon: '💳', path: '/vendor-payments', permission: 'vendor.payment' },
    { key: 'purchase-returns', labelKey: 'purchaseReturns', icon: '↩️', path: '/purchase-returns', permission: 'purchase.return' },
    { key: 'multi-store', labelKey: 'multiStore', icon: '🏪', path: '/multi-store', permission: 'analytics.view' },
    { key: 'audit', labelKey: 'audit', icon: '🔍', path: '/audit', permission: 'audit.view' },
    { key: 'settings', labelKey: 'settings', icon: '⚙️', path: '/settings', permission: 'user.register' },
  ];

  // A session that predates this field has no permissions recorded. Falling back
  // to showing everything is the safe choice here: the entries still 403 if the
  // role cannot use them, and hiding the whole menu from somebody who signed in
  // before a deploy would look like the app had broken.
  const visibleItems =
    permissions.length === 0 ? menuItems : menuItems.filter((item) => permissions.includes(item.permission));

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <div>
          <h1 style={styles.title}>{menuT(lang, 'title')}</h1>
          <p style={styles.storeName}>{store?.name || 'Store'}</p>
        </div>
        <div style={styles.userInfo}>
          <span>{user.username}</span>
          <span style={styles.role}>{user.role}</span>
          <button
            onClick={() => {
              localStorage.clear();
              navigate('/login');
            }}
            style={styles.logoutBtn}
          >
            Logout
          </button>
        </div>
      </div>

      <div style={styles.grid}>
        {visibleItems.map((item) => (
          <div key={item.key} style={styles.card} onClick={() => navigate(item.path)}>
            <span style={styles.icon}>{item.icon}</span>
            <span style={styles.label}>{menuT(lang, item.labelKey)}</span>
          </div>
        ))}
      </div>

      <div style={styles.footer}>
        <p>{menuT(lang, 'tagline')}</p>
        <p style={{ ...styles.offlineStatus, color: isOnline ? '#4caf50' : '#ff9800' }}>
          {isOnline ? '● ' + menuT(lang, 'online') : '● ' + menuT(lang, 'offline')}
          {syncPending > 0 ? ` · ${syncPending} ${menuT(lang, 'pendingSync')}` : ''}
        </p>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    minHeight: '100vh',
    background: '#f5f5f5',
    display: 'flex',
    flexDirection: 'column',
  },
  header: {
    background: '#1976d2',
    color: 'white',
    padding: '1rem',
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  title: { margin: 0, fontSize: '1.5rem' },
  storeName: { margin: 0, opacity: 0.8 },
  userInfo: { display: 'flex', alignItems: 'center', gap: '0.5rem' },
  role: {
    background: 'rgba(255,255,255,0.2)',
    padding: '0.25rem 0.5rem',
    borderRadius: '4px',
    fontSize: '0.75rem',
    textTransform: 'uppercase',
  },
  logoutBtn: {
    padding: '0.5rem 1rem',
    background: 'rgba(255,255,255,0.2)',
    color: 'white',
    border: 'none',
    borderRadius: '4px',
    cursor: 'pointer',
  },
  grid: {
    flex: 1,
    display: 'grid',
    gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
    gap: '1rem',
    padding: '1rem',
  },
  card: {
    background: 'white',
    padding: '1.5rem',
    borderRadius: '8px',
    boxShadow: '0 2px 4px rgba(0,0,0,0.1)',
    cursor: 'pointer',
    textAlign: 'center',
    transition: 'transform 0.2s',
  },
  icon: { fontSize: '2rem', display: 'block', marginBottom: '0.5rem' },
  label: { fontSize: '0.875rem', fontWeight: '500' },
  footer: { padding: '1rem', textAlign: 'center', color: '#666', fontSize: '0.875rem' },
  offlineStatus: { marginTop: '0.5rem' },
};
