import React, { useCallback, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { productApi } from '../services/api';
import { ImportProblem, ImportReport, ImportRowResult } from '../types';
import { DataTable, Column } from '../components/DataTable';
import { Checkbox, FormError } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';

/**
 * Bulk catalogue import.
 *
 * This is what decides whether a shop with two thousand items can start at all.
 * Entering them one at a time through the product form is roughly two working
 * weeks, and nobody does it — they keep the old system, or they enter the four
 * hundred lines they sell most and the rest never get a barcode.
 *
 * The screen is two steps and refuses to collapse them. Choosing a file runs a
 * dry run: the server does the entire import and rolls the transaction back,
 * so what comes back is what would happen — checked by the database, not
 * predicted. Nothing is written until the second button is pressed, and the
 * second button is only offered when the sheet is clean.
 */
export default function ProductImport() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [file, setFile] = useState<File | null>(null);
  const [updateExisting, setUpdateExisting] = useState(false);
  const [report, setReport] = useState<ImportReport | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const storeId = store?.id;

  const run = useCallback(
    async (chosen: File, apply: boolean) => {
      if (!storeId) return;
      setBusy(true);
      setError(null);
      try {
        const { data } = await productApi.import(storeId, chosen, { apply, updateExisting });
        setReport(data);
      } catch (err: any) {
        // A refusal here is about the file itself — no header, a column the
        // reader needs and cannot find — and the server's sentence says which.
        setError(err?.response?.data?.error || 'The file could not be read.');
        setReport(null);
      } finally {
        setBusy(false);
      }
    },
    [storeId, updateExisting]
  );

  const choose = (chosen: File | null) => {
    setFile(chosen);
    setReport(null);
    setError(null);
    if (chosen) run(chosen, false);
  };

  const downloadTemplate = async () => {
    try {
      const { data } = await productApi.importTemplate();
      const url = URL.createObjectURL(data as Blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = 'gbos-catalogue-template.csv';
      a.click();
      URL.revokeObjectURL(url);
    } catch {
      setError('The template could not be downloaded.');
    }
  };

  const clean = !!report && !report.problems?.length && report.failed === 0;
  const willChange = report ? report.created + report.updated : 0;

  const problemColumns: Column<ImportProblem>[] = useMemo(
    () => [
      { key: 'line', header: 'Line', numeric: true, width: '5rem', value: (p) => p.line },
      {
        key: 'column',
        header: 'Column',
        render: (p) => p.column || <span style={styles.muted}>—</span>,
        value: (p) => p.column ?? '',
      },
      { key: 'message', header: 'What is wrong', render: (p) => p.message, value: (p) => p.message },
    ],
    []
  );

  const resultColumns: Column<ImportRowResult>[] = useMemo(
    () => [
      { key: 'line', header: 'Line', numeric: true, width: '5rem', value: (r) => r.line },
      { key: 'name', header: 'Product', render: (r) => r.name, value: (r) => r.name },
      {
        key: 'outcome',
        header: 'What happens',
        width: '9rem',
        render: (r) => <span style={badge(outcomeTone(r.outcome))}>{outcomeLabel(r.outcome)}</span>,
        value: (r) => r.outcome,
      },
      {
        key: 'message',
        header: 'Detail',
        render: (r) =>
          r.message || (r.matched_on ? `matched on ${r.matched_on}` : <span style={styles.muted}>—</span>),
        value: (r) => r.message ?? r.matched_on ?? '',
      },
    ],
    []
  );

  if (!user || user.role === 'cashier') {
    navigate('/billing');
    return null;
  }

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/products')} style={ui.button}>
          ← Products
        </button>
        <h1 style={styles.h1}>Import a catalogue</h1>
      </div>

      <div style={{ ...ui.card, padding: space.lg, marginBottom: space.lg }}>
        <p style={styles.lead}>
          Upload the item list you already have. Column headings are matched by meaning, so a sheet from
          another system usually works as it is. Nothing is written until you have seen what it would do.
        </p>

        <div style={styles.controls}>
          <input
            ref={inputRef}
            type="file"
            accept=".csv,text/csv"
            style={{ display: 'none' }}
            onChange={(e) => choose(e.target.files?.[0] ?? null)}
          />
          <button style={ui.buttonPrimary} onClick={() => inputRef.current?.click()} disabled={busy}>
            {file ? 'Choose a different file' : 'Choose a spreadsheet'}
          </button>
          <button style={ui.button} onClick={downloadTemplate}>
            Download the template
          </button>
          {file && <span style={styles.filename}>{file.name}</span>}
        </div>

        <div style={{ marginTop: space.md }}>
          <Checkbox
            label="Update products already in the catalogue"
            hint="Off by default. With it off, a product the sheet names that is already here is left alone — which is what makes it safe to upload the same sheet twice."
            checked={updateExisting}
            onChange={(checked) => {
              setUpdateExisting(checked);
              if (file) {
                // The answer depends on this setting, so a stale report would
                // be showing the other question's result.
                setReport(null);
              }
            }}
          />
        </div>
      </div>

      <FormError error={error} />

      {busy && !report && <div style={styles.notice}>Checking the sheet…</div>}

      {file && !report && !busy && !error && (
        <div style={styles.notice}>
          <button style={ui.button} onClick={() => run(file, false)}>
            Check this sheet
          </button>
        </div>
      )}

      {report && (
        <>
          <div style={styles.summary}>
            <Stat label="Rows read" value={String(report.rows)} />
            <Stat label="New products" value={String(report.created)} tone={report.created ? 'success' : undefined} />
            <Stat
              label="Updated"
              value={String(report.updated)}
              note={!updateExisting && report.skipped ? 'updates are switched off' : undefined}
            />
            <Stat label="Left alone" value={String(report.skipped)} />
            <Stat
              label="Problems"
              value={String((report.problems?.length ?? 0) + report.failed)}
              tone={clean ? undefined : 'danger'}
            />
          </div>

          {report.applied ? (
            <div style={styles.done} role="status">
              Done. {report.created} added, {report.updated} updated
              {report.categories_created ? `, ${report.categories_created} categories created` : ''}
              {report.prices_set ? `, ${report.prices_set} prices set` : ''}.{' '}
              <button style={styles.link} onClick={() => navigate('/products')}>
                See the catalogue →
              </button>
            </div>
          ) : clean ? (
            <div style={styles.ready}>
              <div>
                <strong>Nothing has been written yet.</strong>{' '}
                {willChange === 0
                  ? 'This sheet would change nothing — every product in it is already in the catalogue.'
                  : `Applying this sheet will add ${report.created} and update ${report.updated} product${report.updated === 1 ? '' : 's'}.`}
                {report.categories_created > 0 && (
                  <div style={styles.sub}>
                    It will also create {report.categories_created}{' '}
                    {report.categories_created === 1 ? 'category' : 'categories'}:{' '}
                    {(report.new_categories ?? []).join(', ')}
                  </div>
                )}
              </div>
              <button
                style={{ ...ui.buttonPrimary, marginLeft: 'auto' }}
                disabled={busy || willChange === 0 || !file}
                onClick={() => file && run(file, true)}
              >
                {busy ? 'Importing…' : 'Apply this sheet'}
              </button>
            </div>
          ) : (
            <div style={styles.blocked}>
              <strong>This sheet was not applied.</strong> Every row is checked before any row is written, so
              a sheet with a bad line changes nothing at all. Fix the lines below in your spreadsheet and
              upload it again.
            </div>
          )}

          {!!report.problems?.length && (
            <section style={styles.section}>
              <h2 style={styles.h2}>Lines that could not be read</h2>
              <DataTable
                rows={report.problems}
                columns={problemColumns}
                rowKey={(p) => `${p.line}-${p.column ?? ''}-${p.message}`}
                searchable={false}
                pageSize={25}
              />
            </section>
          )}

          <section style={styles.section}>
            <h2 style={styles.h2}>{report.applied ? 'What happened' : 'What would happen'}</h2>
            <DataTable
              rows={report.results}
              columns={resultColumns}
              rowKey={(r) => String(r.line)}
              searchPlaceholder="Search by product or line…"
              empty="No rows got as far as being considered."
              rowTone={(r) => (r.outcome === 'failed' ? 'danger' : undefined)}
              pageSize={25}
            />
          </section>
        </>
      )}
    </div>
  );
}

