import React, { useEffect } from 'react';
import { color, font, radius, shadow, space, ui } from '../theme';

/**
 * A dialog that behaves like one.
 *
 * The screens each built their own inline panel, so none of them closed on
 * Escape, several could not be dismissed except by the one Cancel button, and a
 * couple rendered behind the page they belonged to. At a counter, a dialog that
 * will not close is a queue.
 */

interface ModalProps {
  open: boolean;
  title: string;
  subtitle?: string;
  onClose: () => void;
  children: React.ReactNode;
  /** Buttons for the sticky footer. Omit for a read-only view. */
  footer?: React.ReactNode;
  width?: string;
}

export function Modal({ open, title, subtitle, onClose, children, footer, width = '46rem' }: ModalProps) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    // The page behind must not scroll while a dialog is up, or dismissing it
    // returns the reader somewhere they did not choose to be.
    const previous = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      window.removeEventListener('keydown', onKey);
      document.body.style.overflow = previous;
    };
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div
      style={styles.backdrop}
      onClick={onClose}
      role="presentation"
    >
      <div
        style={{ ...ui.card, ...styles.panel, width, boxShadow: shadow.lg }}
        // Without this a click that begins inside the dialog and ends on the
        // backdrop closes it — which loses a half-typed form on a stray drag.
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label={title}
      >
        <div style={styles.header}>
          <div style={{ minWidth: 0 }}>
            <h2 style={styles.title}>{title}</h2>
            {subtitle && <div style={styles.subtitle}>{subtitle}</div>}
          </div>
          <button onClick={onClose} style={styles.close} aria-label="Close">
            ×
          </button>
        </div>

        <div style={styles.body}>{children}</div>

        {footer && <div style={styles.footer}>{footer}</div>}
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  backdrop: {
    position: 'fixed',
    inset: 0,
    background: 'rgba(15, 23, 42, 0.45)',
    display: 'flex',
    alignItems: 'flex-start',
    justifyContent: 'center',
    padding: space.xl,
    zIndex: 1000,
    overflowY: 'auto',
  },
  panel: {
    maxWidth: '100%',
    // Never taller than the viewport: the footer holds Save, and a dialog that
    // pushes its own Save button off-screen cannot be completed.
    maxHeight: 'calc(100vh - 4rem)',
    display: 'flex',
    flexDirection: 'column',
    marginTop: space.xl,
  },
  header: {
    display: 'flex',
    alignItems: 'flex-start',
    gap: space.lg,
    padding: `${space.lg} ${space.xl}`,
    borderBottom: `1px solid ${color.border}`,
  },
  title: { margin: 0, fontSize: font.size.xl, fontWeight: 700 },
  subtitle: { marginTop: space.xs, fontSize: font.size.sm, color: color.textMuted },
  close: {
    marginLeft: 'auto',
    border: 'none',
    background: 'transparent',
    fontSize: '1.5rem',
    lineHeight: 1,
    cursor: 'pointer',
    color: color.textMuted,
    padding: space.xs,
    borderRadius: radius.sm,
  },
  body: { padding: space.xl, overflowY: 'auto', display: 'grid', gap: space.lg },
  footer: {
    display: 'flex',
    gap: space.sm,
    justifyContent: 'flex-end',
    padding: `${space.lg} ${space.xl}`,
    borderTop: `1px solid ${color.border}`,
    background: color.surfaceAlt,
  },
};

export default Modal;
