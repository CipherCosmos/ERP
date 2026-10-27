import React, { useState, useRef, useEffect } from 'react';
import { IconBarcode, IconCamera, IconClose, IconSearch, IconAlert } from './Icons';

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
  const [cameraActive, setCameraActive] = useState(false);
  const [cameraError, setCameraError] = useState('');
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (isOpen) {
      setManualCode('');
      setCameraError('');
      setTimeout(() => {
        inputRef.current?.focus();
      }, 150);
    } else {
      stopCamera();
    }
  }, [isOpen]);

  const startCamera = async () => {
    setCameraError('');
    try {
      if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
        setCameraError('Camera access not supported on this device/browser.');
        return;
      }
      const stream = await navigator.mediaDevices.getUserMedia({
        video: { facingMode: 'environment' },
      });
      streamRef.current = stream;
      if (videoRef.current) {
        videoRef.current.srcObject = stream;
        videoRef.current.play();
      }
      setCameraActive(true);

      // Check if browser has native BarcodeDetector API
      if ('BarcodeDetector' in window) {
        const barcodeDetector = new (window as any).BarcodeDetector({
          formats: ['ean_13', 'ean_8', 'code_128', 'code_39', 'upc_a', 'upc_e', 'qr_code'],
        });

        const scanFrame = async () => {
          if (!videoRef.current || !streamRef.current) return;
          try {
            const barcodes = await barcodeDetector.detect(videoRef.current);
            if (barcodes.length > 0) {
              const code = barcodes[0].rawValue;
              stopCamera();
              onScan(code);
              onClose();
              return;
            }
          } catch (e) {
            // frame detect error or video ended
          }
          if (streamRef.current) {
            requestAnimationFrame(scanFrame);
          }
        };
        requestAnimationFrame(scanFrame);
      }
    } catch (err: any) {
      console.warn('Camera error:', err);
      setCameraError('Could not open camera. Please use manual entry or hardware scanner.');
      setCameraActive(false);
    }
  };

  const stopCamera = () => {
    if (streamRef.current) {
      streamRef.current.getTracks().forEach((track) => track.stop());
      streamRef.current = null;
    }
    setCameraActive(false);
  };

  const handleManualSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = manualCode.trim();
    if (trimmed) {
      stopCamera();
      onScan(trimmed);
      onClose();
    }
  };

  if (!isOpen) return null;

  return (
    <div className="erp-modal-backdrop" onClick={onClose}>
      <div className="erp-modal" style={{ maxWidth: 480 }} onClick={(e) => e.stopPropagation()}>
        <div className="erp-modal-header">
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
            <IconBarcode size={20} color="var(--primary-600)" />
            <h3 style={{ margin: 0, fontSize: '1rem', fontWeight: 600 }}>{title}</h3>
          </div>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            style={{ padding: '0.25rem 0.5rem', border: 'none' }}
            onClick={onClose}
          >
            <IconClose size={18} />
          </button>
        </div>

        <div className="erp-modal-body">
          {cameraActive ? (
            <div style={{ marginBottom: '1rem', textAlign: 'center' }}>
              <div
                style={{
                  position: 'relative',
                  width: '100%',
                  height: 240,
                  backgroundColor: '#000',
                  borderRadius: 'var(--radius-md)',
                  overflow: 'hidden',
                }}
              >
                <video
                  ref={videoRef}
                  style={{ width: '100%', height: '100%', objectFit: 'cover' }}
                />
                <div
                  style={{
                    position: 'absolute',
                    top: '20%',
                    left: '10%',
                    right: '10%',
                    bottom: '20%',
                    border: '2px dashed #22c55e',
                    borderRadius: 4,
                    pointerEvents: 'none',
                  }}
                />
              </div>
              <button
                type="button"
                className="btn btn-outline btn-sm"
                style={{ marginTop: '0.5rem' }}
                onClick={stopCamera}
              >
                Stop Camera
              </button>
            </div>
          ) : (
            <div style={{ marginBottom: '1.25rem', textAlign: 'center' }}>
              <button
                type="button"
                className="btn btn-outline"
                style={{ width: '100%', justifyContent: 'center' }}
                onClick={startCamera}
              >
                <IconCamera size={18} />
                <span>Use Device Camera Scanner</span>
              </button>
              {cameraError && (
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: '0.5rem',
                    color: 'var(--warning-text)',
                    fontSize: '0.75rem',
                    marginTop: '0.5rem',
                    textAlign: 'left',
                  }}
                >
                  <IconAlert size={16} />
                  <span>{cameraError}</span>
                </div>
              )}
            </div>
          )}

          <div
            style={{
              position: 'relative',
              textAlign: 'center',
              margin: '1rem 0',
              borderTop: '1px solid var(--border-light)',
            }}
          >
            <span
              style={{
                position: 'relative',
                top: -10,
                background: 'var(--bg-surface)',
                padding: '0 0.5rem',
                color: 'var(--text-muted)',
                fontSize: '0.75rem',
              }}
            >
              OR ENTER BARCODE / SKU MANUALLY
            </span>
          </div>

          <form onSubmit={handleManualSubmit}>
            <div className="form-group" style={{ marginBottom: '1rem' }}>
              <label className="form-label">Barcode / SKU / Code</label>
              <div style={{ display: 'flex', gap: '0.5rem' }}>
                <input
                  ref={inputRef}
                  type="text"
                  className="form-input text-mono"
                  placeholder="e.g. 8901001100001"
                  value={manualCode}
                  onChange={(e) => setManualCode(e.target.value)}
                />
                <button type="submit" className="btn btn-primary">
                  <IconSearch size={16} />
                  <span>Submit</span>
                </button>
              </div>
            </div>
          </form>

          <p style={{ color: 'var(--text-muted)', fontSize: '0.75rem', margin: 0 }}>
            Tip: You can also scan directly using any USB or Bluetooth handheld scanner without clicking this button.
          </p>
        </div>
      </div>
    </div>
  );
}
