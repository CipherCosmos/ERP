import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { storeApi } from '../services/api';

export default function Settings() {
  const navigate = useNavigate();
  const { user, store, language, setLanguage, setStore } = useAppStore();
  const [activeTab, setActiveTab] = useState<'store' | 'users' | 'sync' | 'language'>('store');
  const [allStores, setAllStores] = useState<{ id: string; name: string }[]>([]);
  const [storeSwitching, setStoreSwitching] = useState(false);
  const [storeForm, setStoreForm] = useState({ name: '', address: '', phone: '', gstin: '', location_type: 'store' as 'store' | 'warehouse' });
  const [savingStore, setSavingStore] = useState(false);
  const [saveError, setSaveError] = useState('');

  useEffect(() => {
    if (!user || user.role !== 'owner') return;
    storeApi
      .list()
      .then((res) => setAllStores((res.data as any[]) || []))
      .catch(() => setAllStores([]));
  }, [user]);

  useEffect(() => {
    if (!store) return;
    setStoreForm({
      name: store.name || '',
      address: store.address || '',
      phone: store.phone || '',
      gstin: store.gstin || '',
      location_type: (store as { location_type?: string }).location_type === 'warehouse' ? 'warehouse' : 'store',
    });
  }, [store]);

  if (!user || user.role !== 'owner') {
    navigate('/');
    return null;
  }

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Settings</h1>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
      </div>

      <div style={styles.tabs}>
        <button style={activeTab === 'store' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('store')}>Store</button>
        <button style={activeTab === 'users' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('users')}>Users</button>
        <button style={activeTab === 'language' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('language')}>Language</button>
        <button style={activeTab === 'sync' ? styles.tabActive : styles.tab} onClick={() => setActiveTab('sync')}>Sync</button>
      </div>

      {activeTab === 'store' && (
        <div style={styles.content}>
          <div style={styles.card}>
            <h3>Active store (owner)</h3>
            <p style={styles.hint}>Switch context for billing, inventory, and reports. JWT must be owner.</p>
            <div style={styles.field}>
              <label>Working store</label>
              <select
                style={styles.select}
                value={store?.id || ''}
                disabled={storeSwitching || allStores.length === 0}
                onChange={async (e) => {
                  const id = e.target.value;
                  if (!id || id === store?.id) return;
                  setStoreSwitching(true);
                  try {
                    const { data } = await storeApi.get(id);
                    localStorage.setItem('storeId', id);
                    setStore(data as any);
                  } catch (err) {
                    console.error(err);
                  } finally {
                    setStoreSwitching(false);
                  }
                }}
              >
                {(allStores.length > 0 ? allStores : store ? [{ id: store.id, name: store.name }] : []).map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </select>
            </div>
          </div>
          <div style={styles.card}>
            <h3>Store Information</h3>
            <p style={styles.hint}>Updates the active store. Managers can edit their assigned store.</p>
            <div style={styles.field}>
              <label>Store Name</label>
              <input
                type="text"
                value={storeForm.name}
                onChange={(e) => setStoreForm((f) => ({ ...f, name: e.target.value }))}
                style={styles.input}
              />
            </div>
            <div style={styles.field}>
              <label>Address</label>
              <input
                type="text"
                value={storeForm.address}
                onChange={(e) => setStoreForm((f) => ({ ...f, address: e.target.value }))}
                style={styles.input}
              />
            </div>
            <div style={styles.field}>
              <label>Phone</label>
              <input
                type="text"
                value={storeForm.phone}
                onChange={(e) => setStoreForm((f) => ({ ...f, phone: e.target.value }))}
                style={styles.input}
              />
            </div>
            <div style={styles.field}>
              <label>GSTIN</label>
              <input
                type="text"
                value={storeForm.gstin}
                onChange={(e) => setStoreForm((f) => ({ ...f, gstin: e.target.value }))}
                style={styles.input}
              />
            </div>
            <div style={styles.field}>
              <label>Location type</label>
              <select
                value={storeForm.location_type}
                onChange={(e) => setStoreForm((f) => ({ ...f, location_type: e.target.value as 'store' | 'warehouse' }))}
                style={styles.select}
              >
                <option value="store">Retail store</option>
                <option value="warehouse">Warehouse / godown</option>
              </select>
              <p style={styles.hint}>Used for reporting and future stock routing; does not change billing by itself.</p>
            </div>
            {saveError && <p style={styles.error}>{saveError}</p>}
            <button
              type="button"
              style={styles.saveBtn}
              disabled={!store || savingStore}
              onClick={async () => {
                if (!store) return;
                setSaveError('');
                setSavingStore(true);
                try {
                  await storeApi.update(store.id, storeForm);
                  const { data } = await storeApi.get(store.id);
                  setStore(data as any);
                } catch (err: any) {
                  setSaveError(err.response?.data?.error || 'Failed to save store');
                } finally {
                  setSavingStore(false);
                }
              }}
            >
              {savingStore ? 'Saving…' : 'Save Changes'}
            </button>
          </div>
        </div>
      )}

      {activeTab === 'users' && (
        <div style={styles.content}>
          <div style={styles.card}>
            <h3>User Management</h3>
            <p style={styles.hint}>Manage user accounts and roles</p>
            <table style={styles.table}>
              <thead>
                <tr>
                  <th style={styles.th}>Username</th>
                  <th style={styles.th}>Role</th>
                  <th style={styles.th}>Status</th>
                  <th style={styles.th}>Action</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td style={styles.td}>{user.username}</td>
                  <td style={styles.td}>{user.role}</td>
                  <td style={styles.td}>Active</td>
                  <td style={styles.td}>-</td>
                </tr>
              </tbody>
            </table>
            <button style={styles.addUserBtn}>+ Add User</button>
          </div>
        </div>
      )}

      {activeTab === 'language' && (
        <div style={styles.content}>
          <div style={styles.card}>
            <h3>Language Settings</h3>
            <p style={styles.hint}>Select your preferred language for the interface</p>
            <div style={styles.languageOptions}>
              <label style={styles.languageOption}>
                <input
                  type="radio"
                  name="language"
                  value="en"
                  checked={language === 'en'}
                  onChange={() => setLanguage('en')}
                />
                <span style={styles.languageLabel}>English</span>
              </label>
              <label style={styles.languageOption}>
                <input
                  type="radio"
                  name="language"
                  value="hi"
                  checked={language === 'hi'}
                  onChange={() => setLanguage('hi')}
                />
                <span style={styles.languageLabel}>हिन्दी (Hindi)</span>
              </label>
              <label style={styles.languageOption}>
                <input
                  type="radio"
                  name="language"
                  value="kn"
                  checked={language === 'kn'}
                  onChange={() => setLanguage('kn')}
                />
                <span style={styles.languageLabel}>ಕನ್ನಡ (Kannada)</span>
              </label>
            </div>
            <p style={styles.hint}>Note: Product names will display in selected language if translations are available.</p>
          </div>
        </div>
      )}

      {activeTab === 'sync' && (
        <div style={styles.content}>
          <div style={styles.card}>
            <h3>Sync Settings</h3>
            <div style={styles.field}>
              <label>Sync Interval</label>
              <select style={styles.select}>
                <option value="5">5 minutes</option>
                <option value="10">10 minutes</option>
                <option value="30">30 minutes</option>
                <option value="60">1 hour</option>
              </select>
            </div>
            <div style={styles.field}>
              <label>Offline Mode</label>
              <p style={styles.hint}>Maximum offline duration: 8 hours</p>
            </div>
            <button style={styles.syncNowBtn}>Sync Now</button>
          </div>
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', background: '#f5f5f5', minHeight: '100vh' },
  header: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' },
  backBtn: { padding: '0.5rem 1rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  tabs: { display: 'flex', gap: '0.5rem', marginBottom: '1rem' },
  tab: { padding: '0.75rem 1.5rem', background: 'white', border: '1px solid #ddd', borderRadius: '4px', cursor: 'pointer' },
  tabActive: { padding: '0.75rem 1.5rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  content: { padding: '0.5rem' },
  card: { background: 'white', padding: '1.5rem', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  field: { marginBottom: '1rem' },
  label: { display: 'block', marginBottom: '0.5rem', fontWeight: '500' },
  input: { width: '100%', padding: '0.75rem', border: '1px solid #ddd', borderRadius: '4px' },
  select: { width: '100%', padding: '0.75rem', border: '1px solid #ddd', borderRadius: '4px' },
  hint: { color: '#666', fontSize: '0.875rem', margin: 0 },
  saveBtn: { padding: '0.75rem 1.5rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  addUserBtn: { marginTop: '1rem', padding: '0.75rem 1.5rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  syncNowBtn: { padding: '0.75rem 1.5rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  table: { width: '100%', borderCollapse: 'collapse', marginTop: '1rem' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  languageOptions: { display: 'flex', flexDirection: 'column', gap: '1rem', marginTop: '1rem' },
  languageOption: { display: 'flex', alignItems: 'center', gap: '0.75rem', padding: '1rem', background: '#f5f5f5', borderRadius: '8px', cursor: 'pointer' },
  languageLabel: { fontSize: '1rem' },
  error: { color: '#d32f2f', fontSize: '0.875rem', margin: '0 0 0.5rem' },
};