import React, { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { authApi, storeApi } from '../services/api';
import { useAppStore } from '../stores/appStore';

interface LoginForm {
  username: string;
  password: string;
}

export default function Login() {
  const navigate = useNavigate();
  const setUser = useAppStore((s) => s.setUser);
  const setStore = useAppStore((s) => s.setStore);
  const [form, setForm] = useState<LoginForm>({ username: '', password: '' });
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
      navigate('/billing');
    } catch (err: any) {
      setError(err.response?.data?.error || 'Login failed');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.card}>
        <h1 style={styles.title}>GBOS</h1>
        <p style={styles.subtitle}>Grocery Business Operating System</p>

        <form onSubmit={handleSubmit} style={styles.form}>
          <input
            type="text"
            placeholder="Username"
            value={form.username}
            onChange={(e) => setForm({ ...form, username: e.target.value })}
            style={styles.input}
            required
          />
          <input
            type="password"
            placeholder="Password"
            value={form.password}
            onChange={(e) => setForm({ ...form, password: e.target.value })}
            style={styles.input}
            required
          />
          {error && <p style={styles.error}>{error}</p>}
          <button type="submit" style={styles.button} disabled={loading}>
            {loading ? 'Logging in...' : 'Login'}
          </button>
        </form>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    minHeight: '100vh',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    background: '#f5f5f5',
  },
  card: {
    background: 'white',
    padding: '2rem',
    borderRadius: '8px',
    boxShadow: '0 2px 8px rgba(0,0,0,0.1)',
    width: '100%',
    maxWidth: '360px',
  },
  title: {
    margin: 0,
    textAlign: 'center',
    color: '#1976d2',
    fontSize: '2rem',
  },
  subtitle: {
    margin: '0.5rem 0 1.5rem',
    textAlign: 'center',
    color: '#666',
    fontSize: '0.9rem',
  },
  form: {
    display: 'flex',
    flexDirection: 'column',
    gap: '1rem',
  },
  input: {
    padding: '0.75rem',
    fontSize: '1rem',
    border: '1px solid #ddd',
    borderRadius: '4px',
  },
  button: {
    padding: '0.75rem',
    fontSize: '1rem',
    background: '#1976d2',
    color: 'white',
    border: 'none',
    borderRadius: '4px',
    cursor: 'pointer',
  },
  error: {
    color: '#d32f2f',
    fontSize: '0.875rem',
    margin: 0,
  },
};