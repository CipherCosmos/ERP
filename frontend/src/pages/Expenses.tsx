import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { expenseApi } from '../services/api';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, NumberInput, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatDate, formatMoney, toISODate, todayISO } from '../utils/format';

interface Expense {
  id: string;
  store_id: string;
  category_id: string;
  category_name: string;
  amount: number;
  description: string;
  expense_date: string;
  user_id: string;
  created_at: string;
}

interface Category {
  id: string;
  name: string;
  is_active?: boolean;
}

interface CategoryTotal {
  category_id: string;
  category: string;
  total: number;
}

const today = todayISO;

const BLANK = { category_id: '', amount: '', description: '', expense_date: today() };

/**
 * What `GET /expenses` returns when nobody asks for a page size — the server's
 * own default, most recent first, and `expenseApi.list` sends no `limit`.
 *
 * The summary endpoint is not capped: it aggregates the whole period. So the
 * totals on this screen can legitimately exceed the sum of the rows below them,
 * and the list has to say when it is showing only the top of the pile.
 */
const LIST_CAP = 50;

/**
 * Expenses, rebuilt on the shared table and form.
 *
 * Two things were wrong beyond the missing table. The date filter sent `start`
 * and `end`, and the API reads `start_date` and `end_date` — so the filter
 * silently did nothing and the screen showed every expense ever recorded while
 * displaying a period at the top. And a rejected save went to the console: the
 * counter saw a form that simply refused to close.
 *
 * The category breakdown stays, because it is the question this screen answers
 * — not "what did I spend" but "on what". The tiles are now the filter: clicking
 * one narrows the table below to that category.
 *
 * There is no edit here, and that is not an oversight. The API offers
 * GET/POST /expenses and nothing else — no PUT, no DELETE — so a recorded
 * expense cannot be changed, and the detail view says so rather than showing a
 * Save button that would 404. Creating a category is the same story one level
 * down: the server has POST /expenses/categories, `expenseApi` has no wrapper
 * for it, and the create dialog says so instead of offering a field that
 * cannot be sent anywhere.
 */
