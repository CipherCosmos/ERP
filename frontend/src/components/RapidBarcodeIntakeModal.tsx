import React, { useState, useEffect, useRef, useCallback } from 'react';
import { productApi, stockApi, barcodeApi } from '../services/api';
import { Store } from '../types';
import {
  IconBarcode,
  IconCheck,
  IconClose,
  IconCamera,
  IconRefresh,
  IconAlert,
} from './Icons';
import { BarcodeScannerModal } from './BarcodeScannerModal';

interface RapidBarcodeIntakeModalProps {
  isOpen: boolean;
  onClose: () => void;
  store: Store | null;
  categories: Array<{ id: string; name: string }>;
  uoms: Array<{ id: string; name: string; code?: string; short_name?: string }>;
  stores: Store[];
  onProductSaved?: () => void;
}

interface IntakeSessionItem {
  id: string;
  name: string;
  barcode: string;
  brand?: string;
  mrp: number;
  sell_price: number;
  qty_added: number;
  warehouse_name: string;
  is_new: boolean;
  timestamp: string;
}

function playScanBeep() {
  try {
    const AudioCtx = window.AudioContext || (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext;
    if (!AudioCtx) return;
    const ctx = new AudioCtx();
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.type = 'sine';
    osc.frequency.setValueAtTime(880, ctx.currentTime);
    gain.gain.setValueAtTime(0.12, ctx.currentTime);
    gain.gain.exponentialRampToValueAtTime(0.0001, ctx.currentTime + 0.12);
    osc.connect(gain);
    gain.connect(ctx.destination);
    osc.start();
    osc.stop(ctx.currentTime + 0.12);
  } catch {
    // Ignore audio restrictions
  }
}

function playApproveBeep() {
  try {
    const AudioCtx = window.AudioContext || (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext;
    if (!AudioCtx) return;
    const ctx = new AudioCtx();
    const osc1 = ctx.createOscillator();
    const gain1 = ctx.createGain();
    osc1.type = 'triangle';
    osc1.frequency.setValueAtTime(523.25, ctx.currentTime); // C5
    gain1.gain.setValueAtTime(0.12, ctx.currentTime);
    gain1.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + 0.08);
    osc1.connect(gain1);
    gain1.connect(ctx.destination);
    osc1.start();
    osc1.stop(ctx.currentTime + 0.08);

    const osc2 = ctx.createOscillator();
    const gain2 = ctx.createGain();
    osc2.type = 'triangle';
    osc2.frequency.setValueAtTime(659.25, ctx.currentTime + 0.08); // E5
    gain2.gain.setValueAtTime(0.12, ctx.currentTime + 0.08);
    gain2.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + 0.2);
    osc2.connect(gain2);
    gain2.connect(ctx.destination);
    osc2.start(ctx.currentTime + 0.08);
    osc2.stop(ctx.currentTime + 0.2);
  } catch {
    // Ignore audio restrictions
  }
}

