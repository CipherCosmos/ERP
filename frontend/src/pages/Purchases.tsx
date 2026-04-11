import React, { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { purchaseApi, vendorApi, productApi } from '../services/api';

interface PurchaseOrder {
  id: string;
  po_no: string;
  vendor_id: string;
  vendor_name: string;
  order_date: string;
  expected_date: string | null;
  total: number;
  status: string;
}

interface VendorRow {
  id: string;
  name: string;
}

interface ProductRow {
  id: string;
  name: string;
  uom_id?: string;
  gst_rate?: number;
}

interface POLine {
  product_id: string;
  quantity: string;
  rate: string;
  discount: string;
}

interface POItemDetail {
  id: string;
  product_id: string;
  name: string;
  quantity: number;
  received_qty: number;
  rate: number;
  discount: number;
  total: number;
  uom_id?: string;
}

function lineTotalServer(rate: number, qty: number, discount: number, gstRate: number): number {
  const taxable = rate * qty - discount;
  const gst = taxable * (gstRate / 100);
  return taxable + gst;
}

export default function Purchases() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [orders, setOrders] = useState<PurchaseOrder[]>([]);
  const [vendors, setVendors] = useState<VendorRow[]>([]);
  const [products, setProducts] = useState<ProductRow[]>([]);
  const [loading, setLoading] = useState(false);
  const [statusFilter, setStatusFilter] = useState('');

  const [showCreate, setShowCreate] = useState(false);
  const [createVendor, setCreateVendor] = useState('');
  const [createExpected, setCreateExpected] = useState('');
  const [createLines, setCreateLines] = useState<POLine[]>([
    { product_id: '', quantity: '1', rate: '0', discount: '0' },
  ]);
  const [createSaving, setCreateSaving] = useState(false);

  const [detailPO, setDetailPO] = useState<PurchaseOrder | null>(null);
  const [detailVendorName, setDetailVendorName] = useState('');
  const [detailItems, setDetailItems] = useState<POItemDetail[]>([]);
  const [detailLoading, setDetailLoading] = useState(false);

  const [receivePO, setReceivePO] = useState<PurchaseOrder | null>(null);
  const [receiveLineItems, setReceiveLineItems] = useState<POItemDetail[]>([]);
  const [receiveQtyByProduct, setReceiveQtyByProduct] = useState<Record<string, string>>({});
  const [receiveLoading, setReceiveLoading] = useState(false);
  const [receiveSaving, setReceiveSaving] = useState(false);

  const loadOrders = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await purchaseApi.list({
        store_id: store.id,
        ...(statusFilter ? { status: statusFilter } : {}),
      });
      setOrders(data || []);
    } catch (err) {
      console.error('Failed to load purchases:', err);
    } finally {
      setLoading(false);
    }
  }, [store, statusFilter]);

  const loadVendorsProducts = useCallback(async () => {
    if (!store) return;
    try {
      const [vRes, pRes] = await Promise.all([
        vendorApi.list(),
        productApi.list({ store_id: store.id }),
      ]);
      setVendors((vRes.data as VendorRow[]) || []);
      setProducts((pRes.data as ProductRow[]) || []);
    } catch (err) {
      console.error(err);
    }
  }, [store]);

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user, navigate]);

  useEffect(() => {
    if (store) {
      loadOrders();
      loadVendorsProducts();
    }
  }, [store, loadOrders, loadVendorsProducts]);

  const openDetail = async (po: PurchaseOrder) => {
    setDetailPO(po);
    setDetailLoading(true);
    setDetailItems([]);
    setDetailVendorName(po.vendor_name);
    try {
      const { data } = await purchaseApi.get(po.id);
      const body = data as { po?: { vendor_id?: string }; vendor_name?: string; items?: POItemDetail[] };
      setDetailVendorName(body.vendor_name || po.vendor_name);
      setDetailItems(body.items || []);
    } catch (err) {
      console.error(err);
      alert('Failed to load purchase order');
      setDetailPO(null);
    } finally {
      setDetailLoading(false);
    }
  };

  const openReceive = async (po: PurchaseOrder) => {
    setReceivePO(po);
    setReceiveLoading(true);
    try {
      const { data } = await purchaseApi.get(po.id);
      const body = data as { items?: POItemDetail[] };
      const items = body.items || [];
      setReceiveLineItems(items);
      const m: Record<string, string> = {};
      for (const it of items) {
        const rem = Math.max(0, it.quantity - it.received_qty);
        m[it.product_id] = rem > 0 ? String(rem) : '0';
      }
      setReceiveQtyByProduct(m);
    } catch (err) {
      console.error(err);
      alert('Failed to load lines for receiving');
      setReceivePO(null);
      setReceiveLineItems([]);
    } finally {
      setReceiveLoading(false);
    }
  };

  const submitCreate = async () => {
    if (!store || !createVendor || createLines.length === 0) {
      alert('Select vendor and add at least one line');
      return;
    }
    const items: { product_id: string; quantity: number; uom_id?: string; rate: number; discount: number; total: number }[] = [];
    for (const ln of createLines) {
      if (!ln.product_id) continue;
      const qty = parseFloat(ln.quantity) || 0;
      const rate = parseFloat(ln.rate) || 0;
      const discount = parseFloat(ln.discount) || 0;
      const p = products.find((x) => x.id === ln.product_id);
      const gst = p?.gst_rate ?? 0;
      if (qty <= 0 || rate < 0) {
        alert('Each line needs quantity > 0 and valid rate');
        return;
      }
      const total = lineTotalServer(rate, qty, discount, gst);
      items.push({
        product_id: ln.product_id,
        quantity: qty,
        uom_id: p?.uom_id,
        rate,
        discount,
        total,
      });
    }
    if (items.length === 0) {
      alert('Add at least one product line');
      return;
    }
    setCreateSaving(true);
    try {
      const orderDate = new Date().toISOString().slice(0, 10);
      await purchaseApi.create({
        store_id: store.id,
        vendor_id: createVendor,
        order_date: orderDate,
        expected_date: createExpected || undefined,
        items,
      });
      setShowCreate(false);
      setCreateLines([{ product_id: '', quantity: '1', rate: '0', discount: '0' }]);
      setCreateExpected('');
      await loadOrders();
    } catch (err: unknown) {
      const ax = err as { response?: { data?: { error?: string } } };
      alert(ax.response?.data?.error || 'Failed to create PO');
    } finally {
      setCreateSaving(false);
    }
  };

  const submitReceive = async () => {
    if (!store || !receivePO) return;
    const items: { product_id: string; quantity: number }[] = [];
    for (const [productId, qStr] of Object.entries(receiveQtyByProduct)) {
      const q = parseFloat(qStr) || 0;
      if (q > 0) items.push({ product_id: productId, quantity: q });
    }
    if (items.length === 0) {
      alert('Enter quantity to receive for at least one line');
      return;
    }
    setReceiveSaving(true);
    try {
      await purchaseApi.receive({ po_id: receivePO.id, store_id: store.id, items });
      setReceivePO(null);
      setReceiveLineItems([]);
      await loadOrders();
    } catch (err: unknown) {
      const ax = err as { response?: { data?: { error?: string } } };
      alert(ax.response?.data?.error || 'Receive failed');
    } finally {
      setReceiveSaving(false);
    }
  };

  const cancelPO = async (id: string) => {
    if (!window.confirm('Cancel this purchase order? Only pending orders can be cancelled.')) return;
    try {
      await purchaseApi.cancel(id);
      await loadOrders();
      setDetailPO(null);
    } catch (err: unknown) {
      const ax = err as { response?: { data?: { error?: string } } };
      alert(ax.response?.data?.error || 'Cancel failed');
    }
  };

  if (!user || user.role === 'cashier') return null;

  const pendingCount = orders.filter((o) => o.status === 'pending').length;
  const receivedCount = orders.filter((o) => o.status === 'received').length;
  const partialCount = orders.filter((o) => o.status === 'partial').length;

  const gstForProduct = (id: string) => products.find((p) => p.id === id)?.gst_rate ?? 0;

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Purchase Orders</h1>
        <button type="button" onClick={() => navigate('/')} style={styles.backBtn}>
          ← Back
        </button>
      </div>

      <div style={styles.summary}>
        <div style={styles.summaryCard}>
          <div style={styles.summaryLabel}>Pending</div>
          <div style={styles.summaryValue}>{pendingCount}</div>
        </div>
        <div style={styles.summaryCard}>
          <div style={styles.summaryLabel}>Partial</div>
          <div style={styles.summaryValue}>{partialCount}</div>
        </div>
        <div style={styles.summaryCard}>
          <div style={styles.summaryLabel}>Received</div>
          <div style={styles.summaryValue}>{receivedCount}</div>
        </div>
      </div>

      <div style={styles.filters}>
        <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)} style={styles.select}>
          <option value="">All Status</option>
          <option value="pending">Pending</option>
          <option value="partial">Partial</option>
          <option value="received">Received</option>
          <option value="cancelled">Cancelled</option>
        </select>
        <button type="button" style={styles.createBtn} onClick={() => setShowCreate(true)}>
          + New Purchase Order
        </button>
      </div>

      {loading ? (
        <p>Loading...</p>
      ) : (
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>PO No</th>
              <th style={styles.th}>Vendor</th>
              <th style={styles.th}>Order Date</th>
              <th style={styles.th}>Expected</th>
              <th style={styles.th}>Total</th>
              <th style={styles.th}>Status</th>
              <th style={styles.th}>Action</th>
            </tr>
          </thead>
          <tbody>
            {orders.map((po) => (
              <tr key={po.id} style={styles.tr}>
                <td style={styles.td}>{po.po_no}</td>
                <td style={styles.td}>{po.vendor_name}</td>
                <td style={styles.td}>{po.order_date}</td>
                <td style={styles.td}>{po.expected_date || '-'}</td>
                <td style={styles.td}>₹{Number(po.total).toFixed(2)}</td>
                <td style={styles.td}>
                  <span
                    style={{
                      ...styles.statusBadge,
                      background:
                        po.status === 'received'
                          ? '#e8f5e9'
                          : po.status === 'pending'
                            ? '#fff3e0'
                            : po.status === 'partial'
                              ? '#e3f2fd'
                              : '#ffebee',
                    }}
                  >
                    {po.status}
                  </span>
                </td>
                <td style={styles.td}>
                  <button type="button" style={styles.viewBtn} onClick={() => openDetail(po)}>
                    View
                  </button>
                  {(po.status === 'pending' || po.status === 'partial') && (
                    <button type="button" style={styles.receiveBtn} onClick={() => openReceive(po)}>
                      Receive
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {showCreate && (
        <div style={styles.modalBackdrop}>
          <div style={styles.modal}>
            <h3>New purchase order</h3>
            <label style={styles.lbl}>Vendor</label>
            <select
              style={styles.selectFull}
              value={createVendor}
              onChange={(e) => setCreateVendor(e.target.value)}
            >
              <option value="">— Select —</option>
              {vendors.map((v) => (
                <option key={v.id} value={v.id}>
                  {v.name}
                </option>
              ))}
            </select>
            <label style={styles.lbl}>Expected date (optional)</label>
            <input
              type="date"
              style={styles.selectFull}
              value={createExpected}
              onChange={(e) => setCreateExpected(e.target.value)}
            />
            <h4 style={{ marginTop: '1rem' }}>Lines</h4>
            {createLines.map((ln, idx) => (
              <div key={idx} style={styles.lineRow}>
                <select
                  style={{ ...styles.selectFull, flex: 2 }}
                  value={ln.product_id}
                  onChange={(e) => {
                    const next = [...createLines];
                    next[idx].product_id = e.target.value;
                    setCreateLines(next);
                  }}
                >
                  <option value="">Product</option>
                  {products.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
                <input
                  type="number"
                  placeholder="Qty"
                  style={styles.inSm}
                  value={ln.quantity}
                  onChange={(e) => {
                    const next = [...createLines];
                    next[idx].quantity = e.target.value;
                    setCreateLines(next);
                  }}
                />
                <input
                  type="number"
                  placeholder="Rate"
                  style={styles.inSm}
                  value={ln.rate}
                  onChange={(e) => {
                    const next = [...createLines];
                    next[idx].rate = e.target.value;
                    setCreateLines(next);
                  }}
                />
                <input
                  type="number"
                  placeholder="Disc"
                  style={styles.inSm}
                  value={ln.discount}
                  onChange={(e) => {
                    const next = [...createLines];
                    next[idx].discount = e.target.value;
                    setCreateLines(next);
                  }}
                />
                <span style={styles.lineHint}>
                  {ln.product_id
                    ? `≈ ₹${lineTotalServer(
                        parseFloat(ln.rate) || 0,
                        parseFloat(ln.quantity) || 0,
                        parseFloat(ln.discount) || 0,
                        gstForProduct(ln.product_id),
                      ).toFixed(2)}`
                    : ''}
                </span>
                <button
                  type="button"
                  style={styles.rmBtn}
                  onClick={() => setCreateLines(createLines.filter((_, i) => i !== idx))}
                >
                  ×
                </button>
              </div>
            ))}
            <button
              type="button"
              style={styles.addLineBtn}
              onClick={() =>
                setCreateLines([...createLines, { product_id: '', quantity: '1', rate: '0', discount: '0' }])
              }
            >
              + Line
            </button>
            <div style={styles.modalActions}>
              <button type="button" style={styles.backBtn} onClick={() => setShowCreate(false)}>
                Cancel
              </button>
              <button type="button" style={styles.createBtn} disabled={createSaving} onClick={submitCreate}>
                {createSaving ? 'Saving…' : 'Create PO'}
              </button>
            </div>
          </div>
        </div>
      )}

      {detailPO && !receivePO && (
        <div style={styles.modalBackdrop}>
          <div style={styles.modal}>
            <h3>{detailPO.po_no}</h3>
            <p>
              Vendor: {detailVendorName} · Status: {detailPO.status}
            </p>
            {detailLoading ? (
              <p>Loading lines…</p>
            ) : (
              <table style={{ ...styles.table, marginTop: '0.5rem' }}>
                <thead>
                  <tr>
                    <th style={styles.th}>Product</th>
                    <th style={styles.th}>Qty</th>
                    <th style={styles.th}>Recv</th>
                    <th style={styles.th}>Total</th>
                  </tr>
                </thead>
                <tbody>
                  {detailItems.map((it) => (
                    <tr key={it.id}>
                      <td style={styles.td}>{it.name}</td>
                      <td style={styles.td}>{it.quantity}</td>
                      <td style={styles.td}>{it.received_qty}</td>
                      <td style={styles.td}>₹{it.total.toFixed(2)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            <div style={styles.modalActions}>
              {detailPO.status === 'pending' && (
                <button type="button" style={styles.dangerBtn} onClick={() => cancelPO(detailPO.id)}>
                  Cancel PO
                </button>
              )}
              <button type="button" style={styles.backBtn} onClick={() => setDetailPO(null)}>
                Close
              </button>
              {(detailPO.status === 'pending' || detailPO.status === 'partial') && (
                <button
                  type="button"
                  style={styles.receiveBtn}
                  onClick={() => {
                    const po = detailPO;
                    setDetailPO(null);
                    openReceive(po);
                  }}
                >
                  Receive stock
                </button>
              )}
            </div>
          </div>
        </div>
      )}

      {receivePO && (
        <div style={styles.modalBackdrop}>
          <div style={styles.modal}>
            <h3>Receive — {receivePO.po_no}</h3>
            <p style={styles.hint}>Quantities add to stock; cost uses catalog cost if not overridden per batch later.</p>
            {receiveLoading ? (
              <p>Loading lines…</p>
            ) : (
            <table style={{ ...styles.table, marginTop: '0.5rem' }}>
              <thead>
                <tr>
                  <th style={styles.th}>Product</th>
                  <th style={styles.th}>Remaining</th>
                  <th style={styles.th}>Receive now</th>
                </tr>
              </thead>
              <tbody>
                {Object.keys(receiveQtyByProduct).length === 0 ? (
                  <tr>
                    <td colSpan={3} style={styles.td}>
                      No lines
                    </td>
                  </tr>
                ) : (
                  Object.entries(receiveQtyByProduct).map(([pid, q]) => {
                    const it = receiveLineItems.find((x) => x.product_id === pid);
                    const rem = it ? Math.max(0, it.quantity - it.received_qty) : 0;
                    return (
                      <tr key={pid}>
                        <td style={styles.td}>{it?.name || pid}</td>
                        <td style={styles.td}>{rem}</td>
                        <td style={styles.td}>
                          <input
                            type="number"
                            style={styles.inSm}
                            min={0}
                            value={q}
                            onChange={(e) =>
                              setReceiveQtyByProduct((prev) => ({ ...prev, [pid]: e.target.value }))
                            }
                          />
                        </td>
                      </tr>
                    );
                  })
                )}
              </tbody>
            </table>
            )}
            <div style={styles.modalActions}>
              <button
                type="button"
                style={styles.backBtn}
                onClick={() => {
                  setReceivePO(null);
                  setReceiveLineItems([]);
                }}
              >
                Cancel
              </button>
              <button
                type="button"
                style={styles.createBtn}
                disabled={receiveSaving || receiveLoading}
                onClick={submitReceive}
              >
                {receiveSaving ? 'Posting…' : 'Confirm receive'}
              </button>
            </div>
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
  summary: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(120px, 1fr))', gap: '1rem', marginBottom: '1rem' },
  summaryCard: { background: 'white', padding: '1rem', borderRadius: '8px', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  summaryLabel: { fontSize: '0.875rem', color: '#666' },
  summaryValue: { fontSize: '1.5rem', fontWeight: 'bold', color: '#1976d2' },
  filters: { display: 'flex', gap: '1rem', marginBottom: '1rem', flexWrap: 'wrap' },
  select: { padding: '0.75rem', border: '1px solid #ddd', borderRadius: '4px' },
  createBtn: { padding: '0.75rem 1.5rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  table: { width: '100%', background: 'white', borderCollapse: 'collapse', boxShadow: '0 1px 3px rgba(0,0,0,0.1)' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  tr: { borderBottom: '1px solid #eee' },
  statusBadge: { padding: '0.25rem 0.5rem', borderRadius: '4px', fontSize: '0.75rem', textTransform: 'capitalize' },
  viewBtn: { padding: '0.25rem 0.75rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer', marginRight: '0.25rem' },
  receiveBtn: { padding: '0.25rem 0.75rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  modalBackdrop: {
    position: 'fixed',
    inset: 0,
    background: 'rgba(0,0,0,0.4)',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    zIndex: 1000,
    padding: '1rem',
  },
  modal: {
    background: 'white',
    borderRadius: '8px',
    padding: '1.25rem',
    maxWidth: '640px',
    width: '100%',
    maxHeight: '90vh',
    overflow: 'auto',
    boxShadow: '0 4px 24px rgba(0,0,0,0.15)',
  },
  lbl: { display: 'block', marginTop: '0.5rem', marginBottom: '0.25rem', fontWeight: 500 },
  selectFull: { width: '100%', padding: '0.5rem', border: '1px solid #ddd', borderRadius: '4px' },
  lineRow: { display: 'flex', flexWrap: 'wrap', gap: '0.5rem', alignItems: 'center', marginBottom: '0.5rem' },
  inSm: { width: '72px', padding: '0.4rem', border: '1px solid #ddd', borderRadius: '4px' },
  lineHint: { fontSize: '0.8rem', color: '#666', minWidth: '80px' },
  rmBtn: { padding: '0.25rem 0.5rem', background: '#f44336', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  addLineBtn: { marginTop: '0.5rem', padding: '0.4rem 0.8rem', background: '#eee', border: '1px solid #ccc', borderRadius: '4px', cursor: 'pointer' },
  modalActions: { display: 'flex', gap: '0.5rem', marginTop: '1rem', flexWrap: 'wrap' },
  dangerBtn: { padding: '0.5rem 1rem', background: '#c62828', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  hint: { color: '#666', fontSize: '0.875rem' },
};