function outcomeLabel(outcome: ImportRowResult['outcome']): string {
  switch (outcome) {
    case 'created':
      return 'New';
    case 'updated':
      return 'Updated';
    case 'skipped':
      return 'Already here';
    default:
      return 'Refused';
  }
}

function outcomeTone(outcome: ImportRowResult['outcome']): 'success' | 'info' | 'neutral' | 'danger' {
  switch (outcome) {
    case 'created':
      return 'success';
    case 'updated':
      return 'info';
    case 'skipped':
      return 'neutral';
    default:
      return 'danger';
  }
}

function Stat({
  label,
  value,
  tone,
  note,
}: {
  label: string;
  value: string;
  tone?: 'success' | 'danger';
  note?: string;
}) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 8rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div
        style={{
          ...styles.statValue,
          color: tone === 'danger' ? color.danger : tone === 'success' ? color.success : color.text,
        }}
      >
        {value}
      </div>
      {note && <div style={styles.sub}>{note}</div>}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  header: { display: 'flex', alignItems: 'center', gap: space.lg, marginBottom: space.lg },
  h1: { margin: 0, fontSize: font.size.xxl },
  h2: { margin: `0 0 ${space.sm} 0`, fontSize: font.size.lg },
  section: { marginTop: space.xl },
  lead: { margin: 0, color: color.textMuted, fontSize: font.size.md, maxWidth: '52rem' },
  controls: { display: 'flex', gap: space.md, alignItems: 'center', marginTop: space.lg, flexWrap: 'wrap' },
  filename: { fontSize: font.size.sm, color: color.textMuted },
  summary: { display: 'flex', gap: space.lg, marginBottom: space.lg, flexWrap: 'wrap' },
  statLabel: {
    fontSize: font.size.xs,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    color: color.textMuted,
    fontWeight: 700,
  },
  statValue: { marginTop: space.xs, fontSize: font.size.xl, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
  sub: { fontSize: font.size.xs, color: color.textMuted, marginTop: 2 },
  muted: { color: color.textFaint },
  notice: {
    padding: space.md,
    marginBottom: space.lg,
    background: color.surfaceAlt,
    borderRadius: '6px',
    fontSize: font.size.md,
    color: color.textMuted,
  },
  ready: {
    display: 'flex',
    alignItems: 'center',
    gap: space.lg,
    padding: space.lg,
    marginBottom: space.lg,
    background: color.primarySoft,
    border: `1px solid ${color.primary}33`,
    borderRadius: '8px',
    fontSize: font.size.md,
    flexWrap: 'wrap',
  },
  blocked: {
    padding: space.lg,
    marginBottom: space.lg,
    background: color.dangerSoft,
    border: `1px solid ${color.danger}33`,
    color: color.danger,
    borderRadius: '8px',
    fontSize: font.size.md,
  },
  done: {
    padding: space.lg,
    marginBottom: space.lg,
    background: color.successSoft,
    border: `1px solid ${color.success}33`,
    color: color.success,
    borderRadius: '8px',
    fontSize: font.size.md,
  },
  link: {
    background: 'none',
    border: 'none',
    color: 'inherit',
    textDecoration: 'underline',
    cursor: 'pointer',
    font: 'inherit',
    padding: 0,
  },
};
