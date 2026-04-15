import React, { useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { menuT } from '../i18n/menu';
import { getPendingInvoiceCount } from '../services/offlineInvoiceQueue';

export default function Menu() {
  const navigate = useNavigate();
  const { user, store, language, isOnline, syncPending, setSyncPending } = useAppStore();

  useEffect(() => {
    setSyncPending(getPendingInvoiceCount());
  }, [setSyncPending]);

  if (!user) {
    navigate('/login');
    return null;
  }

  const role = user.role;
  const lang = language || 'en';

  const menuItems = [
    { key: 'billing', labelKey: 'billing', icon: '💰', path: '/billing', roles: ['owner', 'manager', 'cashier'] },
    { key: 'products', labelKey: 'products', icon: '🏷️', path: '/products', roles: ['owner', 'manager'] },
    { key: 'inventory', labelKey: 'inventory', icon: '📦', path: '/inventory', roles: ['owner', 'manager', 'helper', 'warehouse'] },
    { key: 'expiry', labelKey: 'expiry', icon: '⏰', path: '/expiry', roles: ['owner', 'manager', 'warehouse'] },
    { key: 'purchases', labelKey: 'purchases', icon: '🛒', path: '/purchases', roles: ['owner', 'manager'] },
    { key: 'vendors', labelKey: 'vendors', icon: '🏭', path: '/vendors', roles: ['owner', 'manager'] },
    { key: 'customers', labelKey: 'customers', icon: '👥', path: '/customers', roles: ['owner', 'manager', 'cashier'] },
    { key: 'credit', labelKey: 'credit', icon: '💳', path: '/credit', roles: ['owner', 'manager'] },
    { key: 'settlement', labelKey: 'settlement', icon: '📝', path: '/settlement', roles: ['owner', 'manager'] },
    { key: 'returns', labelKey: 'returns', icon: '↩️', path: '/returns', roles: ['owner', 'manager'] },
    { key: 'losses', labelKey: 'losses', icon: '📉', path: '/losses', roles: ['owner', 'manager'] },
    { key: 'expenses', labelKey: 'expenses', icon: '💸', path: '/expenses', roles: ['owner', 'manager'] },
    { key: 'dashboard', labelKey: 'dashboard', icon: '📊', path: '/dashboard', roles: ['owner', 'manager'] },
    { key: 'reports', labelKey: 'reports', icon: '📈', path: '/reports', roles: ['owner', 'manager'] },
    { key: 'profit-loss', labelKey: 'profitLoss', icon: '💵', path: '/profit-loss', roles: ['owner', 'manager'] },
    { key: 'credit-aging', labelKey: 'creditAging', icon: '📅', path: '/credit-aging', roles: ['owner', 'manager'] },
    { key: 'gstr1', labelKey: 'gstr1', icon: '📋', path: '/gstr1', roles: ['owner', 'manager'] },
    { key: 'invoices', labelKey: 'invoices', icon: '🧾', path: '/invoices', roles: ['owner', 'manager', 'cashier'] },
    { key: 'stock-transfer', labelKey: 'stockTransfer', icon: '🔄', path: '/stock-transfer', roles: ['owner', 'manager', 'warehouse'] },
    { key: 'grn', labelKey: 'grn', icon: '📥', path: '/grn', roles: ['owner', 'manager', 'warehouse'] },
    { key: 'vendor-payments', labelKey: 'vendorPayments', icon: '💳', path: '/vendor-payments', roles: ['owner', 'manager'] },
    { key: 'purchase-returns', labelKey: 'purchaseReturns', icon: '↩️', path: '/purchase-returns', roles: ['owner', 'manager'] },
    { key: 'multi-store', labelKey: 'multiStore', icon: '🏪', path: '/multi-store', roles: ['owner'] },
    { key: 'audit', labelKey: 'audit', icon: '🔍', path: '/audit', roles: ['owner', 'manager'] },
    { key: 'settings', labelKey: 'settings', icon: '⚙️', path: '/settings', roles: ['owner'] },
  ];

  const visibleItems = menuItems.filter((item) => item.roles.includes(role));

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