export function RapidBarcodeIntakeModal({
  isOpen,
  onClose,
  store,
  categories,
  uoms,
  stores,
  onProductSaved,
}: RapidBarcodeIntakeModalProps) {
  const barcodeInputRef = useRef<HTMLInputElement>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);

  const [barcodeInput, setBarcodeInput] = useState('');
  const [isLookingUp, setIsLookingUp] = useState(false);
  const [showCameraScanner, setShowCameraScanner] = useState(false);
  const [successToast, setSuccessToast] = useState<string | null>(null);
  const [errorToast, setErrorToast] = useState<string | null>(null);

  // Intake State
  const [intakeMode, setIntakeMode] = useState<'AWAITING_SCAN' | 'REVIEWING'>('AWAITING_SCAN');
  const [isExistingProduct, setIsExistingProduct] = useState(false);
  const [existingProductId, setExistingProductId] = useState<string | null>(null);

  // Review Form Data
  const [formData, setFormData] = useState({
    barcode: '',
    name: '',
    brand: '',
    category_id: '',
    uom_id: '',
    hsn_code: '',
    gst_rate: 0,
    mrp: 0,
    cost_price: 0,
    sell_price: 0,
    wholesale_sell_price: 0,
    intake_qty: 1,
    batch_no: '',
    expiry_date: '',
    warehouse_id: '',
  });

  // Remember last category & UOM for continuous speed
  const [lastCategoryId, setLastCategoryId] = useState('');
  const [lastUomId, setLastUomId] = useState('');
  const [lastGstRate, setLastGstRate] = useState(0);

  // Session Activity Log
  const [sessionItems, setSessionItems] = useState<IntakeSessionItem[]>([]);
  const [isSubmitting, setIsSubmitting] = useState(false);

  // Focus barcode input on modal open or returning to scan
  useEffect(() => {
    if (isOpen && intakeMode === 'AWAITING_SCAN') {
      setTimeout(() => {
        barcodeInputRef.current?.focus();
      }, 80);
    }
  }, [isOpen, intakeMode]);

  // Handle Barcode Look up
  const processScannedBarcode = useCallback(
    async (rawCode: string) => {
      const code = rawCode.trim();
      if (!code || !store) return;

      setIsLookingUp(true);
      setErrorToast(null);
      playScanBeep();

      try {
        const { data } = await barcodeApi.lookup(code, store.id);
        // Product Found!
        setIsExistingProduct(true);
        setExistingProductId(data.id);
        setFormData({
          barcode: data.barcode || code,
          name: data.name || '',
          brand: data.brand || '',
          category_id: data.category_id || lastCategoryId || (categories[0]?.id || ''),
          uom_id: data.uom_id || lastUomId || (uoms[0]?.id || ''),
          hsn_code: data.hsn_code || '',
          gst_rate: data.gst_rate ?? lastGstRate,
          mrp: data.mrp || 0,
          cost_price: data.cost_price || 0,
          sell_price: data.sell_price || 0,
          wholesale_sell_price: data.wholesale_sell_price || 0,
          intake_qty: 1,
          batch_no: `BAT-${new Date().toISOString().slice(0, 10).replace(/-/g, '')}`,
          expiry_date: '',
          warehouse_id: store.id,
        });
        setIntakeMode('REVIEWING');
      } catch (err: unknown) {
        // Not Found => New Product
        setIsExistingProduct(false);
        setExistingProductId(null);
        setFormData({
          barcode: code,
          name: '',
          brand: '',
          category_id: lastCategoryId || (categories[0]?.id || ''),
          uom_id: lastUomId || (uoms[0]?.id || ''),
          hsn_code: '',
          gst_rate: lastGstRate,
          mrp: 0,
          cost_price: 0,
          sell_price: 0,
          wholesale_sell_price: 0,
          intake_qty: 1,
          batch_no: `BAT-${new Date().toISOString().slice(0, 10).replace(/-/g, '')}`,
          expiry_date: '',
          warehouse_id: store.id,
        });
        setIntakeMode('REVIEWING');
        setTimeout(() => {
          nameInputRef.current?.focus();
        }, 100);
      } finally {
        setIsLookingUp(false);
        setBarcodeInput('');
      }
    },
    [store, categories, uoms, lastCategoryId, lastUomId, lastGstRate]
  );

  // Global Hardware Scanner Listener in modal
  useEffect(() => {
    if (!isOpen) return;

    let buffer = '';
    let lastTime = 0;

    const onKeyDown = (e: KeyboardEvent) => {
      // If modal is reviewing and user presses F9 or Enter in non-textarea
      if (intakeMode === 'REVIEWING' && e.key === 'F9') {
        e.preventDefault();
        handleApprove();
        return;
      }

      if (e.key === 'Escape') {
        if (intakeMode === 'REVIEWING') {
          handleSkipAndResume();
        } else {
          onClose();
        }
        return;
      }

      const now = Date.now();
      const diff = now - lastTime;
      lastTime = now;

      // Handle barcode scanner stream
      if (e.key === 'Enter') {
        if (buffer.length >= 3) {
          e.preventDefault();
          const scanned = buffer;
          buffer = '';
          processScannedBarcode(scanned);
        }
        buffer = '';
        return;
      }

      if (e.key.length === 1) {
        if (diff > 80 && buffer.length > 0) {
          buffer = '';
        }
        buffer += e.key;
      }
    };

    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [isOpen, intakeMode, processScannedBarcode]);

  // Handle Approve & Save
  const handleApprove = async () => {
    if (!store) return;
    if (!formData.name.trim()) {
      setErrorToast('Product name cannot be empty.');
      nameInputRef.current?.focus();
      return;
    }
    if (!formData.category_id || !formData.uom_id) {
      setErrorToast('Please select category and base UOM.');
      return;
    }

    setIsSubmitting(true);
    setErrorToast(null);

    try {
      let productId = existingProductId;
      let created: any = null;

      if (isExistingProduct && productId) {
        // 1. Update existing product master details
        await productApi.update(productId, {
          name: formData.name.trim(),
          brand: formData.brand.trim() || undefined,
          category_id: formData.category_id,
          uom_id: formData.uom_id,
          hsn_code: formData.hsn_code.trim() || undefined,
          gst_rate: Number(formData.gst_rate) || 0,
        });
      } else {
        // 2. Create new product master
        const res = await productApi.create({
          name: formData.name.trim(),
          brand: formData.brand.trim() || undefined,
          barcode: formData.barcode.trim(),
          category_id: formData.category_id,
          uom_id: formData.uom_id,
          hsn_code: formData.hsn_code.trim() || undefined,
          gst_rate: Number(formData.gst_rate) || 0,
          base_unit_qty: 1,
          allow_decimal: false,
          min_stock: 5,
        });
        created = res.data;
        productId = created.id;
      }

      // 3. Set Price
      let unitId = (created as any)?.default_product_unit_id;
      if (!unitId) {
        try {
          const res = await productApi.listUnits(productId!);
          const units = (res.data as any)?.units || res.data || [];
          unitId = units.find((u: any) => u.is_default)?.id || units[0]?.id;
        } catch {
          /* ignore */
        }
      }

      if (unitId) {
        await productApi.setPrice({
          product_id: productId!,
          product_unit_id: unitId,
          store_id: store.id,
          mrp: Number(formData.mrp) || 0,
          cost_price: Number(formData.cost_price) || 0,
          sell_price: Number(formData.sell_price) || Number(formData.mrp) || 0,
          wholesale_sell_price: Number(formData.wholesale_sell_price) || 0,
        });
      }

      // 4. Record stock intake if quantity > 0
      const intakeQty = Number(formData.intake_qty) || 0;
      const targetStoreId = formData.warehouse_id || store.id;

      if (intakeQty > 0) {
        await stockApi.adjust({
          product_id: productId!,
          store_id: targetStoreId,
          quantity: intakeQty,
          type: 'add',
          reason: isExistingProduct ? 'restock' : 'opening_stock',
          batch_no: formData.batch_no || `BAT-${Date.now().toString().slice(-6)}`,
          cost_price: Number(formData.cost_price) || 0,
          expiry_date: formData.expiry_date || undefined,
        });
      }

      // Audio feedback
      playApproveBeep();

      // Remember last category / UOM / GST rate for fast repeat intake
      setLastCategoryId(formData.category_id);
      setLastUomId(formData.uom_id);
      setLastGstRate(Number(formData.gst_rate) || 0);

      // Add to session summary table
      const targetWhName =
        stores.find((s) => s.id === targetStoreId)?.name || store.name;
      setSessionItems((prev) => [
        {
          id: productId!,
          name: formData.name.trim(),
          barcode: formData.barcode.trim(),
          brand: formData.brand.trim(),
          mrp: Number(formData.mrp) || 0,
          sell_price: Number(formData.sell_price) || 0,
          qty_added: intakeQty,
          warehouse_name: targetWhName,
          is_new: !isExistingProduct,
          timestamp: new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }),
        },
        ...prev,
      ]);

      // Flash Success
      setSuccessToast(
        `✓ ${isExistingProduct ? 'Updated & Restocked' : 'Created'}: ${formData.name.trim()} (${intakeQty} pcs)`
      );
      setTimeout(() => setSuccessToast(null), 3000);

      // Trigger catalog refresh
      if (onProductSaved) {
        onProductSaved();
      }

      // Resume Scanning Mode!
      handleSkipAndResume();
    } catch (err: unknown) {
      console.error('Failed to approve product intake:', err);
      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error || 'Failed to save product.';
      setErrorToast(msg);
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleSkipAndResume = () => {
    setIntakeMode('AWAITING_SCAN');
    setIsExistingProduct(false);
    setExistingProductId(null);
    setFormData({
      barcode: '',
      name: '',
      brand: '',
      category_id: lastCategoryId || '',
      uom_id: lastUomId || '',
      hsn_code: '',
      gst_rate: lastGstRate,
      mrp: 0,
      cost_price: 0,
      sell_price: 0,
      wholesale_sell_price: 0,
      intake_qty: 1,
      batch_no: '',
      expiry_date: '',
      warehouse_id: store?.id || '',
    });
    setBarcodeInput('');
    setTimeout(() => {
      barcodeInputRef.current?.focus();
    }, 60);
  };

  if (!isOpen) return null;

  return (
    <div
      style={{
        position: 'fixed',
        inset: 0,
        backgroundColor: 'rgba(15, 23, 42, 0.65)',
        backdropFilter: 'blur(3px)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        zIndex: 1000,
        padding: '1rem',
      }}
    >
      <div
        className="card"
        style={{
          width: '100%',
          maxWidth: '900px',
          maxHeight: '92vh',
          display: 'flex',
          flexDirection: 'column',
          backgroundColor: '#ffffff',
          borderRadius: '12px',
          boxShadow: '0 25px 50px -12px rgba(0, 0, 0, 0.25)',
          overflow: 'hidden',
          animation: 'fadeIn 0.15s ease-out',
        }}
      >
        {/* Header */}
        <div
          style={{
            padding: '1.25rem 1.5rem',
            borderBottom: '1px solid #e2e8f0',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            backgroundColor: '#f8fafc',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
            <div
              style={{
                width: '38px',
                height: '38px',
                borderRadius: '8px',
                backgroundColor: '#eff6ff',
                color: '#2563eb',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
              }}
            >
              <IconBarcode size={22} />
            </div>
            <div>
              <h2 style={{ fontSize: '1.15rem', fontWeight: 600, color: '#0f172a', margin: 0 }}>
                Rapid Product Intake (Continuous Scan Mode)
              </h2>
              <div style={{ fontSize: '0.8rem', color: '#64748b', marginTop: '2px' }}>
                Scan barcode &rarr; Verify / Edit &rarr; Approve (Enter) &rarr; Next barcode scans automatically
              </div>
            </div>
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
            <span
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: '0.35rem',
                padding: '0.25rem 0.6rem',
                borderRadius: '9999px',
                fontSize: '0.75rem',
                fontWeight: 600,
                backgroundColor: '#dcfce7',
                color: '#15803d',
              }}
            >
              Session: {sessionItems.length} Added
            </span>
            <button
              onClick={onClose}
              className="btn btn-outline"
              style={{ padding: '0.35rem', borderRadius: '6px' }}
              title="Close Intake Mode (Esc)"
            >
              <IconClose size={18} />
            </button>
          </div>
        </div>

        {/* Alerts & Toasts */}
        {successToast && (
          <div
            style={{
              padding: '0.65rem 1.25rem',
              backgroundColor: '#ecfdf5',
              borderBottom: '1px solid #a7f3d0',
              color: '#065f46',
              fontSize: '0.85rem',
              fontWeight: 600,
              display: 'flex',
              alignItems: 'center',
              gap: '0.5rem',
            }}
          >
            <IconCheck size={18} color="#059669" />
            <span>{successToast}</span>
          </div>
        )}

        {errorToast && (
          <div
            style={{
              padding: '0.65rem 1.25rem',
              backgroundColor: '#fef2f2',
              borderBottom: '1px solid #fecaca',
              color: '#991b1b',
              fontSize: '0.85rem',
              display: 'flex',
              alignItems: 'center',
              gap: '0.5rem',
            }}
          >
            <IconAlert size={18} color="#dc2626" />
            <span>{errorToast}</span>
          </div>
        )}

        {/* Body Container */}
        <div style={{ padding: '1.25rem 1.5rem', overflowY: 'auto', flex: 1 }}>
          {/* BARCODE SCANNER BAR (Persistent at Top) */}
          <div
            style={{
              padding: '1rem',
              backgroundColor: intakeMode === 'AWAITING_SCAN' ? '#eff6ff' : '#f8fafc',
              border: `2px solid ${intakeMode === 'AWAITING_SCAN' ? '#3b82f6' : '#e2e8f0'}`,
              borderRadius: '8px',
              marginBottom: '1.25rem',
              transition: 'all 0.2s',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '0.5rem' }}>
              <label
                style={{
                  fontSize: '0.85rem',
                  fontWeight: 600,
                  color: intakeMode === 'AWAITING_SCAN' ? '#1d4ed8' : '#475569',
                  display: 'flex',
                  alignItems: 'center',
                  gap: '0.5rem',
                }}
              >
                <IconBarcode size={18} />
                {intakeMode === 'AWAITING_SCAN' ? 'Ready to Scan Next Barcode' : 'Scanned Barcode'}
              </label>

              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                <span
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: '0.35rem',
                    fontSize: '0.75rem',
                    color: intakeMode === 'AWAITING_SCAN' ? '#2563eb' : '#64748b',
                  }}
                >
                  <span
                    style={{
                      width: '8px',
                      height: '8px',
                      borderRadius: '50%',
                      backgroundColor: intakeMode === 'AWAITING_SCAN' ? '#22c55e' : '#94a3b8',
                      display: 'inline-block',
                      animation: intakeMode === 'AWAITING_SCAN' ? 'pulse 1.5s infinite' : 'none',
                    }}
                  />
                  {intakeMode === 'AWAITING_SCAN' ? 'Scanner Listening' : 'Editing details'}
                </span>

                <button
                  type="button"
                  onClick={() => setShowCameraScanner(true)}
                  className="btn btn-outline"
                  style={{ padding: '0.25rem 0.6rem', fontSize: '0.75rem', display: 'flex', alignItems: 'center', gap: '0.3rem' }}
                >
                  <IconCamera size={14} />
                  Camera
                </button>
              </div>
            </div>

            <div style={{ display: 'flex', gap: '0.5rem' }}>
              <input
                ref={barcodeInputRef}
                type="text"
                className="form-control"
                placeholder="Scan barcode with handheld scanner or type barcode..."
                value={intakeMode === 'AWAITING_SCAN' ? barcodeInput : formData.barcode}
                onChange={(e) => {
                  if (intakeMode === 'AWAITING_SCAN') {
                    setBarcodeInput(e.target.value);
                  } else {
                    setFormData({ ...formData, barcode: e.target.value });
                  }
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && intakeMode === 'AWAITING_SCAN' && barcodeInput.trim()) {
                    e.preventDefault();
                    processScannedBarcode(barcodeInput);
                  }
                }}
                style={{
                  fontSize: '1rem',
                  fontWeight: 600,
                  letterSpacing: '0.05em',
                  fontFamily: 'monospace',
                  backgroundColor: intakeMode === 'AWAITING_SCAN' ? '#ffffff' : '#f1f5f9',
                }}
              />

              {intakeMode === 'AWAITING_SCAN' ? (
                <button
                  type="button"
                  className="btn btn-primary"
                  disabled={!barcodeInput.trim() || isLookingUp}
                  onClick={() => processScannedBarcode(barcodeInput)}
                  style={{ minWidth: '110px', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.4rem' }}
                >
                  {isLookingUp ? <IconRefresh size={16} /> : <IconCheck size={16} />}
                  Lookup
                </button>
              ) : (
                <button
                  type="button"
                  className="btn btn-outline"
                  onClick={handleSkipAndResume}
                  title="Discard and scan another barcode (Esc)"
                  style={{ minWidth: '100px' }}
                >
                  Skip & Next
                </button>
              )}
            </div>
          </div>

          {/* REVIEW & APPROVAL FORM */}
          {intakeMode === 'REVIEWING' && (
            <div
              style={{
                backgroundColor: '#ffffff',
                border: '1px solid #cbd5e1',
                borderRadius: '8px',
                padding: '1.25rem',
                marginBottom: '1.5rem',
              }}
            >
              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  marginBottom: '1rem',
                  paddingBottom: '0.75rem',
                  borderBottom: '1px solid #f1f5f9',
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                  <span
                    style={{
                      padding: '0.2rem 0.6rem',
                      borderRadius: '4px',
                      fontSize: '0.75rem',
                      fontWeight: 600,
                      backgroundColor: isExistingProduct ? '#eff6ff' : '#ecfdf5',
                      color: isExistingProduct ? '#1d4ed8' : '#047857',
                    }}
                  >
                    {isExistingProduct ? 'Existing Product Found' : 'New Product Barcode'}
                  </span>
                  <span style={{ fontSize: '0.85rem', color: '#64748b' }}>
                    {isExistingProduct
                      ? 'Review details or restock quantity, then approve.'
                      : 'Fill mandatory name and prices, then approve to save & continue scanning.'}
                  </span>
                </div>

                <div style={{ fontSize: '0.75rem', color: '#94a3b8', fontStyle: 'italic' }}>
                  Shortcut: Press <kbd style={{ padding: '0.1rem 0.35rem', background: '#f1f5f9', borderRadius: '3px' }}>Enter</kbd> or <kbd style={{ padding: '0.1rem 0.35rem', background: '#f1f5f9', borderRadius: '3px' }}>F9</kbd> to Approve
                </div>
              </div>

              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(260px, 1fr))', gap: '1rem' }}>
                {/* Product Name */}
                <div style={{ gridColumn: 'span 2' }}>
                  <label className="form-label" style={{ fontWeight: 600 }}>
                    Product Name <span style={{ color: '#dc2626' }}>*</span>
                  </label>
                  <input
                    ref={nameInputRef}
                    type="text"
                    className="form-control"
                    placeholder="e.g. Parle-G Glucose Biscuits 250g"
                    value={formData.name}
                    onChange={(e) => setFormData({ ...formData, name: e.target.value })}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault();
                        handleApprove();
                      }
                    }}
                    required
                  />
                </div>

                {/* Brand */}
                <div>
                  <label className="form-label">Brand</label>
                  <input
                    type="text"
                    className="form-control"
                    placeholder="e.g. Parle, Nestle, Britannia"
                    value={formData.brand}
                    onChange={(e) => setFormData({ ...formData, brand: e.target.value })}
                  />
                </div>

                {/* Category */}
                <div>
                  <label className="form-label">Category</label>
                  <select
                    className="form-control"
                    value={formData.category_id}
                    onChange={(e) => setFormData({ ...formData, category_id: e.target.value })}
                  >
                    {categories.map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name}
                      </option>
                    ))}
                  </select>
                </div>

                {/* Base Unit */}
                <div>
                  <label className="form-label">Base Unit (UOM)</label>
                  <select
                    className="form-control"
                    value={formData.uom_id}
                    onChange={(e) => setFormData({ ...formData, uom_id: e.target.value })}
                  >
                    {uoms.map((u) => (
                      <option key={u.id} value={u.id}>
                        {u.name} ({u.short_name || u.code || 'pcs'})
                      </option>
                    ))}
                  </select>
                </div>

                {/* MRP */}
                <div>
                  <label className="form-label" style={{ fontWeight: 600 }}>
                    MRP (₹) <span style={{ color: '#dc2626' }}>*</span>
                  </label>
                  <input
                    type="number"
                    step="0.01"
                    min="0"
                    className="form-control"
                    value={formData.mrp || ''}
                    placeholder="0.00"
                    onChange={(e) => {
                      const mrpVal = Number(e.target.value);
                      setFormData({
                        ...formData,
                        mrp: mrpVal,
                        sell_price: formData.sell_price === 0 || formData.sell_price === formData.mrp ? mrpVal : formData.sell_price,
                      });
                    }}
                  />
                </div>

                {/* Cost Price */}
                <div>
                  <label className="form-label">Cost Price (₹)</label>
                  <input
                    type="number"
                    step="0.01"
                    min="0"
                    className="form-control"
                    value={formData.cost_price || ''}
                    placeholder="0.00"
                    onChange={(e) => setFormData({ ...formData, cost_price: Number(e.target.value) })}
                  />
                </div>

                {/* Selling Price */}
                <div>
                  <label className="form-label" style={{ fontWeight: 600 }}>
                    Selling Price (₹) <span style={{ color: '#dc2626' }}>*</span>
                  </label>
                  <input
                    type="number"
                    step="0.01"
                    min="0"
                    className="form-control"
                    value={formData.sell_price || ''}
                    placeholder="0.00"
                    onChange={(e) => setFormData({ ...formData, sell_price: Number(e.target.value) })}
                  />
                </div>

                {/* Wholesale Price */}
                <div>
                  <label className="form-label">Wholesale Price (₹)</label>
                  <input
                    type="number"
                    step="0.01"
                    min="0"
                    className="form-control"
                    value={formData.wholesale_sell_price || ''}
                    placeholder="0.00"
                    onChange={(e) => setFormData({ ...formData, wholesale_sell_price: Number(e.target.value) })}
                  />
                </div>

                {/* GST Rate */}
                <div>
                  <label className="form-label">GST Rate (%)</label>
                  <select
                    className="form-control"
                    value={formData.gst_rate}
                    onChange={(e) => setFormData({ ...formData, gst_rate: Number(e.target.value) })}
                  >
                    <option value={0}>0% (Exempt / Nil)</option>
                    <option value={5}>5%</option>
                    <option value={12}>12%</option>
                    <option value={18}>18%</option>
                    <option value={28}>28%</option>
                  </select>
                </div>

                {/* Intake Quantity */}
                <div>
                  <label className="form-label" style={{ fontWeight: 600, color: '#15803d' }}>
                    + Stock Quantity to Add
                  </label>
                  <input
                    type="number"
                    min="0"
                    className="form-control"
                    value={formData.intake_qty}
                    onChange={(e) => setFormData({ ...formData, intake_qty: Number(e.target.value) })}
                    style={{ borderColor: '#86efac', backgroundColor: '#f0fdf4', fontWeight: 600 }}
                  />
                </div>

                {/* Warehouse Location */}
                <div>
                  <label className="form-label">Target Warehouse</label>
                  <select
                    className="form-control"
                    value={formData.warehouse_id}
                    onChange={(e) => setFormData({ ...formData, warehouse_id: e.target.value })}
                  >
                    {stores.map((s) => (
                      <option key={s.id} value={s.id}>
                        {s.name} ({s.location_type || 'Store'})
                      </option>
                    ))}
                  </select>
                </div>

                {/* Batch No & Expiry */}
                <div>
                  <label className="form-label">Batch No (Optional)</label>
                  <input
                    type="text"
                    className="form-control"
                    placeholder="e.g. BATCH-01"
                    value={formData.batch_no}
                    onChange={(e) => setFormData({ ...formData, batch_no: e.target.value })}
                  />
                </div>
              </div>

              {/* Action Buttons */}
              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'flex-end',
                  gap: '0.75rem',
                  marginTop: '1.25rem',
                  paddingTop: '1rem',
                  borderTop: '1px solid #f1f5f9',
                }}
              >
                <button
                  type="button"
                  className="btn btn-outline"
                  onClick={handleSkipAndResume}
                >
                  Skip (Esc)
                </button>

                <button
                  type="button"
                  className="btn btn-primary"
                  onClick={handleApprove}
                  disabled={isSubmitting}
                  style={{
                    backgroundColor: '#16a34a',
                    borderColor: '#16a34a',
                    padding: '0.6rem 1.5rem',
                    fontSize: '0.95rem',
                    display: 'flex',
                    alignItems: 'center',
                    gap: '0.5rem',
                  }}
                >
                  <IconCheck size={18} />
                  {isSubmitting
                    ? 'Saving...'
                    : isExistingProduct
                    ? '✓ Approve & Update Stock (Enter)'
                    : '✓ Approve & Save Product (Enter)'}
                </button>
              </div>
            </div>
          )}

          {/* SESSION RUNNING LOG */}
          <div>
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                marginBottom: '0.75rem',
              }}
            >
              <h3 style={{ fontSize: '0.9rem', fontWeight: 600, color: '#334155', margin: 0 }}>
                Items Processed in this Session ({sessionItems.length})
              </h3>
              {sessionItems.length > 0 && (
                <span style={{ fontSize: '0.75rem', color: '#64748b' }}>
                  Total added items ready in catalog
                </span>
              )}
            </div>

            {sessionItems.length === 0 ? (
              <div
                style={{
                  textAlign: 'center',
                  padding: '2.5rem 1rem',
                  border: '1px dashed #cbd5e1',
                  borderRadius: '8px',
                  color: '#94a3b8',
                  fontSize: '0.85rem',
                }}
              >
                <IconBarcode size={32} style={{ marginBottom: '0.5rem', opacity: 0.5 }} />
                <div>No items processed yet in this session.</div>
                <div style={{ marginTop: '0.25rem' }}>Scan any product barcode above to start adding!</div>
              </div>
            ) : (
              <div className="table-responsive" style={{ maxHeight: '240px', overflowY: 'auto' }}>
                <table className="table" style={{ fontSize: '0.825rem' }}>
                  <thead>
                    <tr>
                      <th>Time</th>
                      <th>Type</th>
                      <th>Product Name</th>
                      <th>Barcode</th>
                      <th>MRP</th>
                      <th>Added Qty</th>
                      <th>Warehouse</th>
                    </tr>
                  </thead>
                  <tbody>
                    {sessionItems.map((item, idx) => (
                      <tr key={idx}>
                        <td style={{ color: '#64748b' }}>{item.timestamp}</td>
                        <td>
                          <span
                            style={{
                              padding: '0.15rem 0.45rem',
                              borderRadius: '4px',
                              fontSize: '0.7rem',
                              fontWeight: 600,
                              backgroundColor: item.is_new ? '#ecfdf5' : '#eff6ff',
                              color: item.is_new ? '#047857' : '#1d4ed8',
                            }}
                          >
                            {item.is_new ? 'New' : 'Restock'}
                          </span>
                        </td>
                        <td style={{ fontWeight: 600, color: '#0f172a' }}>{item.name}</td>
                        <td style={{ fontFamily: 'monospace', color: '#475569' }}>{item.barcode}</td>
                        <td>₹{item.mrp.toFixed(2)}</td>
                        <td style={{ fontWeight: 600, color: '#16a34a' }}>+{item.qty_added}</td>
                        <td style={{ color: '#64748b' }}>{item.warehouse_name}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </div>

        {/* Footer */}
        <div
          style={{
            padding: '1rem 1.5rem',
            borderTop: '1px solid #e2e8f0',
            backgroundColor: '#f8fafc',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
          }}
        >
          <div style={{ fontSize: '0.8rem', color: '#64748b' }}>
            {intakeMode === 'AWAITING_SCAN' ? (
              <span>Point scanner at barcode or enter numbers manually</span>
            ) : (
              <span>Check and change details, then tap <strong>Approve</strong> to proceed immediately</span>
            )}
          </div>

          <button
            type="button"
            className="btn btn-outline"
            onClick={onClose}
            style={{ fontWeight: 600 }}
          >
            Done & Return to Catalog
          </button>
        </div>
      </div>

      {/* Camera Barcode Scanner Modal if triggered */}
      {showCameraScanner && (
        <BarcodeScannerModal
          isOpen={showCameraScanner}
          onClose={() => setShowCameraScanner(false)}
          onScan={(detectedCode) => {
            setShowCameraScanner(false);
            processScannedBarcode(detectedCode);
          }}
        />
      )}
    </div>
  );
}
