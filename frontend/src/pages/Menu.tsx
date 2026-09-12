import React, { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { menuT } from '../i18n/menu';
import { subscribe, TillState, tillState } from '../services/offline/service';

type MenuItem = {
  key: string;
  labelKey: string;
  icon: string;
  path: string;
  /** The permission this screen's own API calls need. */
  permission: string;
};

export default function Menu() {
  const navigate = useNavigate();
  const { user, store, language, permissions } = useAppStore();

  // The till's own state, not a number read once on mount. The sync loop lives
  // in the app and keeps running while this screen is open, so the count has to
  // come from it rather than from a snapshot taken at render time.
  const [till, setTill] = useState<TillState>(tillState());
  useEffect(() => subscribe(setTill), []);

  if (!user) {
    navigate('/login');
    return null;
  }

  const lang = language || 'en';

  // Grouped, because twenty-five tiles in one grid is a list to search rather
  // than a place to work. The groups are the jobs somebody does in a shop, not
  // the shape of the API: a cashier lives in the first one all day and never
  // opens the last.
  //
  // Each screen names the permission its own API calls need, so this list and
  // the server cannot disagree. It used to name roles, duplicating the matrix,
  // and the copies had drifted in both directions: a warehouse hand who may
  // write off damage had no entry for it, and a helper whose job is reading the
  // reports could not reach them.
  //
  // Hiding an entry is a courtesy. The API refuses on its own account, so a
  // stale list here cannot grant anything.
  const groups: { key: string; title: string; blurb: string; items: MenuItem[] }[] = [
    {
      key: 'counter',
      title: 'At the counter',
      blurb: 'Serving customers',
      items: [
        { key: 'billing', labelKey: 'billing', icon: '💰', path: '/billing', permission: 'invoice.create' },
        { key: 'invoices', labelKey: 'invoices', icon: '🧾', path: '/invoices', permission: 'invoice.read' },
        { key: 'returns', labelKey: 'returns', icon: '↩️', path: '/returns', permission: 'return.read' },
        { key: 'customers', labelKey: 'customers', icon: '👥', path: '/customers', permission: 'catalog.read' },
        { key: 'settlement', labelKey: 'settlement', icon: '📝', path: '/settlement', permission: 'payment.read' },
      ],
    },
    {
      key: 'catalogue',
      title: 'What the shop sells',
      blurb: 'Products, packs and prices',
      items: [
        { key: 'products', labelKey: 'products', icon: '🏷️', path: '/products', permission: 'catalog.read' },
        { key: 'categories', labelKey: 'categories', icon: '🗂️', path: '/categories', permission: 'catalog.read' },
        { key: 'product-import', labelKey: 'productImport', icon: '📤', path: '/products/import', permission: 'product.catalog_write' },
        { key: 'price-changes', labelKey: 'priceChanges', icon: '🏷️', path: '/price-changes', permission: 'product.price' },
        { key: 'units', labelKey: 'units', icon: '⚖️', path: '/units', permission: 'uom.write' },
      ],
    },
    {
      key: 'stock',
      title: 'Stock',
      blurb: 'What is on the shelf',
      items: [
        { key: 'inventory', labelKey: 'inventory', icon: '📦', path: '/inventory', permission: 'catalog.read' },
        { key: 'stock-transfer', labelKey: 'stockTransfer', icon: '🔄', path: '/stock-transfer', permission: 'stock.move' },
        { key: 'expiry', labelKey: 'expiry', icon: '⏰', path: '/expiry', permission: 'expiry.read' },
        { key: 'losses', labelKey: 'losses', icon: '📉', path: '/losses', permission: 'loss.read' },
      ],
    },
    {
      key: 'buying',
      title: 'Buying',
      blurb: 'Vendors and deliveries',
      items: [
        { key: 'purchases', labelKey: 'purchases', icon: '🛒', path: '/purchases', permission: 'purchase.read' },
        { key: 'grn', labelKey: 'grn', icon: '📥', path: '/grn', permission: 'grn.read' },
        { key: 'vendors', labelKey: 'vendors', icon: '🏭', path: '/vendors', permission: 'catalog.read' },
        { key: 'purchase-returns', labelKey: 'purchaseReturns', icon: '↩️', path: '/purchase-returns', permission: 'purchase.return' },
        { key: 'vendor-payments', labelKey: 'vendorPayments', icon: '💳', path: '/vendor-payments', permission: 'vendor.payment' },
      ],
    },
    {
      key: 'money',
      title: 'Money owed and spent',
      blurb: 'Credit and expenses',
      items: [
        { key: 'credit', labelKey: 'credit', icon: '💳', path: '/credit', permission: 'credit.view' },
        { key: 'credit-aging', labelKey: 'creditAging', icon: '📅', path: '/credit-aging', permission: 'report.view' },
        { key: 'expenses', labelKey: 'expenses', icon: '💸', path: '/expenses', permission: 'expense.read' },
      ],
    },
    {
      key: 'reports',
      title: 'How the shop is doing',
      blurb: 'Figures and returns',
      items: [
        { key: 'dashboard', labelKey: 'dashboard', icon: '📊', path: '/dashboard', permission: 'analytics.view' },
        { key: 'reports', labelKey: 'reports', icon: '📈', path: '/reports', permission: 'analytics.view' },
        { key: 'profit-loss', labelKey: 'profitLoss', icon: '💵', path: '/profit-loss', permission: 'report.view' },
        { key: 'gstr1', labelKey: 'gstr1', icon: '📋', path: '/gstr1', permission: 'report.view' },
        { key: 'multi-store', labelKey: 'multiStore', icon: '🏪', path: '/multi-store', permission: 'analytics.view' },
      ],
    },
    {
      key: 'admin',
      title: 'Setting up',
      blurb: 'Shops, counters and people',
      items: [
        { key: 'settings', labelKey: 'settings', icon: '⚙️', path: '/settings', permission: 'user.register' },
        { key: 'audit', labelKey: 'audit', icon: '🔍', path: '/audit', permission: 'audit.view' },
      ],
    },
  ];

  // A session that predates the permission list has none recorded. Falling back
  // to showing everything is the safe choice: the entries still 403 if the role
  // cannot use them, and hiding the whole menu from somebody who signed in
  // before a deploy would look like the app had broken.
  const visibleGroups = groups
    .map((group) => ({
      ...group,
      items:
        permissions.length === 0
          ? group.items
          : group.items.filter((item) => permissions.includes(item.permission)),
    }))
    // A group a role cannot reach at all is not shown as an empty heading.
    .filter((group) => group.items.length > 0);

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

      {visibleGroups.map((group) => (
        <section key={group.key} style={styles.group}>
          <div style={styles.groupHead}>
            <h2 style={styles.groupTitle}>{group.title}</h2>
            <span style={styles.groupBlurb}>{group.blurb}</span>
          </div>
          <div style={styles.grid}>
            {group.items.map((item) => (
              <div key={item.key} style={styles.card} onClick={() => navigate(item.path)}>
                <span style={styles.icon}>{item.icon}</span>
                <span style={styles.label}>{menuT(lang, item.labelKey)}</span>
              </div>
            ))}
          </div>
        </section>
      ))}

      <div style={styles.footer}>
        <p>{menuT(lang, 'tagline')}</p>
        <p
          style={{
            ...styles.offlineStatus,
            color: till.rejected > 0 ? '#dc2626' : till.online ? '#4caf50' : '#ff9800',
          }}
        >
          {till.online ? '● ' + menuT(lang, 'online') : '● ' + menuT(lang, 'offline')}
          {till.queued > 0 ? ` · ${till.queued} ${menuT(lang, 'pendingSync')}` : ''}
          {/* A refused bill is not a queue length, it is a job. It says so in
              words rather than adding to a count somebody reads as "busy". */}
          {till.rejected > 0
            ? ` · ${till.rejected} bill${till.rejected === 1 ? '' : 's'} the server refused`
            : ''}
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
  // A group is a band, not a column: the page scrolls through the jobs in the
  // order somebody does them.
  group: {
    padding: '0 1rem',
  },
  groupHead: {
    display: 'flex',
    alignItems: 'baseline',
    gap: '0.5rem',
    padding: '1rem 0 0.25rem',
    borderBottom: '1px solid #e5e7eb',
  },
  groupTitle: {
    margin: 0,
    fontSize: '0.9375rem',
    fontWeight: 700,
    letterSpacing: '0.02em',
    color: '#111827',
  },
  groupBlurb: {
    fontSize: '0.8125rem',
    color: '#6b7280',
  },
  grid: {
    display: 'grid',
    // Plain minmax, not minmax(min(x, 100%), …): nesting min() inside minmax()
    // inside repeat() is rejected here, and React then drops the whole
    // declaration and collapses the grid to one column.
    gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))',
    gap: '0.75rem',
    padding: '0.75rem 0 0.25rem',
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
