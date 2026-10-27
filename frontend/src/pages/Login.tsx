import React, { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { authApi, storeApi } from '../services/api';
import { useAppStore } from '../stores/appStore';
import { IconAlert, IconCheck } from '../components/Icons';

export default function Login() {
  const navigate = useNavigate();
  const setUser = useAppStore((s) => s.setUser);
  const setStore = useAppStore((s) => s.setStore);

  const [form, setForm] = useState({ username: 'admin', password: 'admin123' });
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setLoading(true);

    try {
      const { data } = await authApi.login(form.username, form.password);
      localStorage.setItem('token', data.token);
      localStorage.setItem('storeId', data.user.store_id);
      setUser(data.user);

      try {
        const storeRes = await storeApi.get(data.user.store_id);
        setStore(storeRes.data as any);
      } catch {
        setStore(null);
      }

      if (data.user.role === 'cashier') {
        navigate('/billing');
      } else {
        navigate('/dashboard');
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
          maxWidth: 400,
          boxShadow: 'var(--shadow-md)',
          border: '1px solid var(--border-medium)',
        }}
      >
        <div style={{ padding: '2rem 1.75rem' }}>
          <div style={{ textAlign: 'center', marginBottom: '1.5rem' }}>
            <div
              style={{
                width: 44,
                height: 44,
                borderRadius: 'var(--radius-md)',
                backgroundColor: 'var(--primary-600)',
                color: '#fff',
                display: 'inline-flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontWeight: 800,
                fontSize: '1.25rem',
                marginBottom: '0.75rem',
              }}
            >
              G
            </div>
            <h1 style={{ fontSize: '1.4rem', fontWeight: 700, margin: 0 }}>
              GBOS ERP
            </h1>
            <p style={{ color: 'var(--text-secondary)', fontSize: '0.85rem', marginTop: '0.25rem' }}>
              Enterprise Retail & Inventory Operating System
            </p>
          </div>

          <form onSubmit={handleSubmit}>
            <div className="form-group">
              <label className="form-label">Username</label>
              <input
                type="text"
                required
                className="form-input"
                placeholder="Enter username"
                value={form.username}
                onChange={(e) => setForm({ ...form, username: e.target.value })}
              />
            </div>

            <div className="form-group" style={{ marginBottom: '1.25rem' }}>
              <label className="form-label">Password</label>
              <input
                type="password"
                required
                className="form-input"
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
                  padding: '0.6rem 0.75rem',
                  borderRadius: 'var(--radius-sm)',
                  backgroundColor: 'var(--danger-bg)',
                  color: 'var(--danger-text)',
                  border: '1px solid var(--danger-border)',
                  fontSize: '0.8rem',
                  marginBottom: '1rem',
                }}
              >
                <IconAlert size={16} />
                <span>{error}</span>
              </div>
            )}

            <button
              type="submit"
              className="btn btn-primary"
              style={{ width: '100%', padding: '0.65rem', justifyContent: 'center' }}
              disabled={loading}
            >
              {loading ? 'Authenticating...' : 'Sign In to Workspace'}
            </button>
          </form>

          {/* Preset Demo Accounts */}
          <div
            style={{
              marginTop: '1.5rem',
              paddingTop: '1.25rem',
              borderTop: '1px solid var(--border-light)',
              fontSize: '0.75rem',
              color: 'var(--text-muted)',
            }}
          >
            <div style={{ fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
              Quick Preset Roles:
            </div>
            <div style={{ display: 'flex', gap: '0.35rem', flexWrap: 'wrap' }}>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setPresetUser('admin', 'admin123')}
              >
                Owner (admin)
              </button>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setPresetUser('cashier', 'cashier123')}
              >
                Cashier
              </button>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setPresetUser('helper', 'helper123')}
              >
                Helper
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}