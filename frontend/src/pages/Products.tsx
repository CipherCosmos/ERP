import React, { useState, useEffect, useMemo } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { productApi, categoryApi, barcodeApi, uomApi, stockApi, storeApi } from '../services/api';
import { AppLayout } from '../components/Layout/AppLayout';
import { BarcodeScannerModal } from '../components/BarcodeScannerModal';
import { RapidBarcodeIntakeModal } from '../components/RapidBarcodeIntakeModal';
import {
  IconPlus,
  IconSearch,
  IconFilter,
  IconBarcode,
  IconRefresh,
  IconClose,
  IconCheck,
  IconAlert,
  IconWarehouse,
} from '../components/Icons';
import { Product, Store } from '../types';

interface ExtendedProduct extends Product {
  category_name?: string;
  uom_name?: string;
  stock?: number;
  cost_price?: number;
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
  const [searchParams] = useSearchParams();
  const { user, store } = useAppStore();

  const [products, setProducts] = useState<ExtendedProduct[]>([]);
  const [categories, setCategories] = useState<Category[]>([]);
  const [uoms, setUoms] = useState<UOM[]>([]);
  const [stores, setStores] = useState<Store[]>([]);
  const [loading, setLoading] = useState(false);

  // Filters
  const [search, setSearch] = useState(searchParams.get('barcode') || '');
  const [selectedCategory, setSelectedCategory] = useState('');
  const [selectedBrand, setSelectedBrand] = useState('');
  const [stockStatusFilter, setStockStatusFilter] = useState<'all' | 'in_stock' | 'low_stock' | 'out_of_stock'>('all');

  // Modals
  const [showCreateModal, setShowCreateModal] = useState(false);
  const [showRapidIntakeModal, setShowRapidIntakeModal] = useState(false);
  const [detailProduct, setDetailProduct] = useState<ExtendedProduct | null>(null);
  const [scannerOpen, setScannerOpen] = useState(false);

  // Details View State
  const [packingUnits, setPackingUnits] = useState<PackingUnit[]>([]);
  const [productWarehouseStock, setProductWarehouseStock] = useState<any[]>([]);
  const [newUnitLabel, setNewUnitLabel] = useState('');
  const [newUnitQty, setNewUnitQty] = useState(1);
  const [newUnitBarcode, setNewUnitBarcode] = useState('');

