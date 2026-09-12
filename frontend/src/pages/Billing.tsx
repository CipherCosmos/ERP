import React, { useState, useEffect, useRef, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { productApi, invoiceApi, paymentApi, customerApi, barcodeApi } from '../services/api';
import { flushPendingInvoiceQueue, getPendingInvoiceCount } from '../services/offlineInvoiceQueue';
import { Product, Customer, SaleUnit } from '../types';
import { formatMoney } from '../utils/format';

function mapBarcodeLookupToProduct(data: Record<string, unknown>): Product {
  return {
    id: String(data.id),
    name: String(data.name),
    name_hi: data.name_hi != null ? String(data.name_hi) : undefined,
    name_kn: data.name_kn != null ? String(data.name_kn) : undefined,
    category_id: String(data.category_id ?? ''),
    hsn_code: String(data.hsn_code ?? ''),
    barcode: String(data.barcode ?? ''),
    uom_id: String(data.uom_id ?? ''),
    base_unit_qty: Number(data.base_unit_qty) || 1,
    gst_rate: Number(data.gst_rate) || 0,
    is_free: Boolean(data.is_free),
    is_active: data.is_active !== false,
    allow_decimal: Boolean(data.allow_decimal),
    min_stock: Number(data.min_stock) || 0,
    sell_price: Number(data.sell_price) || 0,
    mrp: Number(data.mrp) || 0,
    wholesale_sell_price: data.wholesale_sell_price != null ? Number(data.wholesale_sell_price) : undefined,
  };
}

function lineUnitPrice(product: Product, useWholesale: boolean): number {
  const w = product.wholesale_sell_price ?? 0;
  if (useWholesale && w > 0) return w;
  return product.sell_price;
}

/**
 * The ways this product may leave the counter, priced for this customer.
 *
 * The server sends `units` as {unit, price, has_price} triples ordered default
 * first then smallest to largest — a counter reaches for loose far more often
 * than for a sack. A unit with no price is deliberately kept and marked rather
 * than hidden: a unit that silently vanishes looks like a missing product,
 * while one shown as unpriced says exactly what to go and fix.
 */
function sellableUnits(product: any, useWholesale: boolean): SaleUnit[] {
  const raw = Array.isArray(product?.units) ? product.units : [];
  const units: SaleUnit[] = raw
    .filter((x: any) => x?.unit?.is_active !== false && x?.unit?.is_sellable !== false)
    .map((x: any) => {
      const ws = x.price?.wholesale_sell_price ?? 0;
      const sell = useWholesale && ws > 0 ? ws : (x.price?.sell_price ?? 0);
      return {
        id: x.unit.id,
        label: x.unit.label,
        qty_in_base: x.unit.qty_in_base ?? 1,
        sell_price: sell,
        has_price: !!x.has_price,
        barcode: x.unit.barcode ?? '',
      };
    });
  if (units.length > 0) return units;
  // A product from an older response shape, or one whose units never loaded:
  // fall back to the product's own price as a single unnamed unit so the till
  // keeps working rather than refusing to sell anything.
  return [{
    id: (product?.default_product_unit_id as string) ?? '',
    label: 'each',
    qty_in_base: 1,
    sell_price: lineUnitPrice(product as Product, useWholesale),
    has_price: true,
  }];
}

/** The key identifying a cart line: product AND unit. */
const lineKeyOf = (item: { product: { id: string }; unit?: { id: string } }) =>
  `${item.product.id}::${item.unit?.id ?? ''}`;

export default function Billing() {
  const navigate = useNavigate();
  const { user, store, cart, addToCart, updateCartQuantity, updateCartDiscount, removeFromCart, clearCart } = useAppStore();
  const [search, setSearch] = useState('');
  const [products, setProducts] = useState<Product[]>([]);
  const [filteredProducts, setFilteredProducts] = useState<Product[]>([]);
  const [paymentMode, setPaymentMode] = useState('cash');
  const [splitPayments, setSplitPayments] = useState<{ mode: string; amount: string }[]>([{ mode: 'cash', amount: '' }]);
  const [useSplitPayment, setUseSplitPayment] = useState(false);
  const [customers, setCustomers] = useState<Customer[]>([]);
  const [selectedCustomerId, setSelectedCustomerId] = useState('');
  const [useWholesalePrices, setUseWholesalePrices] = useState(false);
  const barcodeRef = useRef<HTMLInputElement>(null);
  const [checkingOut, setCheckingOut] = useState(false);
  /**
   * One key per attempt at this cart, held until the sale succeeds. A retry
   * after a timeout reuses it, so the server returns the original bill instead
   * of raising a second one — which is what stops a double-tapped Checkout
   * charging the customer twice.
   */
  const checkoutKey = useRef<string>('');
  /** Set when a product sells in more than one unit and the counter must choose. */
  const [unitPicker, setUnitPicker] = useState<{ product: Product; units: SaleUnit[] } | null>(null);

  /**
   * Cash handed over. The change is the one sum a cashier does in their head
   * on every cash sale, and getting it wrong costs the shop real money — so
   * the till does it.
   */
  const [tendered, setTendered] = useState('');
  /** A discount off the whole bill, in rupees. Apportioned across lines by the
   *  server before tax, as CGST s.15(3)(a) requires. */
  const [billDiscount, setBillDiscount] = useState('');
  /** Bills parked mid-sale — the customer went back for something they forgot. */
  const [heldBills, setHeldBills] = useState<{ id: string; at: number; cart: any[]; customerId: string; billDiscount: string }[]>(
    () => {
      try {
        return JSON.parse(localStorage.getItem('gbos-held-bills') || '[]');
      } catch {
        return [];
      }
    }
  );
  const [showHeld, setShowHeld] = useState(false);
  /** The line whose discount is being edited, if any. */
  const [discountFor, setDiscountFor] = useState<string | null>(null);

  const loadProducts = useCallback(async () => {
    if (!store) return;
    try {
      const { data } = await productApi.list({ store_id: store.id });
      setProducts(data);
    } catch (err) {
      console.error('Failed to load products:', err);
    }
  }, [store]);

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    loadProducts();
  }, [user, navigate, loadProducts]);

  useEffect(() => {
    if (!store) return;
    customerApi
      .list(store.id)
      .then((res) => setCustomers((res.data as Customer[]) || []))
      .catch(() => setCustomers([]));
  }, [store]);

  useEffect(() => {
    const c = customers.find((x) => x.id === selectedCustomerId);
    if (c?.is_wholesale) {
      setUseWholesalePrices(true);
    } else {
      setUseWholesalePrices(false);
    }
  }, [selectedCustomerId, customers]);

  useEffect(() => {
    useAppStore.getState().setSyncPending(getPendingInvoiceCount());
  }, []);

  useEffect(() => {
    const syncQueued = async () => {
      if (!store || !user || typeof navigator === 'undefined' || !navigator.onLine) return;
      await flushPendingInvoiceQueue(
        (body) => invoiceApi.create(body as any).then((r) => r.data),
        async (invoiceId, payments) => {
          await paymentApi.addSplitPayments({ invoice_id: invoiceId, payments });
        }
      );
      useAppStore.getState().setSyncPending(getPendingInvoiceCount());
    };
    syncQueued();
    window.addEventListener('online', syncQueued);
    return () => window.removeEventListener('online', syncQueued);
  }, [store, user]);

  useEffect(() => {
    if (search.length >= 2) {
      const filtered = products.filter(
        (p) =>
          p.name.toLowerCase().includes(search.toLowerCase()) ||
          p.barcode.includes(search)
      );
      setFilteredProducts(filtered.slice(0, 20));
    } else {
      setFilteredProducts([]);
    }
  }, [search, products]);

  /**
   * Put a product in the cart. When it sells only one way that is immediate;
   * when it sells several the counter is asked which, because guessing at the
   * unit means guessing at the price.
   */
  const addProduct = (product: Product, unitId?: string, qty = 1) => {
    const units = sellableUnits(product, useWholesalePrices);
    const chosen = unitId ? units.find((u) => u.id === unitId) : undefined;
    if (chosen) {
      addToCart({ product, unit: chosen, quantity: qty, discount: 0 });
      return;
    }
    if (units.length === 1) {
      addToCart({ product, unit: units[0], quantity: qty, discount: 0 });
      return;
    }
    setUnitPicker({ product, units });
  };

  /**
   * Keyboard shortcuts.
   *
   * A counter runs on a keyboard and a scanner; reaching for a mouse between
   * every customer is what makes a queue. The whole screen previously had one
   * key handler in 7,600 lines. F-keys are used because a barcode scanner types
   * digits and Enter, so anything bound to a plain character would fire from a
   * scan.
   */
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Never steal a key from a field the cashier is typing in, except the
      // F-keys, which no field wants.
      const inField = /^(INPUT|SELECT|TEXTAREA)$/.test((e.target as HTMLElement)?.tagName || '');
      switch (e.key) {
        case 'F2':
          e.preventDefault();
          barcodeRef.current?.focus();
          barcodeRef.current?.select();
          break;
        case 'F4':
          e.preventDefault();
          document.getElementById('bill-discount')?.focus();
          break;
        case 'F6':
          e.preventDefault();
          document.getElementById('tendered')?.focus();
          break;
        case 'F7':
          e.preventDefault();
          holdBillRef.current?.();
          break;
        case 'F8':
          e.preventDefault();
          setShowHeld((v) => !v);
          break;
        case 'F9':
          e.preventDefault();
          checkoutRef.current?.();
          break;
        case 'Escape':
          if (!inField) {
            e.preventDefault();
            setShowHeld(false);
            setUnitPicker(null);
          }
          break;
        default:
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  // The handlers are reached through refs so the listener above can be
  // registered once rather than re-bound on every cart change.
  const holdBillRef = useRef<() => void>();
  const checkoutRef = useRef<() => void>();

  const persistHeld = (bills: typeof heldBills) => {
    setHeldBills(bills);
    try {
      localStorage.setItem('gbos-held-bills', JSON.stringify(bills));
    } catch {
      /* a full or private-mode storage must not stop a sale */
    }
  };

  /**
   * Park the cart so the counter can serve the next customer.
   *
   * A shop needs this constantly — someone goes back for an item, or pays by
   * card that will not read. Without it the cashier either holds up the queue
   * or throws the basket away and re-scans it.
   */
  const holdBill = () => {
    if (cart.length === 0) return;
    persistHeld([
      ...heldBills,
      { id: String(Date.now()), at: Date.now(), cart: [...cart], customerId: selectedCustomerId, billDiscount },
    ]);
    clearCart();
    setSelectedCustomerId('');
    setBillDiscount('');
    setTendered('');
    barcodeRef.current?.focus();
  };

  const recallBill = (id: string) => {
    const bill = heldBills.find((b) => b.id === id);
    if (!bill) return;
    // Anything in the cart now is parked in turn rather than discarded.
    const rest = heldBills.filter((b) => b.id !== id);
    if (cart.length > 0) {
      rest.push({ id: String(Date.now()), at: Date.now(), cart: [...cart], customerId: selectedCustomerId, billDiscount });
    }
    clearCart();
    bill.cart.forEach((line: any) => addToCart(line));
    setSelectedCustomerId(bill.customerId || '');
    // Restore the bill-level discount too. Resuming at a different total than
    // the one already quoted to the customer is worse than not holding at all.
    setBillDiscount(bill.billDiscount || '');
    persistHeld(rest);
    setShowHeld(false);
    barcodeRef.current?.focus();
  };

  holdBillRef.current = holdBill;

  const handleBarcodeScan = async (barcode: string) => {
    const trimmed = barcode.trim();
    if (!trimmed || !store) return;

    // A unit's own barcode is not on the product row, so a local hit can only
    // ever be the product's own code — the server is asked for the rest.
    const local = products.find((p) => p.barcode === trimmed);
    if (local) {
      addProduct(local);
      setSearch('');
      return;
    }

    try {
      const { data } = await barcodeApi.lookup(trimmed, store.id);
      if (data && typeof data === 'object' && 'id' in data) {
        const d = data as Record<string, unknown>;
        const product = mapBarcodeLookupToProduct(d);
        // The lookup says which unit the code belonged to. Scanning the sack's
        // barcode must ring up a sack at the sack's price, not one loose kilo.
        const scanned = d.scanned_unit as Record<string, unknown> | undefined;
        const scannedPrice = d.scanned_price as Record<string, unknown> | undefined;
        if (scanned?.id) {
          const ws = Number(scannedPrice?.wholesale_sell_price) || 0;
          addToCart({
            product,
            unit: {
              id: String(scanned.id),
              label: String(scanned.label ?? 'each'),
              qty_in_base: Number(scanned.qty_in_base) || 1,
              sell_price: useWholesalePrices && ws > 0 ? ws : Number(scannedPrice?.sell_price) || 0,
              has_price: d.scanned_has_price !== false,
            },
            quantity: 1,
            discount: 0,
          });
        } else {
          addProduct(product);
        }
        setSearch('');
        return;
      }
    } catch {
      /* fall through */
    }

    try {
      const { data } = await productApi.list({ store_id: store.id, barcode: trimmed });
      if (data.length > 0) {
        addProduct(data[0]);
      }
    } catch (err) {
      console.error('Product not found:', err);
    }
    setSearch('');
  };

  const calculateTotals = () => {
    let subtotal = 0;
    let cgst = 0;
    let sgst = 0;
    let igst = 0;

    // Detect inter-state: compare store GSTIN vs customer GSTIN
    const isInterState = (() => {
      if (!store?.gstin || !selectedCustomerId) return false;
      const customer = customers.find(c => c.id === selectedCustomerId);
      if (!customer?.gstin) return false;
      return store.gstin.substring(0, 2) !== customer.gstin.substring(0, 2);
    })();

    // The bill discount is shared across lines in proportion to what each is
    // worth, BEFORE tax — CGST s.15(3)(a) allows a discount to reduce the
    // taxable value only if it is recorded on the invoice, so it cannot simply
    // be subtracted from the total afterwards. The server apportions it the
    // same way and its figures are the ones charged; this is the counter's
    // preview of them.
    const billOff = Math.max(0, Number(billDiscount) || 0);
    const grossAll = cart.reduce((sum, it) => {
      const r = it.unit?.sell_price ?? lineUnitPrice(it.product, useWholesalePrices);
      return sum + r * it.quantity - (it.discount || 0);
    }, 0);

    cart.forEach((item) => {
      // The rate belongs to the unit on the line, not to the product. A sack
      // and a loose kilo of the same rice are two different prices.
      const rate = item.unit?.sell_price ?? lineUnitPrice(item.product, useWholesalePrices);
      const net = rate * item.quantity - (item.discount || 0);
      const share = grossAll > 0 ? (billOff * net) / grossAll : 0;
      const itemTotal = Math.max(0, net - share);
      const gstAmount = (itemTotal * item.product.gst_rate) / 100;

      if (isInterState) {
        igst += gstAmount;
      } else {
        cgst += gstAmount / 2;
        sgst += gstAmount / 2;
      }
      subtotal += itemTotal;
    });

    const roundOff = Math.round(subtotal + cgst + sgst + igst) - (subtotal + cgst + sgst + igst);
    const total = subtotal + cgst + sgst + igst + roundOff;

    return { subtotal, cgst, sgst, igst, roundOff, total };
  };

  const getPaymentAmount = () => {
    const { total } = calculateTotals();
    if (useSplitPayment) {
      return splitPayments.reduce((sum, p) => sum + (parseFloat(p.amount) || 0), 0);
    }
    return total;
  };

  const handleCheckout = async () => {
    if (checkingOut) return;
    if (cart.length === 0 || !store || !user) return;

    if (!useSplitPayment && paymentMode === 'credit' && !selectedCustomerId) {
      alert('Select a customer for credit sales.');
      return;
    }

    if (!checkoutKey.current) {
      checkoutKey.current = crypto.randomUUID();
    }
    const terminal = useAppStore.getState().terminal;
    if (!terminal) {
      alert('This device is not registered to a counter. Sign out and in again, or add a terminal under Settings.');
      return;
    }

    const { total } = calculateTotals();

    if (useSplitPayment) {
      const paidAmount = splitPayments.reduce((sum, p) => sum + (parseFloat(p.amount) || 0), 0);
      // Compared in whole paise rather than with a tolerance. `0.1 + 0.2` is
      // not `0.3` in binary floating point, which is the only reason a
      // tolerance was ever needed here; rounding to the smallest unit the
      // currency actually has makes the comparison exact. It also matches the
      // server, which requires the split to equal the bill to the paisa — so a
      // shortfall is now caught at the counter instead of at the till.
      if (Math.round(paidAmount * 100) !== Math.round(total * 100)) {
        alert(`Split payments (₹${paidAmount.toFixed(2)}) must equal total (₹${total.toFixed(2)})`);
        return;
      }
    }

    // The server prices the sale from its own price list and each product's own
    // tax rate, and returns the authoritative figures. The totals computed above
    // are for the on-screen preview only.
    const invoiceBody = {
      terminal_id: terminal.id,
      customer_id: selectedCustomerId || undefined,
      payment_mode: useSplitPayment ? 'split' : paymentMode,
      round_total: true,
      discount: Math.max(0, Number(billDiscount) || 0),
      items: cart.map((item) => ({
        product_id: item.product.id,
        // unit_qty is what the customer asked for — 2 sacks, not 50 kg. The
        // server multiplies by the unit's size to move stock.
        product_unit_id: item.unit?.id || undefined,
        unit_qty: item.quantity,
        discount: item.discount || 0,
      })),
    };

    const splitLines =
      useSplitPayment && splitPayments.some((p) => parseFloat(p.amount) > 0)
        ? splitPayments
            .filter((p) => parseFloat(p.amount) > 0)
            .map((p) => ({ payment_mode: p.mode, amount: parseFloat(p.amount) }))
        : undefined;

    setCheckingOut(true);
    try {
      const { data } = await invoiceApi.create(invoiceBody as any, checkoutKey.current);
      const invoice = data.invoice;

      if (splitLines && splitLines.length > 0) {
        await paymentApi.addSplitPayments({
          invoice_id: invoice.id,
          payments: splitLines,
        });
      }

      checkoutKey.current = '';

      // The server's lines, not the cart: the paper the customer holds has to
      // match the books. Each line carries its own description, HSN code and
      // tax rate, snapshotted when the sale was written, so the receipt needs
      // no lookup against a catalogue that may since have changed.
      printReceipt(invoice, (data as any).items ?? []);

      clearCart();
      setPaymentMode('cash');
      setUseSplitPayment(false);
      setSplitPayments([{ mode: 'cash', amount: '' }]);
      setSelectedCustomerId('');
      useAppStore.getState().setSyncPending(getPendingInvoiceCount());
      // Reset the counter for the next customer. Leaving the bill discount set
      // hands the next person the previous one's concession silently, and a
      // stale "cash received" makes the change due read as nonsense.
      setBillDiscount('');
      setTendered('');
      setDiscountFor(null);
      alert('Billing completed!');
    } catch (err: any) {
      console.error('Checkout failed:', err);
      const offline = typeof navigator !== 'undefined' && !navigator.onLine;
      const code = err?.code as string | undefined;
      const networkFail =
        !err?.response && (code === 'ERR_NETWORK' || code === 'ECONNABORTED' || err?.message === 'Network Error');
      if (offline || networkFail) {
        // The cart is deliberately kept. Offline billing is not supported yet,
        // and the queue this used to write to was never read by anything — a
        // cashier was told the sale was safe while it was quietly lost. Better
        // to say plainly that nothing was recorded.
        alert('No connection — this sale was NOT recorded. Check the network and press Checkout again.');
        return;
      }
      const message = err?.response?.data?.message || err?.response?.data?.error;
      alert(message || 'Billing failed. Please try again.');
    } finally {
      setCheckingOut(false);
    }
  };

  checkoutRef.current = handleCheckout;

  const addSplitPayment = () => {
    setSplitPayments([...splitPayments, { mode: 'cash', amount: '' }]);
  };

  const updateSplitPayment = (index: number, field: 'mode' | 'amount', value: string) => {
    const updated = [...splitPayments];
    updated[index][field] = value;
    setSplitPayments(updated);
  };

  const removeSplitPayment = (index: number) => {
    if (splitPayments.length > 1) {
      setSplitPayments(splitPayments.filter((_, i) => i !== index));
    }
  };

  /**
   * Print the bill the customer takes away.
   *
   * It prints the SERVER's invoice and lines, not the cart. The two should
   * agree, but the paper in the customer's hand has to match the books exactly,
   * and only one of the two is authoritative. The previous version rebuilt the
   * figures client-side from `calculateTotals()` and priced each line with
   * `lineUnitPrice(item.product)` — the product's price rather than the price of
   * the unit actually sold — so a 25 kg sack printed at the loose per-kilo rate.
   *
   * It is also a tax invoice, which has statutory content. Under CGST Rule 46 a
   * registered supplier's invoice must carry the words "Tax Invoice", the
   * supplier's name and GSTIN, a consecutive serial number, the date, the
   * recipient's GSTIN where registered, the HSN code, the taxable value and the
   * tax under each head. The previous version printed "RECEIPT" with no GSTIN
   * and no HSN at all, which is not a document a shop can legally issue.
   */
  const printReceipt = (invoice: any, lines: any[]) => {
    const customer = selectedCustomerId ? customers.find((c) => c.id === selectedCustomerId) : null;
    const gstin = (store as any)?.gstin ?? '';
    const esc = (v: unknown) =>
      String(v ?? '').replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c] as string));
    const rs = (n: number) => `₹${Number(n ?? 0).toFixed(2)}`;
    const qty = (n: number) => String(Number(n ?? 0)).replace(/\.?0+$/, '');

    const taxLine = (label: string, amount: number) =>
      amount > 0 ? `<tr><td>${label}</td><td class="r">${rs(amount)}</td></tr>` : '';

    const receiptHtml = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>${esc(invoice.invoice_no)}</title>
