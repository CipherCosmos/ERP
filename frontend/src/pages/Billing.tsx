import React, { useState, useEffect, useRef } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { productApi, invoiceApi, paymentApi, customerApi, barcodeApi } from '../services/api';
import {
  enqueuePendingInvoice,
  flushPendingInvoiceQueue,
  getPendingInvoiceCount,
} from '../services/offlineInvoiceQueue';
import { Product, Customer } from '../types';

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

export default function Billing() {
  const navigate = useNavigate();
  const { user, store, cart, addToCart, updateCartQuantity, removeFromCart, clearCart } = useAppStore();
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

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    loadProducts();
  }, [user]);

  useEffect(() => {
    if (!store) return;
    customerApi
      .list(store.id)
      .then((res) => setCustomers((res.data as Customer[]) || []))
      .catch(() => setCustomers([]));
  }, [store?.id]);

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
  }, [store?.id, user?.id]);

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

  const loadProducts = async () => {
    if (!store) return;
    try {
      const { data } = await productApi.list({ store_id: store.id });
      setProducts(data);
    } catch (err) {
      console.error('Failed to load products:', err);
    }
  };

  const handleBarcodeScan = async (barcode: string) => {
    const trimmed = barcode.trim();
    if (!trimmed || !store) return;

    const local = products.find((p) => p.barcode === trimmed);
    if (local) {
      addToCart({ product: local, quantity: 1, discount: 0 });
      setSearch('');
      return;
    }

    try {
      const { data } = await barcodeApi.lookup(trimmed, store.id);
      if (data && typeof data === 'object' && 'id' in data) {
        const d = data as Record<string, unknown>;
        const packingQty = Number(d.packing_qty) || 1;
        addToCart({ product: mapBarcodeLookupToProduct(d), quantity: packingQty, discount: 0 });
        setSearch('');
        return;
      }
    } catch {
      /* fall through */
    }

    try {
      const { data } = await productApi.list({ store_id: store.id, barcode: trimmed });
      if (data.length > 0) {
        addToCart({ product: data[0], quantity: 1, discount: 0 });
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

    cart.forEach((item) => {
      const rate = lineUnitPrice(item.product, useWholesalePrices);
      const itemTotal = rate * item.quantity;
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
    if (cart.length === 0 || !store || !user) return;

    if (!useSplitPayment && paymentMode === 'credit' && !selectedCustomerId) {
      alert('Select a customer for credit sales.');
      return;
    }

    const { subtotal, cgst, sgst, igst, roundOff, total } = calculateTotals();

    if (useSplitPayment) {
      const paidAmount = splitPayments.reduce((sum, p) => sum + (parseFloat(p.amount) || 0), 0);
      if (Math.abs(paidAmount - total) > 0.01) {
        alert(`Split payments (₹${paidAmount.toFixed(2)}) must equal total (₹${total.toFixed(2)})`);
        return;
      }
    }

    const invoiceBody = {
      store_id: store.id,
      customer_id: selectedCustomerId || undefined,
      invoice_date: new Date().toISOString(),
      sub_total: subtotal,
      cgst,
      sgst,
      igst,
      discount: 0,
      round_off: roundOff,
      total,
      status: 'completed',
      payment_mode: useSplitPayment ? 'split' : paymentMode,
      items: cart.map((item) => {
        const rate = lineUnitPrice(item.product, useWholesalePrices);
        return {
          product_id: item.product.id,
          quantity: item.quantity,
          rate,
          discount: item.discount,
          total: rate * item.quantity,
        };
      }),
    };

    const splitLines =
      useSplitPayment && splitPayments.some((p) => parseFloat(p.amount) > 0)
        ? splitPayments
            .filter((p) => parseFloat(p.amount) > 0)
            .map((p) => ({ payment_mode: p.mode, amount: parseFloat(p.amount) }))
        : undefined;

    try {
      const { data: invoice } = await invoiceApi.create(invoiceBody as any);

      if (splitLines && splitLines.length > 0) {
        await paymentApi.addSplitPayments({
          invoice_id: invoice.id,
          payments: splitLines,
        });
      }

      // Print receipt
      printReceipt(invoice, cart);

      clearCart();
      setPaymentMode('cash');
      setUseSplitPayment(false);
      setSplitPayments([{ mode: 'cash', amount: '' }]);
      setSelectedCustomerId('');
      useAppStore.getState().setSyncPending(getPendingInvoiceCount());
      alert('Billing completed!');
    } catch (err: any) {
      console.error('Checkout failed:', err);
      const offline = typeof navigator !== 'undefined' && !navigator.onLine;
      const code = err?.code as string | undefined;
      const networkFail =
        !err?.response && (code === 'ERR_NETWORK' || code === 'ECONNABORTED' || err?.message === 'Network Error');
      if (offline || networkFail) {
        enqueuePendingInvoice({
          invoiceBody: invoiceBody as Record<string, unknown>,
          splitPayments: splitLines,
        });
        useAppStore.getState().setSyncPending(getPendingInvoiceCount());
        clearCart();
        setPaymentMode('cash');
        setUseSplitPayment(false);
        setSplitPayments([{ mode: 'cash', amount: '' }]);
        setSelectedCustomerId('');
        alert('Saved offline — invoice will sync when connection is back.');
        return;
      }
      alert(err?.response?.data?.error || 'Billing failed. Please try again.');
    }
  };

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

  const printReceipt = (invoice: any, items: any[]) => {
    const { cgst, sgst, igst } = calculateTotals();
    const customer = selectedCustomerId ? customers.find(c => c.id === selectedCustomerId) : null;

    const receiptHtml = `
      <!DOCTYPE html>
      <html>
      <head>
        <meta charset="utf-8">
        <title>Receipt ${invoice.invoice_no}</title>
        <style>
          body { font-family: monospace; width: 80mm; margin: 0; padding: 10px; }
          .header { text-align: center; font-weight: bold; margin-bottom: 10px; }
          .line { border-bottom: 1px dashed #000; margin: 5px 0; }
          table { width: 100%; font-size: 12px; }
          th, td { padding: 4px; text-align: left; }
          .text-right { text-align: right; }
          .total { font-weight: bold; }
        </style>
      </head>
      <body>
        <div class="header">RECEIPT</div>
        <div>${store?.name}</div>
        <div class="line"></div>
        <div>Invoice: ${invoice.invoice_no}</div>
        <div>Date: ${new Date(invoice.invoice_date).toLocaleString()}</div>
        ${customer ? `<div>Customer: ${customer.name}</div>` : '<div>Customer: Walk-in</div>'}
        <div class="line"></div>
        <table>
          <thead>
            <tr>
              <th>Item</th>
              <th class="text-right">Qty</th>
              <th class="text-right">Rate</th>
              <th class="text-right">Total</th>
            </tr>
          </thead>
          <tbody>
            ${items.map(item => `
              <tr>
                <td>${item.product.name}</td>
                <td class="text-right">${item.quantity}</td>
                <td class="text-right">₹${lineUnitPrice(item.product, useWholesalePrices).toFixed(2)}</td>
                <td class="text-right">₹${(lineUnitPrice(item.product, useWholesalePrices) * item.quantity).toFixed(2)}</td>
              </tr>
            `).join('')}
          </tbody>
        </table>
        <div class="line"></div>
        <table>
          <tr>
            <td>Subtotal:</td>
            <td class="text-right">₹${calculateTotals().subtotal.toFixed(2)}</td>
          </tr>
          ${cgst > 0 ? `<tr><td>CGST:</td><td class="text-right">₹${cgst.toFixed(2)}</td></tr>` : ''}
          ${sgst > 0 ? `<tr><td>SGST:</td><td class="text-right">₹${sgst.toFixed(2)}</td></tr>` : ''}
          ${igst > 0 ? `<tr><td>IGST:</td><td class="text-right">₹${igst.toFixed(2)}</td></tr>` : ''}
          <tr class="total">
            <td>Total:</td>
            <td class="text-right">₹${calculateTotals().total.toFixed(2)}</td>
          </tr>
        </table>
        <div class="line"></div>
        <div style="text-align: center; font-size: 11px; margin-top: 10px;">
          Thank you!
        </div>
      </body>
      </html>
    `;

    const printWindow = window.open('', '_blank', 'height=400,width=300');
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
                  addToCart({ product: p, quantity: 1, discount: 0 });
                  setSearch('');
                }}
              >
                <span>{p.name}</span>
                <span>₹{lineUnitPrice(p, useWholesalePrices).toFixed(2)}</span>
              </div>
            ))}
          </div>
        )}

        <div style={styles.productGrid}>
          {products.slice(0, 50).map((p) => (
            <div
              key={p.id}
              style={styles.productCard}
              onClick={() => addToCart({ product: p, quantity: 1, discount: 0 })}
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
              <div style={styles.productPrice}>₹{lineUnitPrice(p, useWholesalePrices).toFixed(2)}</div>
            </div>
          ))}
        </div>
      </div>

      <div style={styles.rightPanel}>
        <h2 style={styles.cartTitle}>Cart ({cart.length})</h2>

        <div style={styles.cartItems}>
          {cart.map((item) => (
            <div key={item.product.id} style={styles.cartItem}>
              <div style={styles.cartItemInfo}>
                <div style={styles.cartItemName}>{item.product.name}</div>
                <div style={styles.cartItemPrice}>
                  ₹{lineUnitPrice(item.product, useWholesalePrices).toFixed(2)} × {item.quantity}
                </div>
              </div>
              <div style={styles.cartItemActions}>
                <button
                  onClick={() => {
                    const newQty = item.product.allow_decimal
                      ? item.quantity - 0.5
                      : item.quantity - 1;
                    updateCartQuantity(item.product.id, Math.max(0.5, newQty));
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
                    if (val > 0) updateCartQuantity(item.product.id, val);
                  }}
                  step={item.product.allow_decimal ? '0.5' : '1'}
                  style={{...styles.qtyInput, width: '60px'}}
                />
                <button
                  onClick={() => {
                    const newQty = item.product.allow_decimal
                      ? item.quantity + 0.5
                      : item.quantity + 1;
                    updateCartQuantity(item.product.id, newQty);
                  }}
                  style={styles.qtyBtn}
                >
                  +
                </button>
                <button
                  onClick={() => removeFromCart(item.product.id)}
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
            <span>₹{subtotal.toFixed(2)}</span>
          </div>
          {cgst > 0 && <div style={styles.totalRow}>
            <span>CGST</span>
            <span>₹{cgst.toFixed(2)}</span>
          </div>}
          {sgst > 0 && <div style={styles.totalRow}>
            <span>SGST</span>
            <span>₹{sgst.toFixed(2)}</span>
          </div>}
          {igst > 0 && <div style={styles.totalRow}>
            <span>IGST</span>
            <span>₹{igst.toFixed(2)}</span>
          </div>}
          <div style={styles.totalRow}>
            <span>Round Off</span>
            <span>₹{roundOff.toFixed(2)}</span>
          </div>
          <div style={styles.grandTotal}>
            <span>Total</span>
            <span>₹{total.toFixed(2)}</span>
          </div>
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
                <span>₹{getPaymentAmount().toFixed(2)}</span>
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

          <button onClick={handleCheckout} style={styles.checkoutBtn}>
            Checkout
          </button>
        </div>

        <button
          onClick={() => navigate('/')}
          style={styles.backBtn}
        >
          ← Back to Menu
        </button>
      </div>
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
    display: 'flex',
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