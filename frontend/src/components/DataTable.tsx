import React, { useMemo, useState } from 'react';
import { color, font, space, ui } from '../theme';

/**
 * The table every list screen should have been using.
 *
 * Each page hand-wrote its own `<table>`, which is why none of them can sort,
 * search or page: those are not things a screen has unless somebody writes them
 * again, and nobody wrote them twenty-four times. Putting the behaviour here
 * means a screen gets all of it by describing its columns.
 *
 * Sorting and filtering happen on the rows given. That is the right trade at
 * this scale — a store's product list is hundreds of rows, not millions, and
 * the alternative is a server round trip per keystroke. A screen with a genuinely
 * large table should filter server-side and pass the page in as `rows`.
 */

export interface Column<T> {
  /** Stable key, also the default sort accessor when `value` is omitted. */
  key: string;
  header: string;
  /** What to render. Omit for `String(row[key])`. */
  render?: (row: T) => React.ReactNode;
  /**
   * The comparable/searchable value behind the cell. Give this whenever
   * `render` returns markup: sorting a column by its rendered JSX sorts by
   * "[object Object]", and searching it finds nothing.
   */
  value?: (row: T) => string | number | null | undefined;
  /** Right-aligns and uses lining figures. Use for money and quantities. */
  numeric?: boolean;
  sortable?: boolean;
  width?: string;
}

interface DataTableProps<T> {
  rows: T[];
  columns: Column<T>[];
  rowKey: (row: T) => string;
  loading?: boolean;
  /** Shown when there are genuinely no rows, as opposed to none matching a search. */
  empty?: React.ReactNode;
  onRowClick?: (row: T) => void;
  /** Renders in the toolbar, right of the search box — "+ New", filters, etc. */
  toolbar?: React.ReactNode;
  searchable?: boolean;
  searchPlaceholder?: string;
  pageSize?: number;
  /** Tints a row: overdue red, low stock amber. */
  rowTone?: (row: T) => 'danger' | 'warning' | 'success' | undefined;
}

type SortDir = 'asc' | 'desc';

function cellValue<T>(row: T, col: Column<T>): string | number | null | undefined {
  if (col.value) return col.value(row);
  const raw = (row as Record<string, unknown>)[col.key];
  if (raw === null || raw === undefined) return raw as null | undefined;
  return typeof raw === 'number' ? raw : String(raw);
}

