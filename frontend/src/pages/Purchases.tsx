import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { productApi, purchaseApi, terminalApi, uomApi, vendorApi } from '../services/api';
import { Column, DataTable } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, NumberInput, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatDate, formatMoney, formatQty, todayISO } from '../utils/format';

/**
 * Purchase orders, in the unit the shop actually buys in.
 *
 * A distribution shop buys rice by the 25 kg sack and sells it by the 250 g
 * scoop. The model has said so for a while — a product carries a flat list of
 * units, each with its own `qty_in_base` — but this screen could only order in
 * the base stocking unit, so a buyer ordering ten sacks typed 250 and had to
 * remember what the 250 meant. Nothing on the order said "sacks", so nothing
 * on the delivery note could be checked against it.
 *
 * The invariant this screen is built around, and the reason every readout says
 * both numbers:
 *
 *     quantity (base) = unit_qty x qty_in_base
 *
 * `unit_qty` is what a human asked for — ten. `quantity` is what lands in
 * stock — 250 kg. A line total is `rate x unit_qty`, a multiplication: the rate
 * is per sack, and deriving a per-kilo rate to multiply the 250 back up is what
 * leaves a bill a paisa short.
 */

interface PurchaseOrder {
  id: string;
  po_no: string;
  vendor_id: string;
  vendor_name: string;
  order_date: string;
  expected_date: string | null;
  sub_total: number;
  total: number;
  status: string;
}

interface VendorRow {
  id: string;
  name: string;
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
  price?: { cost_price?: number; sell_price?: number };
  has_price?: boolean;
}

interface ProductRow {
  id: string;
  name: string;
  uom_id?: string;
  gst_rate?: number;
  default_product_unit_id?: string;
  units?: PricedUnit[];
}

interface POItemDetail {
  id: string;
  product_id: string;
  /** The server names the line's product product_name; name is the older key. */
  name?: string;
  product_name?: string;
  /** What was bought — the sack — and how many of them. */
  product_unit_id?: string;
  unit_qty?: number;
  unit_label?: string;
  /** The same amount in the product's base stocking unit: unit_qty x qty_in_base. */
  quantity: number;
  /** Also base units, so it compares against `quantity` however goods turned up. */
  received_qty: number;
  rate: number;
  discount: number;
  total: number;
  uom_id?: string | null;
}

/** A unit this product may be bought in. */
interface BuyingUnit {
  id: string;
  label: string;
  qty_in_base: number;
  cost_price: number;
  is_default: boolean;
}

interface DraftLine {
  product_id: string;
  product_unit_id: string;
  unit_qty: string;
  rate: string;
  discount: string;
}

/** A receiving line, counted in whichever unit the clerk is counting in. */
interface ReceiveLine {
  product_unit_id: string;
  qty: string;
  /**
   * What one of that unit landed at — one sack, not one kilo. Stock is costed
   * per base unit, so the server divides this by the unit size; sending a
   * per-kilo figure against a sack would value the delivery at a twenty-fifth
   * of what it cost.
   */
  cost: string;
}

const BLANK_LINE: DraftLine = { product_id: '', product_unit_id: '', unit_qty: '1', rate: '0', discount: '0' };

/** Falls back to a short product id so a line never renders blank. */
function itemName(it?: POItemDetail): string {
  if (!it) return '';
  return it.name || it.product_name || it.product_id.slice(0, 8);
}

/**
 * The units a product may be bought in.
 *
 * Only `is_purchasable` units: a shop may sell a 250 g pouch it repacks itself
 * and never buys one, and offering it here would raise an order the vendor
 * cannot fill. A unit with no price is kept rather than hidden — a missing cost
 * is a blank rate field, not a reason to make the sack unorderable.
 */
