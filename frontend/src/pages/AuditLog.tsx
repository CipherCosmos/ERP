import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { auditApi } from '../services/api';

interface AuditEntry {
  id: string;
  user_id: string;
  username: string;
  store_id: string;
  action: string;
  table_name: string;
  record_id: string;
  old_value: unknown;
  new_value: unknown;
  created_at: string;
}

const TABLE_OPTIONS = [
  '',
  'products',
  'categories',
  'invoices',
  'invoice_items',
  'customers',
  'stock',
  'purchases',
  'vendors',
  'expenses',
  'returns',
  'losses',
  'payments',
];

export default function AuditLog() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [tableFilter, setTableFilter] = useState('');
  const [limit, setLimit] = useState('50');
  const [expandedId, setExpandedId] = useState<string | null>(null);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadEntries();
  }, [user, store]);

  const loadEntries = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const params: { store_id: string; table?: string; limit?: string } = {
        store_id: store.id,
      };
      if (tableFilter) params.table = tableFilter;
      if (limit) params.limit = limit;
      const { data } = await auditApi.list(params);
      setEntries(data);
    } catch (err) {
      console.error('Failed to load audit logs:', err);
    } finally {
      setLoading(false);
    }
  };

  const handleApplyFilters = () => {
    setExpandedId(null);
    loadEntries();
  };

  const formatTime = (iso: string) => {
    const d = new Date(iso);
    return d.toLocaleString();
  };

  const truncate = (val: unknown, maxLen = 60): string => {
    if (val === null || val === undefined) return '-';
    const s = typeof val === 'string' ? val : JSON.stringify(val);
    return s.length > maxLen ? s.slice(0, maxLen) + '...' : s;
  };

  const formatJson = (val: unknown): string => {
    if (val === null || val === undefined) return '-';
    if (typeof val === 'string') {
      try {
        return JSON.stringify(JSON.parse(val), null, 2);
      } catch {
        return val;
      }
    }
    return JSON.stringify(val, null, 2);
  };

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={styles.backBtn}>
          &larr; Back to Menu
        </button>
        <h1>Audit Log</h1>
      </div>

      <div style={styles.filterBar}>
        <label style={styles.filterLabel}>
          Table
          <select
            value={tableFilter}
            onChange={(e) => setTableFilter(e.target.value)}
            style={styles.select}
          >
            <option value="">All tables</option>
            {TABLE_OPTIONS.filter(Boolean).map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </label>
        <label style={styles.filterLabel}>
          Limit
          <input
            type="number"
            min="1"
            max="500"
            value={limit}
            onChange={(e) => setLimit(e.target.value)}
            style={styles.limitInput}
          />
        </label>
        <button onClick={handleApplyFilters} style={styles.applyBtn}>
          Apply
        </button>
      </div>

      <div style={styles.listCard}>
        {loading ? (
          <p style={{ textAlign: 'center', padding: '2rem' }}>Loading...</p>
        ) : (
          <table style={styles.table}>
            <thead>
              <tr>
                <th style={styles.th}>Time</th>
                <th style={styles.th}>User</th>
                <th style={styles.th}>Action</th>
                <th style={styles.th}>Table</th>
                <th style={styles.th}>Record ID</th>
                <th style={styles.th}>New Value</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((entry) => (
                <React.Fragment key={entry.id}>
                  <tr
                    onClick={() =>
                      setExpandedId(expandedId === entry.id ? null : entry.id)
                    }
                    style={{
                      ...styles.row,
                      background: expandedId === entry.id ? '#e3f2fd' : undefined,
                    }}
                  >
                    <td style={styles.td}>{formatTime(entry.created_at)}</td>
                    <td style={styles.td}>{entry.username}</td>
                    <td style={styles.td}>
                      <span style={actionBadgeStyle(entry.action)}>
                        {entry.action}
                      </span>
                    </td>
                    <td style={styles.td}>{entry.table_name}</td>
                    <td style={styles.tdMono}>{entry.record_id?.slice(0, 8)}</td>
                    <td style={styles.td}>{truncate(entry.new_value)}</td>
                  </tr>
                  {expandedId === entry.id && (
                    <tr>
                      <td colSpan={6} style={styles.expandedCell}>
                        <div style={styles.expandedGrid}>
                          <div style={styles.expandedSection}>
                            <strong>Old Value</strong>
                            <pre style={styles.pre}>
                              {formatJson(entry.old_value)}
                            </pre>
                          </div>
                          <div style={styles.expandedSection}>
                            <strong>New Value</strong>
                            <pre style={styles.pre}>
                              {formatJson(entry.new_value)}
                            </pre>
                          </div>
                        </div>
                      </td>
                    </tr>
                  )}
                </React.Fragment>
              ))}
              {entries.length === 0 && (
                <tr>
                  <td colSpan={6} style={{ textAlign: 'center', padding: '2rem' }}>
                    No audit entries found
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function actionBadgeStyle(action: string): React.CSSProperties {
  const base: React.CSSProperties = {
    padding: '2px 8px',
    borderRadius: '4px',
    fontSize: '0.85rem',
    fontWeight: 'bold',
    textTransform: 'uppercase',
  };
  switch (action?.toLowerCase()) {
    case 'insert':
    case 'create':
      return { ...base, background: '#e8f5e9', color: '#2e7d32' };
    case 'update':
      return { ...base, background: '#fff3e0', color: '#e65100' };
    case 'delete':
      return { ...base, background: '#ffebee', color: '#c62828' };
    default:
      return { ...base, background: '#f5f5f5', color: '#333' };
  }
}

const styles: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', maxWidth: '1100px', margin: '0 auto' },
  header: {
    display: 'flex',
    alignItems: 'center',
    gap: '1rem',
    marginBottom: '1rem',
  },
  backBtn: {
    padding: '0.5rem 1rem',
    background: '#f5f5f5',
    border: '1px solid #ddd',
    borderRadius: '4px',
    cursor: 'pointer',
  },
  filterBar: {
    display: 'flex',
    alignItems: 'flex-end',
    gap: '1rem',
    marginBottom: '1rem',
    flexWrap: 'wrap',
  },
  filterLabel: {
    display: 'flex',
    flexDirection: 'column',
    fontSize: '0.85rem',
    fontWeight: 600,
    gap: '4px',
  },
  select: {
    padding: '0.5rem',
    fontSize: '1rem',
    border: '1px solid #ddd',
    borderRadius: '4px',
    minWidth: '160px',
  },
  limitInput: {
    padding: '0.5rem',
    fontSize: '1rem',
    border: '1px solid #ddd',
    borderRadius: '4px',
    width: '80px',
  },
  applyBtn: {
    padding: '0.5rem 1rem',
    background: '#1976d2',
    color: 'white',
    border: 'none',
    borderRadius: '4px',
    cursor: 'pointer',
    fontWeight: 'bold',
  },
  listCard: {
    background: 'white',
    padding: '1rem',
    borderRadius: '8px',
    boxShadow: '0 2px 4px rgba(0,0,0,0.1)',
    overflowX: 'auto',
  },
  table: { width: '100%', borderCollapse: 'collapse' },
  th: {
    textAlign: 'left',
    padding: '0.75rem 0.5rem',
    borderBottom: '2px solid #eee',
    fontSize: '0.85rem',
    color: '#666',
    whiteSpace: 'nowrap',
  },
  td: {
    padding: '0.6rem 0.5rem',
    borderBottom: '1px solid #f0f0f0',
    fontSize: '0.9rem',
  },
  tdMono: {
    padding: '0.6rem 0.5rem',
    borderBottom: '1px solid #f0f0f0',
    fontSize: '0.9rem',
    fontFamily: 'monospace',
  },
  row: { cursor: 'pointer', transition: 'background 0.15s' },
  expandedCell: {
    padding: '1rem',
    background: '#fafafa',
    borderBottom: '1px solid #e0e0e0',
  },
  expandedGrid: {
    display: 'grid',
    gridTemplateColumns: '1fr 1fr',
    gap: '1rem',
  },
  expandedSection: {
    display: 'flex',
    flexDirection: 'column',
    gap: '0.25rem',
  },
  pre: {
    background: '#f5f5f5',
    padding: '0.75rem',
    borderRadius: '4px',
    fontSize: '0.8rem',
    overflow: 'auto',
    maxHeight: '300px',
    whiteSpace: 'pre-wrap',
    wordBreak: 'break-word',
    margin: 0,
  },
};
