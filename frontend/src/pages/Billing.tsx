import React, { useState, useEffect, useRef, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { productApi, invoiceApi, paymentApi, customerApi, barcodeApi, stockApi, terminalApi } from '../services/api';
import {
  enqueuePendingInvoice,
  flushPendingInvoiceQueue,
  getPendingInvoiceCount,
} from '../services/offlineInvoiceQueue';
import { AppLayout } from '../components/Layout/AppLayout';
import { BarcodeScannerModal } from '../components/BarcodeScannerModal';
import { useBarcodeScanner } from '../hooks/useBarcodeScanner';
import {
  IconPOS,
  IconBarcode,
  IconSearch,
  IconTrash2,
  IconPrinter,
  IconAlert,
  IconCheck,
  IconPlus,
} from '../components/Icons';
import { Product, Customer } from '../types';

interface ExtendedProduct extends Product {
  stock?: number;
  uom_name?: string;
}

/**
 * One line on the bill: a product sold *by a particular unit*.
 *
 * A sack and a loose kilo of the same rice are two different lines at two
 * different rates, which is why `lineKey` keys on the unit as well as the
 * product. Keying on the product alone merges them and one rate silently wins.
 *
 * `quantity` counts selling units — 2 sacks, not 50 kg — and `rate` is the
 * price of one of them, so the line total stays a multiplication. The server
 * converts to the base unit using the unit's own size.
 */
interface CartEntry {
  product: ExtendedProduct;
  /** The product unit this line sells by. Absent means the product's default. */
  unitId?: string;
  unitLabel: string;
  quantity: number;
  rate: number;
  discount: number;
}

/** Identity of a bill line. Two units of one product are two lines. */
function lineKey(productId: string, unitId?: string): string {
  return `${productId}::${unitId ?? 'default'}`;
}

export default function Billing() {
  const navigate = useNavigate();
  const { user, store, terminal, setTerminal } = useAppStore();

  const [products, setProducts] = useState<ExtendedProduct[]>([]);
  const [customers, setCustomers] = useState<Customer[]>([]);
  const [selectedCustomerId, setSelectedCustomerId] = useState('');
  const [useWholesalePrices, setUseWholesalePrices] = useState(false);

  // Active Cart
  const [cart, setCart] = useState<CartEntry[]>(() => {
    try {
      const saved = localStorage.getItem('gbos_active_cart');
      return saved ? JSON.parse(saved) : [];
    } catch {
      return [];
    }
  });

  // Suspended / Held Carts
  const [heldCarts, setHeldCarts] = useState<{ id: string; name: string; date: string; items: CartEntry[]; customerId?: string }[]>(() => {
    try {
      const saved = localStorage.getItem('gbos_held_carts');
      return saved ? JSON.parse(saved) : [];
    } catch {
      return [];
    }
  });

  // Search & Barcode input
  const [searchInput, setSearchInput] = useState('');
  const [searchResults, setSearchResults] = useState<ExtendedProduct[]>([]);
  const searchInputRef = useRef<HTMLInputElement | null>(null);
  const [scannerOpen, setScannerOpen] = useState(false);

  // Payments
  const [paymentMode, setPaymentMode] = useState<'cash' | 'upi' | 'card' | 'bank' | 'credit' | 'split'>('cash');
  const [splitCash, setSplitCash] = useState<number>(0);
  const [splitUpi, setSplitUpi] = useState<number>(0);
  const [splitCredit, setSplitCredit] = useState<number>(0);

  // Status & processing
  const [isProcessing, setIsProcessing] = useState(false);
  const [lastInvoice, setLastInvoice] = useState<any | null>(null);

  // Save active cart to localStorage
  useEffect(() => {
    localStorage.setItem('gbos_active_cart', JSON.stringify(cart));
  }, [cart]);

  // Save held carts to localStorage
  useEffect(() => {
    localStorage.setItem('gbos_held_carts', JSON.stringify(heldCarts));
  }, [heldCarts]);

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    if (store) {
      loadProducts();
      loadCustomers();
    }
  }, [user, store?.id]);

  // Offline queue auto-flush on reconnection
  useEffect(() => {
    const syncQueued = async () => {
      if (!store || !user || !navigator.onLine) return;
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
  }, [store?.id, user?.id]);

  // Hardware USB/Bluetooth Barcode Scanner hook
  useBarcodeScanner({
    onScan: (scannedCode) => {
      handleBarcodeScanned(scannedCode);
    },
    enabled: true,
  });

  // Listen to global layout barcode event
  useEffect(() => {
    const handleGlobalScan = (e: any) => {
      const code = e.detail?.barcode || e.detail?.code;
      if (code) {
        handleBarcodeScanned(code);
      }
    };
    window.addEventListener('gbos-barcode-scan', handleGlobalScan);
    window.addEventListener('gbos:barcode-scanned', handleGlobalScan);
    return () => {
      window.removeEventListener('gbos-barcode-scan', handleGlobalScan);
      window.removeEventListener('gbos:barcode-scanned', handleGlobalScan);
    };
  }, [products, store]);

  // Keyboard shortcut listener (F2, F4, F8, F9, Esc)
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'F2') {
        e.preventDefault();
        searchInputRef.current?.focus();
      } else if (e.key === 'F8') {
        e.preventDefault();
        holdCurrentCart();
      } else if (e.key === 'F9') {
        e.preventDefault();
        handleCheckout();
      } else if (e.key === 'Escape') {
        setSearchResults([]);
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  });

  const loadProducts = async () => {
    try {
      const { data } = await productApi.list({ store_id: store!.id });
      setProducts(data || []);
    } catch (err) {
      console.warn('Load products failed:', err);
    }
  };

  const loadCustomers = async () => {
    try {
      const { data } = await customerApi.list(store!.id);
      setCustomers(data || []);
    } catch (err) {
      console.warn('Load customers failed:', err);
    }
  };

  useEffect(() => {
    if (store && !terminal) {
      terminalApi
        .list(store.id)
        .then(({ data: terms }) => {
          const active = (terms || []).find((t) => t.is_active) || terms?.[0];
          if (active) {
            setTerminal(active);
          }
        })
        .catch((e) => console.warn('Could not load terminals:', e));
    }
  }, [store, terminal, setTerminal]);

  // Selected customer details
  const selectedCustomer = useMemo(() => {
    return customers.find((c) => c.id === selectedCustomerId) || null;
  }, [customers, selectedCustomerId]);

  // Handle wholesale pricing switch based on customer profile
  useEffect(() => {
    if (selectedCustomer?.is_wholesale) {
      setUseWholesalePrices(true);
    } else {
      setUseWholesalePrices(false);
    }
  }, [selectedCustomer]);

  // Search autocomplete
  useEffect(() => {
    const trimmed = searchInput.trim().toLowerCase();
    if (trimmed.length >= 2) {
      const matches = products.filter(
        (p) =>
          p.name.toLowerCase().includes(trimmed) ||
          (p.brand && p.brand.toLowerCase().includes(trimmed)) ||
          p.barcode.includes(trimmed)
      );
      setSearchResults(matches.slice(0, 15));
    } else {
      setSearchResults([]);
    }
  }, [searchInput, products]);

  // Scan-to-add product
  const handleBarcodeScanned = async (barcodeVal: string) => {
    const code = barcodeVal.trim();
    if (!code || !store) return;

    // 1. Search local product catalog
    const found = products.find((p) => p.barcode === code);
    if (found) {
      addProductToCart(found);
      setSearchInput('');
      setSearchResults([]);
      return;
    }

    // 2. Lookup barcode API (handles multi-unit packaging barcodes too)
    try {
      const { data } = await barcodeApi.lookup(code, store.id);
      if (data && data.id) {
        const prod: ExtendedProduct = {
          id: data.id,
          name: data.name,
          category_id: data.category_id,
          hsn_code: data.hsn_code,
          barcode: data.barcode,
          uom_id: data.uom_id,
          base_unit_qty: data.base_unit_qty || 1,
          gst_rate: data.gst_rate || 0,
          is_free: false,
          is_active: true,
          allow_decimal: Boolean(data.allow_decimal),
          min_stock: data.min_stock || 0,
          sell_price: data.sell_price || 0,
          mrp: data.mrp || 0,
          wholesale_sell_price: data.wholesale_sell_price,
          brand: data.brand,
        };
        // The server says which unit this barcode identified and what that
        // unit costs in this store. A sack's barcode must bill a sack at the
        // sack's rate — reading the product's own price here would charge the
        // loose rate for a bulk pack, which is the opposite of the trade.
        const scannedUnit = data.scanned_unit;
        if (scannedUnit?.id && data.scanned_has_price) {
          addProductToCart(prod, 1, {
            id: scannedUnit.id,
            label: scannedUnit.label || 'Piece',
            rate: Number(data.scanned_price?.sell_price) || 0,
            wholesaleRate: Number(data.scanned_price?.wholesale_sell_price) || 0,
          });
        } else if (scannedUnit?.id && !data.scanned_has_price) {
          // An unpriced pack must be refused, not sold at zero: a free item and
          // one nobody has priced look identical otherwise.
          alert(
            `${data.name} has no price set for the ${scannedUnit.label || 'scanned unit'} in this shop. Set its price before selling it.`
          );
          return;
        } else {
          addProductToCart(prod, 1);
        }
        setSearchInput('');
        setSearchResults([]);
        return;
      }
    } catch {
      // not in lookup
    }

    alert(`Product with barcode "${code}" not found. You can add it from the Products module.`);
    setSearchInput('');
  };

  /**
   * Puts a product on the bill by a given unit.
   *
   * `unit` comes from a barcode scan, which tells us exactly which pack was
   * scanned and what that pack costs here. Without it the line is sold by the
   * product's default unit at the product's own price, which is what the server
   * assumes when no unit is named.
   */
  const addProductToCart = (
    product: ExtendedProduct,
    qty = 1,
    unit?: { id: string; label: string; rate: number; wholesaleRate?: number }
  ) => {
    const rate = unit
      ? useWholesalePrices && (unit.wholesaleRate ?? 0) > 0
        ? unit.wholesaleRate!
        : unit.rate
      : useWholesalePrices && (product.wholesale_sell_price ?? 0) > 0
        ? product.wholesale_sell_price!
        : product.sell_price;

    const key = lineKey(product.id, unit?.id);

    setCart((prev) => {
      const existingIdx = prev.findIndex(
        (entry) => lineKey(entry.product.id, entry.unitId) === key
      );
      if (existingIdx >= 0) {
        const copy = [...prev];
        copy[existingIdx] = { ...copy[existingIdx], quantity: copy[existingIdx].quantity + qty };
        return copy;
      }
      return [
        ...prev,
        {
          product,
          unitId: unit?.id,
          unitLabel: unit?.label || 'Piece',
          quantity: qty,
          rate,
          discount: 0,
        },
      ];
    });
  };

  // All three take the line's key rather than a product id: the same product
  // can be on the bill twice, once as a sack and once loose, and editing one
  // must not edit the other.
  const updateQuantity = (key: string, newQty: number) => {
    if (newQty <= 0) {
      removeFromCart(key);
      return;
    }
    setCart((prev) =>
      prev.map((item) =>
        lineKey(item.product.id, item.unitId) === key ? { ...item, quantity: newQty } : item
      )
    );
  };

  const updateDiscount = (key: string, discount: number) => {
    setCart((prev) =>
      prev.map((item) =>
        lineKey(item.product.id, item.unitId) === key
          ? { ...item, discount: Math.max(0, discount) }
          : item
      )
    );
  };

  const removeFromCart = (key: string) => {
    setCart((prev) => prev.filter((item) => lineKey(item.product.id, item.unitId) !== key));
  };

  const clearActiveCart = () => {
    if (cart.length > 0 && !window.confirm('Clear all items in the active bill?')) {
      return;
    }
    setCart([]);
    setSelectedCustomerId('');
  };

  // Suspend / Hold Bill
  const holdCurrentCart = () => {
    if (cart.length === 0) return;
    const name = selectedCustomer ? `${selectedCustomer.name}` : `Bill #${heldCarts.length + 1}`;
    const newHeld = {
      id: Date.now().toString(),
      name,
      date: new Date().toLocaleTimeString(),
      items: [...cart],
      customerId: selectedCustomerId,
    };
    setHeldCarts((prev) => [...prev, newHeld]);
    setCart([]);
    setSelectedCustomerId('');
  };

  const resumeHeldCart = (heldId: string) => {
    const target = heldCarts.find((h) => h.id === heldId);
    if (!target) return;
    setCart(target.items);
    if (target.customerId) setSelectedCustomerId(target.customerId);
    setHeldCarts((prev) => prev.filter((h) => h.id !== heldId));
  };

  // Calculations
  const calculations = useMemo(() => {
    let subtotal = 0;
    let cgst = 0;
    let sgst = 0;
    let igst = 0;

    const isInterState = Boolean(
      store?.gstin &&
        selectedCustomer?.gstin &&
        store.gstin.slice(0, 2) !== selectedCustomer.gstin.slice(0, 2)
    );

    cart.forEach((item) => {
      const taxable = item.rate * item.quantity - item.discount;
      const taxRate = item.product.gst_rate || 0;
      const gstAmount = (Math.max(0, taxable) * taxRate) / 100;

      subtotal += taxable;
      if (isInterState) {
        igst += gstAmount;
      } else {
        cgst += gstAmount / 2;
        sgst += gstAmount / 2;
      }
    });

    const rawTotal = subtotal + cgst + sgst + igst;
    const roundOff = Math.round(rawTotal) - rawTotal;
    const grandTotal = Math.max(0, Math.round(rawTotal));

    return { subtotal, cgst, sgst, igst, roundOff, grandTotal };
  }, [cart, store?.gstin, selectedCustomer?.gstin]);

  // Checkout execution
  const handleCheckout = async () => {
    if (cart.length === 0 || !store || !user || isProcessing) return;

    const { subtotal, cgst, sgst, igst, roundOff, grandTotal } = calculations;

    // Credit limit validation for Khata sales
    if ((paymentMode === 'credit' || (paymentMode === 'split' && splitCredit > 0)) && !selectedCustomerId) {
      alert('Please select a customer before billing on credit (Khata).');
      return;
    }

    if (selectedCustomer && (paymentMode === 'credit' || splitCredit > 0)) {
      const creditDue = paymentMode === 'credit' ? grandTotal : splitCredit;
      if (selectedCustomer.credit_limit > 0 && creditDue > selectedCustomer.credit_limit) {
        const proceed = window.confirm(
          `Warning: Customer credit limit is ₹${selectedCustomer.credit_limit.toFixed(2)}, but credit sale is ₹${creditDue.toFixed(2)}. Proceed with authorized override?`
        );
        if (!proceed) return;
      }
    }

    // Split payment total verification
    if (paymentMode === 'split') {
      const totalEntered = (Number(splitCash) || 0) + (Number(splitUpi) || 0) + (Number(splitCredit) || 0);
      if (Math.abs(totalEntered - grandTotal) > 0.05) {
        alert(
          `Split tenders (₹${totalEntered.toFixed(2)}) must exactly equal Bill Total (₹${grandTotal.toFixed(2)}).`
        );
        return;
      }
    }

    setIsProcessing(true);

    let currentTerminalId = terminal?.id;
    if (!currentTerminalId && store) {
      try {
        const { data: terms } = await terminalApi.list(store.id);
        const active = (terms || []).find((t) => t.is_active) || terms?.[0];
        if (active) {
          currentTerminalId = active.id;
          setTerminal(active);
        }
      } catch (e) {
        console.warn('Terminal check failed:', e);
      }
    }

    if (!currentTerminalId) {
      alert('Billing error: No active counter terminal configured for this shop.');
      setIsProcessing(false);
      return;
    }

    const invoicePayload = {
      terminal_id: currentTerminalId,
      store_id: store.id,
      customer_id: selectedCustomerId || undefined,
      invoice_date: new Date().toISOString(),
      sub_total: subtotal,
      cgst,
      sgst,
      igst,
      discount: 0,
      round_off: roundOff,
      total: grandTotal,
      status: 'completed',
      payment_mode: paymentMode,
      round_total: true,
      // product_unit_id names which pack is leaving; unit_qty counts packs, not
      // base units. Omitting the unit makes the server fall back to the
      // product's default, so a sack would bill as a loose kilo.
      items: cart.map((item) => ({
        product_id: item.product.id,
        ...(item.unitId ? { product_unit_id: item.unitId } : {}),
        unit_qty: item.quantity,
        rate: item.rate,
        discount: item.discount,
        total: item.rate * item.quantity - item.discount,
      })),
    };

    const splitLines =
      paymentMode === 'split'
        ? [
            { payment_mode: 'cash', amount: Number(splitCash) || 0 },
            { payment_mode: 'upi', amount: Number(splitUpi) || 0 },
            { payment_mode: 'credit', amount: Number(splitCredit) || 0 },
          ].filter((p) => p.amount > 0)
        : undefined;

    try {
      const { data: invoice } = await invoiceApi.create(invoicePayload as any);

      if (splitLines && splitLines.length > 0) {
        await paymentApi.addSplitPayments({
          invoice_id: invoice.id,
          payments: splitLines,
        });
      }

      const createdInvoice = (invoice as any)?.invoice || invoice;
      setLastInvoice(createdInvoice);
      setCart([]);
      setSelectedCustomerId('');
      setPaymentMode('cash');
      setSplitCash(0);
      setSplitUpi(0);
      setSplitCredit(0);
      alert(`Sale completed successfully! Invoice #${createdInvoice.invoice_no || createdInvoice.id}`);
    } catch (err: any) {
      console.warn('Checkout network failure, saving offline:', err);
      // Fallback: Queue invoice locally for offline sync
      enqueuePendingInvoice({
        invoiceBody: invoicePayload as Record<string, unknown>,
        splitPayments: splitLines,
      });
      useAppStore.getState().setSyncPending(getPendingInvoiceCount());
      setCart([]);
      setSelectedCustomerId('');
      alert('Network unavailable: Bill recorded locally in offline queue. Will sync automatically upon reconnection.');
    } finally {
      setIsProcessing(false);
    }
  };

  const handlePrint = () => {
    window.print();
  };

  return (
    <AppLayout fullWidth>
      <div style={{ display: 'flex', flexDirection: 'column', height: 'calc(100vh - var(--header-height))' }}>
        {/* Top Control Bar: Barcode scan input + Customer Selector + Bill Hold */}
        <div
          style={{
            backgroundColor: '#ffffff',
            borderBottom: '1px solid var(--border-light)',
            padding: '0.65rem 1.25rem',
            display: 'flex',
            alignItems: 'center',
            gap: '0.875rem',
            flexWrap: 'wrap',
          }}
        >
          {/* Barcode & Product Search Input */}
          <div style={{ flex: '1 1 320px', position: 'relative' }}>
            <div style={{ display: 'flex', gap: '0.35rem' }}>
              <input
                ref={searchInputRef}
                type="text"
                className="form-input text-mono"
                placeholder="Scan Barcode (F2) or type Product Name / Brand..."
                value={searchInput}
                onChange={(e) => setSearchInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && searchInput.trim()) {
                    handleBarcodeScanned(searchInput.trim());
                  }
                }}
              />
              <button
                type="button"
                className="btn btn-outline btn-sm"
                onClick={() => setScannerOpen(true)}
                title="Camera / Manual Barcode Scan"
              >
                <IconBarcode size={18} />
              </button>
            </div>

            {/* Live Autocomplete Dropdown */}
            {searchResults.length > 0 && (
              <div
                style={{
                  position: 'absolute',
                  top: '100%',
                  left: 0,
                  right: 0,
                  backgroundColor: '#ffffff',
                  border: '1px solid var(--border-medium)',
                  borderRadius: 'var(--radius-md)',
                  boxShadow: 'var(--shadow-lg)',
                  zIndex: 200,
                  maxHeight: 280,
                  overflowY: 'auto',
                  marginTop: 4,
                }}
              >
                {searchResults.map((p) => (
                  <div
                    key={p.id}
                    onClick={() => {
                      addProductToCart(p);
                      setSearchInput('');
                      setSearchResults([]);
                    }}
                    style={{
                      padding: '0.5rem 0.875rem',
                      borderBottom: '1px solid var(--border-light)',
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      cursor: 'pointer',
                    }}
                    onMouseEnter={(e) => (e.currentTarget.style.backgroundColor = '#f8fafc')}
                    onMouseLeave={(e) => (e.currentTarget.style.backgroundColor = '#fff')}
                  >
                    <div>
                      <div style={{ fontWeight: 600, fontSize: '0.85rem' }}>{p.name}</div>
                      <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>
                        Brand: <strong>{p.brand || 'Unbranded'}</strong> &middot; Barcode: {p.barcode}
                      </div>
                    </div>
                    <div style={{ textAlign: 'right' }}>
                      <div style={{ fontWeight: 700, fontSize: '0.9rem', color: 'var(--primary-600)' }}>
                        ₹{p.sell_price.toFixed(2)}
                      </div>
                      <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
                        Stock: {p.stock ?? 0}
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>

          {/* Customer Selector */}
          <div style={{ width: 260 }}>
            <select
              className="form-select"
              value={selectedCustomerId}
              onChange={(e) => setSelectedCustomerId(e.target.value)}
            >
              <option value="">Walk-in Retail Customer</option>
              {customers.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name} {c.phone ? `(${c.phone})` : ''} {c.is_wholesale ? '[Wholesale]' : ''}
                </option>
              ))}
            </select>
          </div>

          {/* Customer Khata Status Pill */}
          {selectedCustomer && (
            <div
              style={{
                fontSize: '0.75rem',
                backgroundColor: 'var(--bg-subtle)',
                padding: '0.35rem 0.65rem',
                borderRadius: 'var(--radius-sm)',
                border: '1px solid var(--border-light)',
              }}
            >
              Credit Limit: <strong>₹{selectedCustomer.credit_limit}</strong>
            </div>
          )}

          {/* Wholesale Pricing Toggle */}
          <label style={{ display: 'flex', alignItems: 'center', gap: '0.35rem', fontSize: '0.8rem', cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={useWholesalePrices}
              onChange={(e) => setUseWholesalePrices(e.target.checked)}
            />
            <span>Wholesale Price</span>
          </label>

          {/* Suspended Carts (Hold F8) */}
          <div style={{ display: 'flex', gap: '0.35rem', marginLeft: 'auto' }}>
            {heldCarts.length > 0 && (
              <div style={{ display: 'flex', gap: '0.25rem' }}>
                {heldCarts.map((h) => (
                  <button
                    key={h.id}
                    type="button"
                    className="btn btn-outline btn-sm"
                    style={{ backgroundColor: '#fffbeb', color: '#b45309' }}
                    onClick={() => resumeHeldCart(h.id)}
                    title={`Resume held cart: ${h.name}`}
                  >
                    Resume {h.name}
                  </button>
                ))}
              </div>
            )}

            <button
              type="button"
              className="btn btn-outline btn-sm"
              onClick={holdCurrentCart}
              disabled={cart.length === 0}
              title="Hold active cart to serve next customer (F8)"
            >
              Hold (F8)
            </button>

            <button
              type="button"
              className="btn btn-outline btn-sm"
              onClick={clearActiveCart}
              disabled={cart.length === 0}
            >
              Clear
            </button>
          </div>
        </div>

        {/* Main Work Area: Split into Bill Table (Left) and Payment Panel (Right) */}
        <div style={{ display: 'flex', flex: 1, minHeight: 0, overflow: 'hidden' }}>
          {/* Bill Items Table (70% width) */}
          <div style={{ flex: 1, overflowY: 'auto', padding: '1rem' }}>
            <div className="erp-table-container">
              <table className="erp-table">
                <thead>
                  <tr>
                    <th style={{ width: 40 }}>#</th>
                    <th>Product & Brand</th>
                    <th>Unit / Pack</th>
                    <th className="text-right" style={{ width: 110 }}>Rate (₹)</th>
                    <th className="text-center" style={{ width: 130 }}>Quantity</th>
                    <th className="text-right" style={{ width: 100 }}>Disc (₹)</th>
                    <th className="text-center" style={{ width: 70 }}>GST</th>
                    <th className="text-right" style={{ width: 110 }}>Total (₹)</th>
                    <th className="text-center" style={{ width: 50 }}></th>
                  </tr>
                </thead>
                <tbody>
                  {cart.length === 0 ? (
                    <tr>
                      <td colSpan={9} style={{ textAlign: 'center', padding: '4rem 1rem', color: 'var(--text-muted)' }}>
                        <div style={{ fontSize: '1.1rem', fontWeight: 600, marginBottom: '0.35rem' }}>
                          Cart is empty
                        </div>
                        <p style={{ margin: 0, fontSize: '0.85rem' }}>
                          Scan a barcode or use the search bar above (Press <kbd>F2</kbd>) to add items to the bill.
                        </p>
                      </td>
                    </tr>
                  ) : (
                    cart.map((item, idx) => {
                      const lineTotal = item.rate * item.quantity - item.discount;
                      return (
                        <tr key={lineKey(item.product.id, item.unitId)}>
                          <td className="text-mono" style={{ color: 'var(--text-muted)' }}>{idx + 1}</td>
                          <td>
                            <div style={{ fontWeight: 600 }}>{item.product.name}</div>
                            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
                              Brand: <strong>{item.product.brand || 'Unbranded'}</strong> &middot; {item.product.barcode}
                            </div>
                          </td>
                          <td>
                            <span className="badge badge-neutral" style={{ fontSize: '0.7rem' }}>
                              {item.unitLabel || 'Piece'}
                            </span>
                          </td>
                          <td className="text-right text-mono">
                            ₹{item.rate.toFixed(2)}
                          </td>
                          <td className="text-center">
                            <div style={{ display: 'inline-flex', alignItems: 'center', gap: '0.25rem' }}>
                              <button
                                type="button"
                                className="btn btn-outline btn-sm"
                                style={{ padding: '0.15rem 0.4rem', fontSize: '0.75rem' }}
                                onClick={() => updateQuantity(lineKey(item.product.id, item.unitId), item.quantity - 1)}
                              >
                                -
                              </button>
                              <input
                                type="number"
                                className="form-input text-mono"
                                style={{ width: 60, padding: '0.2rem', textAlign: 'center' }}
                                value={item.quantity}
                                onChange={(e) => updateQuantity(lineKey(item.product.id, item.unitId), parseFloat(e.target.value) || 0)}
                              />
                              <button
                                type="button"
                                className="btn btn-outline btn-sm"
                                style={{ padding: '0.15rem 0.4rem', fontSize: '0.75rem' }}
                                onClick={() => updateQuantity(lineKey(item.product.id, item.unitId), item.quantity + 1)}
                              >
                                +
                              </button>
                            </div>
                          </td>
                          <td className="text-right">
                            <input
                              type="number"
                              className="form-input text-mono"
                              style={{ width: 75, padding: '0.2rem', textAlign: 'right' }}
                              value={item.discount || ''}
                              placeholder="0"
                              onChange={(e) => updateDiscount(lineKey(item.product.id, item.unitId), parseFloat(e.target.value) || 0)}
                            />
                          </td>
                          <td className="text-center text-mono" style={{ fontSize: '0.8rem' }}>
                            {item.product.gst_rate}%
                          </td>
                          <td className="text-right text-mono font-bold">
                            ₹{lineTotal.toFixed(2)}
                          </td>
                          <td className="text-center">
                            <button
                              type="button"
                              onClick={() => removeFromCart(lineKey(item.product.id, item.unitId))}
                              style={{ background: 'none', border: 'none', color: '#dc2626', cursor: 'pointer' }}
                              title="Remove item"
                            >
                              <IconTrash2 size={16} />
                            </button>
                          </td>
                        </tr>
                      );
                    })
                  )}
                </tbody>
              </table>
            </div>
          </div>

          {/* Checkout & Tender Summary Panel (30% width, min 360px) */}
          <div
            style={{
              width: 380,
              backgroundColor: '#ffffff',
              borderLeft: '1px solid var(--border-light)',
              display: 'flex',
              flexDirection: 'column',
              justifyContent: 'space-between',
              padding: '1.25rem',
            }}
          >
            {/* Bill Calculations Breakdown */}
            <div>
              <h3 style={{ margin: '0 0 1rem', fontSize: '1.1rem', fontWeight: 700, borderBottom: '1px solid var(--border-light)', paddingBottom: '0.5rem' }}>
                Bill Summary
              </h3>

              <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '0.4rem', fontSize: '0.875rem' }}>
                <span style={{ color: 'var(--text-secondary)' }}>Items Subtotal:</span>
                <span className="text-mono">₹{calculations.subtotal.toFixed(2)}</span>
              </div>

              {calculations.cgst > 0 && (
                <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '0.4rem', fontSize: '0.85rem' }}>
                  <span style={{ color: 'var(--text-secondary)' }}>CGST (Intra-state):</span>
                  <span className="text-mono">₹{calculations.cgst.toFixed(2)}</span>
                </div>
              )}

              {calculations.sgst > 0 && (
                <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '0.4rem', fontSize: '0.85rem' }}>
                  <span style={{ color: 'var(--text-secondary)' }}>SGST (Intra-state):</span>
                  <span className="text-mono">₹{calculations.sgst.toFixed(2)}</span>
                </div>
              )}

              {calculations.igst > 0 && (
                <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '0.4rem', fontSize: '0.85rem' }}>
                  <span style={{ color: 'var(--text-secondary)' }}>IGST (Inter-state):</span>
                  <span className="text-mono">₹{calculations.igst.toFixed(2)}</span>
                </div>
              )}

              {calculations.roundOff !== 0 && (
                <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '0.4rem', fontSize: '0.85rem' }}>
                  <span style={{ color: 'var(--text-secondary)' }}>Round Off:</span>
                  <span className="text-mono">₹{calculations.roundOff.toFixed(2)}</span>
                </div>
              )}

              {/* Grand Total */}
              <div
                style={{
                  display: 'flex',
                  justifyContent: 'space-between',
                  alignItems: 'baseline',
                  margin: '1rem 0',
                  padding: '0.75rem',
                  backgroundColor: '#f8fafc',
                  borderRadius: 'var(--radius-md)',
                  border: '1px solid var(--border-light)',
                }}
              >
                <span style={{ fontWeight: 700, fontSize: '1rem' }}>Total Payable:</span>
                <span className="text-mono" style={{ fontSize: '1.75rem', fontWeight: 800, color: 'var(--primary-600)' }}>
                  ₹{calculations.grandTotal.toLocaleString('en-IN', { minimumFractionDigits: 2 })}
                </span>
              </div>

              {/* Payment Mode Selection */}
              <div style={{ marginBottom: '1rem' }}>
                <label className="form-label">Payment Method</label>
                <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: '0.35rem', marginBottom: '0.75rem' }}>
                  {[
                    { mode: 'cash', label: 'Cash' },
                    { mode: 'upi', label: 'UPI / QR' },
                    { mode: 'card', label: 'Card' },
                    { mode: 'bank', label: 'Bank' },
                    { mode: 'credit', label: 'Credit (Khata)' },
                    { mode: 'split', label: 'Split Pay' },
                  ].map((m) => (
                    <button
                      key={m.mode}
                      type="button"
                      onClick={() => setPaymentMode(m.mode as any)}
                      className={paymentMode === m.mode ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
                      style={{ padding: '0.5rem 0.25rem', fontSize: '0.75rem', fontWeight: 600 }}
                    >
                      {m.label}
                    </button>
                  ))}
                </div>

                {/* Split Payment inputs if selected */}
                {paymentMode === 'split' && (
                  <div
                    style={{
                      background: '#f8fafc',
                      padding: '0.65rem',
                      borderRadius: 'var(--radius-md)',
                      border: '1px solid var(--border-light)',
                    }}
                  >
                    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.5rem', marginBottom: '0.4rem' }}>
                      <div>
                        <label className="form-label">Cash (₹)</label>
                        <input
                          type="number"
                          className="form-input text-mono"
                          value={splitCash || ''}
                          placeholder="0"
                          onChange={(e) => setSplitCash(parseFloat(e.target.value) || 0)}
                        />
                      </div>
                      <div>
                        <label className="form-label">UPI (₹)</label>
                        <input
                          type="number"
                          className="form-input text-mono"
                          value={splitUpi || ''}
                          placeholder="0"
                          onChange={(e) => setSplitUpi(parseFloat(e.target.value) || 0)}
                        />
                      </div>
                    </div>
                    <div>
                      <label className="form-label">Customer Credit / Khata (₹)</label>
                      <input
                        type="number"
                        className="form-input text-mono"
                        value={splitCredit || ''}
                        placeholder="0"
                        onChange={(e) => setSplitCredit(parseFloat(e.target.value) || 0)}
                      />
                    </div>
                  </div>
                )}
              </div>
            </div>

            {/* Action Buttons */}
            <div>
              <button
                type="button"
                className="btn btn-primary"
                style={{
                  width: '100%',
                  padding: '0.875rem',
                  fontSize: '1.05rem',
                  fontWeight: 700,
                  justifyContent: 'center',
                }}
                disabled={cart.length === 0 || isProcessing}
                onClick={handleCheckout}
              >
                <IconCheck size={20} />
                <span>{isProcessing ? 'Processing Bill...' : 'Complete Sale & Bill (F9)'}</span>
              </button>

              <button
                type="button"
                className="btn btn-outline btn-sm"
                style={{ width: '100%', marginTop: '0.5rem', justifyContent: 'center' }}
                onClick={handlePrint}
              >
                <IconPrinter size={16} />
                <span>Print Last Receipt</span>
              </button>
            </div>
          </div>
        </div>
      </div>

      {/* Barcode Scanner Modal */}
      <BarcodeScannerModal
        isOpen={scannerOpen}
        onClose={() => setScannerOpen(false)}
        onScan={handleBarcodeScanned}
        title="Scan Barcode to Add Item"
      />
    </AppLayout>
  );
}