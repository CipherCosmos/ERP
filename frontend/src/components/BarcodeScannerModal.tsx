import React, { useState, useRef, useEffect, useCallback } from 'react';
// Types only: the ~300kB decoder itself is imported on demand in startScanning so it
// stays out of the main bundle, which counter tablets load on every shift.
import type { BrowserMultiFormatReader, IScannerControls } from '@zxing/browser';
import { IconBarcode, IconCamera, IconClose, IconSearch } from './Icons';

/**
 * getUserMedia only exists in a secure context. Serving the app over plain HTTP on a LAN
 * address (http://192.168.x.x) therefore removes navigator.mediaDevices entirely, which
 * looks to the user like the camera is simply dead. Report that case specifically.
 */
function getCameraUnavailableReason(): string | null {
  if (typeof window !== 'undefined' && !window.isSecureContext) {
    return 'The camera needs a secure connection. Open this app over HTTPS (or on localhost) to scan with the camera. Manual entry and USB scanners still work.';
  }
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    return 'Camera access is not supported by this browser.';
  }
  return null;
}

interface BarcodeScannerModalProps {
  isOpen: boolean;
  onClose: () => void;
  onScan: (barcode: string) => void;
  title?: string;
}

export function BarcodeScannerModal({
  isOpen,
  onClose,
  onScan,
  title = 'Scan Product Barcode',
}: BarcodeScannerModalProps) {
  const [manualCode, setManualCode] = useState('');
  const [isScanning, setIsScanning] = useState(false);
  const [cameraError, setCameraError] = useState('');
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const controlsRef = useRef<IScannerControls | null>(null);
  const readerRef = useRef<BrowserMultiFormatReader | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  const stopScanning = useCallback(() => {
    if (controlsRef.current) {
      try {
        controlsRef.current.stop();
      } catch {
        // ignore
      }
      controlsRef.current = null;
    }
    setIsScanning(false);
  }, []);

  const startScanning = useCallback(async () => {
    setCameraError('');
    setIsScanning(true);

    try {
      const unavailableReason = getCameraUnavailableReason();
      if (unavailableReason) {
        setCameraError(unavailableReason);
        setIsScanning(false);
        return;
      }

      if (!readerRef.current) {
        const [{ BrowserMultiFormatReader }, { BarcodeFormat, DecodeHintType }] = await Promise.all([
          import('@zxing/browser'),
          import('@zxing/library'),
        ]);

        // Restricting formats to the ones retail products actually carry makes each
        // decode attempt cheaper and removes false reads from unrelated symbologies.
        const hints = new Map<number, unknown>([
          [
            DecodeHintType.POSSIBLE_FORMATS,
            [
              BarcodeFormat.EAN_13,
              BarcodeFormat.EAN_8,
              BarcodeFormat.UPC_A,
              BarcodeFormat.UPC_E,
              BarcodeFormat.CODE_128,
              BarcodeFormat.CODE_39,
              BarcodeFormat.ITF,
              BarcodeFormat.CODABAR,
              BarcodeFormat.QR_CODE,
            ],
          ],
          [DecodeHintType.TRY_HARDER, true],
        ]);

        readerRef.current = new BrowserMultiFormatReader(hints as any, {
          delayBetweenScanAttempts: 150,
        });
      }

      // Small delay to ensure the video DOM node is mounted and ready
      await new Promise((r) => setTimeout(r, 100));

      if (!videoRef.current) {
        setIsScanning(false);
        return;
      }

      const scanCallback = (result: any) => {
        if (result) {
          const scannedCode = result.getText();
          try {
            const ctx = new (window.AudioContext || (window as any).webkitAudioContext)();
            const osc = ctx.createOscillator();
            const gain = ctx.createGain();
            osc.type = 'sine';
            osc.frequency.setValueAtTime(1800, ctx.currentTime);
            gain.gain.setValueAtTime(0.15, ctx.currentTime);
            gain.gain.exponentialRampToValueAtTime(0.01, ctx.currentTime + 0.1);
            osc.connect(gain);
            gain.connect(ctx.destination);
            osc.start();
            osc.stop(ctx.currentTime + 0.1);
          } catch {
            // audio context not allowed
          }
          stopScanning();
          onScan(scannedCode);
          onClose();
        }
      };

      let controls: IScannerControls;
      try {
        controls = await readerRef.current.decodeFromConstraints(
          { video: { facingMode: { ideal: 'environment' }, width: { ideal: 1280 }, height: { ideal: 720 } } },
          videoRef.current,
          scanCallback
        );
      } catch (firstErr) {
        console.warn('Primary camera constraint failed, retrying with fallback:', firstErr);
        controls = await readerRef.current.decodeFromConstraints(
          { video: true },
          videoRef.current,
          scanCallback
        );
      }

      controlsRef.current = controls;
    } catch (err: any) {
      console.warn('Barcode camera error:', err);
      setCameraError(
        err?.name === 'NotAllowedError'
          ? 'Camera permission denied. Please allow camera access in your browser settings.'
          : err?.name === 'NotFoundError' || err?.name === 'OverconstrainedError'
            ? 'No usable camera was found on this device.'
            : err?.name === 'NotReadableError'
              ? 'The camera is already in use by another app. Close it and try again.'
              : 'Could not start camera. You can enter the barcode manually below.'
      );
      setIsScanning(false);
    }
  }, [onScan, onClose, stopScanning]);

  useEffect(() => {
    if (isOpen) {
      setManualCode('');
      setCameraError('');
      // Auto-start camera when modal opens
      void startScanning();
      setTimeout(() => {
        inputRef.current?.focus();
      }, 200);
    } else {
      stopScanning();
    }
    return () => {
      stopScanning();
    };
  }, [isOpen, startScanning, stopScanning]);

  const handleManualSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = manualCode.trim();
    if (trimmed) {
      stopScanning();
      onScan(trimmed);
      onClose();
    }
  };

  if (!isOpen) return null;

  return (
    <div
      style={{
        position: 'fixed',
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        backgroundColor: 'rgba(15, 23, 42, 0.65)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        zIndex: 9999,
        padding: '1rem',
      }}
      onClick={onClose}
    >
      <div
        style={{
          backgroundColor: '#ffffff',
          borderRadius: '8px',
          boxShadow: '0 20px 25px -5px rgba(0, 0, 0, 0.2), 0 10px 10px -5px rgba(0, 0, 0, 0.1)',
          width: '100%',
          maxWidth: '480px',
          overflow: 'hidden',
          display: 'flex',
          flexDirection: 'column',
        }}
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div
          style={{
            padding: '1rem 1.25rem',
            borderBottom: '1px solid #e2e8f0',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
            <IconBarcode size={20} color="#2563eb" />
            <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 600 }}>{title}</h3>
          </div>
          <button
            type="button"
            style={{
              background: 'none',
              border: 'none',
              cursor: 'pointer',
              color: '#64748b',
              padding: '0.25rem',
            }}
            onClick={onClose}
          >
            <IconClose size={20} />
          </button>
        </div>

        {/* Body */}
        <div style={{ padding: '1.25rem' }}>
          {/* Camera View Area */}
          <div style={{ marginBottom: '1rem', textAlign: 'center' }}>
            <div
              style={{
                position: 'relative',
                width: '100%',
                height: 240,
                backgroundColor: '#0f172a',
                borderRadius: '6px',
                overflow: 'hidden',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
              }}
            >
              <video
                ref={videoRef}
                autoPlay
                playsInline
                muted
                style={{
                  width: '100%',
                  height: '100%',
                  objectFit: 'cover',
                  display: isScanning && !cameraError ? 'block' : 'none',
                }}
              />

              {isScanning && !cameraError && (
                <div
                  style={{
                    position: 'absolute',
                    top: '20%',
                    left: '12%',
                    right: '12%',
                    bottom: '20%',
                    border: '2px dashed #22c55e',
                    borderRadius: 4,
                    boxShadow: '0 0 0 9999px rgba(0, 0, 0, 0.35)',
                    pointerEvents: 'none',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                  }}
                >
                  <div
                    style={{
                      width: '90%',
                      height: '2px',
                      backgroundColor: '#22c55e',
                      opacity: 0.8,
                      boxShadow: '0 0 8px #22c55e',
                    }}
                  />
                </div>
              )}

              {(!isScanning || cameraError) && (
                <div style={{ padding: '1.5rem', color: '#94a3b8' }}>
                  <IconCamera size={36} color="#64748b" style={{ margin: '0 auto 0.5rem', display: 'block' }} />
                  <p style={{ margin: 0, fontSize: '0.85rem' }}>
                    {cameraError || 'Camera paused.'}
                  </p>
                </div>
              )}
            </div>

            <div style={{ marginTop: '0.5rem', display: 'flex', justifyContent: 'center', gap: '0.5rem' }}>
              {isScanning ? (
                <button
                  type="button"
                  className="btn btn-outline btn-sm"
                  style={{ fontSize: '0.8rem', padding: '0.35rem 0.75rem' }}
                  onClick={stopScanning}
                >
                  Pause Camera
                </button>
              ) : (
                <button
                  type="button"
                  className="btn btn-outline btn-sm"
                  style={{ fontSize: '0.8rem', padding: '0.35rem 0.75rem' }}
                  onClick={() => void startScanning()}
                >
                  <IconCamera size={14} /> Resume Camera
                </button>
              )}
            </div>
          </div>

          {/* Divider */}
          <div
            style={{
              position: 'relative',
              textAlign: 'center',
              margin: '1.25rem 0 1rem',
              borderTop: '1px solid #e2e8f0',
            }}
          >
            <span
              style={{
                position: 'relative',
                top: -10,
                backgroundColor: '#ffffff',
                padding: '0 0.5rem',
                color: '#64748b',
                fontSize: '0.75rem',
                fontWeight: 600,
              }}
            >
              OR ENTER BARCODE MANUALLY
            </span>
          </div>

          {/* Manual Entry */}
          <form onSubmit={handleManualSubmit}>
            <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.75rem' }}>
              <input
                ref={inputRef}
                type="text"
                className="form-input text-mono"
                style={{ flex: 1, padding: '0.55rem 0.75rem', fontSize: '0.95rem' }}
                placeholder="Type barcode or SKU..."
                value={manualCode}
                onChange={(e) => setManualCode(e.target.value)}
              />
              <button
                type="submit"
                className="btn btn-primary"
                style={{ padding: '0.55rem 1rem' }}
                disabled={!manualCode.trim()}
              >
                <IconSearch size={16} /> Enter
              </button>
            </div>
          </form>

          <p style={{ color: '#64748b', fontSize: '0.75rem', margin: '0.5rem 0 0', lineHeight: 1.4 }}>
            Tip: Point the barcode at your webcam or phone camera. USB barcode scanners work automatically without opening this window.
          </p>
        </div>
      </div>
    </div>
  );
}