function buyingUnits(product?: ProductRow): BuyingUnit[] {
  const usable = (product?.units ?? [])
    .filter((x) => x?.unit && x.unit.is_active !== false && x.unit.is_purchasable !== false)
    .map((x) => ({
      id: x.unit.id,
      label: x.unit.label,
      qty_in_base: Number(x.unit.qty_in_base) || 1,
      cost_price: Number(x.price?.cost_price) || 0,
      is_default: x.unit.id === product?.default_product_unit_id || !!x.unit.is_default,
    }));
  // Default first, then smallest pack upward: that is the order a buyer thinks
  // in, and it puts the loose unit next to the default rather than at the end.
  return usable.sort((a, b) => {
    if (a.is_default !== b.is_default) return a.is_default ? -1 : 1;
    return a.qty_in_base - b.qty_in_base;
  });
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

/** What a line comes to, the way the server works it out. */
function lineAmounts(rate: number, unitQty: number, discount: number, gstRate: number) {
  const taxable = rate * unitQty - discount;
  const gst = taxable * (gstRate / 100);
  return { taxable, gst, total: taxable + gst };
}

export default function Purchases() {
  const navigate = useNavigate();
  const { user, store, terminal } = useAppStore();

  const [orders, setOrders] = useState<PurchaseOrder[]>([]);
  const [vendors, setVendors] = useState<VendorRow[]>([]);
  const [products, setProducts] = useState<ProductRow[]>([]);
  const [uoms, setUoms] = useState<{ id: string; code: string }[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState('');

  /**
   * A purchase order is raised from a counter — its number carries the
   * terminal's code, e.g. PO/T1/2026-27/00001 — so the server requires one.
   * The session picks the counter up at login the way Billing does; if that is
   * missing (an older session, or a login that could not reach the terminal
   * list) fall back to the store's first active counter.
   */
  const [terminalId, setTerminalId] = useState(terminal?.id || '');

  const [showCreate, setShowCreate] = useState(false);
  const [createVendor, setCreateVendor] = useState('');
  const [createExpected, setCreateExpected] = useState('');
  const [createLines, setCreateLines] = useState<DraftLine[]>([BLANK_LINE]);
  const [createError, setCreateError] = useState<string | null>(null);
  const [createSaving, setCreateSaving] = useState(false);

  const [detailPO, setDetailPO] = useState<PurchaseOrder | null>(null);
  const [detailItems, setDetailItems] = useState<POItemDetail[]>([]);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState<string | null>(null);

  const [receivePO, setReceivePO] = useState<PurchaseOrder | null>(null);
  const [receiveItems, setReceiveItems] = useState<POItemDetail[]>([]);
  const [receiveLines, setReceiveLines] = useState<Record<string, ReceiveLine>>({});
  const [receiveLoading, setReceiveLoading] = useState(false);
  const [receiveSaving, setReceiveSaving] = useState(false);
  const [receiveError, setReceiveError] = useState<string | null>(null);

  /**
   * Every order, filtered in the browser rather than by a round trip per
   * change of the status box. It also keeps the summary counts honest: asking
   * the server for "pending" and then counting the answer made every card
   * agree with the filter instead of describing the shop.
   */
  const loadOrders = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    setLoadError(null);
    try {
      const { data } = await purchaseApi.list({ store_id: store.id });
      setOrders((data as PurchaseOrder[]) || []);
    } catch (err: any) {
      console.error('Failed to load purchases:', err);
      setLoadError(err?.response?.data?.error || 'Could not load purchase orders.');
    } finally {
      setLoading(false);
    }
  }, [store]);

  const loadReferences = useCallback(async () => {
    if (!store) return;
    try {
      const [vRes, pRes, uRes] = await Promise.all([
        vendorApi.list(),
        productApi.list({ store_id: store.id }),
        uomApi.list(),
      ]);
      setVendors((vRes.data as VendorRow[]) || []);
      setProducts((pRes.data as unknown as ProductRow[]) || []);
      setUoms((uRes.data as { id: string; code: string }[]) || []);
    } catch (err) {
      console.error('Failed to load vendors, products or units:', err);
    }
  }, [store]);

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
    }
  }, [user, navigate]);

  useEffect(() => {
    if (!store) return;
    loadOrders();
    loadReferences();
  }, [store, loadOrders, loadReferences]);

  useEffect(() => {
    if (!store) return;
    if (terminal?.id) {
      setTerminalId(terminal.id);
      return;
    }
    let cancelled = false;
    terminalApi
      .list(store.id)
      .then((res) => {
        if (cancelled) return;
        const active = (res.data || []).find((t) => t.is_active) || null;
        setTerminalId(active?.id || '');
      })
      .catch(() => {
        if (!cancelled) setTerminalId('');
      });
    return () => {
      cancelled = true;
    };
  }, [store, terminal]);

  // Lookups ------------------------------------------------------------------

  const productById = useCallback(
    (id: string): ProductRow | undefined => products.find((p) => p.id === id),
    [products]
  );

  /** The stocking unit's code — kg, NOS — for the "what lands in stock" readout. */
  const baseCode = useCallback(
    (productId: string): string => {
      const p = productById(productId);
      if (!p?.uom_id) return '';
      return uoms.find((u) => u.id === p.uom_id)?.code ?? '';
    },
    [productById, uoms]
  );

  const unitsFor = useCallback((productId: string) => buyingUnits(productById(productId)), [productById]);

  const unitById = useCallback(
    (productId: string, unitId: string): BuyingUnit | undefined =>
      unitsFor(productId).find((u) => u.id === unitId),
    [unitsFor]
  );

  // Create -------------------------------------------------------------------

  const openCreate = () => {
    setCreateLines([BLANK_LINE]);
    setCreateVendor('');
    setCreateExpected('');
    setCreateError(null);
    setShowCreate(true);
  };

  const patchLine = (idx: number, patch: Partial<DraftLine>) => {
    setCreateLines((prev) => prev.map((ln, i) => (i === idx ? { ...ln, ...patch } : ln)));
  };

  /**
   * Choosing the product chooses its default buying unit, and the recorded cost
   * of that unit fills the rate. A buyer who orders the same sack every week
   * should not retype its price, and a wrong prefill is one keystroke away from
   * corrected — whereas an empty rate on a twelve-line order is twelve chances
   * to leave a zero in.
   */
  const chooseProduct = (idx: number, productId: string) => {
    const units = unitsFor(productId);
    const chosen = units[0];
    patchLine(idx, {
      product_id: productId,
      product_unit_id: chosen?.id ?? '',
      rate: chosen && chosen.cost_price > 0 ? String(chosen.cost_price) : '0',
    });
  };

  /** Changing the unit changes what a rate means, so the recorded cost follows it. */
  const chooseUnit = (idx: number, line: DraftLine, unitId: string) => {
    const next = unitById(line.product_id, unitId);
    const current = parseFloat(line.rate) || 0;
    const previous = unitById(line.product_id, line.product_unit_id);
    // Only overwrite a rate the buyer has not set themselves: it is either
    // still zero, or still exactly the last unit's recorded cost.
    const untouched = current === 0 || (previous ? current === previous.cost_price : false);
    patchLine(idx, {
      product_unit_id: unitId,
      ...(untouched && next && next.cost_price > 0 ? { rate: String(next.cost_price) } : null),
    });
  };

  const createTotals = useMemo(() => {
    let subTotal = 0;
    let tax = 0;
    for (const ln of createLines) {
      if (!ln.product_id) continue;
      const gst = productById(ln.product_id)?.gst_rate ?? 0;
      const amounts = lineAmounts(
        parseFloat(ln.rate) || 0,
        parseFloat(ln.unit_qty) || 0,
        parseFloat(ln.discount) || 0,
        gst
      );
      subTotal += amounts.taxable;
      tax += amounts.gst;
    }
    return { subTotal, tax, total: subTotal + tax };
  }, [createLines, productById]);

  const submitCreate = async () => {
    if (!store) return;
    if (!createVendor) {
      setCreateError('Choose the vendor this order goes to.');
      return;
    }
    if (!terminalId) {
      setCreateError(
        'This device is not registered to a counter, and a PO number carries the counter code. Sign out and in again, or add a terminal under Settings.'
      );
      return;
    }

    const items: {
      product_id: string;
      product_unit_id?: string;
      unit_qty: number;
      rate: number;
      discount: number;
    }[] = [];

    for (const [i, ln] of createLines.entries()) {
      if (!ln.product_id) continue;
      const unitQty = parseFloat(ln.unit_qty) || 0;
      const rate = parseFloat(ln.rate) || 0;
      const discount = parseFloat(ln.discount) || 0;
      if (unitQty <= 0) {
        setCreateError(`Line ${i + 1} needs a quantity greater than zero.`);
        return;
      }
      if (rate < 0 || discount < 0) {
        setCreateError(`Line ${i + 1} cannot have a negative rate or discount.`);
        return;
      }
      items.push({
        product_id: ln.product_id,
        // Absent means the product's default unit, which is what the server
        // reads a client written before units existed as meaning.
        product_unit_id: ln.product_unit_id || undefined,
        unit_qty: unitQty,
        rate,
        discount,
      });
    }

    if (items.length === 0) {
      setCreateError('Add at least one product line.');
      return;
    }

    setCreateSaving(true);
    setCreateError(null);
    try {
      await purchaseApi.create({
        store_id: store.id,
        vendor_id: createVendor,
        terminal_id: terminalId,
        order_date: todayISO(),
        expected_date: createExpected || undefined,
        // The request type in services/api.ts predates units and still spells a
        // line {product_id, quantity, ...}. The server takes unit_qty and
        // product_unit_id; `total` is deliberately not sent, because it is
        // checked for exact equality and a float-computed one fails.
        items,
      } as unknown as Parameters<typeof purchaseApi.create>[0]);
      setShowCreate(false);
      setCreateLines([BLANK_LINE]);
      setCreateExpected('');
      await loadOrders();
    } catch (err: any) {
      setCreateError(err?.response?.data?.error || 'Could not create this purchase order.');
    } finally {
      setCreateSaving(false);
    }
  };

  // Detail -------------------------------------------------------------------

  const openDetail = async (po: PurchaseOrder) => {
    setDetailPO(po);
    setDetailItems([]);
    setDetailError(null);
    setDetailLoading(true);
    try {
      const { data } = await purchaseApi.get(po.id);
      const body = data as { items?: POItemDetail[] };
      setDetailItems(body.items || []);
    } catch (err: any) {
      setDetailError(err?.response?.data?.error || 'Could not load this purchase order.');
    } finally {
      setDetailLoading(false);
    }
  };

  const cancelPO = async (id: string) => {
    if (!window.confirm('Cancel this purchase order? Only a pending order can be cancelled.')) return;
    try {
      await purchaseApi.cancel(id);
      setDetailPO(null);
      await loadOrders();
    } catch (err: any) {
      setDetailError(err?.response?.data?.error || 'Could not cancel this order.');
    }
  };

  // Receive ------------------------------------------------------------------

  const openReceive = async (po: PurchaseOrder) => {
    setReceivePO(po);
    setReceiveItems([]);
    setReceiveLines({});
    setReceiveError(null);
    setReceiveLoading(true);
    try {
      const { data } = await purchaseApi.get(po.id);
      const items = (data as { items?: POItemDetail[] }).items || [];
      setReceiveItems(items);
      const draft: Record<string, ReceiveLine> = {};
      for (const it of items) {
        // Default to the unit the line was ordered in, counting what is still
        // outstanding: an order of ten sacks half filled offers five sacks.
        const size = it.unit_qty && it.unit_qty > 0 ? it.quantity / it.unit_qty : 1;
        const outstandingBase = Math.max(0, it.quantity - it.received_qty);
        const outstandingUnits = size > 0 ? outstandingBase / size : outstandingBase;
        draft[it.id] = {
          product_unit_id: it.product_unit_id || '',
          qty: outstandingUnits > 0 ? String(Number(outstandingUnits.toFixed(3))) : '0',
          // The order's own rate is what one of these was bought for, so it is
          // the landed cost unless the delivery says otherwise. It only holds
          // while the line is counted in the unit it was ordered in.
          cost: it.rate > 0 ? String(it.rate) : '',
        };
      }
      setReceiveLines(draft);
    } catch (err: any) {
      setReceiveError(err?.response?.data?.error || 'Could not load the lines to receive.');
    } finally {
      setReceiveLoading(false);
    }
  };

  /**
   * The size of one of whatever unit this receiving line is counted in.
   *
   * A clerk may count a delivery in a different unit than the order was placed
   * in — ten sacks ordered, arriving as fifty 5 kg bags — so the size comes
   * from the chosen unit when the catalogue knows it, and otherwise from the
   * order line itself, which always carries both numbers.
   */
  const receiveUnitSize = useCallback(
    (it: POItemDetail, unitId: string): number => {
      const known = unitById(it.product_id, unitId);
      if (known) return known.qty_in_base;
      if (it.unit_qty && it.unit_qty > 0) return it.quantity / it.unit_qty;
      return 1;
    },
    [unitById]
  );

  const receiveUnitLabel = useCallback(
    (it: POItemDetail, unitId: string): string => {
      const known = unitById(it.product_id, unitId);
      if (known) return known.label;
      return it.unit_label || baseCode(it.product_id) || 'unit';
    },
    [unitById, baseCode]
  );

  const submitReceive = async () => {
    if (!store || !receivePO) return;
    const items: {
      product_id: string;
      product_unit_id?: string;
      received_qty: number;
      cost_price?: number;
    }[] = [];
    for (const it of receiveItems) {
      const line = receiveLines[it.id];
      const qty = parseFloat(line?.qty ?? '') || 0;
      if (qty <= 0) continue;
      const cost = parseFloat(line?.cost ?? '') || 0;
      items.push({
        product_id: it.product_id,
        // Absent means the unit the PO line was placed in, so a clerk who did
        // not touch the unit box confirms the order as written.
        product_unit_id: line?.product_unit_id || undefined,
        received_qty: qty,
        // Zero means "use the store's recorded cost for this unit", which is
        // what the server does with an absent one too.
        cost_price: cost > 0 ? cost : undefined,
      });
    }
    if (items.length === 0) {
      setReceiveError('Enter what arrived for at least one line.');
      return;
    }
    setReceiveSaving(true);
    setReceiveError(null);
    try {
      await purchaseApi.receive({
        po_id: receivePO.id,
        store_id: store.id,
        // As with create: api.ts still types a line {product_id, quantity, ...}.
        items,
      } as unknown as Parameters<typeof purchaseApi.receive>[0]);
      setReceivePO(null);
      setReceiveItems([]);
      setReceiveLines({});
      await loadOrders();
    } catch (err: any) {
      setReceiveError(err?.response?.data?.error || 'Could not record this delivery.');
    } finally {
      setReceiveSaving(false);
    }
  };

  // Rows ---------------------------------------------------------------------

  const rows = useMemo(
    () => (statusFilter ? orders.filter((o) => o.status === statusFilter) : orders),
    [orders, statusFilter]
  );

  const totals = useMemo(
    () => ({
      pending: orders.filter((o) => o.status === 'pending').length,
      partial: orders.filter((o) => o.status === 'partial').length,
      received: orders.filter((o) => o.status === 'received').length,
      open: orders
        .filter((o) => o.status === 'pending' || o.status === 'partial')
        .reduce((sum, o) => sum + (Number(o.total) || 0), 0),
    }),
    [orders]
  );

  const statusTone = (status: string) =>
    status === 'received' ? 'success' : status === 'partial' ? 'info' : status === 'pending' ? 'warning' : 'neutral';

  const columns: Column<PurchaseOrder>[] = [
    {
      key: 'po_no',
      header: 'PO no',
      render: (po) => <span style={{ fontFamily: font.mono, fontWeight: 600 }}>{po.po_no}</span>,
      value: (po) => po.po_no,
    },
    { key: 'vendor_name', header: 'Vendor', value: (po) => po.vendor_name },
    {
      key: 'order_date',
      header: 'Ordered',
      render: (po) => formatDate(po.order_date),
      // Sort on the raw ISO string: DD-MM-YYYY sorts by day of the month.
      value: (po) => po.order_date,
    },
    {
      key: 'expected_date',
      header: 'Expected',
      render: (po) => (po.expected_date ? formatDate(po.expected_date) : <span style={styles.muted}>—</span>),
      value: (po) => po.expected_date,
    },
    {
      key: 'total',
      header: 'Total',
      numeric: true,
      render: (po) => formatMoney(po.total),
      value: (po) => Number(po.total) || 0,
    },
    {
      key: 'status',
      header: 'Status',
      render: (po) => <span style={badge(statusTone(po.status))}>{po.status}</span>,
      value: (po) => po.status,
    },
    {
      key: 'actions',
      header: '',
      sortable: false,
      width: '11rem',
      render: (po) => (
        <div style={{ display: 'flex', gap: space.xs, justifyContent: 'flex-end' }}>
          <button
            style={styles.rowBtn}
            onClick={(e) => {
              e.stopPropagation();
              openDetail(po);
            }}
          >
            View
          </button>
          {(po.status === 'pending' || po.status === 'partial') && (
            <button
              style={{ ...styles.rowBtn, ...ui.buttonPrimary, padding: '0.25rem 0.625rem', fontSize: font.size.sm }}
              onClick={(e) => {
                e.stopPropagation();
                openReceive(po);
              }}
            >
              Receive
            </button>
          )}
        </div>
      ),
    },
  ];

  if (!user || user.role === 'cashier') return null;

  const vendorOptions = vendors.map((v) => ({ value: v.id, label: v.name }));
  const productOptions = products.map((p) => ({ value: p.id, label: p.name }));

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>Purchase orders</h1>
      </div>

      {loadError && (
        <div style={{ marginBottom: space.lg }}>
          <FormError error={loadError} />
        </div>
      )}

      <div style={styles.summary}>
        <Stat label="Pending" value={String(totals.pending)} tone={totals.pending ? 'warning' : undefined} />
        <Stat label="Part received" value={String(totals.partial)} />
        <Stat label="Received" value={String(totals.received)} />
        <Stat label="Value still on order" value={formatMoney(totals.open)} />
      </div>

      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(po) => po.id}
        loading={loading}
        onRowClick={openDetail}
        searchPlaceholder="Search PO number or vendor…"
        // A filtered-out list and an empty shop are different facts. Saying
        // "no orders" while four are sitting in another status sends someone
        // off to raise one that already exists.
        empty={
          statusFilter
            ? `No ${statusFilter} purchase orders. Clear the filter to see the rest.`
            : 'No purchase orders yet. Raise one to buy stock from a vendor.'
        }
        rowTone={(po) => (po.status === 'cancelled' ? 'danger' : undefined)}
        toolbar={
          <>
            <Select
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value)}
              style={{ width: 'auto' }}
              options={[
                { value: '', label: 'All statuses' },
                { value: 'pending', label: 'Pending' },
                { value: 'partial', label: 'Part received' },
                { value: 'received', label: 'Received' },
                { value: 'cancelled', label: 'Cancelled' },
              ]}
            />
            <button style={ui.buttonPrimary} onClick={openCreate}>
              + New purchase order
            </button>
          </>
        }
      />

      {/* The server caps a listing at 100 orders. Saying so is better than a
          screen that quietly stops being the whole picture. */}
      {orders.length >= 100 && (
        <div style={styles.capNote}>
          Showing the most recent 100 orders — older ones are not on this screen.
        </div>
      )}

      {/* Create ----------------------------------------------------------- */}
      <Modal
        open={showCreate}
        title="New purchase order"
        subtitle="Order in the unit you buy in. What lands in stock is worked out beneath each line."
        width="58rem"
        onClose={() => setShowCreate(false)}
        footer={
          <>
            <button style={ui.button} onClick={() => setShowCreate(false)} disabled={createSaving}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={submitCreate} disabled={createSaving}>
              {createSaving ? 'Saving…' : 'Create purchase order'}
            </button>
          </>
        }
      >
        <FormError error={createError} />

        <FormGrid>
          <Field label="Vendor" required htmlFor="po-vendor">
            <Select
              id="po-vendor"
              value={createVendor}
              onChange={(e) => setCreateVendor(e.target.value)}
              placeholder="Choose…"
              options={vendorOptions}
            />
          </Field>
          <Field label="Expected date" hint="When the vendor says it will arrive. Optional." htmlFor="po-expected">
            <TextInput
              id="po-expected"
              type="date"
              value={createExpected}
              onChange={(e) => setCreateExpected(e.target.value)}
            />
          </Field>
        </FormGrid>

        <div style={styles.divider}>Lines</div>

        {createLines.map((ln, idx) => {
          const product = productById(ln.product_id);
          const units = unitsFor(ln.product_id);
          const unit = unitById(ln.product_id, ln.product_unit_id);
          const size = unit?.qty_in_base ?? 1;
          const unitQty = parseFloat(ln.unit_qty) || 0;
          const rate = parseFloat(ln.rate) || 0;
          const discount = parseFloat(ln.discount) || 0;
          const gst = product?.gst_rate ?? 0;
          const amounts = lineAmounts(rate, unitQty, discount, gst);
          const base = unitQty * size;
          const code = baseCode(ln.product_id);

          return (
            <div key={idx} style={styles.lineCard}>
              <div style={styles.lineHead}>
                <div style={{ flex: 1, minWidth: '12rem' }}>
                  <Field label={`Line ${idx + 1}`} htmlFor={`po-p-${idx}`}>
                    <Select
                      id={`po-p-${idx}`}
                      value={ln.product_id}
                      onChange={(e) => chooseProduct(idx, e.target.value)}
                      placeholder="Choose a product…"
                      options={productOptions}
                    />
                  </Field>
                </div>
                {createLines.length > 1 && (
                  <button
                    style={styles.removeBtn}
                    onClick={() => setCreateLines(createLines.filter((_, i) => i !== idx))}
                    aria-label={`Remove line ${idx + 1}`}
                  >
                    ×
                  </button>
                )}
              </div>

              <div style={styles.lineGrid}>
                <Field
                  label="Buying unit"
                  hint={units.length === 0 && ln.product_id ? 'This product is bought in its stocking unit.' : undefined}
                  htmlFor={`po-u-${idx}`}
                >
                  <Select
                    id={`po-u-${idx}`}
                    value={ln.product_unit_id}
                    disabled={!ln.product_id || units.length === 0}
                    onChange={(e) => chooseUnit(idx, ln, e.target.value)}
                    placeholder={units.length === 0 ? 'Default unit' : undefined}
                    options={units.map((u) => ({
                      value: u.id,
                      label: u.qty_in_base === 1 ? u.label : `${u.label} · ${formatQty(u.qty_in_base, code)}`,
                    }))}
                  />
                </Field>
                <Field label={unit ? `How many ${unit.label}` : 'Quantity'} required htmlFor={`po-q-${idx}`}>
                  <NumberInput
                    id={`po-q-${idx}`}
                    min={0}
                    value={ln.unit_qty}
                    onChange={(e) => patchLine(idx, { unit_qty: e.target.value })}
                  />
                </Field>
                <Field label={unit ? `Rate per ${unit.label}` : 'Rate'} required htmlFor={`po-r-${idx}`}>
                  <NumberInput
                    id={`po-r-${idx}`}
                    min={0}
                    value={ln.rate}
                    onChange={(e) => patchLine(idx, { rate: e.target.value })}
                  />
                </Field>
                <Field label="Line discount" htmlFor={`po-d-${idx}`}>
                  <NumberInput
                    id={`po-d-${idx}`}
                    min={0}
                    value={ln.discount}
                    onChange={(e) => patchLine(idx, { discount: e.target.value })}
                  />
                </Field>
              </div>

              {/* The whole point of the screen: what "10 sacks" is in the unit
                  stock is counted in, said before the order is placed rather
                  than discovered on the shelf. */}
              {ln.product_id && (
                <div style={styles.lineFoot}>
                  <span>
                    <strong>
                      {formatQty(unitQty)} × {unit ? unit.label : 'unit'}
                    </strong>
                    {size !== 1 && (
                      <>
                        {' '}= <strong>{formatQty(base, code)}</strong> into stock
                      </>
                    )}
                  </span>
                  <span style={{ marginLeft: 'auto', ...ui.numeric }}>
                    {formatMoney(rate)} × {formatQty(unitQty)}
                    {discount > 0 && <> − {formatMoney(discount)}</>} + {gst}% GST ={' '}
                    <strong>{formatMoney(amounts.total)}</strong>
                  </span>
                </div>
              )}
            </div>
          );
        })}

        <div style={{ display: 'flex', alignItems: 'center', gap: space.lg, flexWrap: 'wrap' }}>
          <button style={ui.button} onClick={() => setCreateLines([...createLines, { ...BLANK_LINE }])}>
            + Add a line
          </button>
          <div style={{ marginLeft: 'auto', ...ui.numeric, fontSize: font.size.md }}>
            <span style={{ color: color.textMuted }}>Goods {formatMoney(createTotals.subTotal)} + GST{' '}
              {formatMoney(createTotals.tax)} = </span>
            <strong style={{ fontSize: font.size.lg }}>{formatMoney(createTotals.total)}</strong>
          </div>
        </div>

        <div style={styles.note}>
          The order stores both numbers: <strong>unit_qty</strong> — what you asked for — and the base quantity it
          amounts to, which is what stock, receipts and every report count in. The server works the totals out itself
          from the product's GST rate, so the figure above is this screen's arithmetic, not a claim it will be held to.
        </div>
      </Modal>

      {/* Detail ------------------------------------------------------------ */}
      <Modal
        open={!!detailPO && !receivePO}
        title={detailPO?.po_no ?? ''}
        subtitle={
          detailPO
            ? `${detailPO.vendor_name} · ordered ${formatDate(detailPO.order_date)} · ${detailPO.status}`
            : undefined
        }
        width="58rem"
        onClose={() => setDetailPO(null)}
        footer={
          <>
            {detailPO?.status === 'pending' && (
              <button style={{ ...ui.buttonDanger, marginRight: 'auto' }} onClick={() => cancelPO(detailPO.id)}>
                Cancel this order
              </button>
            )}
            <button style={ui.button} onClick={() => setDetailPO(null)}>
              Close
            </button>
            {(detailPO?.status === 'pending' || detailPO?.status === 'partial') && (
              <button
                style={ui.buttonPrimary}
                onClick={() => {
                  const po = detailPO;
                  setDetailPO(null);
                  if (po) openReceive(po);
                }}
              >
                Receive stock
              </button>
            )}
          </>
        }
      >
        <FormError error={detailError} />

        {detailLoading ? (
          <p style={styles.muted}>Loading lines…</p>
        ) : detailItems.length === 0 ? (
          <p style={styles.muted}>This order has no lines.</p>
        ) : (
          <table style={styles.lineTable}>
            <thead>
              <tr>
                <th style={styles.lth}>Product</th>
                <th style={{ ...styles.lth, ...ui.numeric }}>Ordered</th>
                <th style={{ ...styles.lth, ...ui.numeric }}>Rate</th>
                <th style={{ ...styles.lth, ...ui.numeric }}>Received</th>
                <th style={{ ...styles.lth, ...ui.numeric }}>Still to come</th>
                <th style={{ ...styles.lth, ...ui.numeric }}>Total</th>
              </tr>
            </thead>
            <tbody>
              {detailItems.map((it) => {
                const label = it.unit_label || baseCode(it.product_id) || 'unit';
                const size = it.unit_qty && it.unit_qty > 0 ? it.quantity / it.unit_qty : 1;
                const code = baseCode(it.product_id);
                const outstanding = Math.max(0, it.quantity - it.received_qty);
                return (
                  <tr key={it.id} style={styles.ltr}>
                    <td style={styles.ltd}>{itemName(it)}</td>
                    <td style={{ ...styles.ltd, ...ui.numeric }}>
                      {/* unit_qty and unit_label, not a bare base number. */}
                      <div style={{ fontWeight: 600 }}>
                        {formatQty(it.unit_qty ?? it.quantity)} × {label}
                      </div>
                      {size !== 1 && <div style={styles.sub}>= {formatQty(it.quantity, code)}</div>}
                    </td>
                    <td style={{ ...styles.ltd, ...ui.numeric }}>
                      <div>{formatMoney(it.rate)}</div>
                      <div style={styles.sub}>per {label}</div>
                    </td>
                    <td style={{ ...styles.ltd, ...ui.numeric }}>
                      <span style={{ color: it.received_qty > 0 ? color.success : color.textFaint, fontWeight: 600 }}>
                        {formatQty(it.received_qty, code)}
                      </span>
                    </td>
                    <td style={{ ...styles.ltd, ...ui.numeric }}>
                      {outstanding > 0 ? (
                        <>
                          <div style={{ color: color.warning, fontWeight: 600 }}>{formatQty(outstanding, code)}</div>
                          {size !== 1 && <div style={styles.sub}>≈ {formatQty(outstanding / size)} × {label}</div>}
                        </>
                      ) : (
                        <span style={styles.muted}>—</span>
                      )}
                    </td>
                    <td style={{ ...styles.ltd, ...ui.numeric, fontWeight: 600 }}>{formatMoney(it.total)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}

        {detailPO && (
          <div style={styles.note}>
            Goods {formatMoney(detailPO.sub_total)} plus tax comes to{' '}
            <strong>{formatMoney(detailPO.total)}</strong>. Received and still-to-come are counted in the product's
            stocking unit, because that is what a delivery books into stock however it was ordered.
          </div>
        )}
      </Modal>

      {/* Receive ----------------------------------------------------------- */}
      <Modal
        open={!!receivePO}
        title={`Receive — ${receivePO?.po_no ?? ''}`}
        subtitle="Count the delivery in whatever unit it arrived in. Accepted goods go into stock at the base quantity shown."
        width="58rem"
        onClose={() => {
          setReceivePO(null);
          setReceiveItems([]);
          setReceiveLines({});
        }}
        footer={
          <>
            <button
              style={ui.button}
              onClick={() => {
                setReceivePO(null);
                setReceiveItems([]);
                setReceiveLines({});
              }}
              disabled={receiveSaving}
            >
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={submitReceive} disabled={receiveSaving || receiveLoading}>
              {receiveSaving ? 'Posting…' : 'Confirm delivery'}
            </button>
          </>
        }
      >
        <FormError error={receiveError} />

        {receiveLoading ? (
          <p style={styles.muted}>Loading lines…</p>
        ) : receiveItems.length === 0 ? (
          <p style={styles.muted}>This order has no lines to receive against.</p>
        ) : (
          receiveItems.map((it) => {
            const line = receiveLines[it.id] ?? { product_unit_id: it.product_unit_id || '', qty: '0', cost: '' };
            const units = unitsFor(it.product_id);
            const size = receiveUnitSize(it, line.product_unit_id);
            const label = receiveUnitLabel(it, line.product_unit_id);
            const code = baseCode(it.product_id);
            const orderedLabel = it.unit_label || code || 'unit';
            const orderedSize = it.unit_qty && it.unit_qty > 0 ? it.quantity / it.unit_qty : 1;
            const qty = parseFloat(line.qty) || 0;
            const base = qty * size;
            const cost = parseFloat(line.cost) || 0;
            const outstanding = Math.max(0, it.quantity - it.received_qty);
            const over = base > outstanding + 1e-9;

            return (
              <div key={it.id} style={styles.lineCard}>
                <div style={{ display: 'flex', gap: space.lg, alignItems: 'baseline', flexWrap: 'wrap' }}>
                  <strong style={{ fontSize: font.size.lg }}>{itemName(it)}</strong>
                  <span style={styles.sub}>
                    ordered {formatQty(it.unit_qty ?? it.quantity)} × {orderedLabel}
                    {orderedSize !== 1 && <> ({formatQty(it.quantity, code)})</>} · received so far{' '}
                    {formatQty(it.received_qty, code)} · still to come{' '}
                    <strong style={{ color: outstanding > 0 ? color.warning : color.success }}>
                      {formatQty(outstanding, code)}
                    </strong>
                  </span>
                </div>

                <div style={styles.lineGrid}>
                  <Field
                    label="Counted in"
                    hint={units.length === 0 ? 'Counted in the unit it was ordered in.' : undefined}
                    htmlFor={`rc-u-${it.id}`}
                  >
                    <Select
                      id={`rc-u-${it.id}`}
                      value={line.product_unit_id}
                      disabled={units.length === 0}
                      onChange={(e) => {
                        const nextUnit = e.target.value;
                        // The order's rate is the cost of one *ordered* unit.
                        // Counting the delivery in something else makes it the
                        // wrong number, so it goes rather than silently
                        // mispricing the batch.
                        const stale = line.cost !== '' && parseFloat(line.cost) === it.rate;
                        setReceiveLines((prev) => ({
                          ...prev,
                          [it.id]: {
                            ...line,
                            product_unit_id: nextUnit,
                            cost: nextUnit === (it.product_unit_id || '') ? String(it.rate || '') : stale ? '' : line.cost,
                          },
                        }));
                      }}
                      placeholder={units.length === 0 ? orderedLabel : undefined}
                      options={units.map((u) => ({
                        value: u.id,
                        label: u.qty_in_base === 1 ? u.label : `${u.label} · ${formatQty(u.qty_in_base, code)}`,
                      }))}
                    />
                  </Field>
                  <Field label={`How many ${label} arrived`} htmlFor={`rc-q-${it.id}`}>
                    <NumberInput
                      id={`rc-q-${it.id}`}
                      min={0}
                      value={line.qty}
                      style={invalidStyle(over)}
                      onChange={(e) =>
                        setReceiveLines((prev) => ({ ...prev, [it.id]: { ...line, qty: e.target.value } }))
                      }
                    />
                  </Field>
                  <Field
                    label={`Cost per ${label}`}
                    hint={
                      cost > 0 && size !== 1
                        ? `${formatMoney(cost / size)} per ${code || 'base unit'}`
                        : 'Blank uses the recorded cost.'
                    }
                    htmlFor={`rc-c-${it.id}`}
                  >
                    <NumberInput
                      id={`rc-c-${it.id}`}
                      min={0}
                      value={line.cost}
                      placeholder="—"
                      onChange={(e) =>
                        setReceiveLines((prev) => ({ ...prev, [it.id]: { ...line, cost: e.target.value } }))
                      }
                    />
                  </Field>
                  <Field label="Goes into stock as">
                    <div style={styles.readout}>{formatQty(base, code)}</div>
                  </Field>
                </div>

                <div style={styles.lineFoot}>
                  <span>
                    <strong>
                      {formatQty(qty)} × {label}
                    </strong>
                    {size !== 1 && (
                      <>
                        {' '}× {formatQty(size, code)} = <strong>{formatQty(base, code)}</strong>
                      </>
                    )}
                  </span>
                  {over && (
                    <span style={{ marginLeft: 'auto', color: color.danger, fontWeight: 600 }}>
                      That is more than the {formatQty(outstanding, code)} still outstanding — the server will refuse
                      it.
                    </span>
                  )}
                </div>
              </div>
            );
          })
        )}

        <div style={styles.note}>
          Leave a line at zero and nothing is booked for it. The goods receipt this raises records the delivery in the
          product's stocking unit — ten 25 kg sacks are written down as the 250 kg that actually reaches the shelf —
          because a receipt row carries no unit of its own to read it back with.
        </div>
      </Modal>
    </div>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'warning' | 'danger' }) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 10rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div
        style={{
          ...styles.statValue,
          color: tone === 'danger' ? color.danger : tone === 'warning' ? color.warning : color.text,
        }}
      >
        {value}
      </div>
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
  rowBtn: { ...ui.button, padding: '0.25rem 0.625rem', fontSize: font.size.sm },
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
  lineHead: { display: 'flex', gap: space.md, alignItems: 'flex-end' },
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
  removeBtn: {
    ...ui.buttonDanger,
    padding: '0.25rem 0.625rem',
    fontSize: font.size.lg,
    lineHeight: 1,
    marginBottom: '0.35rem',
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
