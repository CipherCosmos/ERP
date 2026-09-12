import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { categoryApi, productApi, uomApi } from '../services/api';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Checkbox, Field, FormError, FormGrid, NumberInput, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatMoney, formatQty } from '../utils/format';

interface UnitRow {
  id: string;
  product_id: string;
  uom_id: string | null;
  label: string;
  qty_in_base: number;
  barcode: string;
  is_default: boolean;
  is_sellable: boolean;
  is_purchasable: boolean;
  is_active: boolean;
  sort_order: number;
}

interface PricedUnit {
  unit: UnitRow;
  price: { sell_price: number; wholesale_sell_price: number; mrp: number; cost_price: number };
  has_price: boolean;
}

interface ProductRow {
  id: string;
  name: string;
  category_id: string;
  category_name: string;
  barcode: string;
  uom_id: string;
  gst_rate: number;
  hsn_code: string;
  brand: string;
  is_active: boolean;
  allow_decimal: boolean;
  min_stock: number;
  stock: number;
  mrp: number;
  cost_price: number;
  sell_price: number;
  default_product_unit_id: string;
  units: PricedUnit[];
}

/**
 * The catalogue.
 *
 * This is where the units work becomes visible. A product is stocked in one
 * base unit and sold in as many as the shop likes — loose by the kilo, a 5 kg
 * bag, a 25 kg sack — and each of those carries its own price and its own
 * barcode. Getting that on screen is the point: the schema could not express
 * bulk at all before, and a shop cannot use what it cannot see.
 *
 * The per-kilo figure beside each pack price is doing real work. A pack that
 * costs more per kilo than the loose scoop is nearly always a typo, and it is
 * invisible unless something divides for you.
 */
