import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { grnApi, productApi, purchaseApi, uomApi } from '../services/api';
import { Column, DataTable } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, NumberInput, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatDate, formatMoney, formatQty } from '../utils/format';

/**
 * Goods receipts — the loading bay.
 *
 * A delivery is counted in whatever the goods arrive as: ten sacks, or the
 * fifty 5 kg bags the vendor sent instead. This screen could only count in the
 * product's base stocking unit, so a clerk with ten sacks in front of them had
 * to type 250 and hope the arithmetic in their head matched the order's.
 *
 * Now a line names its unit — defaulting to the one the purchase order was
 * placed in, which is what confirming a delivery against the paperwork means —
 * and the three quantities are counted in it:
 *
 *     received − rejected = accepted,   accepted x qty_in_base = stock
 *
 * The one place this screen deliberately shows base units is the detail view.
 * `goods_receipt_items` has no unit column by schema: a receipt records what
 * arrived in the product's stocking unit, so ten sacks read back as 250 kg. A
 * unit field invented here would vanish on the next read, which is worse than
 * saying plainly what the numbers are counted in.
 */

interface GRNRow {
  id: string;
  po_id: string;
  po_no?: string;
  received_date: string;
  received_by?: string;
  notes?: string;
  items?: GRNItemRow[];
  item_count?: number;
  created_at?: string;
}

/** Every quantity on a receipt row is in the product's base stocking unit. */
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
  id: string;
  product_id: string;
  name?: string;
  product_name?: string;
  product_unit_id?: string;
  unit_qty?: number;
  unit_label?: string;
  /** Base units: unit_qty x qty_in_base. */
  quantity: number;
  /** Also base units. */
  received_qty: number;
  /** Per buying unit — what one sack was ordered at. */
  rate?: number;
}

interface UnitRow {
  id: string;
  label: string;
  qty_in_base: number;
  is_default?: boolean;
  is_active?: boolean;
  is_purchasable?: boolean;
}

interface PricedUnit {
  unit: UnitRow;
  price?: { cost_price?: number };
}

interface ProductRow {
  id: string;
  name: string;
  uom_id?: string;
  default_product_unit_id?: string;
  units?: PricedUnit[];
}

interface BuyingUnit {
  id: string;
  label: string;
  qty_in_base: number;
  is_default: boolean;
}

/** One line being received, counted in `product_unit_id`. */
interface DraftItem {
  po_item_id: string;
  product_id: string;
  product_name: string;
  /** What the order asked for, kept so the line can say what it is confirming. */
  ordered_unit_id: string;
  ordered_unit_qty: number;
  ordered_unit_label: string;
  ordered_base: number;
  already_base: number;
  /** The order's rate, per ordered unit. */
  rate: number;
  product_unit_id: string;
  received: string;
  rejected: string;
  reason: string;
  /**
   * What one of the line's unit landed at — one sack, not one kilo. Stock is
   * costed per base unit and the server divides by the unit size, so a per-kilo
   * figure entered against a sack would value the delivery at a twenty-fifth of
   * what it cost.
   */
  cost: string;
  /** The vendor's lot number, if the goods carry one. */
  batch_no: string;
}

/**
 * The red field for a quantity the server will refuse.
 *
 * Deliberately not the `invalid` prop on Form's inputs: that style sets
 * `borderColor` over the `border` shorthand the shared input already carries,
 * and React logs a "conflicting property" error to the console every time the
 * flag flips. Overriding the whole shorthand says the same thing in silence.
 */
function invalidStyle(bad: boolean): React.CSSProperties | undefined {
  return bad ? { border: `1px solid ${color.danger}`, background: color.dangerSoft } : undefined;
}

