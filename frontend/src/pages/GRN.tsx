import React, { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { grnApi, purchaseApi, storeApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { Store } from '../types';
import {
  IconPurchases,
  IconPlus,
  IconClose,
  IconPrinter,
  IconRefresh,
  IconCheck,
  IconAlert,
} from '../components/Icons';

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

interface POOption {
  id: string;
  po_no: string;
  vendor_name?: string;
  status: string;
}

interface POItemDetail {
  product_id: string;
  name?: string;
  product_name?: string;
  quantity: number;
  received_qty: number;
}

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
  const [stores, setStores] = useState<Store[]>([]);
  const [loading, setLoading] = useState(false);

  // Create Modal
  const [showCreate, setShowCreate] = useState(false);
  const [poOptions, setPoOptions] = useState<POOption[]>([]);
  const [selectedPO, setSelectedPO] = useState('');
  const [receivingWarehouseId, setReceivingWarehouseId] = useState('');
  const [receivedDate, setReceivedDate] = useState(() => new Date().toISOString().slice(0, 10));
  const [notes, setNotes] = useState('');
  const [createItems, setCreateItems] = useState<CreateItem[]>([]);
  const [itemsLoading, setItemsLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  // Detail Modal
  const [detailGRN, setDetailGRN] = useState<GRNRow | null>(null);
  const [detailItems, setDetailItems] = useState<GRNItemRow[]>([]);
  const [detailLoading, setDetailLoading] = useState(false);

  const loadGRNs = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await grnApi.list({ store_id: store.id });
      setGrns(data || []);
    } catch (err) {
      console.error('Failed to load GRNs:', err);
    } finally {
      setLoading(false);
    }
  }, [store]);

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    if (store) {
      loadGRNs();
      storeApi.list().then((r) => setStores(r.data || [])).catch(() => {});
    }
  }, [user, store?.id, loadGRNs]);

  const openCreateForm = async () => {
    setShowCreate(true);
    setSelectedPO('');
    setReceivingWarehouseId(store?.id || '');
    setReceivedDate(new Date().toISOString().slice(0, 10));
    setNotes('');
    setCreateItems([]);

    try {
      const [pendingRes, partialRes] = await Promise.all([
        purchaseApi.list({ store_id: store!.id, status: 'pending' }),
        purchaseApi.list({ store_id: store!.id, status: 'partial' }),
      ]);
      setPoOptions([...(pendingRes.data || []), ...(partialRes.data || [])]);
    } catch (err) {
      console.error('Failed to load pending POs:', err);
    }
  };

  const handlePOSelect = async (poId: string) => {
    setSelectedPO(poId);
    if (!poId) {
      setCreateItems([]);
      return;
    }
    setItemsLoading(true);
    try {
      const { data } = await purchaseApi.get(poId);
      const items: POItemDetail[] = data.items || [];
      setCreateItems(
        items.map((it) => {
          const remaining = Math.max(0, it.quantity - (it.received_qty || 0));
          return {
            product_id: it.product_id,
            product_name: it.name || it.product_name || it.product_id,
            ordered_qty: it.quantity,
            received_qty: String(remaining),
            accepted_qty: String(remaining),
            rejected_qty: '0',
            reason: '',
          };
        })
      );
    } catch (err) {
      console.error('Failed to load PO items:', err);
    } finally {
      setItemsLoading(false);
    }
  };

  const handleItemChange = (index: number, field: keyof CreateItem, val: string) => {
    setCreateItems((prev) => {
      const copy = [...prev];
      copy[index] = { ...copy[index], [field]: val };
      if (field === 'received_qty') {
        const rQty = parseFloat(val) || 0;
        const rejQty = parseFloat(copy[index].rejected_qty) || 0;
        copy[index].accepted_qty = String(Math.max(0, rQty - rejQty));
      }
      if (field === 'rejected_qty') {
        const rQty = parseFloat(copy[index].received_qty) || 0;
        const rejQty = parseFloat(val) || 0;
        copy[index].accepted_qty = String(Math.max(0, rQty - rejQty));
      }
      return copy;
    });
  };

  const handleCreateSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!selectedPO || saving) return;

    setSaving(true);
    try {
      const payload = {
        po_id: selectedPO,
        received_date: receivedDate,
        notes: notes || undefined,
        warehouse_id: receivingWarehouseId || store?.id,
        items: createItems.map((it) => ({
          product_id: it.product_id,
          ordered_qty: it.ordered_qty,
          received_qty: parseFloat(it.received_qty) || 0,
          accepted_qty: parseFloat(it.accepted_qty) || 0,
          rejected_qty: parseFloat(it.rejected_qty) || 0,
          reason: it.reason || undefined,
        })),
      };

      await grnApi.create(payload);
      setShowCreate(false);
      loadGRNs();
      alert('Goods Receipt Note (GRN) created and inventory updated successfully!');
    } catch (err: any) {
      alert(err.response?.data?.error || 'Failed to create Goods Receipt Note.');
    } finally {
      setSaving(false);
    }
  };

  const openDetail = async (grn: GRNRow) => {
    setDetailGRN(grn);
    setDetailLoading(true);
    try {
      const { data } = await grnApi.get(grn.id);
      setDetailItems(data.items || []);
    } catch (err) {
      console.error('Failed to load GRN details:', err);
    } finally {
      setDetailLoading(false);
    }
  };

  return (
    <AppLayout>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          marginBottom: '1rem',
          flexWrap: 'wrap',
          gap: '0.75rem',
        }}
      >
        <div>
          <h1 style={{ margin: 0, fontSize: '1.4rem', fontWeight: 700 }}>
            Goods Receipt Notes (GRN) & Receiving
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Inspect incoming shipments against POs, record accepted vs rejected items, and update warehouse stock
          </p>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => navigate('/purchases')}
          >
            <span>Purchase Orders</span>
          </button>

          <button
            type="button"
            className="btn btn-primary btn-sm"
            onClick={openCreateForm}
          >
            <IconPlus size={16} />
            <span>New Goods Receipt (GRN)</span>
          </button>
        </div>
      </div>

      {/* GRN History Table */}
      <div className="erp-table-container">
        <table className="erp-table">
          <thead>
            <tr>
              <th>GRN Ref</th>
              <th>PO Number</th>
              <th>Received Date</th>
              <th className="text-center">Items Inspected</th>
              <th>Notes / Remarks</th>
              <th className="text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={6} style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-muted)' }}>
                  Loading Goods Receipt Notes...
                </td>
              </tr>
            ) : grns.length === 0 ? (
              <tr>
                <td colSpan={6} style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
                  No Goods Receipt Notes recorded yet. Create one when goods arrive from suppliers.
                </td>
              </tr>
            ) : (
              grns.map((g) => (
                <tr key={g.id}>
                  <td className="text-mono font-bold" style={{ color: 'var(--primary-600)' }}>
                    #{g.id.slice(-6)}
                  </td>
                  <td className="text-mono font-bold">{g.po_no || `#${g.po_id.slice(-6)}`}</td>
                  <td className="text-mono">{g.received_date ? g.received_date.slice(0, 10) : '—'}</td>
                  <td className="text-center text-mono">
                    {g.item_count ?? (g.items ? g.items.length : '—')}
                  </td>
                  <td style={{ color: 'var(--text-secondary)' }}>{g.notes || '—'}</td>
                  <td className="text-right">
                    <button
                      type="button"
                      className="btn btn-outline btn-sm"
                      onClick={() => openDetail(g)}
                    >
                      View Receipt
                    </button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      {/* ===================================================================
          Create GRN Modal
          =================================================================== */}
      {showCreate && (
        <div className="erp-modal-backdrop" onClick={() => setShowCreate(false)}>
          <div className="erp-modal" style={{ maxWidth: 760 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 600 }}>
                Receive Goods Against Purchase Order (GRN)
              </h3>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setShowCreate(false)}
              >
                <IconClose size={18} />
              </button>
            </div>

            <form onSubmit={handleCreateSubmit}>
              <div className="erp-modal-body">
                <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: '0.75rem', marginBottom: '1rem' }}>
                  <div className="form-group">
                    <label className="form-label">Purchase Order *</label>
                    <select
                      required
                      className="form-select"
                      value={selectedPO}
                      onChange={(e) => handlePOSelect(e.target.value)}
                    >
                      <option value="">Select Pending PO</option>
                      {poOptions.map((po) => (
                        <option key={po.id} value={po.id}>
                          {po.po_no} {po.vendor_name ? `(${po.vendor_name})` : ''} [{po.status}]
                        </option>
                      ))}
                    </select>
                  </div>

                  <div className="form-group">
                    <label className="form-label">Receiving Warehouse *</label>
                    <select
                      required
                      className="form-select"
                      value={receivingWarehouseId}
                      onChange={(e) => setReceivingWarehouseId(e.target.value)}
                    >
                      {stores.map((s) => (
                        <option key={s.id} value={s.id}>
                          {s.name} ({s.location_type})
                        </option>
                      ))}
                    </select>
                  </div>

                  <div className="form-group">
                    <label className="form-label">Received Date</label>
                    <input
                      type="date"
                      className="form-input text-mono"
                      value={receivedDate}
                      onChange={(e) => setReceivedDate(e.target.value)}
                    />
                  </div>
                </div>

                {/* Items Quality Inspection Grid */}
                {itemsLoading ? (
                  <p style={{ textAlign: 'center', padding: '1rem', color: 'var(--text-muted)' }}>
                    Loading items from PO...
                  </p>
                ) : createItems.length > 0 ? (
                  <div>
                    <label className="form-label">Physical Inspection & Acceptance</label>
                    <table className="erp-table">
                      <thead>
                        <tr>
                          <th>Product</th>
                          <th className="text-center" style={{ width: 80 }}>Ordered</th>
                          <th style={{ width: 90 }}>Received</th>
                          <th style={{ width: 90 }}>Accepted</th>
                          <th style={{ width: 90 }}>Rejected</th>
                          <th style={{ width: 140 }}>Rejection Reason</th>
                        </tr>
                      </thead>
                      <tbody>
                        {createItems.map((item, idx) => (
                          <tr key={item.product_id + idx}>
                            <td style={{ fontWeight: 600 }}>{item.product_name}</td>
                            <td className="text-center text-mono font-bold">{item.ordered_qty}</td>
                            <td>
                              <input
                                type="number"
                                className="form-input text-mono"
                                value={item.received_qty}
                                onChange={(e) => handleItemChange(idx, 'received_qty', e.target.value)}
                              />
                            </td>
                            <td>
                              <input
                                type="number"
                                className="form-input text-mono font-bold"
                                style={{ color: '#16a34a' }}
                                value={item.accepted_qty}
                                onChange={(e) => handleItemChange(idx, 'accepted_qty', e.target.value)}
                              />
                            </td>
                            <td>
                              <input
                                type="number"
                                className="form-input text-mono"
                                style={{ color: '#dc2626' }}
                                value={item.rejected_qty}
                                onChange={(e) => handleItemChange(idx, 'rejected_qty', e.target.value)}
                              />
                            </td>
                            <td>
                              <input
                                type="text"
                                className="form-input"
                                placeholder="Damage, expiry, etc."
                                value={item.reason}
                                onChange={(e) => handleItemChange(idx, 'reason', e.target.value)}
                              />
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                ) : null}

                <div className="form-group" style={{ marginTop: '0.875rem' }}>
                  <label className="form-label">Inspection Notes</label>
                  <input
                    type="text"
                    className="form-input"
                    placeholder="Delivery vehicle number, seal check, carrier remarks..."
                    value={notes}
                    onChange={(e) => setNotes(e.target.value)}
                  />
                </div>
              </div>

              <div className="erp-modal-footer">
                <button
                  type="button"
                  className="btn btn-outline"
                  onClick={() => setShowCreate(false)}
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="btn btn-primary"
                  disabled={!selectedPO || saving}
                >
                  {saving ? 'Recording Receipt...' : 'Confirm Acceptance & Ingest Stock'}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* ===================================================================
          View GRN Details Modal (Printable)
          =================================================================== */}
      {detailGRN && (
        <div className="erp-modal-backdrop" onClick={() => setDetailGRN(null)}>
          <div className="erp-modal" style={{ maxWidth: 700 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <div>
                <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 700 }}>
                  Goods Receipt Note #{detailGRN.id.slice(-6)}
                </h3>
                <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                  PO: <strong>{detailGRN.po_no || detailGRN.po_id}</strong> &middot; Date: {detailGRN.received_date?.slice(0, 10)}
                </div>
              </div>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setDetailGRN(null)}
              >
                <IconClose size={18} />
              </button>
            </div>

            <div className="erp-modal-body">
              {detailLoading ? (
                <p style={{ textAlign: 'center', padding: '1.5rem', color: 'var(--text-muted)' }}>
                  Loading receipt items...
                </p>
              ) : (
                <table className="erp-table">
                  <thead>
                    <tr>
                      <th>Product</th>
                      <th className="text-center">Ordered</th>
                      <th className="text-center">Received</th>
                      <th className="text-center">Accepted</th>
                      <th className="text-center">Rejected</th>
                      <th>Reason</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detailItems.map((item, idx) => (
                      <tr key={item.id || idx}>
                        <td style={{ fontWeight: 600 }}>{item.product_name || item.product_id}</td>
                        <td className="text-center text-mono">{item.ordered_qty}</td>
                        <td className="text-center text-mono font-bold">{item.received_qty}</td>
                        <td className="text-center text-mono font-bold" style={{ color: '#16a34a' }}>
                          {item.accepted_qty}
                        </td>
                        <td className="text-center text-mono" style={{ color: item.rejected_qty > 0 ? '#dc2626' : 'inherit' }}>
                          {item.rejected_qty}
                        </td>
                        <td style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{item.reason || '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>

            <div className="erp-modal-footer">
              <button
                type="button"
                className="btn btn-outline"
                onClick={() => window.print()}
              >
                <IconPrinter size={16} />
                <span>Print GRN</span>
              </button>
              <button
                type="button"
                className="btn btn-secondary"
                onClick={() => setDetailGRN(null)}
              >
                Close
              </button>
            </div>
          </div>
        </div>
      )}
    </AppLayout>
  );
}
