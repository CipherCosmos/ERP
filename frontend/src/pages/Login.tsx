import React, { useState, useEffect } from 'react';
import { useNavigate, Navigate } from 'react-router-dom';
import { authApi, storeApi, terminalApi } from '../services/api';
import { useAppStore } from '../stores/appStore';
import { IconAlert } from '../components/Icons';

export default function Login() {
  const navigate = useNavigate();
  const user = useAppStore((s) => s.user);
  const setUser = useAppStore((s) => s.setUser);
  const setStore = useAppStore((s) => s.setStore);
  const setPermissions = useAppStore((s) => s.setPermissions);
  const setTerminal = useAppStore((s) => s.setTerminal);

  const [form, setForm] = useState({ username: 'admin', password: 'admin12345' });
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  // If already logged in, redirect away from the login page immediately
  const token = typeof window !== 'undefined' ? localStorage.getItem('token') : null;
  if (user || token) {
    const target = user?.role === 'cashier' ? '/billing' : '/dashboard';
    return <Navigate to={target} replace />;
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setLoading(true);

    try {
      const { data } = await authApi.login(form.username, form.password);
      localStorage.setItem('token', data.token);
      localStorage.setItem('storeId', data.user.store_id);
      setUser(data.user);
      setPermissions(Array.isArray(data.permissions) ? data.permissions : []);

      try {
        const storeRes = await storeApi.get(data.user.store_id);
        setStore(storeRes.data as any);
      } catch {
        setStore(null);
      }

      try {
        const terminals = await terminalApi.list(data.user.store_id);
        const active = (terminals.data || []).find((t: any) => t.is_active) || null;
        setTerminal(active);
      } catch {
        setTerminal(null);
      }

      if (data.user.role === 'cashier') {
        navigate('/billing', { replace: true });
      } else {
        navigate('/dashboard', { replace: true });
      }
    } catch (err: any) {
      setError(err.response?.data?.error || 'Invalid username or password.');
    } finally {
      setLoading(false);
    }
  };

  const setPresetUser = (u: string, p: string) => {
    setForm({ username: u, password: p });
  };

  return (
    <div
      style={{
        minHeight: '100vh',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        backgroundColor: 'var(--bg-app)',
        padding: '1rem',
      }}
    >
      <div
        className="erp-card"
        style={{
          width: '100%',
          maxWidth: 420,
          boxShadow: 'var(--shadow-md)',
          border: '1px solid var(--border-medium)',
          backgroundColor: '#ffffff',
          borderRadius: 'var(--radius-lg)',
        }}
      >
        <div style={{ padding: '2.25rem 2rem' }}>
          <div style={{ textAlign: 'center', marginBottom: '1.75rem' }}>
            <div
              style={{
                width: 48,
                height: 48,
                borderRadius: 'var(--radius-md)',
                backgroundColor: 'var(--primary-600)',
                color: '#fff',
                display: 'inline-flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontWeight: 800,
                fontSize: '1.4rem',
                marginBottom: '0.75rem',
                boxShadow: '0 4px 6px -1px rgba(37, 99, 235, 0.25)',
              }}
            >
              G
            </div>
            <h1 style={{ fontSize: '1.5rem', fontWeight: 700, margin: 0, color: 'var(--text-main)' }}>
              GBOS ERP
            </h1>
            <p style={{ color: 'var(--text-secondary)', fontSize: '0.85rem', marginTop: '0.35rem' }}>
              Enterprise Retail & Inventory Operating System
            </p>
          </div>

          <form onSubmit={handleSubmit}>
            <div className="form-group" style={{ marginBottom: '1rem' }}>
              <label className="form-label" style={{ fontWeight: 600, fontSize: '0.825rem', marginBottom: '0.35rem', display: 'block' }}>
                Username
              </label>
              <input
                type="text"
                required
                className="form-input"
                style={{ width: '100%', padding: '0.65rem 0.85rem' }}
                placeholder="Enter username"
                value={form.username}
                onChange={(e) => setForm({ ...form, username: e.target.value })}
              />
            </div>

            <div className="form-group" style={{ marginBottom: '1.25rem' }}>
              <label className="form-label" style={{ fontWeight: 600, fontSize: '0.825rem', marginBottom: '0.35rem', display: 'block' }}>
                Password
              </label>
              <input
                type="password"
                required
                className="form-input"
                style={{ width: '100%', padding: '0.65rem 0.85rem' }}
                placeholder="Enter password"
                value={form.password}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
              />
            </div>

            {error && (
              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: '0.5rem',
                  padding: '0.65rem 0.85rem',
                  borderRadius: 'var(--radius-sm)',
                  backgroundColor: 'var(--danger-bg)',
                  color: 'var(--danger-text)',
                  border: '1px solid var(--danger-border)',
                  fontSize: '0.825rem',
                  marginBottom: '1.25rem',
                }}
              >
                <IconAlert size={16} />
                <span>{error}</span>
              </div>
            )}

            <button
              type="submit"
              className="btn btn-primary"
              style={{ width: '100%', padding: '0.75rem', justifyContent: 'center', fontSize: '0.9rem', fontWeight: 600 }}
              disabled={loading}
            >
              {loading ? 'Authenticating...' : 'Sign In to Workspace'}
            </button>
          </form>

          {/* Quick Preset Credentials */}
          <div
            style={{
              marginTop: '1.75rem',
              paddingTop: '1.25rem',
              borderTop: '1px solid var(--border-light)',
              fontSize: '0.775rem',
              color: 'var(--text-muted)',
            }}
          >
            <div style={{ fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
              Quick Preset Credentials:
            </div>
            <div style={{ display: 'flex', gap: '0.4rem', flexWrap: 'wrap' }}>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setPresetUser('admin', 'admin12345')}
              >
                Admin (admin12345)
              </button>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setPresetUser('admin', 'admin123')}
              >
                Admin (admin123)
              </button>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setPresetUser('cashier', 'cashier123')}
              >
                Cashier
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