function buyingUnits(product?: ProductRow): BuyingUnit[] {
  const usable = (product?.units ?? [])
    .filter((x) => x?.unit && x.unit.is_active !== false && x.unit.is_purchasable !== false)
    .map((x) => ({
      id: x.unit.id,
      label: x.unit.label,
      qty_in_base: Number(x.unit.qty_in_base) || 1,
      is_default: x.unit.id === product?.default_product_unit_id || !!x.unit.is_default,
    }));
  return usable.sort((a, b) => {
    if (a.is_default !== b.is_default) return a.is_default ? -1 : 1;
    return a.qty_in_base - b.qty_in_base;
  });
}

export default function GRN() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [grns, setGrns] = useState<GRNRow[]>([]);
  const [products, setProducts] = useState<ProductRow[]>([]);
  const [uoms, setUoms] = useState<{ id: string; code: string }[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);

  const [showCreate, setShowCreate] = useState(false);
  const [poOptions, setPoOptions] = useState<POOption[]>([]);
  const [posLoading, setPosLoading] = useState(false);
  const [selectedPO, setSelectedPO] = useState('');
  const [receivedDate, setReceivedDate] = useState(() => new Date().toISOString().slice(0, 10));
  const [notes, setNotes] = useState('');
  const [draftItems, setDraftItems] = useState<DraftItem[]>([]);
  const [itemsLoading, setItemsLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);

  const [detailGRN, setDetailGRN] = useState<GRNRow | null>(null);
  const [detailItems, setDetailItems] = useState<GRNItemRow[]>([]);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState<string | null>(null);

  const loadGRNs = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    setLoadError(null);
    try {
      const { data } = await grnApi.list({ store_id: store.id });
      setGrns((data as GRNRow[]) || []);
    } catch (err: any) {
      console.error('Failed to load GRNs:', err);
      setLoadError(err?.response?.data?.error || 'Could not load goods receipts.');
    } finally {
      setLoading(false);
    }
  }, [store]);

  /**
   * Products carry the units a delivery may be counted in, and the UOM list
   * carries the code of the stocking unit those convert to. Both are loaded
   * once for the screen rather than per line: a receipt of twenty lines would
   * otherwise be twenty round trips at the moment the goods are on the floor.
   */
  const loadReferences = useCallback(async () => {
    if (!store) return;
    try {
      const [pRes, uRes] = await Promise.all([productApi.list({ store_id: store.id }), uomApi.list()]);
      setProducts((pRes.data as unknown as ProductRow[]) || []);
      setUoms((uRes.data as { id: string; code: string }[]) || []);
    } catch (err) {
      console.error('Failed to load products or units:', err);
    }
  }, [store]);

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    if (user.role === 'cashier') navigate('/billing');
  }, [user, navigate]);

  useEffect(() => {
    if (!store) return;
    loadGRNs();
    loadReferences();
  }, [store, loadGRNs, loadReferences]);

  // Lookups ------------------------------------------------------------------

  const productById = useCallback((id: string) => products.find((p) => p.id === id), [products]);

  const baseCode = useCallback(
    (productId: string): string => {
      const p = productById(productId);
      if (!p?.uom_id) return '';
      return uoms.find((u) => u.id === p.uom_id)?.code ?? '';
    },
    [productById, uoms]
  );

  const unitsFor = useCallback((productId: string) => buyingUnits(productById(productId)), [productById]);

  /**
   * How many base units one of the line's chosen unit is worth.
   *
   * The catalogue is the authority when it knows the unit. When it does not —
   * a product deactivated since the order was raised — the order line itself
   * carries both numbers, and their ratio is the size.
   */
  const unitSize = useCallback(
    (item: DraftItem): number => {
      const known = unitsFor(item.product_id).find((u) => u.id === item.product_unit_id);
      if (known) return known.qty_in_base;
      if (item.ordered_unit_qty > 0) return item.ordered_base / item.ordered_unit_qty;
      return 1;
    },
    [unitsFor]
  );

  const unitLabel = useCallback(
    (item: DraftItem): string => {
      const known = unitsFor(item.product_id).find((u) => u.id === item.product_unit_id);
      return known?.label || item.ordered_unit_label || baseCode(item.product_id) || 'unit';
    },
    [unitsFor, baseCode]
  );

  // Create -------------------------------------------------------------------

  const openCreateForm = async () => {
    setShowCreate(true);
    setSelectedPO('');
    setReceivedDate(new Date().toISOString().slice(0, 10));
    setNotes('');
    setDraftItems([]);
    setCreateError(null);
    if (!store) return;
    setPosLoading(true);
    try {
      const [pendingRes, partialRes] = await Promise.all([
        purchaseApi.list({ store_id: store.id, status: 'pending' }),
        purchaseApi.list({ store_id: store.id, status: 'partial' }),
      ]);
      setPoOptions([...((pendingRes.data || []) as POOption[]), ...((partialRes.data || []) as POOption[])]);
    } catch (err: any) {
      console.error('Failed to load POs:', err);
      setPoOptions([]);
      setCreateError(err?.response?.data?.error || 'Could not load the open purchase orders.');
    } finally {
      setPosLoading(false);
    }
  };

  const handlePOSelect = async (poId: string) => {
    setSelectedPO(poId);
    setDraftItems([]);
    setCreateError(null);
    if (!poId) return;
    setItemsLoading(true);
    try {
      const { data } = await purchaseApi.get(poId);
      const items = ((data as { items?: POItemDetail[] }).items || []).filter(
        // A line already filled has nothing left to receive, and offering it
        // only invites the over-receipt the server will refuse.
        (it) => it.quantity - it.received_qty > 1e-9
      );
      setDraftItems(
        items.map((it) => {
          const size = it.unit_qty && it.unit_qty > 0 ? it.quantity / it.unit_qty : 1;
          const outstandingBase = Math.max(0, it.quantity - it.received_qty);
          const outstandingUnits = size > 0 ? outstandingBase / size : outstandingBase;
          return {
            po_item_id: it.id,
            product_id: it.product_id,
            product_name: it.name || it.product_name || it.product_id.slice(0, 8),
            ordered_unit_id: it.product_unit_id || '',
            ordered_unit_qty: it.unit_qty ?? it.quantity,
            ordered_unit_label: it.unit_label || '',
            ordered_base: it.quantity,
            already_base: it.received_qty,
            rate: Number(it.rate) || 0,
            // Default to the unit the order was placed in.
            product_unit_id: it.product_unit_id || '',
            received: String(Number(outstandingUnits.toFixed(3))),
            rejected: '0',
            reason: '',
            // The order's own rate is what one of these was bought for, so it
            // is the landed cost unless the delivery note says otherwise. It
            // only holds while the line is counted in the ordered unit.
            cost: it.rate && it.rate > 0 ? String(it.rate) : '',
            batch_no: '',
          };
        })
      );
    } catch (err: any) {
      console.error('Failed to load PO details:', err);
      setCreateError(err?.response?.data?.error || 'Could not load this purchase order.');
    } finally {
      setItemsLoading(false);
    }
  };

  const patchItem = (index: number, patch: Partial<DraftItem>) => {
    setDraftItems((prev) => prev.map((it, i) => (i === index ? { ...it, ...patch } : it)));
  };

  const submitCreate = async () => {
    if (!selectedPO) {
      setCreateError('Choose the purchase order this delivery is against.');
      return;
    }
    if (draftItems.length === 0) {
      setCreateError('This order has nothing left to receive.');
      return;
    }

    const items: {
      product_id: string;
      product_unit_id?: string;
      received_qty: number;
      rejected_qty: number;
      reason?: string;
      cost_price?: number;
      batch_no?: string;
    }[] = [];

    for (const [i, it] of draftItems.entries()) {
      const received = parseFloat(it.received) || 0;
      const rejected = parseFloat(it.rejected) || 0;
      if (received <= 0) continue;
      if (rejected < 0) {
        setCreateError(`Line ${i + 1} cannot reject a negative quantity.`);
        return;
      }
      if (rejected > received) {
        setCreateError(
          `Line ${i + 1} rejects ${formatQty(rejected)} of the ${formatQty(received)} received — that is more than arrived.`
        );
        return;
      }
      const cost = parseFloat(it.cost) || 0;
      items.push({
        product_id: it.product_id,
        // Absent would mean the unit the PO line was placed in, which is also
        // this line's default; sending it explicitly keeps the request saying
        // what the clerk actually chose.
        product_unit_id: it.product_unit_id || undefined,
        received_qty: received,
        rejected_qty: rejected,
        reason: it.reason.trim() || undefined,
        // Zero means "use the store's recorded cost for this unit", which is
        // also what the server does with an absent one.
        cost_price: cost > 0 ? cost : undefined,
        batch_no: it.batch_no.trim() || undefined,
      });
    }

    if (items.length === 0) {
      setCreateError('Enter what arrived for at least one line.');
      return;
    }

    setSaving(true);
    setCreateError(null);
    try {
      await grnApi.create({
        po_id: selectedPO,
        received_date: receivedDate,
        notes: notes.trim() || undefined,
        // accepted_qty is deliberately not sent. The server derives it as
        // received − rejected, which is exactly the equation this screen shows;
        // sending a figure computed in floating point risks tripping the exact
        // "accepted plus rejected equals received" check for nothing.
        items,
      } as unknown as Parameters<typeof grnApi.create>[0]);
      setShowCreate(false);
      await loadGRNs();
    } catch (err: any) {
      setCreateError(err?.response?.data?.error || 'Could not record this goods receipt.');
    } finally {
      setSaving(false);
    }
  };

  // Detail -------------------------------------------------------------------

  const openDetail = async (grn: GRNRow) => {
    setDetailGRN(grn);
    setDetailItems([]);
    setDetailError(null);
    setDetailLoading(true);
    try {
      const { data } = await grnApi.get(grn.id);
      const body = data as GRNRow;
      setDetailGRN(body);
      setDetailItems(body.items || []);
    } catch (err: any) {
      console.error('Failed to load GRN detail:', err);
      setDetailError(err?.response?.data?.error || 'Could not load this goods receipt.');
    } finally {
      setDetailLoading(false);
    }
  };

  // Rows ---------------------------------------------------------------------

  const totals = useMemo(() => {
    const orders = new Set(grns.map((g) => g.po_id));
    const lines = grns.reduce((sum, g) => sum + (g.item_count ?? g.items?.length ?? 0), 0);
    const today = new Date().toISOString().slice(0, 10);
    return {
      receipts: grns.length,
      orders: orders.size,
      lines,
      today: grns.filter((g) => String(g.received_date).slice(0, 10) === today).length,
    };
  }, [grns]);

  const columns: Column<GRNRow>[] = [
    {
      key: 'id',
      header: 'Receipt',
      render: (g) => <span style={{ fontFamily: font.mono, fontSize: font.size.sm }}>{g.id.slice(0, 8)}</span>,
      value: (g) => g.id,
    },
    {
      key: 'po_no',
      header: 'Against order',
      render: (g) => (
        <span style={{ fontFamily: font.mono, fontWeight: 600 }}>{g.po_no || g.po_id.slice(0, 8)}</span>
      ),
      value: (g) => g.po_no || g.po_id,
    },
    {
      key: 'received_date',
      header: 'Received',
      render: (g) => formatDate(g.received_date),
      // The ISO string sorts chronologically; the rendered DD-MM-YYYY does not.
      value: (g) => g.received_date,
    },
    {
      key: 'received_by',
      header: 'By',
      render: (g) => g.received_by || <span style={styles.muted}>—</span>,
      value: (g) => g.received_by,
    },
    {
      key: 'item_count',
      header: 'Lines',
      numeric: true,
      render: (g) => g.item_count ?? g.items?.length ?? 0,
      value: (g) => g.item_count ?? g.items?.length ?? 0,
    },
    {
      key: 'notes',
      header: 'Notes',
      render: (g) => g.notes || <span style={styles.muted}>—</span>,
      value: (g) => g.notes,
    },
  ];

  if (!user || user.role === 'cashier') return null;

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>Goods receipts</h1>
      </div>

      {loadError && (
        <div style={{ marginBottom: space.lg }}>
          <FormError error={loadError} />
        </div>
      )}

      <div style={styles.summary}>
        <Stat label="Receipts" value={String(totals.receipts)} />
        <Stat label="Orders delivered against" value={String(totals.orders)} />
        <Stat label="Lines received" value={String(totals.lines)} />
        <Stat label="Received today" value={String(totals.today)} />
      </div>

      <DataTable
        rows={grns}
        columns={columns}
        rowKey={(g) => g.id}
        loading={loading}
        onRowClick={openDetail}
        searchPlaceholder="Search order number, receiver or notes…"
        empty="No goods receipts yet. Record one when a delivery arrives against a purchase order."
        toolbar={
          <button style={ui.buttonPrimary} onClick={openCreateForm}>
            + New goods receipt
          </button>
        }
      />

      {grns.length >= 100 && (
        <div style={styles.capNote}>Showing the most recent 100 receipts — older ones are not on this screen.</div>
      )}

      {/* Create ----------------------------------------------------------- */}
      <Modal
        open={showCreate}
        title="Record a goods receipt"
        subtitle="Count the delivery in the unit it arrived in. Rejected goods stay with the vendor; the rest goes into stock."
        width="58rem"
        onClose={() => setShowCreate(false)}
        footer={
          <>
            <button style={ui.button} onClick={() => setShowCreate(false)} disabled={saving}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={submitCreate} disabled={saving || !selectedPO}>
              {saving ? 'Saving…' : 'Record receipt'}
            </button>
          </>
        }
      >
        <FormError error={createError} />

        <FormGrid>
          <Field label="Purchase order" required htmlFor="grn-po">
            <Select
              id="grn-po"
              value={selectedPO}
              disabled={posLoading}
              onChange={(e) => handlePOSelect(e.target.value)}
              placeholder={posLoading ? 'Loading orders…' : 'Choose…'}
              options={poOptions.map((po) => ({
                value: po.id,
                label: `${po.po_no}${po.vendor_name ? ` · ${po.vendor_name}` : ''} (${po.status})`,
              }))}
            />
          </Field>
          <Field label="Received on" required htmlFor="grn-date">
            <TextInput
              id="grn-date"
              type="date"
              value={receivedDate}
              onChange={(e) => setReceivedDate(e.target.value)}
            />
          </Field>
        </FormGrid>

        <Field label="Notes" hint="Anything the paperwork should carry: partial shipment, damaged cartons." htmlFor="grn-notes">
          <TextInput
            id="grn-notes"
            value={notes}
            placeholder="e.g. Two cartons crushed in transit"
            onChange={(e) => setNotes(e.target.value)}
          />
        </Field>

        {!posLoading && poOptions.length === 0 && (
          <div style={styles.note}>
            No purchase order is open. A goods receipt is always against an order, so raise one under{' '}
            <strong>Purchase orders</strong> first.
          </div>
        )}

        {itemsLoading ? (
          <p style={styles.muted}>Loading the order's lines…</p>
        ) : draftItems.length > 0 ? (
          <>
            <div style={styles.divider}>Lines</div>
            {draftItems.map((it, idx) => {
              const units = unitsFor(it.product_id);
              const size = unitSize(it);
              const label = unitLabel(it);
              const code = baseCode(it.product_id);
              const received = parseFloat(it.received) || 0;
              const rejected = parseFloat(it.rejected) || 0;
              const cost = parseFloat(it.cost) || 0;
              const accepted = received - rejected;
              const outstandingBase = Math.max(0, it.ordered_base - it.already_base);
              const acceptedBase = accepted * size;
              const receivedBase = received * size;
              const overReceipt = receivedBase > outstandingBase + 1e-9;
              const overReject = rejected > received + 1e-9;

              return (
                <div key={it.po_item_id} style={styles.lineCard}>
                  <div style={{ display: 'flex', gap: space.lg, alignItems: 'baseline', flexWrap: 'wrap' }}>
                    <strong style={{ fontSize: font.size.lg }}>{it.product_name}</strong>
                    <span style={styles.sub}>
                      ordered {formatQty(it.ordered_unit_qty)} × {it.ordered_unit_label || code || 'unit'}
                      {it.ordered_base !== it.ordered_unit_qty && <> ({formatQty(it.ordered_base, code)})</>}
                      {it.already_base > 0 && <> · already received {formatQty(it.already_base, code)}</>} · still to
                      come{' '}
                      <strong style={{ color: color.warning }}>{formatQty(outstandingBase, code)}</strong>
                    </span>
                  </div>

                  <div style={styles.lineGrid}>
                    <Field
                      label="Counted in"
                      hint={units.length === 0 ? 'Counted in the unit it was ordered in.' : undefined}
                      htmlFor={`grn-u-${it.po_item_id}`}
                    >
                      <Select
                        id={`grn-u-${it.po_item_id}`}
                        value={it.product_unit_id}
                        disabled={units.length === 0}
                        onChange={(e) => {
                          const nextUnit = e.target.value;
                          // The order's rate prices one *ordered* unit. Once the
                          // delivery is counted in something else it is the
                          // wrong number, so it goes rather than quietly
                          // mispricing the batch.
                          const stale = it.cost !== '' && parseFloat(it.cost) === it.rate;
                          patchItem(idx, {
                            product_unit_id: nextUnit,
                            cost:
                              nextUnit === it.ordered_unit_id
                                ? String(it.rate || '')
                                : stale
                                  ? ''
                                  : it.cost,
                          });
                        }}
                        placeholder={units.length === 0 ? it.ordered_unit_label || 'Default unit' : undefined}
                        options={units.map((u) => ({
                          value: u.id,
                          label: u.qty_in_base === 1 ? u.label : `${u.label} · ${formatQty(u.qty_in_base, code)}`,
                        }))}
                      />
                    </Field>
                    <Field label={`${label} received`} required htmlFor={`grn-r-${it.po_item_id}`}>
                      <NumberInput
                        id={`grn-r-${it.po_item_id}`}
                        min={0}
                        value={it.received}
                        style={invalidStyle(overReceipt)}
                        onChange={(e) => patchItem(idx, { received: e.target.value })}
                      />
                    </Field>
                    <Field label={`${label} rejected`} hint="Damaged, short-weight, wrong goods." htmlFor={`grn-x-${it.po_item_id}`}>
                      <NumberInput
                        id={`grn-x-${it.po_item_id}`}
                        min={0}
                        value={it.rejected}
                        style={invalidStyle(overReject)}
                        onChange={(e) => patchItem(idx, { rejected: e.target.value })}
                      />
                    </Field>
                    <Field label={`${label} accepted`} hint="Received less rejected.">
                      {/* Not an input: accepted is the other two subtracted, and a
                          box that can disagree with them only invites a receipt
                          whose own arithmetic does not add up. */}
                      <div style={{ ...styles.readout, color: overReject ? color.danger : color.text }}>
                        {formatQty(Math.max(0, accepted))}
                      </div>
                    </Field>
                  </div>

                  <FormGrid>
                    <Field
                      label={`Cost per ${label}`}
                      hint={
                        cost > 0 && size !== 1
                          ? `${formatMoney(cost / size)} per ${code || 'base unit'} — what the stock is valued at.`
                          : 'Blank uses the recorded cost for this unit.'
                      }
                      htmlFor={`grn-c-${it.po_item_id}`}
                    >
                      <NumberInput
                        id={`grn-c-${it.po_item_id}`}
                        min={0}
                        value={it.cost}
                        placeholder="—"
                        onChange={(e) => patchItem(idx, { cost: e.target.value })}
                      />
                    </Field>
                    <Field
                      label="Batch or lot number"
                      hint="From the vendor's label. Blank names the batch after this receipt."
                      htmlFor={`grn-b-${it.po_item_id}`}
                    >
                      <TextInput
                        id={`grn-b-${it.po_item_id}`}
                        value={it.batch_no}
                        placeholder="—"
                        onChange={(e) => patchItem(idx, { batch_no: e.target.value })}
                      />
                    </Field>
                  </FormGrid>

                  {rejected > 0 && (
                    <Field label="Why they were rejected" htmlFor={`grn-why-${it.po_item_id}`}>
                      <TextInput
                        id={`grn-why-${it.po_item_id}`}
                        value={it.reason}
                        placeholder="e.g. Two sacks torn, weight short"
                        onChange={(e) => patchItem(idx, { reason: e.target.value })}
                      />
                    </Field>
                  )}

                  {/* The arithmetic, in the open. */}
                  <div style={styles.lineFoot}>
                    <span>
                      <strong>{formatQty(received)}</strong> received − <strong>{formatQty(rejected)}</strong> rejected
                      = <strong>{formatQty(Math.max(0, accepted))}</strong> × {label} accepted, which is{' '}
                      {size !== 1 && <>{formatQty(size, code)} each — </>}
                      <strong style={{ color: color.success }}>{formatQty(Math.max(0, acceptedBase), code)}</strong>{' '}
                      into stock
                    </span>
                    {overReceipt && (
                      <span style={{ marginLeft: 'auto', color: color.danger, fontWeight: 600 }}>
                        {formatQty(receivedBase, code)} is more than the {formatQty(outstandingBase, code)} outstanding
                        — the server will refuse it.
                      </span>
                    )}
                    {overReject && !overReceipt && (
                      <span style={{ marginLeft: 'auto', color: color.danger, fontWeight: 600 }}>
                        You cannot reject more than arrived.
                      </span>
                    )}
                  </div>
                </div>
              );
            })}
            <div style={styles.note}>
              Leave a line at zero to receive nothing for it. Rejected goods never enter stock, but they do still count
              as delivered against the order — the line is filled by what arrived, not by what was kept — so an order
              can close having put less on the shelf than it ordered. Getting the money back is a separate{' '}
              <strong>purchase return</strong>.
            </div>
          </>
        ) : selectedPO && !itemsLoading ? (
          <div style={styles.note}>Every line on this order has already been received in full.</div>
        ) : null}
      </Modal>

      {/* Detail ------------------------------------------------------------ */}
      <Modal
        open={!!detailGRN}
        title={detailGRN ? `Receipt ${detailGRN.id.slice(0, 8)}` : ''}
        subtitle={
          detailGRN
            ? `${detailGRN.po_no || detailGRN.po_id.slice(0, 8)} · received ${formatDate(detailGRN.received_date)}${
                detailGRN.received_by ? ` by ${detailGRN.received_by}` : ''
              }`
            : undefined
        }
        width="52rem"
        onClose={() => setDetailGRN(null)}
        footer={
          <button style={ui.button} onClick={() => setDetailGRN(null)}>
            Close
          </button>
        }
      >
        <FormError error={detailError} />

        {detailGRN?.notes && (
          <div>
            <div style={styles.statLabel}>Notes</div>
            <div style={{ marginTop: space.xs }}>{detailGRN.notes}</div>
          </div>
        )}

        {detailLoading ? (
          <p style={styles.muted}>Loading lines…</p>
        ) : detailItems.length === 0 ? (
          <p style={styles.muted}>This receipt has no lines.</p>
        ) : (
          <table style={styles.lineTable}>
            <thead>
              <tr>
                <th style={styles.lth}>Product</th>
                <th style={{ ...styles.lth, ...ui.numeric }}>Ordered</th>
                <th style={{ ...styles.lth, ...ui.numeric }}>Received</th>
                <th style={{ ...styles.lth, ...ui.numeric }}>Rejected</th>
                <th style={{ ...styles.lth, ...ui.numeric }}>Accepted</th>
                <th style={styles.lth}>Reason</th>
              </tr>
            </thead>
            <tbody>
              {detailItems.map((item) => {
                const code = baseCode(item.product_id);
                return (
                  <tr key={item.id || item.product_id} style={styles.ltr}>
                    <td style={styles.ltd}>{item.product_name || item.product_id.slice(0, 8)}</td>
                    <td style={{ ...styles.ltd, ...ui.numeric }}>{formatQty(item.ordered_qty, code)}</td>
                    <td style={{ ...styles.ltd, ...ui.numeric }}>{formatQty(item.received_qty, code)}</td>
                    <td style={{ ...styles.ltd, ...ui.numeric }}>
                      {item.rejected_qty > 0 ? (
                        <span style={badge('danger')}>{formatQty(item.rejected_qty, code)}</span>
                      ) : (
                        <span style={styles.muted}>—</span>
                      )}
                    </td>
                    <td style={{ ...styles.ltd, ...ui.numeric, fontWeight: 600, color: color.success }}>
                      {formatQty(item.accepted_qty, code)}
                    </td>
                    <td style={styles.ltd}>{item.reason || <span style={styles.muted}>—</span>}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}

        <div style={styles.note}>
          These figures are in each product's <strong>stocking unit</strong>, not in the unit the delivery was counted
          in. A goods receipt row carries no unit of its own — ten 25 kg sacks are recorded as the 250 kg they book
          into stock — so what the clerk counted is on the purchase order rather than here.
        </div>
      </Modal>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 10rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div style={styles.statValue}>{value}</div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  header: { display: 'flex', alignItems: 'center', gap: space.lg, marginBottom: space.lg },
  h1: { margin: 0, fontSize: font.size.xxl },
  summary: { display: 'flex', gap: space.lg, marginBottom: space.lg, flexWrap: 'wrap' },
  statLabel: {
    fontSize: font.size.xs,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    color: color.textMuted,
    fontWeight: 700,
  },
  statValue: { marginTop: space.xs, fontSize: font.size.xl, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
  sub: { fontSize: font.size.xs, color: color.textMuted },
  muted: { color: color.textFaint, fontSize: font.size.sm },
  capNote: { marginTop: space.md, fontSize: font.size.sm, color: color.textMuted },
  divider: {
    paddingTop: space.md,
    borderTop: `1px solid ${color.border}`,
    fontSize: font.size.xs,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    fontWeight: 700,
    color: color.textMuted,
  },
  lineCard: {
    border: `1px solid ${color.border}`,
    borderRadius: '10px',
    padding: space.lg,
    background: color.surfaceAlt,
    display: 'grid',
    gap: space.md,
  },
  lineGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(9rem, 1fr))', gap: space.md },
  lineFoot: {
    display: 'flex',
    gap: space.md,
    flexWrap: 'wrap',
    alignItems: 'baseline',
    fontSize: font.size.sm,
    color: color.textMuted,
    paddingTop: space.sm,
    borderTop: `1px solid ${color.border}`,
  },
  readout: {
    ...ui.input,
    background: color.primarySoft,
    borderColor: `${color.primary}44`,
    fontWeight: 700,
    fontFamily: font.mono,
    textAlign: 'right',
  },
  note: {
    padding: space.md,
    background: color.primarySoft,
    border: `1px solid ${color.primary}22`,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
  lineTable: { width: '100%', borderCollapse: 'collapse', fontSize: font.size.md },
  lth: {
    textAlign: 'left',
    padding: `${space.sm} ${space.md}`,
    fontSize: font.size.xs,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    fontWeight: 700,
    color: color.textMuted,
    borderBottom: `1px solid ${color.border}`,
    whiteSpace: 'nowrap',
  },
  ltr: { borderBottom: `1px solid ${color.border}` },
  ltd: { padding: `${space.sm} ${space.md}`, verticalAlign: 'top' },
};
