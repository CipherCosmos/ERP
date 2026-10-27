import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { storeApi, stockApi, productApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { Store, Product } from '../types';
import {
  IconWarehouse,
  IconTransfer,
  IconPlus,
  IconPrinter,
  IconCheck,
  IconAlert,
  IconRefresh,
} from '../components/Icons';

interface StockItem {
  id: string;
  product_id: string;
  product_name?: string;
  batch_no: string;
  quantity: number;
  reserved_qty: number;
  expiry_date: string | null;
  cost_price: number;
}

interface TransferLine {
  product_id: string;
  product_name: string;
  batch_no: string;
  available_qty: number;
  transfer_qty: number;
  cost_price: number;
}

export default function StockTransfer() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [stores, setStores] = useState<Store[]>([]);
  const [fromStoreId, setFromStoreId] = useState('');
  const [toStoreId, setToStoreId] = useState('');

  // Source Stock Items
  const [availableStock, setAvailableStock] = useState<StockItem[]>([]);
  const [productsMap, setProductsMap] = useState<Record<string, Product>>({});
  const [stockLoading, setStockLoading] = useState(false);

  // Multi-item transfer lines
  const [transferLines, setTransferLines] = useState<TransferLine[]>([]);
  const [selectedStockKey, setSelectedStockKey] = useState('');
  const [addQty, setAddQty] = useState(1);

  // Completed Transfer Note
  const [lastTransferNote, setLastTransferNote] = useState<any | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    loadStores();
  }, [user]);

  const loadStores = async () => {
    try {
      const { data } = await storeApi.list();
      const list = (data as Store[]) || [];
      setStores(list);
      if (store) {
        setFromStoreId(store.id);
        const destination = list.find((s) => s.id !== store.id);
        if (destination) setToStoreId(destination.id);
      }
    } catch (err) {
      console.error('Failed to load stores:', err);
    }
  };

  useEffect(() => {
    if (fromStoreId) {
      loadSourceStock(fromStoreId);
    } else {
      setAvailableStock([]);
    }
  }, [fromStoreId]);

  const loadSourceStock = async (storeId: string) => {
    setStockLoading(true);
    try {
      const [stockRes, prodRes] = await Promise.all([
        stockApi.list(storeId),
        productApi.list({ store_id: storeId }),
      ]);
      const prods: Product[] = prodRes.data || [];
      const map: Record<string, Product> = {};
      prods.forEach((p) => {
        map[p.id] = p;
      });
      setProductsMap(map);

      const items: StockItem[] = (stockRes.data || []).filter(
        (item: StockItem) => item.quantity > 0
      );
      setAvailableStock(items);
    } catch (err) {
      console.error('Failed to load source stock:', err);
      setAvailableStock([]);
    } finally {
      setStockLoading(false);
    }
  };

  const handleAddLine = () => {
    if (!selectedStockKey || addQty <= 0) return;
    const [productId, batchNo] = selectedStockKey.split('|');
    const stockItem = availableStock.find(
      (s) => s.product_id === productId && s.batch_no === batchNo
    );
    if (!stockItem) return;

    const availableUnreserved = stockItem.quantity - (stockItem.reserved_qty || 0);
    if (addQty > availableUnreserved) {
      alert(`Cannot transfer more than available unreserved quantity (${availableUnreserved}).`);
      return;
    }

    const prod = productsMap[productId];
    const prodName = prod ? `${prod.name} ${prod.brand ? `(${prod.brand})` : ''}` : 'Product';

    setTransferLines((prev) => {
      const existing = prev.find((l) => l.product_id === productId && l.batch_no === batchNo);
      if (existing) {
        return prev.map((l) =>
          l.product_id === productId && l.batch_no === batchNo
            ? { ...l, transfer_qty: l.transfer_qty + addQty }
            : l
        );
      }
      return [
        ...prev,
        {
          product_id: productId,
          product_name: prodName,
          batch_no: batchNo,
          available_qty: availableUnreserved,
          transfer_qty: addQty,
          cost_price: stockItem.cost_price,
        },
      ];
    });

    setSelectedStockKey('');
    setAddQty(1);
  };

  const removeTransferLine = (productId: string, batchNo: string) => {
    setTransferLines((prev) =>
      prev.filter((l) => !(l.product_id === productId && l.batch_no === batchNo))
    );
  };

  const handleExecuteTransfer = async () => {
    if (transferLines.length === 0 || !fromStoreId || !toStoreId || isSubmitting) return;

    if (fromStoreId === toStoreId) {
      alert('Source location and Destination location must be different.');
      return;
    }

    setIsSubmitting(true);
    const transferId = 'TRF-' + Date.now().toString().slice(-6);

    try {
      // Execute each transfer line transactionally
      for (const line of transferLines) {
        await stockApi.transfer({
          product_id: line.product_id,
          from_store_id: fromStoreId,
          to_store_id: toStoreId,
          quantity: line.transfer_qty,
          batch_no: line.batch_no,
        });
      }

      const fromStoreObj = stores.find((s) => s.id === fromStoreId);
      const toStoreObj = stores.find((s) => s.id === toStoreId);

      setLastTransferNote({
        transfer_id: transferId,
        date: new Date().toLocaleString(),
        from: fromStoreObj ? `${fromStoreObj.name} (${fromStoreObj.location_type})` : fromStoreId,
        to: toStoreObj ? `${toStoreObj.name} (${toStoreObj.location_type})` : toStoreId,
        user: user?.username,
        lines: [...transferLines],
      });

      setTransferLines([]);
      loadSourceStock(fromStoreId);
      alert(`Transfer ${transferId} completed successfully! Stock updated at both locations.`);
    } catch (err: any) {
      console.error('Transfer failed:', err);
      alert(err.response?.data?.error || 'Failed to complete stock transfer.');
    } finally {
      setIsSubmitting(false);
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
            Multi-Warehouse Stock Transfers
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Inter-warehouse stock movements, transit dispatch notes, and balance reconciliation
          </p>
        </div>

        {lastTransferNote && (
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => window.print()}
          >
            <IconPrinter size={16} />
            <span>Print Transfer Challan</span>
          </button>
        )}
      </div>

      {/* Transfer Dispatch Note (Printable / Visible after successful transfer) */}
      {lastTransferNote && (
        <div
          className="erp-card"
          style={{
            marginBottom: '1.25rem',
            border: '2px solid #22c55e',
            backgroundColor: '#f0fdf4',
          }}
        >
          <div className="erp-card-header" style={{ backgroundColor: '#dcfce7' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
              <IconCheck size={18} color="#15803d" />
              <strong style={{ color: '#15803d' }}>
                Transfer Challan #{lastTransferNote.transfer_id} Dispatched & Received
              </strong>
            </div>
            <span style={{ fontSize: '0.75rem', color: '#166534' }}>{lastTransferNote.date}</span>
          </div>
          <div className="erp-card-body" style={{ padding: '0.875rem' }}>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: '0.5rem', marginBottom: '0.75rem', fontSize: '0.85rem' }}>
              <div>From: <strong>{lastTransferNote.from}</strong></div>
              <div>To: <strong>{lastTransferNote.to}</strong></div>
              <div>Authorized By: <strong>{lastTransferNote.user}</strong></div>
            </div>
            <table className="erp-table">
              <thead>
                <tr>
                  <th>Product</th>
                  <th>Batch No</th>
                  <th className="text-right">Transferred Qty</th>
                </tr>
              </thead>
              <tbody>
                {lastTransferNote.lines.map((l: TransferLine, idx: number) => (
                  <tr key={idx}>
                    <td>{l.product_name}</td>
                    <td className="text-mono">{l.batch_no}</td>
                    <td className="text-right text-mono font-bold">{l.transfer_qty}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* Transfer Builder */}
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fit, minmax(420px, 1fr))',
          gap: '1.25rem',
        }}
      >
        {/* Source & Destination Configuration */}
        <div className="erp-card">
          <div className="erp-card-header">
            <h3 style={{ margin: 0, fontSize: '0.95rem', fontWeight: 600 }}>
              1. Transfer Locations & Stock Selection
            </h3>
          </div>
          <div className="erp-card-body">
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem', marginBottom: '1rem' }}>
              <div className="form-group">
                <label className="form-label">Source (From) Location *</label>
                <select
                  className="form-select"
                  value={fromStoreId}
                  onChange={(e) => setFromStoreId(e.target.value)}
                >
                  <option value="">Select Source</option>
                  {stores.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.name} ({s.location_type === 'warehouse' ? 'Warehouse' : 'Store'})
                    </option>
                  ))}
                </select>
              </div>

              <div className="form-group">
                <label className="form-label">Destination (To) Location *</label>
                <select
                  className="form-select"
                  value={toStoreId}
                  onChange={(e) => setToStoreId(e.target.value)}
                >
                  <option value="">Select Destination</option>
                  {stores.map((s) => (
                    <option key={s.id} value={s.id} disabled={s.id === fromStoreId}>
                      {s.name} ({s.location_type === 'warehouse' ? 'Warehouse' : 'Store'})
                    </option>
                  ))}
                </select>
              </div>
            </div>

            {/* Product & Batch Picker */}
            <div style={{ borderTop: '1px solid var(--border-light)', paddingTop: '0.75rem' }}>
              <label className="form-label">Available Product & Batch at Source</label>
              {stockLoading ? (
                <p style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>Loading source inventory...</p>
              ) : availableStock.length === 0 ? (
                <p style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>
                  No available stock found at selected source location.
                </p>
              ) : (
                <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end', marginTop: '0.25rem' }}>
                  <div style={{ flex: 3 }}>
                    <select
                      className="form-select"
                      value={selectedStockKey}
                      onChange={(e) => setSelectedStockKey(e.target.value)}
                    >
                      <option value="">Select Item & Batch to Move</option>
                      {availableStock.map((s) => {
                        const prod = productsMap[s.product_id];
                        const label = prod
                          ? `${prod.name} ${prod.brand ? `[${prod.brand}]` : ''}`
                          : s.product_id;
                        return (
                          <option key={`${s.product_id}|${s.batch_no}`} value={`${s.product_id}|${s.batch_no}`}>
                            {label} &middot; Batch: {s.batch_no} &middot; Avail: {s.quantity - s.reserved_qty}
                          </option>
                        );
                      })}
                    </select>
                  </div>
                  <div style={{ width: 90 }}>
                    <input
                      type="number"
                      min="1"
                      className="form-input text-mono"
                      placeholder="Qty"
                      value={addQty}
                      onChange={(e) => setAddQty(parseFloat(e.target.value) || 1)}
                    />
                  </div>
                  <button
                    type="button"
                    className="btn btn-outline"
                    onClick={handleAddLine}
                    disabled={!selectedStockKey}
                  >
                    <IconPlus size={16} />
                    <span>Add</span>
                  </button>
                </div>
              )}
            </div>
          </div>
        </div>

        {/* Transfer Manifest & Execution */}
        <div className="erp-card">
          <div className="erp-card-header">
            <h3 style={{ margin: 0, fontSize: '0.95rem', fontWeight: 600 }}>
              2. Transfer Manifest ({transferLines.length} Items)
            </h3>
          </div>
          <div className="erp-card-body" style={{ padding: 0 }}>
            <table className="erp-table">
              <thead>
                <tr>
                  <th>Product & Brand</th>
                  <th>Batch</th>
                  <th className="text-right">Qty to Move</th>
                  <th className="text-center"></th>
                </tr>
              </thead>
              <tbody>
                {transferLines.length === 0 ? (
                  <tr>
                    <td colSpan={4} style={{ textAlign: 'center', padding: '2.5rem', color: 'var(--text-muted)' }}>
                      No items added to transfer manifest yet. Select items from the left.
                    </td>
                  </tr>
                ) : (
                  transferLines.map((line) => (
                    <tr key={`${line.product_id}|${line.batch_no}`}>
                      <td style={{ fontWeight: 600 }}>{line.product_name}</td>
                      <td className="text-mono" style={{ fontSize: '0.8rem' }}>{line.batch_no}</td>
                      <td className="text-right text-mono font-bold" style={{ color: 'var(--primary-600)' }}>
                        {line.transfer_qty}
                      </td>
                      <td className="text-center">
                        <button
                          type="button"
                          className="btn btn-outline btn-sm"
                          style={{ padding: '0.2rem 0.4rem', border: 'none', color: '#dc2626' }}
                          onClick={() => removeTransferLine(line.product_id, line.batch_no)}
                        >
                          ✕
                        </button>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>

            <div style={{ padding: '1rem', borderTop: '1px solid var(--border-light)' }}>
              <button
                type="button"
                className="btn btn-primary"
                style={{ width: '100%', justifyContent: 'center' }}
                disabled={transferLines.length === 0 || !fromStoreId || !toStoreId || isSubmitting}
                onClick={handleExecuteTransfer}
              >
                <IconTransfer size={18} />
                <span>{isSubmitting ? 'Transferring Stock...' : 'Confirm & Dispatch Transfer'}</span>
              </button>
            </div>
          </div>
        </div>
      </div>
    </AppLayout>
  );
}
