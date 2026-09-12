/**
 * One place the screens agree on.
 *
 * Every page in this app declared its own `styles` object — sixteen to
 * twenty-two of them each — so a button was `#1976d2` on one screen and
 * `#4caf50` on the next, table padding varied by page, and a change of mind
 * about spacing meant editing twenty-four files. That is also why the screens
 * feel thin: with nothing shared, each one only has whatever someone hand-wrote
 * for it, so no screen gets sorting or filtering unless it is written again.
 *
 * These are plain objects rather than CSS-in-JS on purpose. The app already
 * styles with inline objects and adding a styling library to convert it would
 * be a bigger change than the problem warrants.
 */

export const color = {
  // A blue-leaning neutral ramp. Pure grey reads as unconsidered next to the
  // brand blue; a slight hue bias makes the surfaces look chosen.
  bg: '#f4f6f8',
  surface: '#ffffff',
  surfaceAlt: '#f8fafc',
  border: '#e2e8f0',
  borderStrong: '#cbd5e1',

  text: '#0f172a',
  textMuted: '#64748b',
  textFaint: '#94a3b8',

  primary: '#1d4ed8',
  primaryHover: '#1e40af',
  primarySoft: '#eff6ff',

  // Semantic, and deliberately distinct from the brand blue so that "this is a
  // link" and "this is fine" never look like the same thing.
  success: '#15803d',
  successSoft: '#f0fdf4',
  warning: '#b45309',
  warningSoft: '#fffbeb',
  danger: '#b91c1c',
  dangerSoft: '#fef2f2',
} as const;

export const space = {
  xs: '0.25rem',
  sm: '0.5rem',
  md: '0.75rem',
  lg: '1rem',
  xl: '1.5rem',
  xxl: '2rem',
} as const;

export const radius = { sm: '4px', md: '6px', lg: '10px', pill: '999px' } as const;

export const font = {
  // System stack: a POS runs on whatever is on the counter, and a webfont that
  // has not loaded yet is a blank bill.
  body: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif",
  // Digits that line up in columns. Money read down a column with proportional
  // figures is genuinely harder to check against a cash drawer.
  mono: "'SF Mono', 'Roboto Mono', Menlo, Consolas, monospace",
  size: { xs: '0.75rem', sm: '0.8125rem', md: '0.875rem', lg: '1rem', xl: '1.25rem', xxl: '1.5rem' },
} as const;

export const shadow = {
  sm: '0 1px 2px rgba(15, 23, 42, 0.06)',
  md: '0 2px 8px rgba(15, 23, 42, 0.08)',
  lg: '0 8px 24px rgba(15, 23, 42, 0.12)',
} as const;

/** Shared element styles, so a button looks the same on every screen. */
export const ui: Record<string, React.CSSProperties> = {
  page: {
    padding: space.lg,
    maxWidth: '1400px',
    margin: '0 auto',
    fontFamily: font.body,
    color: color.text,
  },
  card: {
    background: color.surface,
    border: `1px solid ${color.border}`,
    borderRadius: radius.lg,
    boxShadow: shadow.sm,
  },
  button: {
    padding: '0.5rem 0.875rem',
    fontSize: font.size.md,
    fontFamily: font.body,
    border: `1px solid ${color.borderStrong}`,
    borderRadius: radius.md,
    background: color.surface,
    color: color.text,
    cursor: 'pointer',
    lineHeight: 1.4,
  },
  buttonPrimary: {
    padding: '0.5rem 0.875rem',
    fontSize: font.size.md,
    fontFamily: font.body,
    border: `1px solid ${color.primary}`,
    borderRadius: radius.md,
    background: color.primary,
    color: '#fff',
    cursor: 'pointer',
    fontWeight: 600,
    lineHeight: 1.4,
  },
  buttonDanger: {
    padding: '0.5rem 0.875rem',
    fontSize: font.size.md,
    border: `1px solid ${color.danger}`,
    borderRadius: radius.md,
    background: color.surface,
    color: color.danger,
    cursor: 'pointer',
    lineHeight: 1.4,
  },
  input: {
    padding: '0.5rem 0.625rem',
    fontSize: font.size.md,
    fontFamily: font.body,
    border: `1px solid ${color.borderStrong}`,
    borderRadius: radius.md,
    background: color.surface,
    color: color.text,
    width: '100%',
    boxSizing: 'border-box',
  },
  label: {
    display: 'block',
    fontSize: font.size.sm,
    fontWeight: 600,
    color: color.textMuted,
    marginBottom: space.xs,
  },
  /** Numeric cells: right-aligned with figures that line up. */
  numeric: {
    textAlign: 'right',
    fontVariantNumeric: 'tabular-nums',
  },
};

/** A status pill. Colour carries the state so a row reads at a glance. */
export function badge(tone: 'neutral' | 'success' | 'warning' | 'danger' | 'info'): React.CSSProperties {
  const tones = {
    neutral: { bg: color.surfaceAlt, fg: color.textMuted, br: color.border },
    success: { bg: color.successSoft, fg: color.success, br: color.success },
    warning: { bg: color.warningSoft, fg: color.warning, br: color.warning },
    danger: { bg: color.dangerSoft, fg: color.danger, br: color.danger },
    info: { bg: color.primarySoft, fg: color.primary, br: color.primary },
  }[tone];
  return {
    display: 'inline-block',
    padding: '0.125rem 0.5rem',
    borderRadius: radius.pill,
    fontSize: font.size.xs,
    fontWeight: 600,
    background: tones.bg,
    color: tones.fg,
    border: `1px solid ${tones.br}33`,
    whiteSpace: 'nowrap',
  };
}