export default function Expenses() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [expenses, setExpenses] = useState<Expense[]>([]);
  const [categories, setCategories] = useState<Category[]>([]);
  const [summary, setSummary] = useState<{ summary?: CategoryTotal[]; grand_total?: number } | null>(null);
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [viewing, setViewing] = useState<Expense | null>(null);
  const [form, setForm] = useState(BLANK);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [categoryFilter, setCategoryFilter] = useState('');

  // Named for the query parameters the server actually reads. The screen this
  // replaces held {start, end} and spread it straight into the request, where
  // both keys were ignored.
  const [range, setRange] = useState({ start_date: '', end_date: '' });

  const loadCategories = useCallback(async () => {
    try {
      const { data } = await expenseApi.getCategories();
      setCategories(data as Category[]);
    } catch (err) {
      console.error('Failed to load categories:', err);
    }
  }, []);

  const loadExpenses = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await expenseApi.list({
        store_id: store.id,
        // undefined rather than '': axios drops the parameter entirely, so an
        // unset side of the range never reaches the server as a blank date for
        // it to decide what to do with.
        start_date: range.start_date || undefined,
        end_date: range.end_date || undefined,
        // The category narrows the query rather than the rows that came back.
        // Filtering locally would be filtering the fifty most recent entries:
        // pick a category whose spending is older than that and the table would
        // read "nothing recorded" under a tile that says ₹12,500.
        category_id: categoryFilter || undefined,
      });
      setExpenses(data as Expense[]);
    } catch (err) {
      console.error('Failed to load expenses:', err);
    } finally {
      setLoading(false);
    }
  }, [store, range, categoryFilter]);

  const loadSummary = useCallback(async () => {
    if (!store) return;
    try {
      const { data } = await expenseApi.getSummary({
        store_id: store.id,
        start_date: range.start_date || undefined,
        end_date: range.end_date || undefined,
      });
      setSummary(data as { summary?: CategoryTotal[]; grand_total?: number });
    } catch (err) {
      console.error('Failed to load summary:', err);
    }
  }, [store, range]);

  // The category list does not depend on the period, so it is fetched on its
  // own. Folded in with the two below it would be refetched on every change of
  // a date input, for an answer that cannot have changed.
  useEffect(() => {
    if (!user || !store) return;
    loadCategories();
  }, [user, store, loadCategories]);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadExpenses();
  }, [user, store, navigate, loadExpenses]);

  // The breakdown deliberately does not follow the category filter — it is the
  // control that sets it, and a set of tiles that collapsed to the one already
  // chosen would leave no way to choose another. Kept in its own effect so that
  // picking a category refetches the list without refetching this.
  useEffect(() => {
    if (!user || !store) return;
    loadSummary();
  }, [user, store, loadSummary]);

  const openNew = () => {
    setForm({ ...BLANK, category_id: categoryFilter || categories[0]?.id || '' });
    setSaveError(null);
    setCreating(true);
  };

  const close = () => {
    setCreating(false);
    setSaveError(null);
  };

  const save = async () => {
    if (!store) return;
    if (!form.category_id) {
      setSaveError('Choose the category this spend belongs to.');
      return;
    }
    const amount = parseFloat(form.amount);
    if (!Number.isFinite(amount) || amount <= 0) {
      setSaveError('Enter the amount spent.');
      return;
    }
    if (!form.expense_date) {
      setSaveError('An expense needs the date it was incurred.');
      return;
    }
    setSaving(true);
    setSaveError(null);
    try {
      await expenseApi.create({
        store_id: store.id,
        category_id: form.category_id,
        amount,
        description: form.description,
        expense_date: form.expense_date,
      });
      // An entry recorded against a category the list is not showing would
      // vanish on save, which reads as a save that failed and invites a second
      // one. The filter gives way to the thing just recorded.
      const hidden = !!categoryFilter && categoryFilter !== form.category_id;
      close();
      if (hidden) {
        // Clearing the filter reloads the list through the effect that watches
        // it, so only the summary needs asking for here.
        setCategoryFilter('');
        await loadSummary();
      } else {
        await Promise.all([loadExpenses(), loadSummary()]);
      }
    } catch (err: any) {
      // Flattened by the axios layer, so this carries the server's field-level
      // reason. Dropping the raw object in here is what used to unmount a page.
      setSaveError(err?.response?.data?.error || 'Could not record this expense.');
    } finally {
      setSaving(false);
    }
  };

  const setPreset = (preset: 'month' | 'thirty' | 'all') => {
    if (preset === 'all') {
      setRange({ start_date: '', end_date: '' });
      return;
    }
    const end = new Date();
    const start = new Date();
    if (preset === 'month') start.setDate(1);
    else start.setDate(end.getDate() - 29);
    setRange({ start_date: toISODate(start), end_date: toISODate(end) });
  };

  const breakdown = useMemo(() => summary?.summary ?? [], [summary]);

  const period = useMemo(() => {
    if (!range.start_date && !range.end_date) return 'all time';
    if (range.start_date && range.end_date) return `${formatDate(range.start_date)} to ${formatDate(range.end_date)}`;
    if (range.start_date) return `from ${formatDate(range.start_date)}`;
    return `up to ${formatDate(range.end_date)}`;
  }, [range]);

  const largest = useMemo(
    () => breakdown.reduce<CategoryTotal | null>((top, c) => (!top || c.total > top.total ? c : top), null),
    [breakdown]
  );

  const filterName = categories.find((c) => c.id === categoryFilter)?.name;

  // The chosen category's spend comes from the summary, which the server
  // aggregates over the whole period. Adding up the visible rows would report
  // less than the tile above whenever the listing is capped.
  const filterTotal = breakdown.find((c) => c.category_id === categoryFilter)?.total ?? 0;

  const capped = expenses.length >= LIST_CAP;

  const columns: Column<Expense>[] = [
    {
      key: 'expense_date',
      header: 'Date',
      render: (e) => formatDate(e.expense_date),
      // The ISO value leads so the column sorts chronologically; the readable
      // form trails it so a search for "08-09-2026" still finds the row.
      value: (e) => (e.expense_date ? `${e.expense_date} ${formatDate(e.expense_date)}` : ''),
    },
    {
      key: 'category_name',
      header: 'Category',
      render: (e) => <span style={badge('info')}>{e.category_name || 'Uncategorised'}</span>,
      value: (e) => e.category_name,
    },
    {
      key: 'description',
      header: 'Description',
      render: (e) => e.description || <span style={styles.muted}>No note</span>,
      value: (e) => e.description,
    },
    {
      key: 'amount',
      header: 'Amount',
      numeric: true,
      render: (e) => <span style={{ fontWeight: 600 }}>{formatMoney(e.amount)}</span>,
      value: (e) => e.amount,
    },
  ];

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>Expenses</h1>
      </div>

      <div style={{ ...ui.card, padding: space.lg, marginBottom: space.lg }}>
        <div style={styles.filterRow}>
          <Field label="From" htmlFor="x-start">
            <TextInput
              id="x-start"
              type="date"
              value={range.start_date}
              onChange={(e) => setRange({ ...range, start_date: e.target.value })}
            />
          </Field>
          <Field label="To" htmlFor="x-end">
            <TextInput
              id="x-end"
              type="date"
              value={range.end_date}
              onChange={(e) => setRange({ ...range, end_date: e.target.value })}
            />
          </Field>
          <div style={styles.presets}>
            <button style={ui.button} onClick={() => setPreset('month')}>
              This month
            </button>
            <button style={ui.button} onClick={() => setPreset('thirty')}>
              Last 30 days
            </button>
            <button style={ui.button} onClick={() => setPreset('all')}>
              All time
            </button>
          </div>
        </div>
      </div>

      <div style={styles.summary}>
        <Stat label="Spend in period" value={formatMoney(summary?.grand_total ?? 0)} note={period} />
        <Stat
          label="Entries listed"
          value={String(expenses.length)}
          note={
            capped
              ? `the ${LIST_CAP} most recent — totals above cover the whole period`
              : categoryFilter
                ? `in ${filterName ?? 'this category'}`
                : undefined
          }
        />
        <Stat
          label="Biggest category"
          value={largest ? largest.category : '—'}
          note={largest ? formatMoney(largest.total) : undefined}
        />
      </div>

      {breakdown.length > 0 && (
        <div style={{ ...ui.card, padding: space.lg, marginBottom: space.lg }}>
          <div style={styles.statLabel}>Spend by category · {period}</div>
          <div style={styles.categoryGrid}>
            {breakdown.map((c) => {
              const active = categoryFilter === c.category_id;
              return (
                <button
                  key={c.category_id}
                  onClick={() => setCategoryFilter(active ? '' : c.category_id)}
                  style={{
                    ...styles.categoryTile,
                    borderColor: active ? color.primary : color.border,
                    background: active ? color.primarySoft : color.surfaceAlt,
                  }}
                  aria-pressed={active}
                  aria-label={`${c.category}, ${formatMoney(c.total)} — filter the list to this category`}
                >
                  <span style={{ color: active ? color.primary : color.textMuted, fontWeight: 600 }}>{c.category}</span>
                  <span style={styles.categoryTotal}>{formatMoney(c.total)}</span>
                </button>
              );
            })}
          </div>
          <div style={styles.grandTotal}>
            <span>Grand total</span>
            <span style={{ fontVariantNumeric: 'tabular-nums' }}>{formatMoney(summary?.grand_total ?? 0)}</span>
          </div>
        </div>
      )}

      <DataTable
        rows={expenses}
        columns={columns}
        rowKey={(e) => e.id}
        loading={loading}
        onRowClick={setViewing}
        searchPlaceholder="Search category or description…"
        // The period and the category are applied by the server, so the table
        // cannot tell an empty ledger from an empty month. Saying "no expenses"
        // while a date range hides twelve of them is a lie worth avoiding: it
        // sends someone off to record an entry that is already there.
        empty={
          categoryFilter
            ? `Nothing recorded against ${filterName ?? 'that category'} in this period.`
            : range.start_date || range.end_date
              ? 'No expenses in this period. Widen the dates, or record one.'
              : 'No expenses recorded yet.'
        }
        toolbar={
          <>
            {categoryFilter && (
              <span style={styles.sub}>
                Showing {filterName ?? 'one category'} · {formatMoney(filterTotal)} this period
              </span>
            )}
            <Select
              value={categoryFilter}
              onChange={(e) => setCategoryFilter(e.target.value)}
              style={{ width: 'auto' }}
              placeholder="All categories"
              options={categories.map((c) => ({ value: c.id, label: c.name }))}
            />
            <button style={ui.buttonPrimary} onClick={openNew}>
              + Record expense
            </button>
          </>
        }
      />

      <Modal
        open={creating}
        title="Record expense"
        subtitle="Recorded against this store and today's user, and cannot be edited afterwards."
        onClose={close}
        footer={
          <>
            <button style={ui.button} onClick={close} disabled={saving}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={save} disabled={saving}>
              {saving ? 'Saving…' : 'Record expense'}
            </button>
          </>
        }
      >
        <FormError error={saveError} />

        {categories.length === 0 && (
          <div style={styles.warn}>
            No expense categories exist yet, and an expense must belong to one. The server can create a category
            (POST /expenses/categories) but this app has no wrapper for it, so a category has to be added by whoever
            administers the API before spending can be recorded here.
          </div>
        )}

        <FormGrid>
          <Field label="Category" required htmlFor="x-cat">
            <Select
              id="x-cat"
              value={form.category_id}
              onChange={(e) => setForm({ ...form, category_id: e.target.value })}
              placeholder="Choose a category"
              options={categories.map((c) => ({ value: c.id, label: c.name }))}
            />
          </Field>
          <Field label="Amount" required htmlFor="x-amt">
            <NumberInput
              id="x-amt"
              value={form.amount}
              min={0}
              autoFocus
              onChange={(e) => setForm({ ...form, amount: e.target.value })}
            />
          </Field>
        </FormGrid>

        <FormGrid>
          <Field label="Date" required hint="The day the money went out, not today." htmlFor="x-date">
            <TextInput
              id="x-date"
              type="date"
              value={form.expense_date}
              onChange={(e) => setForm({ ...form, expense_date: e.target.value })}
            />
          </Field>
          <Field label="Description" hint="What it was for — this is the only note the entry keeps." htmlFor="x-desc">
            <TextInput
              id="x-desc"
              value={form.description}
              onChange={(e) => setForm({ ...form, description: e.target.value })}
            />
          </Field>
        </FormGrid>
      </Modal>

      <Modal
        open={!!viewing}
        title={viewing ? formatMoney(viewing.amount) : ''}
        subtitle={viewing ? `${viewing.category_name} · ${formatDate(viewing.expense_date)}` : undefined}
        onClose={() => setViewing(null)}
        width="32rem"
        footer={
          <button style={ui.button} onClick={() => setViewing(null)}>
            Close
          </button>
        }
      >
        {viewing && (
          <>
            <div style={styles.detailRow}>
              <span style={styles.statLabel}>Description</span>
              <span>{viewing.description || '—'}</span>
            </div>
            <div style={styles.detailRow}>
              <span style={styles.statLabel}>Recorded</span>
              <span>{formatDate(viewing.created_at)}</span>
            </div>
            <div style={styles.warn}>
              A recorded expense cannot be changed or removed: the API exposes only listing and creation for
              expenses. Correct a mistake by recording the right entry and noting the wrong one in its description.
            </div>
          </>
        )}
      </Modal>
    </div>
  );
}