export function DataTable<T>({
  rows,
  columns,
  rowKey,
  loading,
  empty = 'Nothing here yet',
  onRowClick,
  toolbar,
  searchable = true,
  searchPlaceholder = 'Search…',
  pageSize = 25,
  rowTone,
}: DataTableProps<T>) {
  const [query, setQuery] = useState('');
  const [sortKey, setSortKey] = useState<string | null>(null);
  const [sortDir, setSortDir] = useState<SortDir>('asc');
  const [page, setPage] = useState(0);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return rows;
    return rows.filter((row) =>
      columns.some((col) => {
        const v = cellValue(row, col);
        return v !== null && v !== undefined && String(v).toLowerCase().includes(q);
      })
    );
  }, [rows, columns, query]);

  const sorted = useMemo(() => {
    if (!sortKey) return filtered;
    const col = columns.find((c) => c.key === sortKey);
    if (!col) return filtered;
    // Copy first: sorting `filtered` in place mutates `rows` when no search is
    // active, which reorders the caller's state behind its back.
    return [...filtered].sort((a, b) => {
      const av = cellValue(a, col);
      const bv = cellValue(b, col);
      // Blanks sort last in both directions — an empty cell is not "smallest",
      // it is missing, and burying it under a descending sort hides it.
      if (av === null || av === undefined || av === '') return 1;
      if (bv === null || bv === undefined || bv === '') return -1;
      const cmp =
        typeof av === 'number' && typeof bv === 'number'
          ? av - bv
          : String(av).localeCompare(String(bv), undefined, { numeric: true });
      return sortDir === 'asc' ? cmp : -cmp;
    });
  }, [filtered, columns, sortKey, sortDir]);

  const pageCount = Math.max(1, Math.ceil(sorted.length / pageSize));
  // Clamp rather than store: a search that shortens the list can leave the page
  // index past the end, and the screen would show nothing with no way back.
  const currentPage = Math.min(page, pageCount - 1);
  const visible = sorted.slice(currentPage * pageSize, currentPage * pageSize + pageSize);

  const toggleSort = (col: Column<T>) => {
    if (col.sortable === false) return;
    if (sortKey === col.key) {
      setSortDir(sortDir === 'asc' ? 'desc' : 'asc');
    } else {
      setSortKey(col.key);
      setSortDir('asc');
    }
  };

  return (
    <div style={{ ...ui.card, overflow: 'hidden' }}>
      {(searchable || toolbar) && (
        <div style={styles.toolbar}>
          {searchable && (
            <input
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setPage(0);
              }}
              placeholder={searchPlaceholder}
              style={{ ...ui.input, maxWidth: '20rem' }}
              aria-label={searchPlaceholder}
            />
          )}
          <div style={{ marginLeft: 'auto', display: 'flex', gap: space.sm, alignItems: 'center' }}>
            {toolbar}
          </div>
        </div>
      )}

      {/* Wide tables scroll inside their own container so the page body never
          scrolls sideways. */}
      <div style={{ overflowX: 'auto' }}>
        <table style={styles.table}>
          <thead>
            <tr>
              {columns.map((col) => {
                const active = sortKey === col.key;
                const canSort = col.sortable !== false;
                return (
                  <th
                    key={col.key}
                    onClick={() => toggleSort(col)}
                    style={{
                      ...styles.th,
                      ...(col.numeric ? ui.numeric : null),
                      width: col.width,
                      cursor: canSort ? 'pointer' : 'default',
                      color: active ? color.primary : color.textMuted,
                    }}
                    aria-sort={active ? (sortDir === 'asc' ? 'ascending' : 'descending') : undefined}
                  >
                    {col.header}
                    {canSort && (
                      <span style={styles.sortMark}>{active ? (sortDir === 'asc' ? '▲' : '▼') : '⇅'}</span>
                    )}
                  </th>
                );
              })}
            </tr>
          </thead>
          <tbody>
            {loading && (
              <tr>
                <td colSpan={columns.length} style={styles.message}>
                  Loading…
                </td>
              </tr>
            )}

            {!loading &&
              visible.map((row) => {
                const tone = rowTone?.(row);
                return (
                  <tr
                    key={rowKey(row)}
                    onClick={onRowClick ? () => onRowClick(row) : undefined}
                    style={{
                      ...styles.tr,
                      cursor: onRowClick ? 'pointer' : 'default',
                      background: tone
                        ? { danger: color.dangerSoft, warning: color.warningSoft, success: color.successSoft }[tone]
                        : undefined,
                    }}
                  >
                    {columns.map((col) => (
                      <td key={col.key} style={{ ...styles.td, ...(col.numeric ? ui.numeric : null) }}>
                        {col.render ? col.render(row) : (cellValue(row, col) ?? '—')}
                      </td>
                    ))}
                  </tr>
                );
              })}

            {/* "No results for X" and "nothing here yet" are different facts and
                lead to different actions: clear the search, or go add one. */}
            {!loading && visible.length === 0 && (
              <tr>
                <td colSpan={columns.length} style={styles.message}>
                  {query ? `Nothing matches “${query}”` : empty}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {sorted.length > pageSize && (
        <div style={styles.footer}>
          <span style={{ color: color.textMuted, fontSize: font.size.sm }}>
            {currentPage * pageSize + 1}–{Math.min((currentPage + 1) * pageSize, sorted.length)} of {sorted.length}
          </span>
          <div style={{ marginLeft: 'auto', display: 'flex', gap: space.xs }}>
            <button
              style={{ ...ui.button, opacity: currentPage === 0 ? 0.4 : 1 }}
              onClick={() => setPage(currentPage - 1)}
              disabled={currentPage === 0}
            >
              ← Prev
            </button>
            <button
              style={{ ...ui.button, opacity: currentPage >= pageCount - 1 ? 0.4 : 1 }}
              onClick={() => setPage(currentPage + 1)}
              disabled={currentPage >= pageCount - 1}
            >
              Next →
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  toolbar: {
    display: 'flex',
    gap: space.sm,
    alignItems: 'center',
    padding: space.md,
    borderBottom: `1px solid ${color.border}`,
    background: color.surfaceAlt,
    flexWrap: 'wrap',
  },
  table: { width: '100%', borderCollapse: 'collapse', fontSize: font.size.md },
  th: {
    textAlign: 'left',
    padding: `${space.md} ${space.lg}`,
    fontSize: font.size.xs,
    fontWeight: 700,
    letterSpacing: '0.04em',
    textTransform: 'uppercase',
    borderBottom: `1px solid ${color.border}`,
    whiteSpace: 'nowrap',
    userSelect: 'none',
  },
  sortMark: { marginLeft: space.xs, fontSize: '0.625rem', opacity: 0.7 },
  tr: { borderBottom: `1px solid ${color.border}` },
  td: { padding: `${space.md} ${space.lg}`, verticalAlign: 'middle' },
  message: { padding: space.xxl, textAlign: 'center', color: color.textMuted },
  footer: {
    display: 'flex',
    alignItems: 'center',
    gap: space.sm,
    padding: space.md,
    borderTop: `1px solid ${color.border}`,
    background: color.surfaceAlt,
  },
};

export default DataTable;