  // Create Form State
  const [form, setForm] = useState({
    name: '',
    brand: '',
    description: '',
    category_id: '',
    hsn_code: '',
    sac_code: '',
    barcode: '',
    uom_id: '',
    base_unit_qty: 1,
    gst_rate: 0,
    allow_decimal: false,
    min_stock: 5,
    mrp: 0,
    cost_price: 0,
    sell_price: 0,
    wholesale_sell_price: 0,
    opening_stock: 0,
    warehouse_id: '',
    batch_no: '',
    expiry_date: '',
  });

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
  }, [user]);

  useEffect(() => {
    if (store) {
      loadProducts();
      loadMetadata();
    }
  }, [store?.id, search, selectedCategory, selectedBrand]);

  const loadMetadata = async () => {
    try {
      const [catsRes, uomsRes, storesRes] = await Promise.all([
        categoryApi.list(store!.id),
        uomApi.list(),
        storeApi.list(),
      ]);
      setCategories(catsRes.data || []);
      setUoms(uomsRes.data || []);
      setStores(storesRes.data || []);
    } catch (err) {
      console.warn('Metadata load error:', err);
    }
  };

  const loadProducts = async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await productApi.list({
        store_id: store.id,
        category_id: selectedCategory || undefined,
        barcode: undefined,
        search: search || undefined,
      });

      // Also get low stock counts
      const productList = (data as ExtendedProduct[]) || [];
      setProducts(productList);
    } catch (err) {
      console.error('Failed to load products:', err);
    } finally {
      setLoading(false);
    }
  };

  // Distinct brands list for filtering
  const availableBrands = useMemo(() => {
    const brandsSet = new Set<string>();
    products.forEach((p) => {
      if (p.brand && p.brand.trim() !== '') {
        brandsSet.add(p.brand.trim());
      }
    });
    return Array.from(brandsSet).sort();
  }, [products]);

  // Filtered products list
  const filteredProducts = useMemo(() => {
    return products.filter((p) => {
      if (selectedBrand && p.brand !== selectedBrand) return false;
      const stock = p.stock ?? 0;
      if (stockStatusFilter === 'in_stock' && stock <= 0) return false;
      if (stockStatusFilter === 'out_of_stock' && stock > 0) return false;
      if (stockStatusFilter === 'low_stock' && (stock > p.min_stock || stock === 0)) return false;
      return true;
    });
  }, [products, selectedBrand, stockStatusFilter]);

  const openProductDetails = async (product: ExtendedProduct) => {
    setDetailProduct(product);
    try {
      const [unitsRes, stockRes] = await Promise.all([
        productApi.listPackingUnits(product.id),
        stockApi.getProductStock(product.id, store!.id),
      ]);
      setPackingUnits(unitsRes.data || []);
      setProductWarehouseStock(stockRes.data || []);
    } catch (err) {
      console.error('Failed to load product details:', err);
    }
  };

  const handleAddPackingUnit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!detailProduct || !newUnitLabel) return;
    try {
      await productApi.createPackingUnit(detailProduct.id, {
        label: newUnitLabel,
        qty_in_parent: Number(newUnitQty) || 1,
        barcode: newUnitBarcode || undefined,
      });
      const { data } = await productApi.listPackingUnits(detailProduct.id);
      setPackingUnits(data || []);
      setNewUnitLabel('');
      setNewUnitQty(1);
      setNewUnitBarcode('');
    } catch (err) {
      alert('Failed to add packaging conversion unit.');
    }
  };

  const handleGenerateBarcode = async () => {
    try {
      const { data } = await barcodeApi.generate();
      setForm((prev) => ({ ...prev, barcode: data.barcode }));
    } catch {
      // fallback random ean13 format
      const rand = '890' + Math.floor(1000000000 + Math.random() * 9000000000).toString();
      setForm((prev) => ({ ...prev, barcode: rand }));
    }
  };

  const handleCreateProduct = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.name || !form.category_id || !form.uom_id) {
      alert('Product Name, Category, and Base Unit are mandatory.');
      return;
    }

    try {
      // 1. Create Product Master
      const { data: created } = await productApi.create({
        name: form.name,
        brand: form.brand,
        description: form.description,
        category_id: form.category_id,
        hsn_code: form.hsn_code,
        sac_code: form.sac_code,
        barcode: form.barcode,
        uom_id: form.uom_id,
        base_unit_qty: Number(form.base_unit_qty) || 1,
        gst_rate: Number(form.gst_rate) || 0,
        allow_decimal: form.allow_decimal,
        min_stock: Number(form.min_stock) || 0,
      });

      const productId = created.id;

      // 2. Set Store Price
      if (store) {
        await productApi.setPrice({
          product_id: productId,
          store_id: store.id,
          mrp: Number(form.mrp) || 0,
          cost_price: Number(form.cost_price) || 0,
          sell_price: Number(form.sell_price) || 0,
          wholesale_sell_price: Number(form.wholesale_sell_price) || 0,
        });

        // 3. Opening Stock if provided
        if (Number(form.opening_stock) > 0) {
          const targetStore = form.warehouse_id || store.id;
          await stockApi.adjust({
            product_id: productId,
            store_id: targetStore,
            quantity: Number(form.opening_stock),
            type: 'add',
            reason: 'opening_stock',
            batch_no: form.batch_no || 'OPENING-1',
            cost_price: Number(form.cost_price) || 0,
            expiry_date: form.expiry_date || undefined,
          });
        }
      }

      setShowCreateModal(false);
      setForm({
        name: '',
        brand: '',
        description: '',
        category_id: '',
        hsn_code: '',
        sac_code: '',
        barcode: '',
        uom_id: '',
        base_unit_qty: 1,
        gst_rate: 0,
        allow_decimal: false,
        min_stock: 5,
        mrp: 0,
        cost_price: 0,
        sell_price: 0,
        wholesale_sell_price: 0,
        opening_stock: 0,
        warehouse_id: '',
        batch_no: '',
        expiry_date: '',
      });
      loadProducts();
    } catch (err: any) {
      console.error('Create product failed:', err);
      alert(err.response?.data?.error || 'Failed to create product.');
    }
  };

  return (
    <AppLayout>
      {/* Header Bar */}
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
            Product Catalog & Brand Management
          </h1>
          <p style={{ margin: '0.2rem 0 0', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
            Multi-brand SKU differentiation, barcode mapping, packaging units, and warehouse stock
          </p>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => setScannerOpen(true)}
          >
            <IconBarcode size={16} />
            <span>Scan to Find</span>
          </button>

          <button
            type="button"
            className="btn btn-primary btn-sm"
            style={{
              backgroundColor: '#1d4ed8',
              borderColor: '#1d4ed8',
              display: 'flex',
              alignItems: 'center',
              gap: '0.4rem',
              fontWeight: 600,
            }}
            onClick={() => setShowRapidIntakeModal(true)}
          >
            <IconBarcode size={16} />
            <span>⚡ Rapid Barcode Intake</span>
          </button>

          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => {
              setShowCreateModal(true);
              if (categories.length > 0) setForm((p) => ({ ...p, category_id: categories[0].id }));
              if (uoms.length > 0) setForm((p) => ({ ...p, uom_id: uoms[0].id }));
            }}
          >
            <IconPlus size={16} />
            <span>Single Product Form</span>
          </button>
        </div>
      </div>

      {/* Filter and Search Bar */}
      <div
        className="erp-card"
        style={{ padding: '0.75rem 1rem', marginBottom: '1.25rem', backgroundColor: '#fff' }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', flexWrap: 'wrap' }}>
          {/* Text Search */}
          <div style={{ flex: '1 1 240px', position: 'relative' }}>
            <input
              type="text"
              className="form-input"
              placeholder="Search by product name, brand, SKU, or barcode..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>

          {/* Brand Filter */}
          <div style={{ minWidth: 160 }}>
            <select
              className="form-select"
              value={selectedBrand}
              onChange={(e) => setSelectedBrand(e.target.value)}
            >
              <option value="">All Brands ({availableBrands.length})</option>
              {availableBrands.map((b) => (
                <option key={b} value={b}>
                  Brand: {b}
                </option>
              ))}
            </select>
          </div>

          {/* Category Filter */}
          <div style={{ minWidth: 160 }}>
            <select
              className="form-select"
              value={selectedCategory}
              onChange={(e) => setSelectedCategory(e.target.value)}
            >
              <option value="">All Categories ({categories.length})</option>
              {categories.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </div>

          {/* Stock Filter */}
          <div style={{ minWidth: 140 }}>
            <select
              className="form-select"
              value={stockStatusFilter}
              onChange={(e) => setStockStatusFilter(e.target.value as any)}
            >
              <option value="all">Stock: All</option>
              <option value="in_stock">In Stock (&gt;0)</option>
              <option value="low_stock">Low Stock (≤Min)</option>
              <option value="out_of_stock">Out of Stock (=0)</option>
            </select>
          </div>

          {(search || selectedBrand || selectedCategory || stockStatusFilter !== 'all') && (
            <button
              type="button"
              className="btn btn-outline btn-sm"
              onClick={() => {
                setSearch('');
                setSelectedBrand('');
                setSelectedCategory('');
                setStockStatusFilter('all');
              }}
            >
              Reset Filters
            </button>
          )}
        </div>
      </div>

      {/* Products Table */}
      <div className="erp-table-container">
        <table className="erp-table">
          <thead>
            <tr>
              <th>Barcode</th>
              <th>Brand</th>
              <th>Product Name</th>
              <th>Category</th>
              <th>Base Unit</th>
              <th className="text-right">Cost Price</th>
              <th className="text-right">Retail Sell</th>
              <th className="text-right">MRP</th>
              <th className="text-center">GST</th>
              <th className="text-center">Stock</th>
              <th className="text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={11} style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-muted)' }}>
                  Loading product catalog...
                </td>
              </tr>
            ) : filteredProducts.length === 0 ? (
              <tr>
                <td colSpan={11} style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
                  No products found matching the criteria. Click "Create New Product" or scan a barcode to add.
                </td>
              </tr>
            ) : (
              filteredProducts.map((p) => {
                const stockQty = p.stock ?? 0;
                const isLow = stockQty <= p.min_stock && stockQty > 0;
                const isOut = stockQty <= 0;

                return (
                  <tr key={p.id}>
                    <td className="text-mono" style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                      {p.barcode || '—'}
                    </td>
                    <td>
                      {p.brand ? (
                        <span className="badge badge-neutral" style={{ fontSize: '0.7rem' }}>
                          {p.brand}
                        </span>
                      ) : (
                        <span style={{ color: 'var(--text-muted)' }}>Unbranded</span>
                      )}
                    </td>
                    <td style={{ fontWeight: 600 }}>
                      <span
                        onClick={() => openProductDetails(p)}
                        style={{ cursor: 'pointer', color: 'var(--primary-600)' }}
                      >
                        {p.name}
                      </span>
                    </td>
                    <td>
                      {categories.find((c) => c.id === p.category_id)?.name || 'General'}
                    </td>
                    <td>
                      {uoms.find((u) => u.id === p.uom_id)?.code || 'NOS'}
                    </td>
                    <td className="text-right text-mono">
                      ₹{(p.cost_price ?? 0).toFixed(2)}
                    </td>
                    <td className="text-right text-mono" style={{ fontWeight: 600 }}>
                      ₹{(p.sell_price || 0).toFixed(2)}
                    </td>
                    <td className="text-right text-mono" style={{ color: 'var(--text-muted)' }}>
                      ₹{(p.mrp || 0).toFixed(2)}
                    </td>
                    <td className="text-center text-mono">
                      {p.gst_rate}%
                    </td>
                    <td className="text-center text-mono font-bold">
                      {isOut ? (
                        <span className="badge badge-danger">0 Out</span>
                      ) : isLow ? (
                        <span className="badge badge-warning">{stockQty} Low</span>
                      ) : (
                        <span className="badge badge-success">{stockQty}</span>
                      )}
                    </td>
                    <td className="text-right">
                      <button
                        type="button"
                        className="btn btn-outline btn-sm"
                        onClick={() => openProductDetails(p)}
                      >
                        Details
                      </button>
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>

      {/* ===================================================================
          Create Product Modal (Full multi-brand, packaging & pricing support)
          =================================================================== */}
      {showCreateModal && (
        <div className="erp-modal-backdrop" onClick={() => setShowCreateModal(false)}>
          <div className="erp-modal" style={{ maxWidth: 680 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 600 }}>
                Create Product (Multi-Brand Support)
              </h3>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setShowCreateModal(false)}
              >
                <IconClose size={18} />
              </button>
            </div>

            <form onSubmit={handleCreateProduct}>
              <div className="erp-modal-body" style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.875rem' }}>
                {/* Product Name */}
                <div className="form-group" style={{ gridColumn: 'span 2' }}>
                  <label className="form-label">Product Name *</label>
                  <input
                    type="text"
                    required
                    className="form-input"
                    placeholder="e.g. Sunflower Cooking Oil 1 Litre"
                    value={form.name}
                    onChange={(e) => setForm({ ...form, name: e.target.value })}
                  />
                </div>

                {/* Brand */}
                <div className="form-group">
                  <label className="form-label">Brand Name</label>
                  <input
                    type="text"
                    className="form-input"
                    placeholder="e.g. Fortune, Saffola, Gemini"
                    value={form.brand}
                    onChange={(e) => setForm({ ...form, brand: e.target.value })}
                  />
                  <span style={{ fontSize: '0.7rem', color: 'var(--text-muted)' }}>
                    Separate brand records ensure distinct SKUs & pricing.
                  </span>
                </div>

                {/* Category */}
                <div className="form-group">
                  <label className="form-label">Category *</label>
                  <select
                    required
                    className="form-select"
                    value={form.category_id}
                    onChange={(e) => setForm({ ...form, category_id: e.target.value })}
                  >
                    <option value="">Select Category</option>
                    {categories.map((c) => (
                      <option key={c.id} value={c.id}>{c.name}</option>
                    ))}
                  </select>
                </div>

                {/* Barcode with Auto-Generate */}
                <div className="form-group">
                  <label className="form-label">Barcode / SKU</label>
                  <div style={{ display: 'flex', gap: '0.35rem' }}>
                    <input
                      type="text"
                      className="form-input text-mono"
                      placeholder="e.g. 8901001100001"
                      value={form.barcode}
                      onChange={(e) => setForm({ ...form, barcode: e.target.value })}
                    />
                    <button
                      type="button"
                      className="btn btn-outline btn-sm"
                      onClick={handleGenerateBarcode}
                      title="Auto Generate Barcode"
                    >
                      Gen
                    </button>
                  </div>
                </div>

                {/* Unit of Measure */}
                <div className="form-group">
                  <label className="form-label">Base Unit (UOM) *</label>
                  <select
                    required
                    className="form-select"
                    value={form.uom_id}
                    onChange={(e) => setForm({ ...form, uom_id: e.target.value })}
                  >
                    <option value="">Select Unit</option>
                    {uoms.map((u) => (
                      <option key={u.id} value={u.id}>{u.name} ({u.code})</option>
                    ))}
                  </select>
                </div>

                {/* GST Rate */}
                <div className="form-group">
                  <label className="form-label">GST Tax Slab</label>
                  <select
                    className="form-select"
                    value={form.gst_rate}
                    onChange={(e) => setForm({ ...form, gst_rate: Number(e.target.value) })}
                  >
                    <option value={0}>0% (Exempt)</option>
                    <option value={5}>5%</option>
                    <option value={12}>12%</option>
                    <option value={18}>18%</option>
                    <option value={28}>28%</option>
                  </select>
                </div>

                {/* HSN Code */}
                <div className="form-group">
                  <label className="form-label">HSN Code</label>
                  <input
                    type="text"
                    className="form-input text-mono"
                    placeholder="e.g. 1512"
                    value={form.hsn_code}
                    onChange={(e) => setForm({ ...form, hsn_code: e.target.value })}
                  />
                </div>

                {/* Pricing Block */}
                <div style={{ gridColumn: 'span 2', borderTop: '1px solid var(--border-light)', paddingTop: '0.75rem' }}>
                  <div style={{ fontWeight: 600, fontSize: '0.85rem', marginBottom: '0.5rem', color: 'var(--text-secondary)' }}>
                    Pricing & Profit Margins (₹)
                  </div>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: '0.5rem' }}>
                    <div>
                      <label className="form-label">Cost Price</label>
                      <input
                        type="number"
                        step="0.01"
                        className="form-input text-mono"
                        value={form.cost_price}
                        onChange={(e) => setForm({ ...form, cost_price: parseFloat(e.target.value) || 0 })}
                      />
                    </div>
                    <div>
                      <label className="form-label">Retail Sell Price *</label>
                      <input
                        type="number"
                        step="0.01"
                        required
                        className="form-input text-mono"
                        value={form.sell_price}
                        onChange={(e) => setForm({ ...form, sell_price: parseFloat(e.target.value) || 0 })}
                      />
                    </div>
                    <div>
                      <label className="form-label">Wholesale Price</label>
                      <input
                        type="number"
                        step="0.01"
                        className="form-input text-mono"
                        value={form.wholesale_sell_price}
                        onChange={(e) => setForm({ ...form, wholesale_sell_price: parseFloat(e.target.value) || 0 })}
                      />
                    </div>
                    <div>
                      <label className="form-label">MRP</label>
                      <input
                        type="number"
                        step="0.01"
                        className="form-input text-mono"
                        value={form.mrp}
                        onChange={(e) => setForm({ ...form, mrp: parseFloat(e.target.value) || 0 })}
                      />
                    </div>
                  </div>
                </div>

                {/* Opening Inventory Block */}
                <div style={{ gridColumn: 'span 2', borderTop: '1px solid var(--border-light)', paddingTop: '0.75rem' }}>
                  <div style={{ fontWeight: 600, fontSize: '0.85rem', marginBottom: '0.5rem', color: 'var(--text-secondary)' }}>
                    Initial Opening Inventory
                  </div>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: '0.5rem' }}>
                    <div>
                      <label className="form-label">Opening Quantity</label>
                      <input
                        type="number"
                        className="form-input text-mono"
                        value={form.opening_stock}
                        onChange={(e) => setForm({ ...form, opening_stock: parseFloat(e.target.value) || 0 })}
                      />
                    </div>
                    <div>
                      <label className="form-label">Location / Warehouse</label>
                      <select
                        className="form-select"
                        value={form.warehouse_id}
                        onChange={(e) => setForm({ ...form, warehouse_id: e.target.value })}
                      >
                        <option value="">Default ({store?.name})</option>
                        {stores.map((s) => (
                          <option key={s.id} value={s.id}>
                            {s.name} ({s.location_type})
                          </option>
                        ))}
                      </select>
                    </div>
                    <div>
                      <label className="form-label">Batch Number</label>
                      <input
                        type="text"
                        className="form-input text-mono"
                        placeholder="e.g. BATCH-01"
                        value={form.batch_no}
                        onChange={(e) => setForm({ ...form, batch_no: e.target.value })}
                      />
                    </div>
                  </div>
                </div>
              </div>

              <div className="erp-modal-footer">
                <button
                  type="button"
                  className="btn btn-outline"
                  onClick={() => setShowCreateModal(false)}
                >
                  Cancel
                </button>
                <button type="submit" className="btn btn-primary">
                  Save Product & Inventory
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* ===================================================================
          Product Details Drawer / Modal (Conversions & Multi-Warehouse Stock)
          =================================================================== */}
      {detailProduct && (
        <div className="erp-modal-backdrop" onClick={() => setDetailProduct(null)}>
          <div className="erp-modal" style={{ maxWidth: 720 }} onClick={(e) => e.stopPropagation()}>
            <div className="erp-modal-header">
              <div>
                <h3 style={{ margin: 0, fontSize: '1.1rem', fontWeight: 700 }}>
                  {detailProduct.name}
                </h3>
                <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                  Brand: <strong>{detailProduct.brand || 'Unbranded'}</strong> &middot; Barcode:{' '}
                  <span className="text-mono">{detailProduct.barcode}</span>
                </div>
              </div>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setDetailProduct(null)}
              >
                <IconClose size={18} />
              </button>
            </div>

            <div className="erp-modal-body">
              {/* Warehouse Stock Matrix */}
              <div style={{ marginBottom: '1.25rem' }}>
                <h4 style={{ marginBottom: '0.5rem', display: 'flex', alignItems: 'center', gap: '0.4rem' }}>
                  <IconWarehouse size={16} />
                  <span>Physical Stock Across Warehouses</span>
                </h4>
                {productWarehouseStock.length === 0 ? (
                  <p style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>
                    No stock batches recorded for this product.
                  </p>
                ) : (
                  <table className="erp-table">
                    <thead>
                      <tr>
                        <th>Batch No</th>
                        <th className="text-center">Expiry</th>
                        <th className="text-right">Cost Price</th>
                        <th className="text-right">Available Qty</th>
                        <th className="text-right">Reserved Qty</th>
                      </tr>
                    </thead>
                    <tbody>
                      {productWarehouseStock.map((b, i) => (
                        <tr key={b.id || i}>
                          <td className="text-mono font-bold">{b.batch_no || 'DEFAULT'}</td>
                          <td className="text-center text-mono">
                            {b.expiry_date ? b.expiry_date.slice(0, 10) : 'None'}
                          </td>
                          <td className="text-right text-mono">₹{(b.cost_price || 0).toFixed(2)}</td>
                          <td className="text-right text-mono font-bold" style={{ color: '#16a34a' }}>
                            {b.quantity}
                          </td>
                          <td className="text-right text-mono">{b.reserved_qty}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>

              {/* Multi-Unit Packaging Conversions */}
              <div>
                <h4 style={{ marginBottom: '0.5rem' }}>
                  Multi-Unit Packaging Conversions (Cartons, Boxes, Packs)
                </h4>
                <p style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
                  Define secondary sellable packages with conversion ratios and separate barcodes.
                </p>

                {packingUnits.length > 0 && (
                  <table className="erp-table" style={{ marginBottom: '0.75rem' }}>
                    <thead>
                      <tr>
                        <th>Unit Label</th>
                        <th className="text-center">Contains (Qty in Base)</th>
                        <th>Packaging Barcode</th>
                      </tr>
                    </thead>
                    <tbody>
                      {packingUnits.map((u) => (
                        <tr key={u.id}>
                          <td style={{ fontWeight: 600 }}>{u.label}</td>
                          <td className="text-center text-mono">{u.qty_in_parent}x Base</td>
                          <td className="text-mono">{u.barcode || '—'}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}

                {/* Add Unit Form */}
                <form
                  onSubmit={handleAddPackingUnit}
                  style={{
                    display: 'flex',
                    gap: '0.5rem',
                    alignItems: 'flex-end',
                    background: '#f8fafc',
                    padding: '0.75rem',
                    borderRadius: 'var(--radius-md)',
                    border: '1px solid var(--border-light)',
                  }}
                >
                  <div style={{ flex: 2 }}>
                    <label className="form-label">Package Label</label>
                    <input
                      type="text"
                      required
                      className="form-input"
                      placeholder="e.g. Carton of 24"
                      value={newUnitLabel}
                      onChange={(e) => setNewUnitLabel(e.target.value)}
                    />
                  </div>
                  <div style={{ flex: 1 }}>
                    <label className="form-label">Multiplier</label>
                    <input
                      type="number"
                      required
                      min="1"
                      className="form-input text-mono"
                      value={newUnitQty}
                      onChange={(e) => setNewUnitQty(parseInt(e.target.value) || 1)}
                    />
                  </div>
                  <div style={{ flex: 2 }}>
                    <label className="form-label">Unit Barcode</label>
                    <input
                      type="text"
                      className="form-input text-mono"
                      placeholder="Barcode for carton"
                      value={newUnitBarcode}
                      onChange={(e) => setNewUnitBarcode(e.target.value)}
                    />
                  </div>
                  <button type="submit" className="btn btn-primary btn-sm">
                    Add Unit
                  </button>
                </form>
              </div>
            </div>

            <div className="erp-modal-footer">
              <button
                type="button"
                className="btn btn-outline"
                onClick={() => setDetailProduct(null)}
              >
                Close
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Barcode Scanner Modal */}
      <BarcodeScannerModal
        isOpen={scannerOpen}
        onClose={() => setScannerOpen(false)}
        onScan={(code) => {
          setSearch(code);
        }}
        title="Scan Barcode to Filter Products"
      />

      {/* Rapid Continuous Barcode Intake Modal */}
      <RapidBarcodeIntakeModal
        isOpen={showRapidIntakeModal}
        onClose={() => {
          setShowRapidIntakeModal(false);
          loadProducts();
        }}
        store={store}
        categories={categories}
        uoms={uoms}
        stores={stores}
        onProductSaved={() => {
          loadProducts();
        }}
      />
    </AppLayout>
  );
}