function Stat({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 10rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div style={styles.statValue}>{value}</div>
      {note && <div style={styles.sub}>{note}</div>}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  header: { display: 'flex', alignItems: 'center', gap: space.lg, marginBottom: space.lg },
  h1: { margin: 0, fontSize: font.size.xxl },
  summary: { display: 'flex', gap: space.lg, marginBottom: space.lg, flexWrap: 'wrap' },
  statLabel: {
    fontSize: font.size.xs,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    color: color.textMuted,
    fontWeight: 700,
  },
  statValue: { marginTop: space.xs, fontSize: font.size.xl, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
  sub: { fontSize: font.size.xs, color: color.textMuted },
  muted: { color: color.textFaint },
  filterRow: { display: 'flex', gap: space.lg, alignItems: 'flex-end', flexWrap: 'wrap' },
  presets: { display: 'flex', gap: space.sm, marginLeft: 'auto' },
  categoryGrid: {
    display: 'grid',
    gridTemplateColumns: 'repeat(auto-fill, minmax(11rem, 1fr))',
    gap: space.sm,
    margin: `${space.md} 0`,
  },
  categoryTile: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    gap: space.sm,
    padding: `${space.sm} ${space.md}`,
    border: `1px solid ${color.border}`,
    borderRadius: '6px',
    cursor: 'pointer',
    fontFamily: font.body,
    fontSize: font.size.md,
    textAlign: 'left',
  },
  categoryTotal: { fontWeight: 700, fontVariantNumeric: 'tabular-nums', color: color.text },
  grandTotal: {
    display: 'flex',
    justifyContent: 'space-between',
    padding: `${space.md} 0 0`,
    borderTop: `1px solid ${color.border}`,
    fontWeight: 700,
    fontSize: font.size.lg,
  },
  detailRow: { display: 'flex', flexDirection: 'column', gap: space.xs },
  warn: {
    padding: space.md,
    background: color.warningSoft,
    border: `1px solid ${color.warning}33`,
    color: color.warning,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
};
