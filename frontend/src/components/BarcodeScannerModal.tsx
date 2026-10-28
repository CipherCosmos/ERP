import React, { useState, useRef, useEffect, useCallback } from 'react';
import { IconBarcode, IconCamera, IconClose, IconSearch } from './Icons';

/**
 * getUserMedia only exists in a secure context. Serving the app over plain HTTP on a LAN
 * address (http://192.168.x.x) therefore removes navigator.mediaDevices entirely.
 */
function getCameraUnavailableReason(): string | null {
  if (typeof window !== 'undefined' && !window.isSecureContext) {
    return 'The camera needs a secure connection. Open this app over HTTPS or on localhost to scan with the camera. Manual entry and USB scanners still work.';
  }
  if (!navigator?.mediaDevices?.getUserMedia) {
    return 'Camera access is not supported by this browser. Manual entry and USB scanners still work.';
  }
  return null;
}

interface BarcodeScannerModalProps {
  isOpen: boolean;
  onClose: () => void;
  onScan: (barcode: string) => void;
  title?: string;
}

interface VideoDevice {
  deviceId: string;
  label: string;
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
  const [statusMessage, setStatusMessage] = useState('Initializing camera...');
  const [devices, setDevices] = useState<VideoDevice[]>([]);
  const [selectedDeviceId, setSelectedDeviceId] = useState<string>('');
  const [imageScanning, setImageScanning] = useState(false);

  const videoRef = useRef<HTMLVideoElement | null>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const isScanningRef = useRef<boolean>(false);
  const timerRef = useRef<any>(null);
  const zxingReaderRef = useRef<any>(null);

  const playBeep = useCallback(() => {
    try {
      const AudioCtx = window.AudioContext || (window as any).webkitAudioContext;
      if (!AudioCtx) return;
      const ctx = new AudioCtx();
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.type = 'sine';
      osc.frequency.setValueAtTime(1850, ctx.currentTime);
      gain.gain.setValueAtTime(0.2, ctx.currentTime);
      gain.gain.exponentialRampToValueAtTime(0.01, ctx.currentTime + 0.12);
      osc.connect(gain);
      gain.connect(ctx.destination);
      osc.start();
      osc.stop(ctx.currentTime + 0.12);
    } catch {
      // Audio playback might be restricted without user interaction
    }
  }, []);

  const stopStream = useCallback(() => {
    isScanningRef.current = false;
    if (timerRef.current) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
    if (streamRef.current) {
      try {
        streamRef.current.getTracks().forEach((t) => t.stop());
      } catch {
        // ignore
      }
      streamRef.current = null;
    }
    if (videoRef.current) {
      videoRef.current.srcObject = null;
    }
    setIsScanning(false);
  }, []);

  const handleBarcodeFound = useCallback(
    (code: string) => {
      const trimmed = code.trim();
      if (!trimmed) return;
      playBeep();
      stopStream();
      onScan(trimmed);
      onClose();
    },
    [onScan, onClose, playBeep, stopStream]
  );

