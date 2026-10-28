import React, { useState, useEffect } from 'react';
import { useNavigate, useLocation } from 'react-router-dom';
import { useAppStore } from '../../stores/appStore';
import { storeApi, authApi } from '../../services/api';
import { Store } from '../../types';
import {
  IconDashboard,
  IconPOS,
  IconProducts,
  IconInventory,
  IconWarehouse,
  IconPurchases,
  IconCustomers,
  IconVendors,
  IconCredit,
  IconReturns,
  IconExpenses,
  IconReports,
  IconSettings,
  IconBarcode,
  IconLogout,
  IconStore,
  IconMenu,
  IconClose,
  IconAlert,
  IconCheck,
} from '../Icons';
import { BarcodeScannerModal } from '../BarcodeScannerModal';

interface NavItem {
  id: string;
  section: string;
  label: string;
  path: string;
  icon: React.ReactNode;
  roles: string[];
  badge?: string | number;
}

interface AppLayoutProps {
  children: React.ReactNode;
  fullWidth?: boolean;
}

export function AppLayout({ children, fullWidth = false }: AppLayoutProps) {
  const navigate = useNavigate();
  const location = useLocation();
  const { user, store, setStore, isOnline, syncPending } = useAppStore();

  const [stores, setStores] = useState<Store[]>([]);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [scannerOpen, setScannerOpen] = useState(false);
  const [shortcutsOpen, setShortcutsOpen] = useState(false);

  useEffect(() => {
    const token = localStorage.getItem('token');
    if (!user && !token) {
      navigate('/login');
      return;
    }
    // Load available stores & warehouses
    storeApi
      .list()
      .then((res) => {
        const list = (res.data as Store[]) || [];
        setStores(list);
        if (!store && list.length > 0) {
          setStore(list[0]);
          localStorage.setItem('storeId', list[0].id);
        }
      })
      .catch((err) => console.warn('Could not load stores:', err));
  }, [user]);

  const role = user?.role || 'cashier';

  // Professional Organized Navigation Sections
  const navItems: NavItem[] = [
    // Operations
    {
      id: 'dashboard',
      section: 'Operations',
      label: 'Dashboard',
      path: '/dashboard',
      icon: <IconDashboard size={17} />,
      roles: ['owner', 'manager'],
    },
    {
      id: 'billing',
      section: 'Operations',
      label: 'Point of Sale (POS)',
      path: '/billing',
      icon: <IconPOS size={17} />,
      roles: ['owner', 'manager', 'cashier'],
    },
    {
      id: 'invoices',
      section: 'Operations',
      label: 'Sales Invoices',
      path: '/invoices',
      icon: <IconPOS size={17} />,
      roles: ['owner', 'manager', 'cashier'],
    },
    {
      id: 'products',
      section: 'Operations',
      label: 'Products & Brands',
      path: '/products',
      icon: <IconProducts size={17} />,
      roles: ['owner', 'manager'],
    },
    {
      id: 'categories',
      section: 'Operations',
      label: 'Categories',
      path: '/categories',
      icon: <IconProducts size={17} />,
      roles: ['owner', 'manager'],
    },
    {
      id: 'units',
      section: 'Operations',
      label: 'Units of Measure',
      path: '/units',
      icon: <IconWarehouse size={17} />,
      roles: ['owner', 'manager'],
    },
    {
      id: 'price-changes',
      section: 'Operations',
      label: 'Price Changes',
      path: '/price-changes',
      icon: <IconDashboard size={17} />,
      roles: ['owner', 'manager'],
    },
    // Inventory
    {
      id: 'inventory',
      section: 'Inventory & Stock',
      label: 'Inventory & Batches',
      path: '/inventory',
      icon: <IconInventory size={17} />,
      roles: ['owner', 'manager', 'warehouse', 'helper'],
    },
    {
      id: 'reorder',
      section: 'Inventory & Stock',
      label: 'Low Stock Reorder',
      path: '/reorder',
      icon: <IconAlert size={17} />,
      roles: ['owner', 'manager', 'warehouse'],
    },
    {
      id: 'expiry',
      section: 'Inventory & Stock',
      label: 'Expiry Tracker',
      path: '/expiry',
      icon: <IconAlert size={17} />,
      roles: ['owner', 'manager', 'warehouse'],
    },
    {
      id: 'transfers',
      section: 'Inventory & Stock',
      label: 'Warehouse Transfers',
      path: '/stock-transfer',
      icon: <IconWarehouse size={17} />,
      roles: ['owner', 'manager', 'warehouse'],
    },
    {
      id: 'grn',
      section: 'Inventory & Stock',
      label: 'Goods Receiving (GRN)',
      path: '/grn',
      icon: <IconPurchases size={17} />,
      roles: ['owner', 'manager', 'warehouse'],
    },
    // Commercial & Khata
    {
      id: 'customers',
      section: 'Commercial & Khata',
      label: 'Customers & Dues',
      path: '/customers',
      icon: <IconCustomers size={17} />,
      roles: ['owner', 'manager', 'cashier'],
    },
    {
      id: 'credit',
      section: 'Commercial & Khata',
      label: 'Khata Credit Ledger',
      path: '/credit',
      icon: <IconCredit size={17} />,
      roles: ['owner', 'manager'],
    },
    {
      id: 'vendors',
      section: 'Commercial & Khata',
      label: 'Vendors & Payables',
      path: '/vendors',
      icon: <IconVendors size={17} />,
      roles: ['owner', 'manager'],
    },
    {
      id: 'purchases',
      section: 'Commercial & Khata',
      label: 'Purchase Orders (PO)',
      path: '/purchases',
      icon: <IconPurchases size={17} />,
      roles: ['owner', 'manager'],
    },
    {
      id: 'vendor-payments',
      section: 'Commercial & Khata',
      label: 'Vendor Payments',
      path: '/vendor-payments',
      icon: <IconVendors size={17} />,
      roles: ['owner', 'manager'],
    },
    // Financials
    {
      id: 'expenses',
      section: 'Financials & Audit',
      label: 'Operating Expenses',
      path: '/expenses',
      icon: <IconExpenses size={17} />,
      roles: ['owner', 'manager'],
    },
    {
      id: 'settlement',
      section: 'Financials & Audit',
      label: 'Daily Settlement',
      path: '/settlement',
      icon: <IconPOS size={17} />,
      roles: ['owner', 'manager', 'cashier'],
    },
    {
      id: 'profit-loss',
      section: 'Financials & Audit',
      label: 'Profit & Loss',
      path: '/profit-loss',
      icon: <IconReports size={17} />,
      roles: ['owner', 'manager'],
    },
    {
      id: 'reports',
      section: 'Financials & Audit',
      label: 'Financial Reports',
      path: '/reports',
      icon: <IconReports size={17} />,
      roles: ['owner', 'manager'],
    },
    {
      id: 'gstr1',
      section: 'Financials & Audit',
      label: 'GST Returns (GSTR-1)',
      path: '/gstr1',
      icon: <IconReports size={17} />,
      roles: ['owner', 'manager'],
    },
    {
      id: 'returns',
      section: 'Financials & Audit',
      label: 'Returns & Losses',
      path: '/returns',
      icon: <IconReturns size={17} />,
      roles: ['owner', 'manager'],
    },
    {
      id: 'audit',
      section: 'Financials & Audit',
      label: 'Audit Trail',
      path: '/audit',
      icon: <IconSettings size={17} />,
      roles: ['owner'],
    },
    // Admin
    {
      id: 'settings',
      section: 'Administration',
      label: 'Users & Roles',
      path: '/settings',
      icon: <IconSettings size={17} />,
      roles: ['owner'],
    },
    {
      id: 'multistore',
      section: 'Administration',
      label: 'Multi-Store Dashboard',
      path: '/multi-store',
      icon: <IconStore size={17} />,
      roles: ['owner'],
    },
  ];

  const visibleNav = navItems.filter((item) => item.roles.includes(role));

  const handleStoreChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const selected = stores.find((s) => s.id === e.target.value);
    if (selected) {
      setStore(selected);
      localStorage.setItem('storeId', selected.id);
      window.location.reload();
    }
  };

  const handleLogout = () => {
    try {
      authApi.logout().catch(() => {});
    } catch {}
    localStorage.removeItem('token');
    localStorage.removeItem('storeId');
    localStorage.clear();
    useAppStore.getState().setUser(null);
    useAppStore.getState().setStore(null);
    useAppStore.getState().setTerminal(null);
    useAppStore.getState().setPermissions([]);
    window.location.href = '/login';
  };

  // Breadcrumb generator
  const getBreadcrumbs = () => {
    const current = navItems.find((item) => item.path === location.pathname);
    if (current) {
      return [{ label: 'GBOS ERP', path: '/' }, { label: current.label.replace(/^\d+\.\s*/, ''), path: current.path }];
    }
    // Sub-screens
    const subScreens: Record<string, string> = {
      '/grn': 'Goods Receipt Notes (GRN)',
      '/vendor-payments': 'Vendor Payments & Settlement',
      '/purchase-returns': 'Purchase Debit Notes / Returns',
      '/credit-aging': 'Credit Aging Analysis',
      '/profit-loss': 'Profit & Loss Statement',
      '/gstr1': 'GSTR-1 Tax Return Filing',
      '/invoices': 'Invoices & Sales Archive',
      '/losses': 'Inventory Losses & Shrinkage',
      '/expiry': 'Expiring Stock Batches',
      '/audit': 'System Audit Logs',
    };
    if (subScreens[location.pathname]) {
      return [
        { label: 'GBOS ERP', path: '/' },
        { label: subScreens[location.pathname], path: location.pathname },
      ];
    }
    return [{ label: 'GBOS ERP', path: '/' }];
  };

  return (
    <div style={{ display: 'flex', minHeight: '100vh', background: 'var(--bg-app)' }}>
      {/* ===================================================================
          Sidebar Navigation (Desktop fixed, Mobile off-canvas)
          =================================================================== */}
      <aside
        style={{
          width: 'var(--sidebar-width)',
          backgroundColor: '#0f172a', // Deep corporate slate
          color: '#cbd5e1',
          display: 'flex',
          flexDirection: 'column',
          position: 'fixed',
          top: 0,
          bottom: 0,
          left: 0,
          zIndex: 100,
          transition: 'transform 0.2s ease-in-out',
          transform: window.innerWidth < 1024 && !sidebarOpen ? 'translateX(-100%)' : 'translateX(0)',
          borderRight: '1px solid #1e293b',
        }}
      >
        {/* Brand Header */}
        <div
          style={{
            height: 'var(--header-height)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            padding: '0 1rem',
            borderBottom: '1px solid #1e293b',
            background: '#090d16',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.65rem' }}>
            <div
              style={{
                width: 28,
                height: 28,
                borderRadius: 'var(--radius-sm)',
                backgroundColor: 'var(--primary-600)',
                color: '#fff',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontWeight: 700,
                fontSize: '0.85rem',
              }}
            >
              G
            </div>
            <div>
              <div style={{ fontWeight: 700, fontSize: '0.95rem', color: '#f8fafc', lineHeight: 1.1 }}>
                GBOS ERP
              </div>
              <div style={{ fontSize: '0.7rem', color: '#94a3b8' }}>Enterprise v2.4</div>
            </div>
          </div>
          {window.innerWidth < 1024 && (
            <button
              type="button"
              onClick={() => setSidebarOpen(false)}
              style={{ background: 'none', border: 'none', color: '#cbd5e1', cursor: 'pointer' }}
            >
              <IconClose size={20} />
            </button>
          )}
        </div>

        {/* Location / Warehouse Selector in Sidebar */}
        <div style={{ padding: '0.75rem 1rem', borderBottom: '1px solid #1e293b', background: '#111827' }}>
          <label style={{ display: 'block', fontSize: '0.7rem', color: '#94a3b8', marginBottom: '0.25rem', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
            Current Location
          </label>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
            <IconStore size={16} color="#60a5fa" />
            <select
              value={store?.id || ''}
              onChange={handleStoreChange}
              style={{
                flex: 1,
                background: '#1e293b',
                color: '#f8fafc',
                border: '1px solid #334155',
                borderRadius: 'var(--radius-sm)',
                padding: '0.35rem 0.5rem',
                fontSize: '0.8rem',
                outline: 'none',
                cursor: 'pointer',
              }}
            >
              {stores.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name} {s.location_type === 'warehouse' ? '(Warehouse)' : '(Store)'}
                </option>
              ))}
            </select>
          </div>
        </div>

        {/* Navigation Items Grouped by Sections */}
        <nav style={{ flex: 1, overflowY: 'auto', padding: '0.5rem 0' }}>
          {Array.from(new Set(visibleNav.map((i) => i.section))).map((secName) => {
            const secItems = visibleNav.filter((i) => i.section === secName);
            return (
              <div key={secName} style={{ marginBottom: '0.65rem' }}>
                <div
                  style={{
                    padding: '0.65rem 1rem 0.25rem',
                    fontSize: '0.675rem',
                    fontWeight: 700,
                    textTransform: 'uppercase',
                    letterSpacing: '0.08em',
                    color: '#64748b',
                  }}
                >
                  {secName}
                </div>
                {secItems.map((item) => {
                  const isActive = location.pathname === item.path;
                  return (
                    <button
                      key={item.id}
                      type="button"
                      onClick={() => {
                        navigate(item.path);
                        if (window.innerWidth < 1024) setSidebarOpen(false);
                      }}
                      style={{
                        width: 'calc(100% - 1.2rem)',
                        margin: '0.1rem 0.6rem',
                        display: 'flex',
                        alignItems: 'center',
                        gap: '0.65rem',
                        padding: '0.5rem 0.75rem',
                        borderRadius: '6px',
                        background: isActive ? '#2563eb' : 'transparent',
                        color: isActive ? '#ffffff' : '#cbd5e1',
                        border: 'none',
                        cursor: 'pointer',
                        textAlign: 'left',
                        fontSize: '0.825rem',
                        fontWeight: isActive ? 600 : 500,
                        transition: 'all 0.12s ease',
                      }}
                      onMouseEnter={(e) => {
                        if (!isActive) (e.currentTarget as HTMLElement).style.background = 'rgba(255, 255, 255, 0.07)';
                      }}
                      onMouseLeave={(e) => {
                        if (!isActive) (e.currentTarget as HTMLElement).style.background = 'transparent';
                      }}
                    >
                      <span style={{ color: isActive ? '#ffffff' : '#94a3b8', display: 'flex', alignItems: 'center' }}>
                        {item.icon}
                      </span>
                      <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {item.label}
                      </span>
                      {item.badge && (
                        <span className="badge badge-warning" style={{ fontSize: '0.65rem' }}>
                          {item.badge}
                        </span>
                      )}
                    </button>
                  );
                })}
              </div>
            );
          })}
        </nav>

        {/* User Profile & Logout */}
        <div
          style={{
            padding: '0.75rem 1rem',
            borderTop: '1px solid #1e293b',
            background: '#090d16',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', overflow: 'hidden' }}>
            <div
              style={{
                width: 32,
                height: 32,
                borderRadius: '50%',
                background: '#334155',
                color: '#f8fafc',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontWeight: 600,
                fontSize: '0.8rem',
              }}
            >
              {user?.username?.substring(0, 2).toUpperCase() || 'U'}
            </div>
            <div style={{ overflow: 'hidden' }}>
              <div style={{ fontSize: '0.8rem', fontWeight: 600, color: '#f8fafc', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {user?.username}
              </div>
              <span className="badge badge-neutral" style={{ fontSize: '0.65rem', padding: '0.1rem 0.35rem' }}>
                {user?.role}
              </span>
            </div>
          </div>
          <button
            type="button"
            title="Log out"
            onClick={handleLogout}
            style={{
              background: 'none',
              border: 'none',
              color: '#94a3b8',
              cursor: 'pointer',
              padding: '0.35rem',
              borderRadius: 'var(--radius-sm)',
            }}
          >
            <IconLogout size={18} />
          </button>
        </div>
      </aside>

      {/* Backdrop for mobile sidebar */}
      {sidebarOpen && (
        <div
          style={{
            position: 'fixed',
            inset: 0,
            background: 'rgba(0,0,0,0.5)',
            zIndex: 90,
          }}
          onClick={() => setSidebarOpen(false)}
        />
      )}

      {/* ===================================================================
          Main Content Shell (Header + Page Content)
          =================================================================== */}
      <div
        style={{
          flex: 1,
          marginLeft: window.innerWidth >= 1024 ? 'var(--sidebar-width)' : 0,
          display: 'flex',
          flexDirection: 'column',
          minWidth: 0,
        }}
      >
        {/* Top Header */}
        <header
          style={{
            height: 'var(--header-height)',
            backgroundColor: 'var(--bg-surface)',
            borderBottom: '1px solid var(--border-light)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            padding: '0 1.25rem',
            position: 'sticky',
            top: 0,
            zIndex: 80,
          }}
        >
          {/* Left: Mobile hamburger + Breadcrumbs */}
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
            {window.innerWidth < 1024 && (
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setSidebarOpen(true)}
                style={{ padding: '0.35rem 0.5rem' }}
              >
                <IconMenu size={18} />
              </button>
            )}
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', fontSize: '0.85rem' }}>
              {getBreadcrumbs().map((bc, idx, arr) => (
                <React.Fragment key={bc.path + idx}>
                  <span
                    onClick={() => navigate(bc.path)}
                    style={{
                      cursor: idx < arr.length - 1 ? 'pointer' : 'default',
                      color: idx === arr.length - 1 ? 'var(--text-main)' : 'var(--text-muted)',
                      fontWeight: idx === arr.length - 1 ? 600 : 400,
                    }}
                  >
                    {bc.label}
                  </span>
                  {idx < arr.length - 1 && <span style={{ color: 'var(--text-muted)' }}>/</span>}
                </React.Fragment>
              ))}
            </div>
          </div>

          {/* Right Controls: Quick Barcode Scan + Online/Sync Status + Shortcuts Help */}
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.65rem' }}>
            {/* Quick Barcode Scanner Trigger */}
            <button
              type="button"
              className="btn btn-outline btn-sm"
              onClick={() => setScannerOpen(true)}
              title="Barcode Scanner (USB / Camera / Manual)"
            >
              <IconBarcode size={16} />
              <span>Scan Barcode</span>
            </button>

            {/* Offline/Online Sync Badge */}
            <div
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: '0.35rem',
                padding: '0.25rem 0.65rem',
                borderRadius: 'var(--radius-sm)',
                fontSize: '0.75rem',
                fontWeight: 600,
                backgroundColor: isOnline ? 'var(--success-bg)' : 'var(--warning-bg)',
                color: isOnline ? 'var(--success-text)' : 'var(--warning-text)',
                border: `1px solid ${isOnline ? 'var(--success-border)' : 'var(--warning-border)'}`,
              }}
            >
              <span
                style={{
                  width: 7,
                  height: 7,
                  borderRadius: '50%',
                  backgroundColor: isOnline ? '#16a34a' : '#f59e0b',
                }}
              />
              <span>{isOnline ? 'Online' : 'Offline'}</span>
              {syncPending > 0 && <span>({syncPending} pending)</span>}
            </div>

            {/* Keyboard Shortcuts Guide */}
            <button
              type="button"
              className="btn btn-outline btn-sm"
              style={{ padding: '0.35rem 0.5rem' }}
              title="Keyboard Shortcuts"
              onClick={() => setShortcutsOpen(true)}
            >
              <span style={{ fontSize: '0.75rem', fontWeight: 600 }}>⌨ Help</span>
            </button>
          </div>
        </header>

        {/* Page Content Body */}
        <main
          style={{
            flex: 1,
            padding: fullWidth ? 0 : '1.25rem',
            maxWidth: fullWidth ? '100%' : '1400px',
            width: '100%',
            margin: '0 auto',
          }}
        >
          {children}
        </main>
      </div>

      {/* Global Barcode Scanner Modal */}
      <BarcodeScannerModal
        isOpen={scannerOpen}
        onClose={() => setScannerOpen(false)}
        onScan={(code) => {
          // If in billing, product catalog, etc., dispatch custom event
          window.dispatchEvent(new CustomEvent('gbos-barcode-scan', { detail: { barcode: code } }));
          // If not currently in billing or products, navigate to products or billing
          if (location.pathname !== '/billing' && location.pathname !== '/products') {
            navigate(`/products?barcode=${encodeURIComponent(code)}`);
          }
        }}
      />

      {/* Keyboard Shortcuts Modal */}
      {shortcutsOpen && (
        <div className="erp-modal-backdrop" onClick={() => setShortcutsOpen(false)}>
          <div className="erp-modal" style={{ maxWidth: 440 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <h3 style={{ margin: 0, fontSize: '1rem' }}>Keyboard Shortcuts</h3>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setShortcutsOpen(false)}
              >
                <IconClose size={18} />
              </button>
            </div>
            <div className="erp-modal-body">
              <table className="erp-table">
                <thead>
                  <tr>
                    <th>Key</th>
                    <th>Action</th>
                  </tr>
                </thead>
                <tbody>
                  <tr>
                    <td><kbd className="text-mono" style={{ background: '#f1f5f9', padding: '2px 6px', borderRadius: 3, border: '1px solid #cbd5e1' }}>F2</kbd></td>
                    <td>Focus Barcode / Product Search (Billing)</td>
                  </tr>
                  <tr>
                    <td><kbd className="text-mono" style={{ background: '#f1f5f9', padding: '2px 6px', borderRadius: 3, border: '1px solid #cbd5e1' }}>F4</kbd></td>
                    <td>Select Customer / Khata</td>
                  </tr>
                  <tr>
                    <td><kbd className="text-mono" style={{ background: '#f1f5f9', padding: '2px 6px', borderRadius: 3, border: '1px solid #cbd5e1' }}>F8</kbd></td>
                    <td>Hold / Suspend Active Cart</td>
                  </tr>
                  <tr>
                    <td><kbd className="text-mono" style={{ background: '#f1f5f9', padding: '2px 6px', borderRadius: 3, border: '1px solid #cbd5e1' }}>F9</kbd></td>
                    <td>Checkout & Print Bill</td>
                  </tr>
                  <tr>
                    <td><kbd className="text-mono" style={{ background: '#f1f5f9', padding: '2px 6px', borderRadius: 3, border: '1px solid #cbd5e1' }}>Esc</kbd></td>
                    <td>Cancel Dialog / Clear Search</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