<style>
  @page { margin: 4mm; }
  body { font-family: ui-monospace, "SF Mono", Menlo, monospace; width: 76mm; margin: 0; font-size: 11px; color: #000; }
  .c { text-align: center; }
  .r { text-align: right; }
  .b { font-weight: 700; }
  .rule { border-top: 1px dashed #000; margin: 4px 0; }
  table { width: 100%; border-collapse: collapse; }
  td, th { padding: 1px 0; vertical-align: top; }
  .small { font-size: 10px; }
  .title { font-size: 13px; letter-spacing: 1px; }
</style></head><body>
  <div class="c b title">TAX INVOICE</div>
  <div class="c b">${esc(store?.name)}</div>
  ${(store as any)?.address ? `<div class="c small">${esc((store as any).address)}</div>` : ''}
  ${(store as any)?.phone ? `<div class="c small">Ph: ${esc((store as any).phone)}</div>` : ''}
  ${gstin ? `<div class="c small">GSTIN: ${esc(gstin)}</div>` : ''}
  <div class="rule"></div>
  <table class="small">
    <tr><td>Invoice</td><td class="r b">${esc(invoice.invoice_no)}</td></tr>
    <tr><td>Date</td><td class="r">${new Date(invoice.invoice_date ?? Date.now()).toLocaleString('en-IN')}</td></tr>
    <tr><td>Customer</td><td class="r">${esc(customer?.name ?? 'Walk-in')}</td></tr>
    ${customer?.gstin ? `<tr><td>Cust GSTIN</td><td class="r">${esc(customer.gstin)}</td></tr>` : ''}
  </table>
  <div class="rule"></div>
  <table>
    <tr class="b"><th style="text-align:left">Item</th><th class="r">Qty</th><th class="r">Rate</th><th class="r">Amount</th></tr>
    ${lines
      .map((l: any) => {
        // The unit is what the customer asked for — "2 x 25 kg sack" — and the
        // rate is the price of one of those, so rate x qty is the amount shown.
        const unit = l.unit_label ? ` ${esc(l.unit_label)}` : '';
        const hsn = l.hsn_code ? `<div class="small">HSN ${esc(l.hsn_code)}</div>` : '';
        return `<tr><td>${esc(l.product_name ?? '')}${hsn}</td>` +
          `<td class="r">${qty(l.unit_qty ?? l.quantity)}${unit}</td>` +
          `<td class="r">${rs(l.rate)}</td>` +
          `<td class="r">${rs(l.total)}</td></tr>`;
      })
      .join('')}
  </table>
  <div class="rule"></div>
  <table>
    <tr><td>Taxable value</td><td class="r">${rs(invoice.sub_total)}</td></tr>
    ${invoice.discount > 0 ? `<tr><td>Discount</td><td class="r">- ${rs(invoice.discount)}</td></tr>` : ''}
    ${taxLine('CGST', invoice.cgst)}
    ${taxLine('SGST', invoice.sgst)}
    ${taxLine('IGST', invoice.igst)}
    ${invoice.round_off ? `<tr><td>Round off</td><td class="r">${rs(invoice.round_off)}</td></tr>` : ''}
    <tr class="b"><td>TOTAL</td><td class="r">${rs(invoice.total)}</td></tr>
    <tr><td>Paid by</td><td class="r">${esc(invoice.payment_mode)}</td></tr>
    ${tendered ? `<tr><td>Cash</td><td class="r">${rs(Number(tendered))}</td></tr>
    <tr class="b"><td>Change</td><td class="r">${rs(Number(tendered) - Number(invoice.total))}</td></tr>` : ''}
  </table>
  <div class="rule"></div>
  <div class="c small">Thank you — goods once sold are taken back only as per store policy.</div>
</body></html>`;

    const printWindow = window.open('', '_blank', 'height=600,width=380');
    if (printWindow) {
      printWindow.document.write(receiptHtml);
      printWindow.document.close();
      setTimeout(() => printWindow.print(), 250);
    }
  };

  const { subtotal, cgst, sgst, igst, roundOff, total } = calculateTotals();

  return (
    <div style={styles.container}>
      <div style={styles.leftPanel}>
        <div style={styles.searchBar}>
          <input
            ref={barcodeRef}
            type="text"
            placeholder="Scan barcode or search product..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && search) {
                handleBarcodeScan(search);
              }
            }}
            style={styles.searchInput}
            autoFocus
          />
        </div>

        {filteredProducts.length > 0 && (
          <div style={styles.suggestions}>
            {filteredProducts.map((p) => (
              <div
                key={p.id}
                style={styles.suggestionItem}
                onClick={() => {
                  // Through addProduct, not addToCart: a line with no unit has
                  // no price and no way to move stock. The search list was
                  // missed when the tiles were converted.
                  addProduct(p);
                  setSearch('');
                }}
              >
                <span>{p.name}</span>
                <span>{formatMoney(lineUnitPrice(p, useWholesalePrices))}</span>
              </div>
            ))}
          </div>
        )}

        <div style={styles.productGrid}>
          {products.slice(0, 50).map((p) => (
            <div
              key={p.id}
              style={styles.productCard}
              onClick={() => addProduct(p)}
            >
              {p.image_url && (
                <img
                  src={p.image_url}
                  alt={p.name}
                  style={styles.productImage}
                  onError={(e) => (e.currentTarget.style.display = 'none')}
                />
              )}
              <div style={styles.productName}>{p.name}</div>
              <div style={styles.productPrice}>{formatMoney(lineUnitPrice(p, useWholesalePrices))}</div>
            </div>
          ))}
        </div>
      </div>

      <div style={styles.rightPanel}>
        <h2 style={styles.cartTitle}>Cart ({cart.length})</h2>

        <div style={styles.cartItems}>
          {cart.map((item) => (
            <div key={lineKeyOf(item)} style={styles.cartItem}>
              <div style={styles.cartItemInfo}>
                <div style={styles.cartItemName}>{item.product.name}</div>
                <div style={styles.cartItemPrice}>
                  {formatMoney((item.unit?.sell_price ?? lineUnitPrice(item.product, useWholesalePrices)))} × {item.quantity}
                  {item.unit?.label && <span style={styles.cartItemUnit}> {item.unit.label}</span>}
                  {/* What actually leaves the shelf, when it differs from what
                      the customer asked for: 2 sacks is 50 kg of stock. */}
                  {item.unit && item.unit.qty_in_base !== 1 && (
                    <span style={styles.cartItemBase}>
                      {' '}= {(item.quantity * item.unit.qty_in_base).toFixed(3).replace(/\.?0+$/, '')} base
                    </span>
                  )}
                </div>
              </div>
              <div style={styles.cartItemActions}>
                {/* A discount off this line. The counter gives these constantly
                    — a damaged label, a regular customer — and the field was
                    hardcoded to zero, so the only way to honour one was to
                    change the price for everybody. */}
                {discountFor === lineKeyOf(item) ? (
                  <input
                    type="number"
                    autoFocus
                    min={0}
                    value={item.discount || ''}
                    placeholder="₹ off"
                    onChange={(e) => {
                      const v = Math.max(0, parseFloat(e.target.value) || 0);
                      updateCartDiscount(lineKeyOf(item), v);
                    }}
                    onBlur={() => setDiscountFor(null)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' || e.key === 'Escape') setDiscountFor(null);
                    }}
                    style={{ ...styles.qtyInput, width: '64px' }}
                  />
                ) : (
                  <button
                    onClick={() => setDiscountFor(lineKeyOf(item))}
                    title="Discount this line"
                    style={{
                      ...styles.qtyBtn,
                      width: 'auto',
                      padding: '0 6px',
                      color: item.discount ? '#b45309' : '#64748b',
                      fontWeight: item.discount ? 700 : 400,
                    }}
                  >
                    {item.discount ? `-₹${item.discount}` : '%'}
                  </button>
                )}
                <button
                  onClick={() => {
                    const newQty = item.product.allow_decimal
                      ? item.quantity - 0.5
                      : item.quantity - 1;
                    updateCartQuantity(lineKeyOf(item), Math.max(0.5, newQty));
                  }}
                  style={styles.qtyBtn}
                >
                  -
                </button>
                <input
                  type={item.product.allow_decimal ? 'number' : 'text'}
                  value={item.quantity}
                  onChange={(e) => {
                    const val = parseFloat(e.target.value) || 0;
                    if (val > 0) updateCartQuantity(lineKeyOf(item), val);
                  }}
                  step={item.product.allow_decimal ? '0.5' : '1'}
                  style={{...styles.qtyInput, width: '60px'}}
                />
                <button
                  onClick={() => {
                    const newQty = item.product.allow_decimal
                      ? item.quantity + 0.5
                      : item.quantity + 1;
                    updateCartQuantity(lineKeyOf(item), newQty);
                  }}
                  style={styles.qtyBtn}
                >
                  +
                </button>
                <button
                  onClick={() => removeFromCart(lineKeyOf(item))}
                  style={styles.removeBtn}
                >
                  ✕
                </button>
              </div>
            </div>
          ))}
        </div>

        <div style={styles.totals}>
          <div style={styles.totalRow}>
            <span>Subtotal</span>
            <span>{formatMoney(subtotal)}</span>
          </div>
          {cgst > 0 && <div style={styles.totalRow}>
            <span>CGST</span>
            <span>{formatMoney(cgst)}</span>
          </div>}
          {sgst > 0 && <div style={styles.totalRow}>
            <span>SGST</span>
            <span>{formatMoney(sgst)}</span>
          </div>}
          {igst > 0 && <div style={styles.totalRow}>
            <span>IGST</span>
            <span>{formatMoney(igst)}</span>
          </div>}
          <div style={styles.totalRow}>
            <span>Round Off</span>
            <span>{formatMoney(roundOff)}</span>
          </div>
          <div style={styles.grandTotal}>
            <span>Total</span>
            <span>{formatMoney(total)}</span>
          </div>

          <div style={styles.counterRow}>
            <label style={styles.counterLabel} htmlFor="bill-discount">Bill discount</label>
            <input
              id="bill-discount"
              type="number"
              min={0}
              value={billDiscount}
              placeholder="0.00"
              onChange={(e) => setBillDiscount(e.target.value)}
              style={styles.counterInput}
            />
          </div>

          {/* Cash and change. Shown only when cash is actually being taken —
              on a card or UPI sale there is nothing to count out. */}
          {!useSplitPayment && paymentMode === 'cash' && total > 0 && (
            <>
              <div style={styles.counterRow}>
                <label style={styles.counterLabel} htmlFor="tendered">Cash received</label>
                <input
                  id="tendered"
                  type="number"
                  min={0}
                  value={tendered}
                  placeholder={total.toFixed(2)}
                  onChange={(e) => setTendered(e.target.value)}
                  style={styles.counterInput}
                />
              </div>
              {Number(tendered) > 0 && (
                <div
                  style={{
                    ...styles.change,
                    // Short is the case worth shouting about: handing over goods
                    // for less than the bill is money gone.
                    background: Number(tendered) + 0.005 < total ? '#fef2f2' : '#f0fdf4',
                    color: Number(tendered) + 0.005 < total ? '#b91c1c' : '#15803d',
                  }}
                >
                  <span>{Number(tendered) + 0.005 < total ? 'Short by' : 'Change due'}</span>
                  <span>{formatMoney(Math.abs(Number(tendered) - total))}</span>
                </div>
              )}
            </>
          )}
        </div>

        <div style={styles.paymentSection}>
          <div style={{ marginBottom: '0.5rem' }}>
            <label style={{ display: 'block', fontSize: '0.8rem', color: '#555', marginBottom: 4 }}>Customer (required for credit)</label>
            <select
              value={selectedCustomerId}
              onChange={(e) => setSelectedCustomerId(e.target.value)}
              style={{ ...styles.select, width: '100%' }}
            >
              <option value="">Walk-in</option>
              {customers.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                  {c.phone ? ` · ${c.phone}` : ''}
                  {c.is_wholesale ? ' (wholesale)' : ''}
                </option>
              ))}
            </select>
          </div>
          <label style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '10px', cursor: 'pointer', fontSize: '0.9rem' }}>
            <input
              type="checkbox"
              checked={useWholesalePrices}
              onChange={(e) => setUseWholesalePrices(e.target.checked)}
            />
            Use wholesale price when set
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '8px', cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={useSplitPayment}
              onChange={(e) => setUseSplitPayment(e.target.checked)}
            />
            Split Payment
          </label>

          {useSplitPayment ? (
            <div style={styles.splitPaymentContainer}>
              {splitPayments.map((payment, index) => (
                <div key={index} style={styles.splitPaymentRow}>
                  <select
                    value={payment.mode}
                    onChange={(e) => updateSplitPayment(index, 'mode', e.target.value)}
                    style={{ ...styles.select, flex: 1 }}
                  >
                    <option value="cash">Cash</option>
                    <option value="upi">UPI</option>
                    <option value="card">Card</option>
                    <option value="bank">Bank</option>
                  </select>
                  <input
                    type="number"
                    placeholder="Amount"
                    value={payment.amount}
                    onChange={(e) => updateSplitPayment(index, 'amount', e.target.value)}
                    style={{ ...styles.input, flex: 1 }}
                  />
                  {splitPayments.length > 1 && (
                    <button onClick={() => removeSplitPayment(index)} style={styles.removeBtn}>
                      ✕
                    </button>
                  )}
                </div>
              ))}
              <button onClick={addSplitPayment} style={styles.addSplitBtn}>
                + Add Payment Method
              </button>
              <div style={styles.totalRow}>
                <span>Total</span>
                <span>{formatMoney(getPaymentAmount())}</span>
              </div>
            </div>
          ) : (
            <select
              value={paymentMode}
              onChange={(e) => setPaymentMode(e.target.value)}
              style={styles.select}
            >
              <option value="cash">Cash</option>
              <option value="upi">UPI</option>
              <option value="card">Card</option>
              <option value="bank">Bank</option>
              <option value="credit">Credit</option>
            </select>
          )}

          <div style={styles.counterActions}>
            <button
              onClick={holdBill}
              disabled={cart.length === 0}
              style={{ ...styles.holdBtn, opacity: cart.length === 0 ? 0.45 : 1 }}
              title="Park this bill (F7)"
            >
              Hold
            </button>
            <button
              onClick={() => setShowHeld((v) => !v)}
              style={styles.holdBtn}
              title="Parked bills (F8)"
            >
              Parked{heldBills.length ? ` (${heldBills.length})` : ''}
            </button>
          </div>

          <button onClick={handleCheckout} style={styles.checkoutBtn}>
            Checkout
          </button>

          <div style={styles.keyHints}>
            F2 scan · F4 discount · F6 cash · F7 hold · F8 parked · F9 checkout
          </div>

          {showHeld && (
            <div style={styles.heldPanel}>
              {heldBills.length === 0 ? (
                <div style={styles.heldEmpty}>No parked bills.</div>
              ) : (
                heldBills.map((b) => {
                  const worth = b.cart.reduce(
                    (sum: number, l: any) => sum + (l.unit?.sell_price ?? 0) * l.quantity - (l.discount || 0),
                    0
                  );
                  return (
                    <div key={b.id} style={styles.heldRow}>
                      <div>
                        <div style={{ fontWeight: 600 }}>
                          {b.cart.length} item{b.cart.length === 1 ? '' : 's'} · {formatMoney(worth)}
                        </div>
                        <div style={styles.heldWhen}>
                          parked {new Date(b.at).toLocaleTimeString('en-IN')}
                        </div>
                      </div>
                      <div style={{ display: 'flex', gap: '0.25rem' }}>
                        <button onClick={() => recallBill(b.id)} style={styles.recallBtn}>
                          Resume
                        </button>
                        <button
                          onClick={() => persistHeld(heldBills.filter((x) => x.id !== b.id))}
                          style={styles.discardBtn}
                          title="Discard this parked bill"
                        >
                          ✕
                        </button>
                      </div>
                    </div>
                  );
                })
              )}
            </div>
          )}
        </div>

        <button
          onClick={() => navigate('/')}
          style={styles.backBtn}
        >
          ← Back to Menu
        </button>
      </div>

      {/*
        Choosing the unit.

        This is the counter's version of the whole units change: the same rice
        goes out as 250 g weighed into a bag or as a 25 kg sack, at different
        prices, and the till has to ask which. The per-base figure is shown
        because a cashier comparing a bag against loose needs the comparison
        made for them, not the arithmetic.
      */}
      {unitPicker && (
        <div style={styles.pickerBackdrop} onClick={() => setUnitPicker(null)} role="presentation">
          <div
            style={styles.pickerPanel}
            onClick={(e) => e.stopPropagation()}
            role="dialog"
            aria-modal="true"
            aria-label={`Choose a unit for ${unitPicker.product.name}`}
          >
            <div style={styles.pickerHead}>
              <strong>{unitPicker.product.name}</strong>
              <span style={styles.pickerHint}>How is it going out?</span>
            </div>
            {unitPicker.units.map((u) => (
              <button
                key={u.id}
                style={{ ...styles.pickerRow, opacity: u.has_price === false ? 0.55 : 1 }}
                disabled={u.has_price === false}
                onClick={() => {
                  addToCart({ product: unitPicker.product, unit: u, quantity: 1, discount: 0 });
                  setUnitPicker(null);
                  barcodeRef.current?.focus();
                }}
              >
                <span style={{ fontWeight: 600 }}>{u.label}</span>
                <span style={styles.pickerRate}>
                  {u.has_price === false ? 'no price set' : `₹${u.sell_price.toFixed(2)}`}
                  {u.has_price !== false && u.qty_in_base !== 1 && (
                    <span style={styles.pickerPerBase}>
                      {' '}· {formatMoney((u.sell_price / u.qty_in_base))} per base
                    </span>
                  )}
                </span>
              </button>
            ))}
            <button style={styles.pickerCancel} onClick={() => setUnitPicker(null)}>
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    display: 'flex',
    height: '100vh',
    background: '#f5f5f5',
  },
  leftPanel: {
    flex: 1,
    padding: '1rem',
    overflow: 'auto',
  },
  rightPanel: {
    width: '360px',
    padding: '1rem',
    background: 'white',
    display: 'flex',
    flexDirection: 'column',
    borderLeft: '1px solid #ddd',
  },
  searchBar: {
    marginBottom: '1rem',
  },
  searchInput: {
    width: '100%',
    padding: '0.75rem',
    fontSize: '1.1rem',
    border: '1px solid #ddd',
    borderRadius: '4px',
  },
  suggestions: {
    background: 'white',
    border: '1px solid #ddd',
    borderRadius: '4px',
    marginBottom: '1rem',
    maxHeight: '200px',
    overflow: 'auto',
  },
  suggestionItem: {
    padding: '0.75rem',
    display: 'flex',
    justifyContent: 'space-between',
    cursor: 'pointer',
    borderBottom: '1px solid #eee',
  },
  productGrid: {
    display: 'grid',
    gridTemplateColumns: 'repeat(auto-fill, minmax(120px, 1fr))',
    gap: '0.5rem',
  },
  productCard: {
    padding: '0.75rem',
    background: 'white',
    borderRadius: '4px',
    cursor: 'pointer',
    textAlign: 'center',
    boxShadow: '0 1px 3px rgba(0,0,0,0.1)',
  },
  productImage: {
    width: '100%',
    height: '80px',
    objectFit: 'cover',
    borderRadius: '4px',
    marginBottom: '0.5rem',
  },
  productName: {
    fontSize: '0.875rem',
    marginBottom: '0.25rem',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  productPrice: {
    fontWeight: 'bold',
    color: '#1976d2',
  },
  cartTitle: {
    margin: '0 0 1rem',
  },
  cartItems: {
    flex: 1,
    overflow: 'auto',
  },
  cartItem: {
    padding: '0.75rem',
    borderBottom: '1px solid #eee',
  },
  cartItemInfo: {
    marginBottom: '0.5rem',
  },
  cartItemName: {
    fontWeight: '500',
  },
  pickerBackdrop: {
    position: 'fixed', inset: 0, background: 'rgba(15,23,42,0.45)',
    display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000, padding: '1rem',
  },
  pickerPanel: {
    background: '#fff', borderRadius: '10px', padding: '1rem',
    minWidth: '20rem', maxWidth: '28rem', width: '100%',
    boxShadow: '0 8px 24px rgba(15,23,42,0.2)', display: 'flex', flexDirection: 'column', gap: '0.5rem',
  },
  pickerHead: { display: 'flex', flexDirection: 'column', gap: '0.125rem', marginBottom: '0.25rem' },
  pickerHint: { fontSize: '0.8125rem', color: '#64748b' },
  pickerRow: {
    display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '1rem',
    padding: '0.75rem 0.875rem', border: '1px solid #cbd5e1', borderRadius: '6px',
    background: '#fff', cursor: 'pointer', textAlign: 'left', fontSize: '1rem',
  },
  pickerRate: { fontVariantNumeric: 'tabular-nums', whiteSpace: 'nowrap' },
  pickerPerBase: { color: '#64748b', fontSize: '0.8125rem' },
  pickerCancel: {
    padding: '0.5rem', border: 'none', background: 'transparent',
    color: '#64748b', cursor: 'pointer', fontSize: '0.875rem',
  },
  counterActions: { display: 'flex', gap: '0.5rem', marginTop: '0.75rem' },
  holdBtn: {
    flex: 1, padding: '0.5rem', fontSize: '0.875rem', cursor: 'pointer',
    border: '1px solid #cbd5e1', borderRadius: '6px', background: '#fff', color: '#0f172a',
  },
  keyHints: {
    marginTop: '0.5rem', fontSize: '0.6875rem', color: '#94a3b8',
    textAlign: 'center', lineHeight: 1.5,
  },
  heldPanel: {
    marginTop: '0.5rem', border: '1px solid #e2e8f0', borderRadius: '6px',
    background: '#f8fafc', maxHeight: '12rem', overflowY: 'auto',
  },
  heldEmpty: { padding: '0.75rem', fontSize: '0.8125rem', color: '#64748b', textAlign: 'center' },
  heldRow: {
    display: 'flex', justifyContent: 'space-between', alignItems: 'center',
    gap: '0.5rem', padding: '0.5rem 0.625rem', borderBottom: '1px solid #e2e8f0', fontSize: '0.8125rem',
  },
  heldWhen: { fontSize: '0.6875rem', color: '#94a3b8' },
  recallBtn: {
    padding: '0.25rem 0.5rem', fontSize: '0.8125rem', cursor: 'pointer',
    border: '1px solid #1d4ed8', background: '#1d4ed8', color: '#fff', borderRadius: '4px',
  },
  discardBtn: {
    padding: '0.25rem 0.5rem', fontSize: '0.8125rem', cursor: 'pointer',
    border: '1px solid #cbd5e1', background: '#fff', color: '#64748b', borderRadius: '4px',
  },
  counterRow: {
    display: 'flex', alignItems: 'center', justifyContent: 'space-between',
    gap: '0.5rem', marginTop: '0.5rem',
  },
  counterLabel: { fontSize: '0.8125rem', color: '#475569' },
  counterInput: {
    width: '7rem', padding: '0.375rem 0.5rem', fontSize: '0.9375rem',
    textAlign: 'right', fontVariantNumeric: 'tabular-nums',
    border: '1px solid #cbd5e1', borderRadius: '4px',
  },
  change: {
    display: 'flex', justifyContent: 'space-between', alignItems: 'center',
    marginTop: '0.5rem', padding: '0.5rem 0.625rem', borderRadius: '6px',
    fontWeight: 700, fontSize: '1rem', fontVariantNumeric: 'tabular-nums',
  },
  cartItemUnit: { color: '#1d4ed8', fontWeight: 600 },
  cartItemBase: { color: '#94a3b8' },
  cartItemPrice: {
    color: '#666',
    fontSize: '0.875rem',
  },
  cartItemActions: {
    display: 'flex',
    alignItems: 'center',
    gap: '0.5rem',
  },
  qtyBtn: {
    width: '28px',
    height: '28px',
    border: '1px solid #ddd',
    borderRadius: '4px',
    background: 'white',
    cursor: 'pointer',
  },
  qtyInput: {
    border: '1px solid #ddd',
    borderRadius: '4px',
    padding: '4px',
    fontSize: '0.875rem',
    textAlign: 'center',
  },
  qty: {
    minWidth: '24px',
    textAlign: 'center',
  },
  removeBtn: {
    marginLeft: 'auto',
    background: 'none',
    border: 'none',
    color: '#d32f2f',
    cursor: 'pointer',
  },
  totals: {
    borderTop: '1px solid #ddd',
    paddingTop: '1rem',
    marginTop: 'auto',
  },
  totalRow: {
    display: 'flex',
    justifyContent: 'space-between',
    marginBottom: '0.5rem',
  },
  grandTotal: {
    display: 'flex',
    justifyContent: 'space-between',
    fontSize: '1.25rem',
    fontWeight: 'bold',
    borderTop: '1px solid #ddd',
    paddingTop: '0.5rem',
  },
  paymentSection: {
    // A column, not a row. These are stacked controls — the customer, two
    // checkboxes, the payment mode and Checkout — inside a 360px panel, and as
    // a row they were crushed into four unreadable columns with every label
    // wrapping over three lines. Worse, the row overflowed the panel and pushed
    // the Checkout button past the right edge of the window, so the till's one
    // essential button sat off-screen. The children already carry marginBottom,
    // which only ever made sense in a column: the direction was simply never
    // stated and defaulted to row.
    display: 'flex',
    flexDirection: 'column',
    gap: '0.5rem',
    marginTop: '1rem',
  },
  select: {
    flex: 1,
    padding: '0.75rem',
    fontSize: '1rem',
  },
  checkoutBtn: {
    flex: 1,
    padding: '0.75rem',
    fontSize: '1rem',
    background: '#4caf50',
    color: 'white',
    border: 'none',
    borderRadius: '4px',
    cursor: 'pointer',
    fontWeight: 'bold',
  },
  backBtn: {
    marginTop: '1rem',
    padding: '0.75rem',
    background: 'none',
    border: '1px solid #ddd',
    borderRadius: '4px',
    cursor: 'pointer',
  },
  splitPaymentContainer: {
    width: '100%',
    display: 'flex',
    flexDirection: 'column',
    gap: '0.5rem',
  },
  splitPaymentRow: {
    display: 'flex',
    gap: '0.5rem',
    alignItems: 'center',
  },
  input: {
    padding: '0.75rem',
    fontSize: '1rem',
    border: '1px solid #ddd',
    borderRadius: '4px',
  },
  addSplitBtn: {
    padding: '0.5rem',
    background: '#f5f5f5',
    border: '1px dashed #ccc',
    borderRadius: '4px',
    cursor: 'pointer',
    fontSize: '0.875rem',
  },
};