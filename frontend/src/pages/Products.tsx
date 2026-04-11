import React, { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { productApi, categoryApi, barcodeApi, uomApi } from '../services/api';

interface Product {
  id: string;
  name: string;
  name_hi: string;
  name_kn: string;
  category_id: string;
  category_name?: string;
  hsn_code: string;
  barcode: string;
  uom_id: string;
  uom_name?: string;
  base_unit_qty: number;
  gst_rate: number;
  allow_decimal: boolean;
  min_stock: number;
  sell_price: number;
  mrp: number;
  cost_price?: number;
  wholesale_sell_price?: number;
  stock?: number;
}

interface Category {
  id: string;
  name: string;
}

interface UOM {
  id: string;
  code: string;
  name: string;
}

interface PackingUnit {
  id: string;
  product_id: string;
  label: string;
  qty_in_parent: number;
  barcode?: string;
  parent_unit_id?: string;
  sort_order: number;
}

export default function Products() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();
  const [products, setProducts] = useState<Product[]>([]);
  const [categories, setCategories] = useState<Category[]>([]);
  const [uoms, setUoms] = useState<UOM[]>([]);
  const [loading, setLoading] = useState(false);
  const [search, setSearch] = useState('');
  const [showModal, setShowModal] = useState(false);
  const [form, setForm] = useState({
    name: '', name_hi: '', name_kn: '', category_id: '', hsn_code: '', barcode: '',
    uom_id: '', base_unit_qty: 1, gst_rate: 0, allow_decimal: false, min_stock: 0
  });
  const [priceForm, setPriceForm] = useState({ mrp: 0, cost_price: 0, sell_price: 0, wholesale_sell_price: 0 });
  const [selectedProduct, setSelectedProduct] = useState<Product | null>(null);
  const [unitsProduct, setUnitsProduct] = useState<Product | null>(null);
  const [packingUnits, setPackingUnits] = useState<PackingUnit[]>([]);
  const [unitsLoading, setUnitsLoading] = useState(false);
  const [unitForm, setUnitForm] = useState({ label: '', qty_in_parent: 1, barcode: '', sort_order: 0 });

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user]);

  useEffect(() => {
    if (store) {
      loadProducts();
      loadCategories();
      loadUoms();
    }
  }, [store, search]);

  const loadProducts = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await productApi.list({ store_id: store.id, search: search || undefined });
      setProducts(data || []);
    } catch (err) {
      console.error('Failed to load products:', err);
    } finally {
      setLoading(false);
    }
  };

  const loadCategories = async () => {
    try {
      const { data } = await categoryApi.list(store.id);
      setCategories(data || []);
    } catch (err) {
      console.error('Failed to load categories:', err);
    }
  };

  const loadUoms = async () => {
    try {
      const { data } = await uomApi.list();
      setUoms(data || []);
    } catch (err) {
      console.error('Failed to load UOMs:', err);
    }
  };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await productApi.create(form as any);
      setShowModal(false);
      setForm({ name: '', name_hi: '', name_kn: '', category_id: '', hsn_code: '', barcode: '', uom_id: '', base_unit_qty: 1, gst_rate: 0, allow_decimal: false, min_stock: 0 });
      loadProducts();
    } catch (err) {
      console.error('Failed to create product:', err);
    }
  };

  const handleSetPrice = async (productId: string) => {
    if (!store) return;
    try {
      await productApi.setPrice({ product_id: productId, store_id: store.id, ...priceForm });
      setSelectedProduct(null);
      setPriceForm({ mrp: 0, cost_price: 0, sell_price: 0, wholesale_sell_price: 0 });
      loadProducts();
    } catch (err) {
      console.error('Failed to set price:', err);
    }
  };

  const openPackingUnits = async (p: Product) => {
    setUnitsProduct(p);
    setUnitForm({ label: '', qty_in_parent: 1, barcode: '', sort_order: 0 });
    setUnitsLoading(true);
    try {
      const { data } = await productApi.listPackingUnits(p.id);
      setPackingUnits((data as PackingUnit[]) || []);
    } catch (err) {
      console.error(err);
      setPackingUnits([]);
    } finally {
      setUnitsLoading(false);
    }
  };

  const handleCreatePackingUnit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!unitsProduct || !unitForm.label.trim()) return;
    try {
      await productApi.createPackingUnit(unitsProduct.id, {
        label: unitForm.label.trim(),
        qty_in_parent: unitForm.qty_in_parent || 1,
        barcode: unitForm.barcode.trim() || undefined,
        sort_order: unitForm.sort_order,
      });
      setUnitForm({ label: '', qty_in_parent: 1, barcode: '', sort_order: 0 });
      const { data } = await productApi.listPackingUnits(unitsProduct.id);
      setPackingUnits((data as PackingUnit[]) || []);
    } catch (err) {
      console.error('Failed to add packing unit:', err);
    }
  };

  const handleGenerateBarcode = async () => {
    try {
      const { data } = await barcodeApi.generate({ category_id: form.category_id || undefined });
      setForm({ ...form, barcode: (data as { barcode?: string }).barcode || '' });
    } catch (err) {
      console.error('Failed to generate barcode:', err);
    }
  };

  if (!user || user.role === 'cashier') return null;

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <h1>Products</h1>
        <button onClick={() => navigate('/')} style={styles.backBtn}>← Back</button>
      </div>

      <div style={styles.toolbar}>
        <input type="text" placeholder="Search products..." value={search} onChange={e => setSearch(e.target.value)} style={styles.searchInput} />
        <button onClick={() => setShowModal(true)} style={styles.addBtn}>+ Add Product</button>
      </div>

      {loading ? (
        <p>Loading...</p>
      ) : (
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Name</th>
              <th style={styles.th}>Barcode</th>
              <th style={styles.th}>Category</th>
              <th style={styles.th}>HSN</th>
              <th style={styles.th}>GST %</th>
              <th style={styles.th}>MRP</th>
              <th style={styles.th}>Sell Price</th>
              <th style={styles.th}>Stock</th>
              <th style={styles.th}>Units</th>
              <th style={styles.th}>Action</th>
            </tr>
          </thead>
          <tbody>
            {products.map((p) => (
              <tr key={p.id} style={styles.tr}>
                <td style={styles.td}>{p.name}</td>
                <td style={styles.td}>{p.barcode}</td>
                <td style={styles.td}>{p.category_name || '-'}</td>
                <td style={styles.td}>{p.hsn_code || '-'}</td>
                <td style={styles.td}>{p.gst_rate}%</td>
                <td style={styles.td}>₹{p.mrp.toFixed(2)}</td>
                <td style={styles.td}>₹{p.sell_price.toFixed(2)}</td>
                <td style={styles.td}>{(p.stock || 0).toFixed(2)}</td>
                <td style={styles.td}>
                  <button type="button" onClick={() => openPackingUnits(p)} style={styles.unitsBtn}>
                    Packing units
                  </button>
                </td>
                <td style={styles.td}>
                  <button onClick={() => { setSelectedProduct(p); setPriceForm({ mrp: p.mrp, cost_price: p.cost_price || 0, sell_price: p.sell_price, wholesale_sell_price: p.wholesale_sell_price || 0 }); }} style={styles.priceBtn}>Set Price</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {showModal && (
        <div style={styles.modal}>
          <div style={styles.modalContent}>
            <h2>Add Product</h2>
            <form onSubmit={handleCreate}>
              <div style={styles.formRow}>
                <input type="text" placeholder="Name *" value={form.name} onChange={e => setForm({...form, name: e.target.value})} style={styles.input} required />
                <input type="text" placeholder="Name (Hindi)" value={form.name_hi} onChange={e => setForm({...form, name_hi: e.target.value})} style={styles.input} />
              </div>
              <div style={styles.formRow}>
                <input type="text" placeholder="Name (Kannada)" value={form.name_kn} onChange={e => setForm({...form, name_kn: e.target.value})} style={styles.input} />
                <select value={form.category_id} onChange={e => setForm({...form, category_id: e.target.value})} style={styles.input} required>
                  <option value="">Select Category *</option>
                  {categories.map(c => <option key={c.id} value={c.id}>{c.name}</option>)}
                </select>
              </div>
              <div style={styles.formRow}>
                <input type="text" placeholder="HSN Code" value={form.hsn_code} onChange={e => setForm({...form, hsn_code: e.target.value})} style={styles.input} />
                <input type="text" placeholder="Barcode" value={form.barcode} onChange={e => setForm({...form, barcode: e.target.value})} style={styles.input} />
                <button type="button" onClick={handleGenerateBarcode} style={styles.genBtn}>Generate</button>
              </div>
              <div style={styles.formRow}>
                <select value={form.uom_id} onChange={e => setForm({...form, uom_id: e.target.value})} style={styles.input} required>
                  <option value="">Unit *</option>
                  {uoms.map(u => <option key={u.id} value={u.id}>{u.name} ({u.code})</option>)}
                </select>
                <input type="number" placeholder="GST Rate %" value={form.gst_rate} onChange={e => setForm({...form, gst_rate: parseFloat(e.target.value) || 0})} style={styles.input} />
              </div>
              <div style={styles.formRow}>
                <input type="number" placeholder="Base Qty" value={form.base_unit_qty} onChange={e => setForm({...form, base_unit_qty: parseFloat(e.target.value) || 1})} style={styles.input} />
                <input type="number" placeholder="Min Stock" value={form.min_stock} onChange={e => setForm({...form, min_stock: parseFloat(e.target.value) || 0})} style={styles.input} />
              </div>
              <label style={styles.checkbox}>
                <input type="checkbox" checked={form.allow_decimal} onChange={e => setForm({...form, allow_decimal: e.target.checked})} />
                Allow Decimal Quantity
              </label>
              <div style={styles.modalActions}>
                <button type="button" onClick={() => setShowModal(false)} style={styles.cancelBtn}>Cancel</button>
                <button type="submit" style={styles.submitBtn}>Save</button>
              </div>
            </form>
          </div>
        </div>
      )}

      {unitsProduct && (
        <div style={styles.modal}>
          <div style={styles.modalContent}>
            <h2>Packing units — {unitsProduct.name}</h2>
            <p style={styles.hint}>Optional alternate barcodes (e.g. case/piece). Lookup uses these when stock is summed on the base product.</p>
            {unitsLoading ? (
              <p>Loading…</p>
            ) : (
              <table style={{ ...styles.table, marginBottom: '1rem' }}>
                <thead>
                  <tr>
                    <th style={styles.th}>Label</th>
                    <th style={styles.th}>Qty in parent</th>
                    <th style={styles.th}>Barcode</th>
                  </tr>
                </thead>
                <tbody>
                  {packingUnits.length === 0 ? (
                    <tr>
                      <td colSpan={3} style={styles.td}>
                        No packing units yet.
                      </td>
                    </tr>
                  ) : (
                    packingUnits.map((u) => (
                      <tr key={u.id}>
                        <td style={styles.td}>{u.label}</td>
                        <td style={styles.td}>{u.qty_in_parent}</td>
                        <td style={styles.td}>{u.barcode || '—'}</td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            )}
            <form onSubmit={handleCreatePackingUnit}>
              <h3 style={{ fontSize: '1rem', margin: '0 0 0.5rem' }}>Add unit</h3>
              <div style={styles.formRow}>
                <input
                  type="text"
                  placeholder="Label (e.g. Case of 12)"
                  value={unitForm.label}
                  onChange={(e) => setUnitForm({ ...unitForm, label: e.target.value })}
                  style={styles.input}
                  required
                />
                <input
                  type="number"
                  placeholder="Qty in parent"
                  value={unitForm.qty_in_parent}
                  onChange={(e) => setUnitForm({ ...unitForm, qty_in_parent: parseFloat(e.target.value) || 1 })}
                  style={styles.input}
                  min={0.01}
                  step={0.01}
                />
              </div>
              <div style={styles.formRow}>
                <input
                  type="text"
                  placeholder="Barcode (optional)"
                  value={unitForm.barcode}
                  onChange={(e) => setUnitForm({ ...unitForm, barcode: e.target.value })}
                  style={styles.input}
                />
                <input
                  type="number"
                  placeholder="Sort order"
                  value={unitForm.sort_order}
                  onChange={(e) => setUnitForm({ ...unitForm, sort_order: parseInt(e.target.value, 10) || 0 })}
                  style={styles.input}
                />
              </div>
              <div style={styles.modalActions}>
                <button type="button" onClick={() => setUnitsProduct(null)} style={styles.cancelBtn}>
                  Close
                </button>
                <button type="submit" style={styles.submitBtn}>
                  Add unit
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {selectedProduct && (
        <div style={styles.modal}>
          <div style={styles.modalContent}>
            <h2>Set Price - {selectedProduct.name}</h2>
            <div style={styles.formRow}>
              <input type="number" placeholder="MRP" value={priceForm.mrp} onChange={e => setPriceForm({...priceForm, mrp: parseFloat(e.target.value) || 0})} style={styles.input} />
              <input type="number" placeholder="Cost Price" value={priceForm.cost_price} onChange={e => setPriceForm({...priceForm, cost_price: parseFloat(e.target.value) || 0})} style={styles.input} />
              <input type="number" placeholder="Sell Price *" value={priceForm.sell_price} onChange={e => setPriceForm({...priceForm, sell_price: parseFloat(e.target.value) || 0})} style={styles.input} />
              <input type="number" placeholder="Wholesale sell" value={priceForm.wholesale_sell_price} onChange={e => setPriceForm({...priceForm, wholesale_sell_price: parseFloat(e.target.value) || 0})} style={styles.input} />
            </div>
            <div style={styles.modalActions}>
              <button onClick={() => setSelectedProduct(null)} style={styles.cancelBtn}>Cancel</button>
              <button onClick={() => handleSetPrice(selectedProduct.id)} style={styles.submitBtn}>Save Price</button>
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
  toolbar: { display: 'flex', gap: '1rem', marginBottom: '1rem' },
  searchInput: { flex: 1, padding: '0.75rem', border: '1px solid #ddd', borderRadius: '4px' },
  addBtn: { padding: '0.75rem 1.5rem', background: '#4caf50', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  table: { width: '100%', background: 'white', borderCollapse: 'collapse', boxShadow: '0 1px 3px rgba(0,0,0,0.1)', fontSize: '0.875rem' },
  th: { padding: '0.75rem', textAlign: 'left', borderBottom: '2px solid #ddd', background: '#f9f9f9' },
  td: { padding: '0.75rem', borderBottom: '1px solid #eee' },
  tr: { borderBottom: '1px solid #eee' },
  priceBtn: { padding: '0.25rem 0.5rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer', fontSize: '0.75rem' },
  unitsBtn: { padding: '0.25rem 0.5rem', background: '#5c6bc0', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer', fontSize: '0.75rem' },
  modal: { position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.5)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000 },
  modalContent: { background: 'white', padding: '1.5rem', borderRadius: '8px', width: '560px', maxWidth: '90%', maxHeight: '90vh', overflow: 'auto' },
  hint: { color: '#666', fontSize: '0.8rem', margin: '0 0 0.75rem' },
  formRow: { display: 'flex', gap: '0.5rem', marginBottom: '0.5rem', flexWrap: 'wrap' },
  input: { flex: 1, minWidth: '120px', padding: '0.5rem', border: '1px solid #ddd', borderRadius: '4px' },
  genBtn: { padding: '0.5rem 1rem', background: '#ff9800', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  checkbox: { display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '1rem' },
  modalActions: { display: 'flex', gap: '1rem', justifyContent: 'flex-end', marginTop: '1rem' },
  cancelBtn: { padding: '0.75rem 1.5rem', background: '#ddd', border: 'none', borderRadius: '4px', cursor: 'pointer' },
  submitBtn: { padding: '0.75rem 1.5rem', background: '#1976d2', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' },
};