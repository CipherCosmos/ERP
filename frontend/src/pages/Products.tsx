import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { categoryApi, productApi, uomApi, vendorApi } from '../services/api';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Checkbox, Field, FormError, FormGrid, NumberInput, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatDate, formatMoney, formatQty } from '../utils/format';
import { PriceMovement } from '../types';

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
  const [vendors, setVendors] = useState<{ id: string; name: string }[]>([]);
  const [loading, setLoading] = useState(false);
  const [categoryFilter, setCategoryFilter] = useState('');
  const [showInactive, setShowInactive] = useState(false);
  const [onlyUnclassified, setOnlyUnclassified] = useState(false);
  const [search, setSearch] = useState('');
  // What the search box holds versus what has been asked for. They differ for
  // a moment while somebody is still typing.
  const [searchTerm, setSearchTerm] = useState('');
  const [unclassifiedCount, setUnclassifiedCount] = useState<number | null>(null);

  const [editing, setEditing] = useState<ProductRow | null>(null);
  const [creating, setCreating] = useState(false);
  const [detailTab, setDetailTab] = useState<'details' | 'units' | 'history'>('details');
  const [history, setHistory] = useState<PriceMovement[] | null>(null);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const [form, setForm] = useState<Record<string, any>>({});
  const [saveError, setSaveError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Unit editor -------------------------------------------------------------
  const [unitDraft, setUnitDraft] = useState<Partial<UnitRow> | null>(null);
  const [unitError, setUnitError] = useState<string | null>(null);
  const [priceDraft, setPriceDraft] = useState<{ unit: UnitRow; sell: string; wholesale: string; mrp: string; cost: string; note: string } | null>(null);
  // Errors from the two actions that used to raise a blocking alert. At a
  // counter a dialog nobody can dismiss without reading is a queue, and an
  // alert also throws away the server's own sentence the moment it is closed.
  const [priceError, setPriceError] = useState<string | null>(null);

  /**
   * How many rows the server will return at most.
   *
   * It is the server's own ceiling, and the screen has to say when it is
   * reached. A shop that has just imported two thousand items and sees a
   * hundred of them, with a search box that only looks at those hundred, is
   * being told its catalogue is smaller than it is.
   */
  const LIST_CAP = 500;

  /**
   * Every filter is applied by the server, not by this screen.
   *
   * It used to filter the arrived rows in the browser, and two of the three
   * controls could not work that way. "Show inactive" narrowed a list that had
   * never contained an inactive product, so ticking it did nothing at all. And
   * the search box only ever saw the first hundred rows the server sent, which
   * after a bulk import is a small fraction of the catalogue — a product that
   * exists, searched for by name, came back "no products".
   */
  const loadProducts = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await productApi.list({
        store_id: store.id,
        limit: LIST_CAP,
        ...(categoryFilter ? { category_id: categoryFilter } : {}),
        ...(searchTerm ? { search: searchTerm } : {}),
        ...(showInactive ? { include_inactive: true } : {}),
        ...(onlyUnclassified ? { unclassified: true } : {}),
      });
      setProducts(data as unknown as ProductRow[]);
    } catch (err) {
      console.error('Failed to load products:', err);
    } finally {
      setLoading(false);
    }
  }, [store, categoryFilter, searchTerm, showInactive, onlyUnclassified]);

  /**
   * How many products carry no HSN or SAC code.
   *
   * Counted with its own request rather than off the listing, because the
   * listing is filtered and capped: a screen showing one category would
   * otherwise report that category's unclassified products as the shop's
   * total. Every invoice selling one of these goes out without the
   * classification a tax invoice has to print, so the number belongs on the
   * screen whether or not anybody filtered for it.
   */
  const loadUnclassified = useCallback(async () => {
    if (!store) return;
    try {
      const { data } = await productApi.list({
        store_id: store.id,
        unclassified: true,
        limit: LIST_CAP,
      });
      setUnclassifiedCount((data as unknown as ProductRow[]).length);
    } catch {
      // A count nobody could fetch is shown as unknown rather than as zero.
      setUnclassifiedCount(null);
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

  const loadVendors = useCallback(async () => {
    try {
      const { data } = await vendorApi.list();
      setVendors(data as { id: string; name: string }[]);
    } catch (err) {
      // The supplier field is optional, so a shop with no vendors yet — or one
      // whose role cannot read them — still gets a working product form.
      console.error('Failed to load vendors:', err);
    }
  }, []);

  // A request per keystroke would be one per letter of "sunflower". A quarter
  // of a second is long enough to finish a word and short enough that nobody
  // waits for it.
  useEffect(() => {
    const timer = setTimeout(() => setSearchTerm(search.trim()), 250);
    return () => clearTimeout(timer);
  }, [search]);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadProducts();
  }, [user, store, navigate, loadProducts]);

  useEffect(() => {
    if (!user || !store) return;
    loadCategories();
    loadUoms();
    loadVendors();
    loadUnclassified();
  }, [user, store, loadCategories, loadUoms, loadVendors, loadUnclassified]);

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
      name_hi: '',
      name_kn: '',
      category_id: categories[0]?.id ?? '',
      uom_id: uoms.find((u) => u.code === 'NOS')?.id ?? uoms[0]?.id ?? '',
      gst_rate: 5,
      hsn_code: '',
      barcode: '',
      brand: '',
      allow_decimal: false,
      tracks_expiry: false,
      shelf_life_days: '',
      preferred_vendor_id: '',
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
      name_hi: (p as any).name_hi ?? '',
      name_kn: (p as any).name_kn ?? '',
      category_id: p.category_id,
      uom_id: p.uom_id,
      gst_rate: p.gst_rate,
      hsn_code: p.hsn_code ?? '',
      barcode: p.barcode ?? '',
      brand: p.brand ?? '',
      allow_decimal: !!p.allow_decimal,
      tracks_expiry: !!(p as any).tracks_expiry,
      shelf_life_days: (p as any).shelf_life_days ?? '',
      preferred_vendor_id: (p as any).preferred_vendor_id ?? '',
      min_stock: p.min_stock ?? 0,
    });
    setEditing(p);
    setCreating(false);
    setDetailTab('details');
    // Dropped rather than kept: a history left over from the last product
    // opened would be another product's prices under this one's name.
    setHistory(null);
    setHistoryError(null);
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
    const shelfLife = String(form.shelf_life_days ?? '').trim();
    if (shelfLife && !form.tracks_expiry) {
      setSaveError('A shelf life belongs to something that expires. Tick "Has a shelf life", or clear the days.');
      return;
    }
    // Blank is not an identifier and not a number. Sending "" for either is how
    // a form turns a field somebody left alone into a validation error they
    // cannot see the cause of.
    const payload = {
      ...form,
      shelf_life_days: shelfLife ? Number(shelfLife) : undefined,
      preferred_vendor_id: form.preferred_vendor_id || undefined,
      name_hi: form.name_hi || undefined,
      name_kn: form.name_kn || undefined,
    };

    setBusy(true);
    setSaveError(null);
    try {
      if (editing) {
        await productApi.update(editing.id, payload as any);
      } else {
        await productApi.create({ ...payload, store_id: store.id } as any);
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

  /**
   * Price history, fetched when the tab is opened rather than with the product.
   *
   * It is the answer to a question most people looking at a product are not
   * asking, and it carries cost prices — which is also why the endpoint sits
   * behind the price permission rather than the catalogue one.
   */
  useEffect(() => {
    if (detailTab !== 'history' || !editing || !store || history) return;
    let cancelled = false;
    setHistoryError(null);
    productApi
      .priceHistory(editing.id, store.id)
      .then(({ data }) => {
        if (!cancelled) setHistory(data || []);
      })
      .catch((err: any) => {
        if (cancelled) return;
        setHistoryError(
          err?.response?.status === 403
            ? 'Price history includes cost prices, which your role cannot see.'
            : err?.response?.data?.error || 'The price history could not be loaded.'
        );
        setHistory([]);
      });
    return () => {
      cancelled = true;
    };
  }, [detailTab, editing, store, history]);

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
        note: priceDraft.note.trim(),
      });
      setPriceDraft(null);
      // The history now has an entry that was not there a moment ago.
      setHistory(null);
      await refreshEditing();
    } catch (err: any) {
      setPriceError(err?.response?.data?.error || 'Could not save this price.');
    } finally {
      setBusy(false);
    }
  };

  const makeDefault = async (u: UnitRow) => {
    if (!editing) return;
    setUnitError(null);
    try {
      await productApi.setDefaultUnit(editing.id, u.id);
      await refreshEditing();
    } catch (err: any) {
      // The server refuses a unit that is inactive or not sellable and says
      // which, so this lands beside the units it is about.
      setUnitError(err?.response?.data?.error || 'Could not change the default unit.');
    }
  };

  // The server has already applied every filter, so the rows it sent are the
  // rows to show. Filtering them again here is what made "Show inactive" a
  // control that could only ever narrow.
  const rows = products;
  const capped = products.length >= LIST_CAP;

  const lowStock = useMemo(() => products.filter((p) => p.is_active && p.min_stock > 0 && p.stock <= p.min_stock), [products]);
  const multiUnit = useMemo(() => products.filter((p) => (p.units?.length ?? 0) > 1), [products]);

  const filtered = !!(categoryFilter || searchTerm || showInactive || onlyUnclassified);

  /**
   * What to say when nothing is on screen.
   *
   * An empty result under a filter is not an empty catalogue, and dressing the
   * first up as the second sends somebody off to add a product they already
   * have.
   */
  const emptyMessage = () => {
    if (onlyUnclassified) return 'Every product carries an HSN or SAC code. Nothing to fix.';
    if (searchTerm) return `Nothing matches "${searchTerm}".`;
    if (categoryFilter) return 'No products in this category.';
    return 'No products yet. Add one, or import the list you already have.';
  };

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
        <div style={{ marginLeft: 'auto', display: 'flex', gap: space.sm }}>
          <button style={ui.button} onClick={() => navigate('/categories')}>Categories</button>
          <button style={ui.button} onClick={() => navigate('/products/import')}>Import a catalogue</button>
        </div>
      </div>

      <div style={styles.summary}>
        <Stat
          label="Products"
          value={String(products.filter((p) => p.is_active).length)}
          note={
            capped
              ? `first ${LIST_CAP} — search to reach the rest`
              : filtered
                ? 'matching the filters below'
                : undefined
          }
        />
        <Stat label="Sold in more than one unit" value={String(multiUnit.length)} />
        <Stat label="At or below minimum" value={String(lowStock.length)} tone={lowStock.length ? 'danger' : undefined} />
        <Stat
          label="No HSN code"
          value={unclassifiedCount === null ? 'n/a' : String(unclassifiedCount)}
          tone={unclassifiedCount ? 'danger' : undefined}
          note={
            unclassifiedCount
              ? 'invoices selling these cannot be filed'
              : unclassifiedCount === null
                ? 'could not be counted'
                : undefined
          }
        />
      </div>

      {!!unclassifiedCount && !onlyUnclassified && (
        <div style={styles.alert} role="status">
          <span>
            <strong>
              {unclassifiedCount} {unclassifiedCount === 1 ? 'product carries' : 'products carry'} no HSN or
              SAC code.
            </strong>{' '}
            A tax invoice has to print the classification of what it sells, so every bill that includes one
            of these is a document the shop cannot file. New products cannot be saved without one; these
            predate the rule.
          </span>
          <button
            style={{ ...ui.button, marginLeft: 'auto', whiteSpace: 'nowrap' }}
            onClick={() => setOnlyUnclassified(true)}
          >
            Show them
          </button>
        </div>
      )}

      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(p) => p.id}
        loading={loading}
        onRowClick={openEdit}
        // The table's own search looks only at the rows it was given. That is
        // the right tool for a list that arrived whole and the wrong one for a
        // list the server capped, so the search box below goes to the server
        // instead and this one is switched off.
        searchable={false}
        empty={emptyMessage()}
        rowTone={(p) => (p.min_stock > 0 && p.stock <= p.min_stock ? 'warning' : undefined)}
        toolbar={
          <>
            <TextInput
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search name or barcode…"
              style={{ width: '16rem' }}
              aria-label="Search products"
            />
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
            <label style={{ display: 'flex', alignItems: 'center', gap: space.xs, fontSize: font.size.sm, whiteSpace: 'nowrap' }}>
              <input
                type="checkbox"
                checked={onlyUnclassified}
                onChange={(e) => setOnlyUnclassified(e.target.checked)}
              />
              No HSN only
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
            <button style={detailTab === 'history' ? styles.tabActive : styles.tab} onClick={() => setDetailTab('history')}>
              Price history
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
              {/* The bill can print these and until now nothing could enter
                  them. A shop in Bangalore serving a customer who reads Kannada
                  had the column, the renderer, and no way to fill it in. */}
              <Field label="Name in Hindi" hint="Printed on the bill when the customer reads Hindi." htmlFor="p-name-hi">
                <TextInput
                  id="p-name-hi"
                  lang="hi"
                  value={form.name_hi ?? ''}
                  onChange={(e) => setForm({ ...form, name_hi: e.target.value })}
                />
              </Field>
              <Field label="Name in Kannada" hint="Printed on the bill when the customer reads Kannada." htmlFor="p-name-kn">
                <TextInput
                  id="p-name-kn"
                  lang="kn"
                  value={form.name_kn ?? ''}
                  onChange={(e) => setForm({ ...form, name_kn: e.target.value })}
                />
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

            {/* Expiry belongs to the product, not to whoever is typing at the
                receiving bay. Until this existed the expiry report was only as
                complete as their memory: goods that should have been watched
                were missing from it because nobody had entered a date, and
                nothing said one was missing. */}
            <Checkbox
              label="Has a shelf life"
              hint="Receiving will ask for an expiry date, and this appears on the expiry report."
              checked={!!form.tracks_expiry}
              onChange={(v) =>
                setForm({ ...form, tracks_expiry: v, shelf_life_days: v ? form.shelf_life_days : '' })
              }
            />

            <FormGrid>
              {form.tracks_expiry && (
                <Field
                  label="Shelf life (days)"
                  hint="Leave blank if the date is printed on the packet. Filling it in lets receiving work the date out."
                  htmlFor="p-shelf"
                >
                  <NumberInput
                    id="p-shelf"
                    min={1}
                    value={form.shelf_life_days ?? ''}
                    onChange={(e) => setForm({ ...form, shelf_life_days: e.target.value })}
                  />
                </Field>
              )}
              <Field
                label="Usual supplier"
                hint="Who this is normally bought from, so a low-stock list can become an order."
                htmlFor="p-vendor"
              >
                <Select
                  id="p-vendor"
                  value={form.preferred_vendor_id ?? ''}
                  onChange={(e) => setForm({ ...form, preferred_vendor_id: e.target.value })}
                  placeholder="Not set"
                  options={vendors.map((v) => ({ value: v.id, label: v.name }))}
                />
              </Field>
            </FormGrid>

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
                          onClick={() => {
                            setUnitError(null);
                            setPriceError(null);
                            setPriceDraft({
                              note: '',
                              unit,
                              sell: String(price?.sell_price ?? 0),
                              wholesale: String(price?.wholesale_sell_price ?? 0),
                              mrp: String(price?.mrp ?? 0),
                              cost: String(price?.cost_price ?? 0),
                            });
                          }}
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

        {detail && detailTab === 'history' && (
          <PriceHistoryPanel history={history} error={historyError} />
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
        onClose={() => {
          setPriceDraft(null);
          setPriceError(null);
        }}
        footer={
          <>
            <button
              style={ui.button}
              onClick={() => {
                setPriceDraft(null);
                setPriceError(null);
              }}
              disabled={busy}
            >
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={savePrice} disabled={busy}>{busy ? 'Saving…' : 'Save price'}</button>
          </>
        }
      >
        <FormError error={priceError} />

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

        <Field
          label="Why is it changing?"
          hint="Optional, and worth the four words. It is kept with the figures, so next month somebody can see why the rate went up instead of guessing."
          htmlFor="pr-note"
        >
          <TextInput
            id="pr-note"
            maxLength={200}
            placeholder="vendor raised the rate"
            value={priceDraft?.note ?? ''}
            onChange={(e) => setPriceDraft(priceDraft ? { ...priceDraft, note: e.target.value } : null)}
          />
        </Field>
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

function Stat({
  label,
  value,
  tone,
  note,
}: {
  label: string;
  value: string;
  tone?: 'danger';
  note?: string;
}) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 10rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div style={{ ...styles.statValue, color: tone === 'danger' ? color.danger : color.text }}>{value}</div>
      {/* A figure that stands for something other than the whole shop has to
          say so on the tile. "Products 500" over a capped listing is a lie the
          reader has no way to detect. */}
      {note && <div style={styles.historySub}>{note}</div>}
    </div>
  );
}

/**
 * What this price has been.
 *
 * Each row is a state, and the movement beside it is derived from the row below
 * — which is why the list must stay newest first. Storing the difference would
 * let it disagree with the figures either side of it; deriving it cannot.
 */
function PriceHistoryPanel({ history, error }: { history: PriceMovement[] | null; error: string | null }) {
  if (error) return <div style={styles.historyEmpty}>{error}</div>;
  if (history === null) return <div style={styles.historyEmpty}>Loading…</div>;
  if (history.length === 0) {
    return <div style={styles.historyEmpty}>This product has no recorded price yet.</div>;
  }

  return (
    <div style={{ overflowX: 'auto' }}>
      <table style={styles.utable}>
        <thead>
          <tr>
            <th style={styles.uth}>When</th>
            <th style={styles.uth}>Unit</th>
            <th style={{ ...styles.uth, textAlign: 'right' }}>Selling</th>
            <th style={{ ...styles.uth, textAlign: 'right' }}>Cost</th>
            <th style={{ ...styles.uth, textAlign: 'right' }}>Margin</th>
            <th style={styles.uth}>Who and why</th>
          </tr>
        </thead>
        <tbody>
          {history.map((entry) => {
            const sellMoved = entry.previous ? entry.sell_price - entry.previous.sell_price : 0;
            const costMoved = entry.previous ? entry.cost_price - entry.previous.cost_price : 0;
            const margin = entry.sell_price - entry.cost_price;
            return (
              <tr key={entry.id}>
                <td style={styles.utd}>
                  <div>{formatDate(entry.at)}</div>
                  <div style={styles.historySub}>{sourceLabel(entry.source)}</div>
                </td>
                <td style={styles.utd}>{entry.unit_label || '—'}</td>
                <td style={{ ...styles.utd, textAlign: 'right' }}>
                  <div style={{ fontWeight: 600 }}>{formatMoney(entry.sell_price)}</div>
                  <Delta amount={sellMoved} />
                </td>
                <td style={{ ...styles.utd, textAlign: 'right' }}>
                  <div>{formatMoney(entry.cost_price)}</div>
                  <Delta amount={costMoved} />
                </td>
                <td style={{ ...styles.utd, textAlign: 'right' }}>
                  <span style={{ color: margin < 0 ? color.danger : color.text }}>{formatMoney(margin)}</span>
                </td>
                <td style={styles.utd}>
                  <div>{entry.changed_by_name || <span style={styles.historySub}>—</span>}</div>
                  {entry.note && <div style={styles.historySub}>{entry.note}</div>}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/** How much a figure moved, or nothing at all where it did not move. */
function Delta({ amount }: { amount: number }) {
  if (!amount) return null;
  const rose = amount > 0;
  return (
    <div style={{ ...styles.historySub, color: rose ? color.danger : color.success }}>
      {rose ? '▲' : '▼'} {formatMoney(Math.abs(amount))}
    </div>
  );
}

function sourceLabel(source: PriceMovement['source']): string {
  switch (source) {
    case 'opening':
      return 'where it started';
    case 'import':
      return 'from a catalogue sheet';
    default:
      return 'typed in';
  }
}


const styles: Record<string, React.CSSProperties> = {
  alert: {
    display: 'flex',
    alignItems: 'center',
    gap: space.md,
    padding: space.lg,
    marginBottom: space.lg,
    background: color.dangerSoft,
    border: `1px solid ${color.danger}33`,
    color: color.danger,
    borderRadius: '8px',
    fontSize: font.size.md,
    flexWrap: 'wrap',
  },
  historyEmpty: { padding: space.lg, color: color.textMuted, fontSize: font.size.md },
  historySub: { fontSize: font.size.xs, color: color.textMuted },
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
