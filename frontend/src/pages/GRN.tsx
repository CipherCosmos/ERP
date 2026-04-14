import React, { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { grnApi, purchaseApi } from '../services/api';

interface GRNRow {
  id: string;
  po_id: string;
  po_no?: string;
  received_date: string;
  notes?: string;
  items?: GRNItemRow[];
  item_count?: number;
}

interface GRNItemRow {
  id?: string;
  product_id: string;
  product_name?: string;
  ordered_qty: number;
  received_qty: number;
  accepted_qty: number;
  rejected_qty: number;
  reason?: string;
}

interface POOption { id: string; po_no: string; vendor_name?: string; status: string }

interface POItemDetail { product_id: string; name?: string; product_name?: string; quantity: number; received_qty: number }

interface CreateItem {
  product_id: string;
  product_name: string;
  ordered_qty: number;
  received_qty: string;
  accepted_qty: string;
  rejected_qty: string;
  reason: string;
}

export default function GRN() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [grns, setGrns] = useState<GRNRow[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const [showCreate, setShowCreate] = useState(false);
  const [poOptions, setPoOptions] = useState<POOption[]>([]);
  const [posLoading, setPosLoading] = useState(false);
  const [selectedPO, setSelectedPO] = useState('');
  const [receivedDate, setReceivedDate] = useState(() => new Date().toISOString().slice(0, 10));
  const [notes, setNotes] = useState('');
  const [createItems, setCreateItems] = useState<CreateItem[]>([]);
  const [itemsLoading, setItemsLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  const [detailGRN, setDetailGRN] = useState<GRNRow | null>(null);
  const [detailItems, setDetailItems] = useState<GRNItemRow[]>([]);
  const [detailLoading, setDetailLoading] = useState(false);

  const loadGRNs = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    setError('');
    try {
      const { data } = await grnApi.list({ store_id: store.id });
      setGrns(data || []);
    } catch (err) {
      console.error('Failed to load GRNs:', err);
      setError('Failed to load Goods Receipt Notes');
    } finally {
      setLoading(false);
    }
  }, [store]);

  useEffect(() => {
    if (!user) { navigate('/login'); return; }
    if (user.role === 'cashier') navigate('/billing');
  }, [user, navigate]);

  useEffect(() => { if (store) loadGRNs(); }, [store, loadGRNs]);

  const openCreateForm = async () => {
    setShowCreate(true);
    setSelectedPO('');
    setReceivedDate(new Date().toISOString().slice(0, 10));
    setNotes('');
    setCreateItems([]);
    if (!store) return;
    setPosLoading(true);
    try {
      const [pendingRes, partialRes] = await Promise.all([
        purchaseApi.list({ store_id: store.id, status: 'pending' }),
        purchaseApi.list({ store_id: store.id, status: 'partial' }),
      ]);
      setPoOptions([...(pendingRes.data || []) as POOption[], ...(partialRes.data || []) as POOption[]]);
    } catch (err) {
      console.error('Failed to load POs:', err);
      setPoOptions([]);
    } finally {
      setPosLoading(false);
    }
  };

  const handlePOSelect = async (poId: string) => {
    setSelectedPO(poId);
    setCreateItems([]);
    if (!poId) return;
    setItemsLoading(true);
    try {
      const { data } = await purchaseApi.get(poId);
      const items = (data as { items?: POItemDetail[] }).items || [];
      setCreateItems(items.map((it) => {
        const remaining = Math.max(0, it.quantity - it.received_qty);
        return {
          product_id: it.product_id,
          product_name: it.name || it.product_name || it.product_id.slice(0, 8),
          ordered_qty: it.quantity,
          received_qty: String(remaining),
          accepted_qty: String(remaining),
          rejected_qty: '0',
          reason: '',
        };
      }));
    } catch (err) {
      console.error('Failed to load PO details:', err);
      alert('Failed to load purchase order items');
    } finally {
      setItemsLoading(false);
    }
  };

  const updateCreateItem = (index: number, field: keyof CreateItem, value: string) => {
    setCreateItems((prev) => {
      const next = [...prev];
      next[index] = { ...next[index], [field]: value };
      if (field === 'received_qty') {
        const received = parseFloat(value) || 0;
        next[index].accepted_qty = String(Math.max(0, received - (parseFloat(next[index].rejected_qty) || 0)));
      } else if (field === 'rejected_qty') {
        const received = parseFloat(next[index].received_qty) || 0;
        next[index].accepted_qty = String(Math.max(0, received - (parseFloat(value) || 0)));
      }
      return next;
    });
  };

  const submitCreate = async () => {
    if (!selectedPO) { alert('Please select a purchase order'); return; }
    if (createItems.length === 0) { alert('No items to receive'); return; }
    const items = createItems.map((it) => ({
      product_id: it.product_id,
      ordered_qty: it.ordered_qty,
      received_qty: parseFloat(it.received_qty) || 0,
      accepted_qty: parseFloat(it.accepted_qty) || 0,
      rejected_qty: parseFloat(it.rejected_qty) || 0,
      reason: it.reason || undefined,
    }));
    if (!items.some((it) => it.received_qty > 0)) {
      alert('At least one item must have a received quantity greater than 0');
      return;
    }
    setSaving(true);
    try {
      await grnApi.create({ po_id: selectedPO, received_date: receivedDate, notes: notes || undefined, items });
      setShowCreate(false);
      await loadGRNs();
    } catch (err: unknown) {
      const ax = err as { response?: { data?: { error?: string } } };
      alert(ax.response?.data?.error || 'Failed to create GRN');
    } finally {
      setSaving(false);
    }
  };

  const openDetail = async (grn: GRNRow) => {
    setDetailGRN(grn);
    setDetailItems([]);
    setDetailLoading(true);
    try {
      const { data } = await grnApi.get(grn.id);
      const body = data as GRNRow;
      setDetailGRN(body);
      setDetailItems(body.items || []);
    } catch (err) {
      console.error('Failed to load GRN detail:', err);
      alert('Failed to load GRN details');
      setDetailGRN(null);
    } finally {
      setDetailLoading(false);
    }
  };

  if (!user || user.role === 'cashier') return null;

  return (
    <div style={s.container}>
      <div style={s.header}>
        <div style={s.headerLeft}>
          <button type="button" onClick={() => navigate('/')} style={s.backBtn}>← Back</button>
          <h1 style={s.title}>Goods Receipt Notes</h1>
        </div>
        <button type="button" style={s.createBtn} onClick={openCreateForm}>+ New GRN</button>
      </div>

      {error && (
        <div style={s.errorBar}>
          {error}
          <button type="button" style={s.dismissBtn} onClick={() => setError('')}>x</button>
        </div>
      )}

      <div style={s.card}>
        {loading ? (
          <div style={s.centered}><p style={s.hint}>Loading GRNs...</p></div>
        ) : grns.length === 0 ? (
          <div style={s.centered}>
            <p style={s.emptyText}>No Goods Receipt Notes found.</p>
            <p style={s.hint}>Click "+ New GRN" to record goods received against a purchase order.</p>
          </div>
        ) : (
          <div style={s.tableWrap}>
            <table style={s.table}>
              <thead>
                <tr>
                  <th style={s.th}>GRN ID</th>
                  <th style={s.th}>PO Reference</th>
                  <th style={s.th}>Received Date</th>
                  <th style={s.th}>Notes</th>
                  <th style={s.th}>Items</th>
                </tr>
              </thead>
              <tbody>
                {grns.map((grn, idx) => (
                  <tr
                    key={grn.id}
                    style={{ ...s.tr, background: idx % 2 === 1 ? '#f9f9f9' : 'white', cursor: 'pointer' }}
                    onClick={() => openDetail(grn)}
                  >
                    <td style={s.td}><span style={s.idText}>{grn.id.slice(0, 8)}</span></td>
                    <td style={s.td}>{grn.po_no || grn.po_id.slice(0, 8)}</td>
                    <td style={s.td}>{grn.received_date}</td>
                    <td style={s.td}>{grn.notes || '-'}</td>
                    <td style={s.td}>{grn.item_count ?? grn.items?.length ?? '-'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Create GRN Modal */}
      {showCreate && (
        <div style={s.modalBackdrop}>
          <div style={s.modal}>
            <h3 style={{ margin: '0 0 1rem 0' }}>Create Goods Receipt Note</h3>
            <label style={s.lbl}>Purchase Order</label>
            {posLoading ? (
              <p style={s.hint}>Loading purchase orders...</p>
            ) : (
              <select style={s.selectFull} value={selectedPO} onChange={(e) => handlePOSelect(e.target.value)}>
                <option value="">-- Select Purchase Order --</option>
                {poOptions.map((po) => (
                  <option key={po.id} value={po.id}>
                    {po.po_no} {po.vendor_name ? `(${po.vendor_name})` : ''} [{po.status}]
                  </option>
                ))}
              </select>
            )}
            <label style={s.lbl}>Received Date</label>
            <input type="date" style={s.selectFull} value={receivedDate} onChange={(e) => setReceivedDate(e.target.value)} />
            <label style={s.lbl}>Notes (optional)</label>
            <input type="text" style={s.selectFull} placeholder="e.g. Partial shipment, damaged cartons..." value={notes} onChange={(e) => setNotes(e.target.value)} />

            {itemsLoading ? (
              <p style={{ ...s.hint, marginTop: '1rem' }}>Loading PO items...</p>
            ) : createItems.length > 0 ? (
              <>
                <h4 style={{ marginTop: '1.25rem', marginBottom: '0.5rem' }}>Line Items</h4>
                <div style={s.tableWrap}>
                  <table style={s.table}>
                    <thead>
                      <tr>
                        <th style={s.th}>Product</th>
                        <th style={s.th}>Ordered</th>
                        <th style={s.th}>Received</th>
                        <th style={s.th}>Accepted</th>
                        <th style={s.th}>Rejected</th>
                        <th style={s.th}>Reason</th>
                      </tr>
                    </thead>
                    <tbody>
                      {createItems.map((item, idx) => (
                        <tr key={item.product_id} style={{ background: idx % 2 === 1 ? '#f9f9f9' : 'white' }}>
                          <td style={s.td}>{item.product_name}</td>
                          <td style={s.td}>{item.ordered_qty}</td>
                          <td style={s.td}>
                            <input type="number" min={0} style={s.inSm} value={item.received_qty} onChange={(e) => updateCreateItem(idx, 'received_qty', e.target.value)} />
                          </td>
                          <td style={s.td}>
                            <input type="number" min={0} style={s.inSm} value={item.accepted_qty} onChange={(e) => updateCreateItem(idx, 'accepted_qty', e.target.value)} />
                          </td>
                          <td style={s.td}>
                            <input type="number" min={0} style={s.inSm} value={item.rejected_qty} onChange={(e) => updateCreateItem(idx, 'rejected_qty', e.target.value)} />
                          </td>
                          <td style={s.td}>
                            <input type="text" placeholder="If rejected" style={s.inReason} value={item.reason} onChange={(e) => updateCreateItem(idx, 'reason', e.target.value)} />
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </>
            ) : selectedPO ? (
              <p style={{ ...s.emptyText, marginTop: '1rem' }}>No items found for the selected purchase order.</p>
            ) : null}

            <div style={s.modalActions}>
              <button type="button" style={s.cancelBtn} onClick={() => setShowCreate(false)}>Cancel</button>
              <button
                type="button"
                style={{ ...s.submitBtn, opacity: saving || !selectedPO ? 0.6 : 1, cursor: saving || !selectedPO ? 'not-allowed' : 'pointer' }}
                disabled={saving || !selectedPO}
                onClick={submitCreate}
              >
                {saving ? 'Saving...' : 'Create GRN'}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Detail Modal */}
      {detailGRN && (
        <div style={s.modalBackdrop}>
          <div style={s.modal}>
            <h3 style={{ margin: '0 0 1rem 0' }}>GRN Detail</h3>
            <div style={s.detailGrid}>
              <div style={s.detailField}>
                <span style={s.detailLabel}>GRN ID</span>
                <span style={s.detailValue}>{detailGRN.id.slice(0, 8)}</span>
              </div>
              <div style={s.detailField}>
                <span style={s.detailLabel}>PO Reference</span>
                <span style={s.detailValue}>{detailGRN.po_no || detailGRN.po_id.slice(0, 8)}</span>
              </div>
              <div style={s.detailField}>
                <span style={s.detailLabel}>Received Date</span>
                <span style={s.detailValue}>{detailGRN.received_date}</span>
              </div>
              <div style={s.detailField}>
                <span style={s.detailLabel}>Notes</span>
                <span style={s.detailValue}>{detailGRN.notes || '-'}</span>
              </div>
            </div>

            {detailLoading ? (
              <p style={{ ...s.hint, marginTop: '1rem' }}>Loading items...</p>
            ) : detailItems.length > 0 ? (
              <div style={{ ...s.tableWrap, marginTop: '1rem' }}>
                <table style={s.table}>
                  <thead>
                    <tr>
                      <th style={s.th}>Product</th>
                      <th style={s.th}>Ordered</th>
                      <th style={s.th}>Received</th>
                      <th style={s.th}>Accepted</th>
                      <th style={s.th}>Rejected</th>
                      <th style={s.th}>Reason</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detailItems.map((item, idx) => (
                      <tr key={item.id || item.product_id} style={{ background: idx % 2 === 1 ? '#f9f9f9' : 'white' }}>
                        <td style={s.td}>{item.product_name || item.product_id.slice(0, 8)}</td>
                        <td style={s.td}>{item.ordered_qty}</td>
                        <td style={s.td}>{item.received_qty}</td>
                        <td style={s.td}>{item.accepted_qty}</td>
                        <td style={s.td}>
                          {item.rejected_qty > 0 ? <span style={s.rejBadge}>{item.rejected_qty}</span> : 0}
                        </td>
                        <td style={s.td}>{item.reason || '-'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <p style={{ ...s.emptyText, marginTop: '1rem' }}>No items in this GRN.</p>
            )}

            <div style={s.modalActions}>
              <button type="button" style={s.cancelBtn} onClick={() => setDetailGRN(null)}>Close</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

const s: Record<string, React.CSSProperties> = {
  container: { padding: '1rem', background: '#f5f5f5', minHeight: '100vh' },
  header: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', background: '#1976d2', color: 'white', padding: '0.75rem 1rem', borderRadius: '8px', marginBottom: '1rem' },
  headerLeft: { display: 'flex', alignItems: 'center', gap: '1rem' },
  backBtn: { padding: '0.4rem 0.75rem', background: 'rgba(255,255,255,0.2)', color: 'white', border: '1px solid rgba(255,255,255,0.4)', borderRadius: '4px', cursor: 'pointer', fontSize: '0.9rem' },
  title: { margin: 0, fontSize: '1.25rem', fontWeight: 600 },
  createBtn: { padding: '0.5rem 1.25rem', background: 'white', color: '#1976d2', border: 'none', borderRadius: '4px', cursor: 'pointer', fontWeight: 600, fontSize: '0.9rem' },
  errorBar: { display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '0.75rem 1rem', background: '#ffebee', color: '#c62828', border: '1px solid #ef9a9a', borderRadius: '4px', marginBottom: '1rem' },
  dismissBtn: { background: 'none', border: 'none', color: '#c62828', fontSize: '1.25rem', cursor: 'pointer', padding: '0 0.25rem' },
  card: { background: 'white', borderRadius: '8px', boxShadow: '0 2px 4px rgba(0,0,0,0.1)', padding: '1rem' },
  centered: { textAlign: 'center' as const, padding: '2rem 1rem' },
  hint: { color: '#888', fontSize: '0.9rem' },
  emptyText: { color: '#666', fontSize: '1rem', margin: '0 0 0.5rem 0' },
  tableWrap: { overflowX: 'auto' as const },
  table: { width: '100%', borderCollapse: 'collapse' as const },
  th: { padding: '0.75rem', textAlign: 'left' as const, borderBottom: '2px solid #ddd', background: '#f9f9f9', fontSize: '0.85rem', color: '#555', whiteSpace: 'nowrap' as const },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee', fontSize: '0.9rem' },
  tr: { borderBottom: '1px solid #eee' },
  idText: { fontFamily: 'monospace', fontSize: '0.85rem', color: '#1976d2', fontWeight: 500 },
  modalBackdrop: { position: 'fixed' as const, inset: 0, background: 'rgba(0,0,0,0.4)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000, padding: '1rem' },
  modal: { background: 'white', borderRadius: '8px', padding: '1.5rem', maxWidth: '800px', width: '100%', maxHeight: '90vh', overflow: 'auto', boxShadow: '0 4px 24px rgba(0,0,0,0.15)' },
  lbl: { display: 'block', marginTop: '0.75rem', marginBottom: '0.25rem', fontWeight: 500, fontSize: '0.9rem', color: '#444' },
  selectFull: { width: '100%', padding: '0.5rem', border: '1px solid #ddd', borderRadius: '4px', fontSize: '0.95rem', boxSizing: 'border-box' as const },
  inSm: { width: '72px', padding: '0.4rem', border: '1px solid #ddd', borderRadius: '4px', fontSize: '0.9rem' },
  inReason: { width: '120px', padding: '0.4rem', border: '1px solid #ddd', borderRadius: '4px', fontSize: '0.85rem' },
  modalActions: { display: 'flex', justifyContent: 'flex-end', gap: '0.5rem', marginTop: '1.25rem' },
  cancelBtn: { padding: '0.5rem 1.25rem', background: '#e0e0e0', color: '#333', border: 'none', borderRadius: '4px', cursor: 'pointer', fontSize: '0.9rem' },
  submitBtn: { padding: '0.5rem 1.5rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer', fontWeight: 600, fontSize: '0.9rem' },
  detailGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: '1rem', padding: '0.75rem', background: '#f9f9f9', borderRadius: '4px' },
  detailField: { display: 'flex', flexDirection: 'column' as const },
  detailLabel: { fontSize: '0.75rem', color: '#888', textTransform: 'uppercase' as const, marginBottom: '0.2rem' },
  detailValue: { fontSize: '0.95rem', fontWeight: 500, color: '#333' },
  rejBadge: { display: 'inline-block', padding: '0.15rem 0.5rem', background: '#ffebee', color: '#c62828', borderRadius: '4px', fontSize: '0.85rem', fontWeight: 600 },
};