  // Initialize or decode from video frames
  const runDetectionLoop = useCallback(
    async (stream: MediaStream) => {
      const video = videoRef.current;
      if (!video) return;

      isScanningRef.current = true;
      setIsScanning(true);
      setStatusMessage('Align barcode inside the green target box');

      // Check for native BarcodeDetector API (built into modern Chrome, Safari, Android)
      const hasNativeBarcodeDetector = 'BarcodeDetector' in window;
      let nativeDetector: any = null;

      if (hasNativeBarcodeDetector) {
        try {
          const supported = await (window as any).BarcodeDetector.getSupportedFormats?.();
          const desired = ['ean_13', 'ean_8', 'upc_a', 'upc_e', 'code_128', 'code_39', 'code_93', 'itf', 'qr_code'];
          const formats = Array.isArray(supported) ? desired.filter((f) => supported.includes(f)) : desired;
          nativeDetector = new (window as any).BarcodeDetector({ formats });
        } catch (e) {
          console.warn('Native BarcodeDetector initialization warning:', e);
          nativeDetector = null;
        }
      }

      // If native detector isn't available, lazy load ZXing library
      let zxingModules: any = null;
      let canvas: HTMLCanvasElement | null = null;
      let ctx: CanvasRenderingContext2D | null = null;

      if (!nativeDetector) {
        try {
          const zxing = await import('@zxing/library');
          const hints = new Map<number, unknown>([
            [
              zxing.DecodeHintType.POSSIBLE_FORMATS,
              [
                zxing.BarcodeFormat.EAN_13,
                zxing.BarcodeFormat.EAN_8,
                zxing.BarcodeFormat.UPC_A,
                zxing.BarcodeFormat.UPC_E,
                zxing.BarcodeFormat.CODE_128,
                zxing.BarcodeFormat.CODE_39,
                zxing.BarcodeFormat.ITF,
                zxing.BarcodeFormat.QR_CODE,
              ],
            ],
            [zxing.DecodeHintType.TRY_HARDER, true],
          ]);
          const reader = new zxing.MultiFormatReader();
          reader.setHints(hints);
          zxingReaderRef.current = reader;
          zxingModules = zxing;

          canvas = document.createElement('canvas');
          ctx = canvas.getContext('2d', { willReadFrequently: true });
        } catch (err) {
          console.error('Failed to load barcode decoder:', err);
          setCameraError('Unable to load barcode reader engine. Please use manual entry or USB scanner.');
          stopStream();
          return;
        }
      }

      // Frame loop with safe readyState checking
      const scanFrame = async () => {
        if (!isScanningRef.current) return;
        const currentVideo = videoRef.current;

        if (
          currentVideo &&
          currentVideo.readyState >= 2 &&
          currentVideo.videoWidth > 0 &&
          !currentVideo.paused &&
          !currentVideo.ended
        ) {
          try {
            if (nativeDetector) {
              const detected = await nativeDetector.detect(currentVideo);
              if (detected && detected.length > 0 && detected[0].rawValue) {
                handleBarcodeFound(detected[0].rawValue);
                return;
              }
            } else if (zxingModules && canvas && ctx && zxingReaderRef.current) {
              canvas.width = currentVideo.videoWidth;
              canvas.height = currentVideo.videoHeight;
              ctx.drawImage(currentVideo, 0, 0, canvas.width, canvas.height);
              const imgData = ctx.getImageData(0, 0, canvas.width, canvas.height);
              const luminanceSource = new zxingModules.RGBLuminanceSource(
                imgData.data,
                canvas.width,
                canvas.height
              );
              const binaryBitmap = new zxingModules.BinaryBitmap(
                new zxingModules.HybridBinarizer(luminanceSource)
              );
              const result = zxingReaderRef.current.decode(binaryBitmap);
              if (result && result.getText()) {
                handleBarcodeFound(result.getText());
                return;
              }
            }
          } catch (e: any) {
            // NotFoundException or normal non-match during video streaming is expected
          }
        }

        if (isScanningRef.current) {
          timerRef.current = setTimeout(scanFrame, 120);
        }
      };

      // Start frame scanning loop
      timerRef.current = setTimeout(scanFrame, 150);
    },
    [handleBarcodeFound, stopStream]
  );

  const startCamera = useCallback(
    async (deviceIdToUse?: string) => {
      stopStream();
      setCameraError('');
      setStatusMessage('Starting camera stream...');

      const unavailableReason = getCameraUnavailableReason();
      if (unavailableReason) {
        setCameraError(unavailableReason);
        return;
      }

      try {
        const constraints: MediaStreamConstraints = deviceIdToUse
          ? {
              video: {
                deviceId: { exact: deviceIdToUse },
                width: { ideal: 1280 },
                height: { ideal: 720 },
              },
            }
          : {
              video: {
                facingMode: { ideal: 'environment' },
                width: { ideal: 1280 },
                height: { ideal: 720 },
              },
            };

        let stream: MediaStream;
        try {
          stream = await navigator.mediaDevices.getUserMedia(constraints);
        } catch (firstErr) {
          console.warn('Initial camera constraint failed, retrying with simple constraint:', firstErr);
          stream = await navigator.mediaDevices.getUserMedia({ video: true });
        }

        streamRef.current = stream;

        // Query available devices for the switcher
        try {
          const deviceList = await navigator.mediaDevices.enumerateDevices();
          const videoInputs = deviceList
            .filter((d) => d.kind === 'videoinput')
            .map((d, idx) => ({
              deviceId: d.deviceId,
              label: d.label || `Camera ${idx + 1}`,
            }));
          setDevices(videoInputs);

          const activeTrack = stream.getVideoTracks()[0];
          const activeSettings = activeTrack?.getSettings?.();
          if (activeSettings?.deviceId) {
            setSelectedDeviceId(activeSettings.deviceId);
          }
        } catch {
          // Device enumeration optional
        }

        const video = videoRef.current;
        if (video) {
          video.srcObject = stream;
          video.muted = true;
          video.playsInline = true;
          await video.play().catch((playErr) => {
            console.warn('video.play() failed:', playErr);
          });
        }

        void runDetectionLoop(stream);
      } catch (err: any) {
        console.warn('Barcode camera startup error:', err);
        setCameraError(
          err?.name === 'NotAllowedError'
            ? 'Camera permission denied. Please allow camera access in your browser address bar.'
            : err?.name === 'NotFoundError' || err?.name === 'OverconstrainedError'
              ? 'No usable camera found on this device.'
              : err?.name === 'NotReadableError'
                ? 'Camera is in use by another program. Close it and try again.'
                : 'Could not access camera. You can enter the barcode manually or upload an image.'
        );
        stopStream();
      }
    },
    [runDetectionLoop, stopStream]
  );