export default function Products() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [products, setProducts] = useState<ProductRow[]>([]);
  const [categories, setCategories] = useState<{ id: string; name: string }[]>([]);
  const [uoms, setUoms] = useState<{ id: string; code: string; name: string }[]>([]);
  const [loading, setLoading] = useState(false);
  const [categoryFilter, setCategoryFilter] = useState('');
  const [showInactive, setShowInactive] = useState(false);

  const [editing, setEditing] = useState<ProductRow | null>(null);
  const [creating, setCreating] = useState(false);
  const [detailTab, setDetailTab] = useState<'details' | 'units'>('details');
  const [form, setForm] = useState<Record<string, any>>({});
  const [saveError, setSaveError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Unit editor -------------------------------------------------------------
  const [unitDraft, setUnitDraft] = useState<Partial<UnitRow> | null>(null);
  const [unitError, setUnitError] = useState<string | null>(null);
  const [priceDraft, setPriceDraft] = useState<{ unit: UnitRow; sell: string; wholesale: string; mrp: string; cost: string } | null>(null);

  const loadProducts = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await productApi.list({ store_id: store.id });
      setProducts(data as unknown as ProductRow[]);
    } catch (err) {
      console.error('Failed to load products:', err);
    } finally {
      setLoading(false);
    }
  }, [store]);

  const loadCategories = useCallback(async () => {
    if (!store) return;
    try {
      const { data } = await categoryApi.list(store.id);
      setCategories(data as { id: string; name: string }[]);
    } catch (err) {
      console.error('Failed to load categories:', err);
    }
  }, [store]);

  const loadUoms = useCallback(async () => {
    try {
      const { data } = await uomApi.list();
      setUoms(data as { id: string; code: string; name: string }[]);
    } catch (err) {
      console.error('Failed to load units of measure:', err);
    }
  }, []);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadProducts();
    loadCategories();
    loadUoms();
  }, [user, store, navigate, loadProducts, loadCategories, loadUoms]);

  /** Refresh one product in place, so editing units does not reset the table. */
  const refreshEditing = useCallback(async () => {
    if (!store || !editing) return;
    const { data } = await productApi.get(editing.id, store.id);
    setEditing(data as ProductRow);
    await loadProducts();
  }, [store, editing, loadProducts]);

  const openNew = () => {
    setForm({
      name: '',
      category_id: categories[0]?.id ?? '',
      uom_id: uoms.find((u) => u.code === 'NOS')?.id ?? uoms[0]?.id ?? '',
      gst_rate: 5,
      hsn_code: '',
      barcode: '',
      brand: '',
      allow_decimal: false,
      min_stock: 0,
      sell_price: 0,
      cost_price: 0,
      mrp: 0,
    });
    setEditing(null);
    setCreating(true);
    setDetailTab('details');
    setSaveError(null);
  };

  const openEdit = (p: ProductRow) => {
    setForm({
      name: p.name,
      category_id: p.category_id,
      uom_id: p.uom_id,
      gst_rate: p.gst_rate,
      hsn_code: p.hsn_code ?? '',
      barcode: p.barcode ?? '',
      brand: p.brand ?? '',
      allow_decimal: !!p.allow_decimal,
      min_stock: p.min_stock ?? 0,
    });
    setEditing(p);
    setCreating(false);
    setDetailTab('details');
    setSaveError(null);
  };

  const close = () => {
    setEditing(null);
    setCreating(false);
    setUnitDraft(null);
    setPriceDraft(null);
    setSaveError(null);
  };

  const saveProduct = async () => {
    if (!store) return;
    if (!form.name?.trim()) {
      setSaveError('A product needs a name.');
      return;
    }
    setBusy(true);
    setSaveError(null);
    try {
      if (editing) {
        await productApi.update(editing.id, form as any);
      } else {
        await productApi.create({ ...form, store_id: store.id } as any);
      }
      close();
      await loadProducts();
    } catch (err: any) {
      setSaveError(err?.response?.data?.error || 'Could not save this product.');
    } finally {
      setBusy(false);
    }
  };

  const saveUnit = async () => {
    if (!editing || !unitDraft) return;
    const qty = Number(unitDraft.qty_in_base);
    if (!unitDraft.label?.trim()) {
      setUnitError('Give the unit a label — what staff call it.');
      return;
    }
    if (!Number.isFinite(qty) || qty <= 0) {
      setUnitError('Say how many stocking units one of these is. A 25 kg sack is 25.');
      return;
    }
    setBusy(true);
    setUnitError(null);
    try {
      const payload = {
        label: unitDraft.label.trim(),
        qty_in_base: qty,
        barcode: unitDraft.barcode?.trim() || undefined,
        is_sellable: unitDraft.is_sellable !== false,
        is_purchasable: unitDraft.is_purchasable !== false,
      };
      if (unitDraft.id) {
        await productApi.updateUnit(editing.id, unitDraft.id, payload);
      } else {
        await productApi.createUnit(editing.id, payload);
      }
      setUnitDraft(null);
      await refreshEditing();
    } catch (err: any) {
      setUnitError(err?.response?.data?.error || 'Could not save this unit.');
    } finally {
      setBusy(false);
    }
  };

  const savePrice = async () => {
    if (!editing || !store || !priceDraft) return;
    setBusy(true);
    try {
      await productApi.setPrice({
        product_id: editing.id,
        product_unit_id: priceDraft.unit.id,
        store_id: store.id,
        sell_price: Number(priceDraft.sell) || 0,
        wholesale_sell_price: Number(priceDraft.wholesale) || 0,
        mrp: Number(priceDraft.mrp) || 0,
        cost_price: Number(priceDraft.cost) || 0,
      });
      setPriceDraft(null);
      await refreshEditing();
    } catch (err: any) {
      window.alert(err?.response?.data?.error || 'Could not save this price.');
    } finally {
      setBusy(false);
    }
  };

  const makeDefault = async (u: UnitRow) => {
    if (!editing) return;
    try {
      await productApi.setDefaultUnit(editing.id, u.id);
      await refreshEditing();
    } catch (err: any) {
      window.alert(err?.response?.data?.error || 'Could not change the default unit.');
    }
  };

  const rows = useMemo(() => {
    let r = products;
    if (!showInactive) r = r.filter((p) => p.is_active);
    if (categoryFilter) r = r.filter((p) => p.category_id === categoryFilter);
    return r;
  }, [products, categoryFilter, showInactive]);

  const lowStock = useMemo(() => products.filter((p) => p.is_active && p.min_stock > 0 && p.stock <= p.min_stock), [products]);
  const multiUnit = useMemo(() => products.filter((p) => (p.units?.length ?? 0) > 1), [products]);

  const baseUnitCode = (p: ProductRow) => uoms.find((u) => u.id === p.uom_id)?.code ?? '';

  const columns: Column<ProductRow>[] = [
    {
      key: 'name',
      header: 'Product',
      render: (p) => (
        <div>
          <div style={{ fontWeight: 600 }}>{p.name}</div>
          <div style={styles.sub}>
            {p.brand ? `${p.brand} · ` : ''}
            {p.category_name || 'Uncategorised'}
            {p.barcode ? ` · ${p.barcode}` : ''}
          </div>
          {/* A product with no HSN code issues invoices missing a particular a
              tax invoice has to print, and the gap is invisible until a return
              is prepared. New products cannot be saved without one; this marks
              the ones that predate the rule, where they can be fixed. */}
          {!p.hsn_code && (
            <div style={styles.needsHsn}>HSN code missing — invoices for this cannot be filed</div>
          )}
        </div>
      ),
      value: (p) => `${p.name} ${p.brand ?? ''} ${p.category_name ?? ''} ${p.barcode ?? ''}`,
    },
    {
      key: 'units',
      header: 'Sold as',
      // The heart of the screen: how many ways this product leaves the shop.
      render: (p) => {
        const us = p.units ?? [];
        if (us.length <= 1) return <span style={styles.muted}>{baseUnitCode(p) || 'single unit'}</span>;
        return (
          <div style={{ display: 'flex', gap: space.xs, flexWrap: 'wrap' }}>
            {us.slice(0, 4).map((x) => (
              <span key={x.unit.id} style={badge(x.has_price ? 'info' : 'warning')} title={x.has_price ? '' : 'No price set'}>
                {x.unit.label}
              </span>
            ))}
            {us.length > 4 && <span style={styles.muted}>+{us.length - 4}</span>}
          </div>
        );
      },
      value: (p) => p.units?.length ?? 0,
    },
    {
      key: 'sell_price',
      header: 'Price',
      numeric: true,
      render: (p) => (
        <div>
          <div style={{ fontWeight: 600 }}>{formatMoney(p.sell_price)}</div>
          <div style={styles.sub}>per {baseUnitCode(p) || 'unit'}</div>
        </div>
      ),
      value: (p) => p.sell_price,
    },
    {
      key: 'stock',
      header: 'Stock',
      numeric: true,
      render: (p) => {
        const low = p.min_stock > 0 && p.stock <= p.min_stock;
        return (
          <span style={{ fontWeight: 600, color: low ? color.danger : p.stock > 0 ? color.text : color.textFaint }}>
            {formatQty(p.stock, baseUnitCode(p))}
          </span>
        );
      },
      value: (p) => p.stock,
    },
    { key: 'gst_rate', header: 'GST', numeric: true, render: (p) => `${p.gst_rate}%`, value: (p) => p.gst_rate },
    {
      key: 'is_active',
      header: '',
      sortable: false,
      width: '5rem',
      render: (p) =>
        p.is_active ? (
          <button style={{ ...ui.button, padding: '0.25rem 0.625rem', fontSize: font.size.sm }} onClick={(e) => { e.stopPropagation(); openEdit(p); }}>
            Edit
          </button>
        ) : (
          <span style={badge('neutral')}>Inactive</span>
        ),
    },
  ];

  const detail = editing;

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>← Back</button>
        <h1 style={styles.h1}>Products</h1>
      </div>

      <div style={styles.summary}>
        <Stat label="Products" value={String(products.filter((p) => p.is_active).length)} />
        <Stat label="Sold in more than one unit" value={String(multiUnit.length)} />
        <Stat label="At or below minimum" value={String(lowStock.length)} tone={lowStock.length ? 'danger' : undefined} />
      </div>

      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(p) => p.id}
        loading={loading}
        onRowClick={openEdit}
        searchPlaceholder="Search name, brand, category or barcode…"
        empty={categoryFilter ? 'No products in this category.' : 'No products yet.'}
        rowTone={(p) => (p.min_stock > 0 && p.stock <= p.min_stock ? 'warning' : undefined)}
        toolbar={
          <>
            <Select
              value={categoryFilter}
              onChange={(e) => setCategoryFilter(e.target.value)}
              placeholder="All categories"
              options={categories.map((c) => ({ value: c.id, label: c.name }))}
              style={{ width: 'auto' }}
            />
            <label style={{ display: 'flex', alignItems: 'center', gap: space.xs, fontSize: font.size.sm, whiteSpace: 'nowrap' }}>
              <input type="checkbox" checked={showInactive} onChange={(e) => setShowInactive(e.target.checked)} />
              Show inactive
            </label>
            <button style={ui.buttonPrimary} onClick={openNew}>+ New product</button>
          </>
        }
      />

      <Modal
        open={creating || !!detail}
        title={detail ? detail.name : 'New product'}
        subtitle={detail ? `Stocked in ${baseUnitCode(detail) || 'its base unit'} · ${formatQty(detail.stock, baseUnitCode(detail))} on hand` : undefined}
        width="52rem"
        onClose={close}
        footer={
          detailTab === 'details' ? (
            <>
              <button style={ui.button} onClick={close} disabled={busy}>Cancel</button>
              <button style={ui.buttonPrimary} onClick={saveProduct} disabled={busy}>
                {busy ? 'Saving…' : detail ? 'Save changes' : 'Create product'}
              </button>
            </>
          ) : (
            <button style={ui.button} onClick={close}>Done</button>
          )
        }
      >
        {detail && (
          <div style={styles.tabs}>
            <button style={detailTab === 'details' ? styles.tabActive : styles.tab} onClick={() => setDetailTab('details')}>
              Details
            </button>
            <button style={detailTab === 'units' ? styles.tabActive : styles.tab} onClick={() => setDetailTab('units')}>
              Units &amp; prices ({detail.units?.length ?? 0})
            </button>
          </div>
        )}

        {(!detail || detailTab === 'details') && (
          <>
            <FormError error={saveError} />
            <FormGrid>
              <Field label="Name" required htmlFor="p-name">
                <TextInput id="p-name" autoFocus value={form.name ?? ''} onChange={(e) => setForm({ ...form, name: e.target.value })} />
              </Field>
              <Field label="Brand" htmlFor="p-brand">
                <TextInput id="p-brand" value={form.brand ?? ''} onChange={(e) => setForm({ ...form, brand: e.target.value })} />
              </Field>
              <Field label="Category" required htmlFor="p-cat">
                <Select
                  id="p-cat"
                  value={form.category_id ?? ''}
                  onChange={(e) => setForm({ ...form, category_id: e.target.value })}
                  placeholder="Choose…"
                  options={categories.map((c) => ({ value: c.id, label: c.name }))}
                />
              </Field>
              <Field
                label="Stocking unit"
                required
                hint={creating ? 'Stock is counted in this. Everything else converts to it.' : 'Changing this does not restate existing stock.'}
                htmlFor="p-uom"
              >
                <Select
                  id="p-uom"
                  value={form.uom_id ?? ''}
                  onChange={(e) => setForm({ ...form, uom_id: e.target.value })}
                  options={uoms.map((u) => ({ value: u.id, label: `${u.name} (${u.code})` }))}
                />
              </Field>
              <Field label="GST rate %" required htmlFor="p-gst">
                <NumberInput id="p-gst" min={0} max={100} value={form.gst_rate ?? 0} onChange={(e) => setForm({ ...form, gst_rate: Number(e.target.value) })} />
              </Field>
              <Field
                label="HSN code"
                required
                hint="Printed on every tax invoice. 4, 6 or 8 digits."
                htmlFor="p-hsn"
              >
                <TextInput
                  id="p-hsn"
                  inputMode="numeric"
                  value={form.hsn_code ?? ''}
                  onChange={(e) => setForm({ ...form, hsn_code: e.target.value })}
                />
              </Field>
              <Field label="Barcode" hint="The product's own code. Packs get their own." htmlFor="p-bc">
                <TextInput id="p-bc" value={form.barcode ?? ''} onChange={(e) => setForm({ ...form, barcode: e.target.value })} />
              </Field>
              <Field label="Reorder at" hint="Flagged on this screen when stock falls to here." htmlFor="p-min">
                <NumberInput id="p-min" min={0} value={form.min_stock ?? 0} onChange={(e) => setForm({ ...form, min_stock: Number(e.target.value) })} />
              </Field>
            </FormGrid>

            <Checkbox
              label="Can be sold in fractions"
              hint="On for anything weighed or measured. Off for pieces — half a bar of soap is not a sale."
              checked={!!form.allow_decimal}
              onChange={(v) => setForm({ ...form, allow_decimal: v })}
            />

            {creating && (
              <>
                <div style={styles.divider}>Opening price, for the stocking unit</div>
                <FormGrid>
                  <Field label="Selling price" required htmlFor="p-sell">
                    <NumberInput id="p-sell" min={0} value={form.sell_price ?? 0} onChange={(e) => setForm({ ...form, sell_price: Number(e.target.value) })} />
                  </Field>
                  <Field label="Cost price" htmlFor="p-cost">
                    <NumberInput id="p-cost" min={0} value={form.cost_price ?? 0} onChange={(e) => setForm({ ...form, cost_price: Number(e.target.value) })} />
                  </Field>
                  <Field label="MRP" hint="Zero if not a packaged commodity." htmlFor="p-mrp">
                    <NumberInput id="p-mrp" min={0} value={form.mrp ?? 0} onChange={(e) => setForm({ ...form, mrp: Number(e.target.value) })} />
                  </Field>
                </FormGrid>
                <div style={styles.note}>
                  Bags, sacks and pouches are added afterwards, under <strong>Units &amp; prices</strong> — each with its own
                  price and barcode.
                </div>
              </>
            )}
          </>
        )}

        {detail && detailTab === 'units' && (
          <>
            <div style={styles.note}>
              Stock is one pool of <strong>{formatQty(detail.stock, baseUnitCode(detail))}</strong>. Every unit below draws
              from it, so selling a sack and selling loose can never disagree about what is on the shelf.
            </div>

            <table style={styles.unitTable}>
              <thead>
                <tr>
                  <th style={styles.uth}>Unit</th>
                  <th style={{ ...styles.uth, textAlign: 'right' }}>Size</th>
                  <th style={{ ...styles.uth, textAlign: 'right' }}>Price</th>
                  <th style={{ ...styles.uth, textAlign: 'right' }}>Per {baseUnitCode(detail) || 'unit'}</th>
                  <th style={styles.uth}>Barcode</th>
                  <th style={styles.uth} />
                </tr>
              </thead>
              <tbody>
                {(detail.units ?? []).map(({ unit, price, has_price }) => {
                  const perBase = has_price && unit.qty_in_base > 0 ? price.sell_price / unit.qty_in_base : null;
                  const looseRate = detail.sell_price;
                  // A pack dearer per kilo than the loose scoop is nearly always
                  // a mistyped price, and nothing else on the screen shows it.
                  const dearer = perBase !== null && looseRate > 0 && unit.qty_in_base > 1 && perBase > looseRate;
                  return (
                    <tr key={unit.id} style={styles.utr}>
                      <td style={styles.utd}>
                        <span style={{ fontWeight: 600 }}>{unit.label}</span>
                        {unit.is_default && <span style={{ ...badge('info'), marginLeft: space.xs }}>default</span>}
                        {!unit.is_sellable && <span style={{ ...badge('neutral'), marginLeft: space.xs }}>buy only</span>}
                        {!unit.is_active && <span style={{ ...badge('neutral'), marginLeft: space.xs }}>retired</span>}
                      </td>
                      <td style={{ ...styles.utd, ...ui.numeric }}>
                        {formatQty(unit.qty_in_base, baseUnitCode(detail))}
                      </td>
                      <td style={{ ...styles.utd, ...ui.numeric }}>
                        {has_price ? formatMoney(price.sell_price) : <span style={badge('warning')}>no price</span>}
                      </td>
                      <td style={{ ...styles.utd, ...ui.numeric, color: dearer ? color.danger : color.textMuted }}>
                        {perBase !== null ? formatMoney(perBase) : '—'}
                        {dearer && <div style={styles.sub}>dearer than loose</div>}
                      </td>
                      <td style={{ ...styles.utd, fontFamily: font.mono, fontSize: font.size.sm }}>{unit.barcode || '—'}</td>
                      <td style={{ ...styles.utd, textAlign: 'right', whiteSpace: 'nowrap' }}>
                        <button
                          style={{ ...ui.button, padding: '0.125rem 0.5rem', fontSize: font.size.xs }}
                          onClick={() =>
                            setPriceDraft({
                              unit,
                              sell: String(price?.sell_price ?? 0),
                              wholesale: String(price?.wholesale_sell_price ?? 0),
                              mrp: String(price?.mrp ?? 0),
                              cost: String(price?.cost_price ?? 0),
                            })
                          }
                        >
                          Price
                        </button>{' '}
                        <button
                          style={{ ...ui.button, padding: '0.125rem 0.5rem', fontSize: font.size.xs }}
                          onClick={() => { setUnitDraft(unit); setUnitError(null); }}
                        >
                          Edit
                        </button>{' '}
                        {!unit.is_default && unit.is_sellable && unit.is_active && (
                          <button
                            style={{ ...ui.button, padding: '0.125rem 0.5rem', fontSize: font.size.xs }}
                            onClick={() => makeDefault(unit)}
                          >
                            Make default
                          </button>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>

            <div>
              <button
                style={ui.buttonPrimary}
                onClick={() => {
                  setUnitDraft({ label: '', qty_in_base: undefined, barcode: '', is_sellable: true, is_purchasable: true });
                  setUnitError(null);
                }}
              >
                + Add a unit
              </button>
            </div>
          </>
        )}
      </Modal>

      {/* Unit editor ------------------------------------------------------ */}
      <Modal
        open={!!unitDraft}
        title={unitDraft?.id ? `Edit ${unitDraft.label}` : 'Add a unit'}
        subtitle={detail ? `Sizes are in ${baseUnitCode(detail) || 'the stocking unit'} — a 25 kg sack of a product stocked in kg is 25.` : undefined}
        width="34rem"
        onClose={() => setUnitDraft(null)}
        footer={
          <>
            <button style={ui.button} onClick={() => setUnitDraft(null)} disabled={busy}>Cancel</button>
            <button style={ui.buttonPrimary} onClick={saveUnit} disabled={busy}>{busy ? 'Saving…' : 'Save unit'}</button>
          </>
        }
      >
        <FormError error={unitError} />
        <FormGrid>
          <Field label="Label" required hint="What staff call it: “25 kg sack”." htmlFor="u-label">
            <TextInput
              id="u-label"
              autoFocus
              value={unitDraft?.label ?? ''}
              onChange={(e) => setUnitDraft({ ...(unitDraft ?? {}), label: e.target.value })}
            />
          </Field>
          <Field
            label={`Size in ${detail ? baseUnitCode(detail) || 'base units' : 'base units'}`}
            required
            hint="A 250 g pouch of something stocked in kg is 0.25."
            htmlFor="u-qty"
          >
            <NumberInput
              id="u-qty"
              min={0}
              value={unitDraft?.qty_in_base ?? ''}
              onChange={(e) => setUnitDraft({ ...(unitDraft ?? {}), qty_in_base: Number(e.target.value) })}
            />
          </Field>
        </FormGrid>
        <Field label="Barcode" hint="Its own code, so a scan picks this unit and its price.">
          <TextInput
            value={unitDraft?.barcode ?? ''}
            onChange={(e) => setUnitDraft({ ...(unitDraft ?? {}), barcode: e.target.value })}
          />
        </Field>
        <FormGrid>
          <Checkbox
            label="Can be sold"
            checked={unitDraft?.is_sellable !== false}
            onChange={(v) => setUnitDraft({ ...(unitDraft ?? {}), is_sellable: v })}
          />
          <Checkbox
            label="Can be bought"
            hint="A supplier's pallet you never sell whole."
            checked={unitDraft?.is_purchasable !== false}
            onChange={(v) => setUnitDraft({ ...(unitDraft ?? {}), is_purchasable: v })}
          />
        </FormGrid>
      </Modal>

      {/* Price editor ----------------------------------------------------- */}
      <Modal
        open={!!priceDraft}
        title={`Price — ${priceDraft?.unit.label ?? ''}`}
        subtitle={
          priceDraft && detail
            ? `One ${priceDraft.unit.label} is ${formatQty(priceDraft.unit.qty_in_base, baseUnitCode(detail))}`
            : undefined
        }
        width="34rem"
        onClose={() => setPriceDraft(null)}
        footer={
          <>
            <button style={ui.button} onClick={() => setPriceDraft(null)} disabled={busy}>Cancel</button>
            <button style={ui.buttonPrimary} onClick={savePrice} disabled={busy}>{busy ? 'Saving…' : 'Save price'}</button>
          </>
        }
      >
        <FormGrid>
          <Field label="Selling price" required htmlFor="pr-sell">
            <NumberInput
              id="pr-sell"
              autoFocus
              min={0}
              value={priceDraft?.sell ?? ''}
              onChange={(e) => setPriceDraft(priceDraft ? { ...priceDraft, sell: e.target.value } : null)}
            />
          </Field>
          <Field label="Wholesale price" hint="Charged to wholesale customers. Zero to use the normal price." htmlFor="pr-ws">
            <NumberInput
              id="pr-ws"
              min={0}
              value={priceDraft?.wholesale ?? ''}
              onChange={(e) => setPriceDraft(priceDraft ? { ...priceDraft, wholesale: e.target.value } : null)}
            />
          </Field>
          <Field label="Cost price" htmlFor="pr-cost">
            <NumberInput
              id="pr-cost"
              min={0}
              value={priceDraft?.cost ?? ''}
              onChange={(e) => setPriceDraft(priceDraft ? { ...priceDraft, cost: e.target.value } : null)}
            />
          </Field>
          <Field label="MRP" hint="Selling above it is an offence, so the server refuses. Zero if unpackaged." htmlFor="pr-mrp">
            <NumberInput
              id="pr-mrp"
              min={0}
              value={priceDraft?.mrp ?? ''}
              onChange={(e) => setPriceDraft(priceDraft ? { ...priceDraft, mrp: e.target.value } : null)}
            />
          </Field>
        </FormGrid>
        {priceDraft && detail && priceDraft.unit.qty_in_base > 0 && Number(priceDraft.sell) > 0 && (
          <div style={styles.note}>
            That works out at <strong>{formatMoney(Number(priceDraft.sell) / priceDraft.unit.qty_in_base)}</strong> per{' '}
            {baseUnitCode(detail) || 'unit'}
            {detail.sell_price > 0 && <> — loose is {formatMoney(detail.sell_price)}.</>}
          </div>
        )}
      </Modal>
    </div>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'danger' }) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 10rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div style={{ ...styles.statValue, color: tone === 'danger' ? color.danger : color.text }}>{value}</div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  header: { display: 'flex', alignItems: 'center', gap: space.lg, marginBottom: space.lg },
  h1: { margin: 0, fontSize: font.size.xxl },
  summary: { display: 'flex', gap: space.lg, marginBottom: space.lg, flexWrap: 'wrap' },
  statLabel: { fontSize: font.size.xs, textTransform: 'uppercase', letterSpacing: '0.04em', color: color.textMuted, fontWeight: 700 },
  statValue: { marginTop: space.xs, fontSize: font.size.xl, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
  sub: { fontSize: font.size.xs, color: color.textMuted },
  needsHsn: { fontSize: font.size.xs, color: color.danger, fontWeight: 600 },
  muted: { color: color.textFaint, fontSize: font.size.sm },
  tabs: { display: 'flex', gap: space.xs },
  tab: { ...ui.button, borderRadius: '999px', padding: '0.25rem 0.75rem', fontSize: font.size.sm },
  tabActive: { ...ui.buttonPrimary, borderRadius: '999px', padding: '0.25rem 0.75rem', fontSize: font.size.sm },
  divider: {
    paddingTop: space.md,
    borderTop: `1px solid ${color.border}`,
    fontSize: font.size.xs,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    fontWeight: 700,
    color: color.textMuted,
  },
  note: {
    padding: space.md,
    background: color.primarySoft,
    border: `1px solid ${color.primary}22`,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
  unitTable: { width: '100%', borderCollapse: 'collapse', fontSize: font.size.md },
  uth: {
    textAlign: 'left',
    padding: `${space.sm} ${space.md}`,
    fontSize: font.size.xs,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    fontWeight: 700,
    color: color.textMuted,
    borderBottom: `1px solid ${color.border}`,
  },
  utr: { borderBottom: `1px solid ${color.border}` },
  utd: { padding: `${space.sm} ${space.md}`, verticalAlign: 'middle' },
};
