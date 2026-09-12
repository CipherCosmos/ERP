import React from 'react';
import { color, font, radius, space, ui } from '../theme';

/**
 * Form primitives.
 *
 * Every screen wrote its own bare `<input>` with a placeholder standing in for
 * a label. That costs more than it looks: a placeholder disappears the moment
 * someone types, so a half-filled form stops saying what its fields are, and a
 * screen reader gets nothing. These carry a real label, an optional hint, and a
 * field-level error — which is what the server has been sending all along in
 * `error.fields`, with no component able to display it.
 */

interface FieldProps {
  label: string;
  htmlFor?: string;
  required?: boolean;
  hint?: string;
  error?: string;
  children: React.ReactNode;
  /** Grid span when inside a <FormGrid>. */
  span?: number;
}

export function Field({ label, htmlFor, required, hint, error, children, span }: FieldProps) {
  return (
    <div style={{ gridColumn: span ? `span ${span}` : undefined, minWidth: 0 }}>
      <label htmlFor={htmlFor} style={ui.label}>
        {label}
        {required && <span style={{ color: color.danger, marginLeft: 2 }}>*</span>}
      </label>
      {children}
      {/* Error replaces the hint rather than stacking with it: once something is
          wrong, the correction is the only thing worth the reader's attention. */}
      {error ? (
        <div style={{ ...styles.sub, color: color.danger }}>{error}</div>
      ) : hint ? (
        <div style={{ ...styles.sub, color: color.textFaint }}>{hint}</div>
      ) : null}
    </div>
  );
}

/**
 * Lays fields out in as many columns as will comfortably fit.
 *
 * `min` is the narrowest a column may be before the grid drops to fewer of
 * them — which is what `auto-fit` + `minmax` actually means. The previous
 * version computed it as `12 / columns`, so asking for two columns set a
 * minimum of 6rem and produced *eight*: seven fields across a dialog at about
 * 100px each, with every label and hint shredded into four-line slivers.
 * Asking for fewer columns made more, which is the opposite of the knob's name.
 *
 * 14rem is the width at which a label, an input and a one-line hint still read
 * as a group. A field that needs the full width passes `span` on its Field.
 */
export function FormGrid({ min = '14rem', children }: { min?: string; children: React.ReactNode }) {
  return (
    <div
      style={{
        display: 'grid',
        // Plain minmax, not minmax(min(x, 100%), …): nesting min() inside
        // minmax() inside repeat() is rejected here, and React then drops the
        // whole declaration, collapsing the grid to a single column.
        gridTemplateColumns: `repeat(auto-fit, minmax(${min}, 1fr))`,
        gap: space.lg,
      }}
    >
      {children}
    </div>
  );
}

type InputProps = React.InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean };

export function TextInput({ invalid, style, ...rest }: InputProps) {
  return (
    <input
      {...rest}
      style={{ ...ui.input, ...(invalid ? styles.invalid : null), ...style }}
    />
  );
}

/**
 * NumberInput for money and quantities.
 *
 * `inputMode="decimal"` puts the numeric keypad on a tablet at the counter,
 * where most of this app's typing happens. `step` defaults to "any" because the
 * browser default of 1 makes a scale-weighed 2.5 kg fail native validation.
 */
export function NumberInput({ invalid, style, step = 'any', ...rest }: InputProps) {
  return (
    <input
      {...rest}
      type="number"
      inputMode="decimal"
      step={step}
      style={{
        ...ui.input,
        ...ui.numeric,
        fontFamily: font.mono,
        ...(invalid ? styles.invalid : null),
        ...style,
      }}
    />
  );
}

interface SelectProps extends React.SelectHTMLAttributes<HTMLSelectElement> {
  invalid?: boolean;
  options: { value: string; label: string }[];
  placeholder?: string;
}

export function Select({ invalid, options, placeholder, style, ...rest }: SelectProps) {
  return (
    <select {...rest} style={{ ...ui.input, ...(invalid ? styles.invalid : null), ...style }}>
      {placeholder && <option value="">{placeholder}</option>}
      {options.map((o) => (
        <option key={o.value} value={o.value}>
          {o.label}
        </option>
      ))}
    </select>
  );
}

export function Checkbox({
  label,
  checked,
  onChange,
  hint,
}: {
  label: string;
  checked: boolean;
  onChange: (v: boolean) => void;
  hint?: string;
}) {
  return (
    <label style={{ display: 'flex', gap: space.sm, alignItems: 'flex-start', cursor: 'pointer' }}>
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        style={{ marginTop: '0.2rem', width: '1rem', height: '1rem', cursor: 'pointer' }}
      />
      <span>
        <span style={{ fontSize: font.size.md }}>{label}</span>
        {hint && <span style={{ ...styles.sub, color: color.textFaint }}>{hint}</span>}
      </span>
    </label>
  );
}

/**
 * FormError shows what the server refused.
 *
 * The API answers a rejected write with `{error: {message, fields}}`, and the
 * axios layer flattens that to a string. Before this existed the object was
 * dropped straight into JSX, where React refuses it and unmounts the page — so
 * the screens crashed on exactly the validation messages they existed to show.
 */
export function FormError({ error }: { error?: string | null }) {
  if (!error) return null;
  return (
    <div
      role="alert"
      style={{
        padding: space.md,
        background: color.dangerSoft,
        border: `1px solid ${color.danger}33`,
        borderRadius: radius.md,
        color: color.danger,
        fontSize: font.size.md,
      }}
    >
      {error}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  sub: { display: 'block', marginTop: space.xs, fontSize: font.size.xs, lineHeight: 1.4 },
  // The whole `border` shorthand, not just borderColor. ui.input sets `border`,
  // and React logs "Updating a style property during rerender when a conflicting
  // property is set" every time the two are mixed — a console error on each
  // keystroke that flips the flag.
  invalid: { border: `1px solid ${color.danger}`, background: color.dangerSoft },
};