  // Switch camera device
  const handleDeviceChange = (deviceId: string) => {
    setSelectedDeviceId(deviceId);
    void startCamera(deviceId);
  };

  // Upload & scan barcode image
  const handleImageUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;

    setImageScanning(true);
    setCameraError('');

    try {
      const img = new Image();
      const objectUrl = URL.createObjectURL(file);

      await new Promise<void>((resolve, reject) => {
        img.onload = () => resolve();
        img.onerror = () => reject(new Error('Failed to load image'));
        img.src = objectUrl;
      });

      // Try native BarcodeDetector first
      let foundBarcode: string | null = null;
      if ('BarcodeDetector' in window) {
        try {
          const detector = new (window as any).BarcodeDetector({
            formats: ['ean_13', 'ean_8', 'upc_a', 'upc_e', 'code_128', 'code_39', 'qr_code'],
          });
          const results = await detector.detect(img);
          if (results && results.length > 0 && results[0].rawValue) {
            foundBarcode = results[0].rawValue;
          }
        } catch {
          // Fall back to ZXing
        }
      }

      if (!foundBarcode) {
        const zxing = await import('@zxing/library');
        const canvas = document.createElement('canvas');
        canvas.width = img.naturalWidth || img.width;
        canvas.height = img.naturalHeight || img.height;
        const ctx = canvas.getContext('2d');
        if (ctx) {
          ctx.drawImage(img, 0, 0);
          const imgData = ctx.getImageData(0, 0, canvas.width, canvas.height);
          const luminanceSource = new zxing.RGBLuminanceSource(imgData.data, canvas.width, canvas.height);
          const binaryBitmap = new zxing.BinaryBitmap(new zxing.HybridBinarizer(luminanceSource));
          const reader = new zxing.MultiFormatReader();
          const result = reader.decode(binaryBitmap);
          if (result && result.getText()) {
            foundBarcode = result.getText();
          }
        }
      }

      URL.revokeObjectURL(objectUrl);

      if (foundBarcode) {
        handleBarcodeFound(foundBarcode);
      } else {
        setCameraError('No barcode was detected in this photo. Please try a clearer picture or enter manually.');
      }
    } catch (err: any) {
      console.warn('Image barcode detection error:', err);
      setCameraError('Could not decode barcode from this photo. Please enter manually.');
    } finally {
      setImageScanning(false);
      if (fileInputRef.current) {
        fileInputRef.current.value = '';
      }
    }
  };

  useEffect(() => {
    if (isOpen) {
      setManualCode('');
      setCameraError('');
      void startCamera();
      setTimeout(() => {
        inputRef.current?.focus();
      }, 250);
    } else {
      stopStream();
    }
    return () => {
      stopStream();
    };
  }, [isOpen, startCamera, stopStream]);

  const handleManualSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = manualCode.trim();
    if (trimmed) {
      handleBarcodeFound(trimmed);
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
          borderRadius: '10px',
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
            backgroundColor: '#f8fafc',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
            <IconBarcode size={22} color="#2563eb" />
            <h3 style={{ margin: 0, fontSize: '1.05rem', fontWeight: 600, color: '#0f172a' }}>{title}</h3>
          </div>
          <button
            type="button"
            style={{
              background: 'none',
              border: 'none',
              cursor: 'pointer',
              color: '#64748b',
              padding: '0.25rem',
              borderRadius: '4px',
            }}
            onClick={onClose}
            aria-label="Close"
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
                height: 250,
                backgroundColor: '#090d16',
                borderRadius: '8px',
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

              {/* Viewfinder Target Reticle */}
              {isScanning && !cameraError && (
                <div
                  style={{
                    position: 'absolute',
                    top: '20%',
                    left: '12%',
                    right: '12%',
                    bottom: '20%',
                    border: '2px solid #22c55e',
                    borderRadius: 6,
                    boxShadow: '0 0 0 9999px rgba(0, 0, 0, 0.4)',
                    pointerEvents: 'none',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                  }}
                >
                  {/* Animated Laser Scan Line */}
                  <div
                    style={{
                      width: '90%',
                      height: '2px',
                      backgroundColor: '#22c55e',
                      boxShadow: '0 0 10px #22c55e',
                    }}
                  />
                </div>
              )}

              {/* Overlay / Fallback Message */}
              {(!isScanning || cameraError) && (
                <div style={{ padding: '1.5rem', color: '#94a3b8', maxWidth: '380px' }}>
                  <IconCamera size={38} color="#64748b" style={{ margin: '0 auto 0.75rem', display: 'block' }} />
                  <p style={{ margin: 0, fontSize: '0.85rem', color: cameraError ? '#ef4444' : '#94a3b8' }}>
                    {cameraError || 'Camera paused.'}
                  </p>
                </div>
              )}

              {/* Status Pill */}
              {isScanning && !cameraError && (
                <div
                  style={{
                    position: 'absolute',
                    bottom: '8px',
                    left: '50%',
                    transform: 'translateX(-50%)',
                    backgroundColor: 'rgba(15, 23, 42, 0.8)',
                    color: '#ffffff',
                    padding: '3px 10px',
                    borderRadius: '12px',
                    fontSize: '0.75rem',
                    pointerEvents: 'none',
                    whiteSpace: 'nowrap',
                  }}
                >
                  {statusMessage}
                </div>
              )}
            </div>

            {/* Controls under camera: Camera switcher & pause */}
            <div
              style={{
                marginTop: '0.65rem',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                gap: '0.5rem',
                flexWrap: 'wrap',
              }}
            >
              {/* Device Selector if multiple cameras exist */}
              {devices.length > 1 && (
                <select
                  value={selectedDeviceId}
                  onChange={(e) => handleDeviceChange(e.target.value)}
                  style={{
                    fontSize: '0.78rem',
                    padding: '0.25rem 0.5rem',
                    borderRadius: '4px',
                    border: '1px solid #cbd5e1',
                    backgroundColor: '#f8fafc',
                    color: '#334155',
                    maxWidth: '180px',
                  }}
                  title="Choose Camera"
                >
                  {devices.map((d) => (
                    <option key={d.deviceId} value={d.deviceId}>
                      {d.label}
                    </option>
                  ))}
                </select>
              )}

              <div style={{ display: 'flex', gap: '0.5rem', marginLeft: 'auto' }}>
                {/* Upload Image Option */}
                <input
                  ref={fileInputRef}
                  type="file"
                  accept="image/*"
                  style={{ display: 'none' }}
                  onChange={handleImageUpload}
                />
                <button
                  type="button"
                  className="btn btn-outline btn-sm"
                  style={{ fontSize: '0.78rem', padding: '0.3rem 0.65rem' }}
                  onClick={() => fileInputRef.current?.click()}
                  disabled={imageScanning}
                >
                  {imageScanning ? 'Scanning photo...' : 'Upload Photo'}
                </button>

                {isScanning ? (
                  <button
                    type="button"
                    className="btn btn-outline btn-sm"
                    style={{ fontSize: '0.78rem', padding: '0.3rem 0.65rem' }}
                    onClick={stopStream}
                  >
                    Pause
                  </button>
                ) : (
                  <button
                    type="button"
                    className="btn btn-outline btn-sm"
                    style={{ fontSize: '0.78rem', padding: '0.3rem 0.65rem' }}
                    onClick={() => void startCamera(selectedDeviceId)}
                  >
                    <IconCamera size={13} /> Resume
                  </button>
                )}
              </div>
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
                fontSize: '0.72rem',
                fontWeight: 600,
                letterSpacing: '0.04em',
              }}
            >
              OR ENTER BARCODE MANUALLY
            </span>
          </div>

          {/* Manual Entry */}
          <form onSubmit={handleManualSubmit}>
            <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.5rem' }}>
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
                style={{ padding: '0.55rem 1.1rem' }}
                disabled={!manualCode.trim()}
              >
                <IconSearch size={16} /> Enter
              </button>
            </div>
          </form>

          <p style={{ color: '#64748b', fontSize: '0.74rem', margin: '0.5rem 0 0', lineHeight: 1.4 }}>
            💡 <strong>Hardware Scanners:</strong> USB and Bluetooth barcode scanners automatically type into search and POS without opening this modal.
          </p>
        </div>
      </div>
    </div>
  );
}
