import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { categoryApi } from '../services/api';
import { Category } from '../types';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';

interface Draft {
  name: string;
  name_hi: string;
  name_kn: string;
  hsn_code: string;
  parent_id: string;
}

const BLANK: Draft = { name: '', name_hi: '', name_kn: '', hsn_code: '', parent_id: '' };

/**
 * Every category a product can be filed under, and the only place they can be
 * put in order.
 *
 * Until now a category could be created — inline, from the product form, as a
 * side effect of adding a product — and never touched again. A typo was
 * permanent. A duplicate made at the counter ("Biscuit" beside "Biscuits")
 * split the shop's own reporting in two for good, because every report that
 * groups by category treats them as different headings. And `parent_id` has
 * been in the schema from the first migration with nothing able to set it, so
 * every shop's catalogue was one flat list however large it grew.
 *
 * The three things this screen adds are rename, reparent and merge. Merge is
 * the important one: it is what makes retiring a category safe, because it says
 * where the contents go instead of leaving products filed under a heading that
 * no longer appears anywhere.
 */
export default function Categories() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [categories, setCategories] = useState<Category[]>([]);
  const [loading, setLoading] = useState(false);
  const [showRetired, setShowRetired] = useState(false);

  const [editing, setEditing] = useState<Category | null>(null);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState<Draft>(BLANK);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [confirmOff, setConfirmOff] = useState(false);
  const [mergeInto, setMergeInto] = useState('');
  const [notice, setNotice] = useState<string | null>(null);

  const storeId = store?.id;

  const load = useCallback(async () => {
    if (!storeId) return;
    setLoading(true);
    try {
      const { data } = await categoryApi.list(storeId, showRetired);
      setCategories(data || []);
    } catch (err) {
      console.error('Failed to load categories:', err);
    } finally {
      setLoading(false);
    }
  }, [storeId, showRetired]);

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
      return;
    }
    load();
  }, [user, navigate, load]);

  /**
   * Descendants of a category, from the rows already on screen.
   *
   * This is what keeps the parent dropdown from offering a move the server is
   * going to refuse. The server checks it too, in the transaction, and that
   * check is the one that makes it true — this one only keeps the shopkeeper
   * from being told no after they had already picked.
   */
  const descendantsOf = useCallback(
    (id: string) => {
      const found = new Set<string>([id]);
      // Repeat until nothing new is added: the list is not in depth order, so
      // one pass would miss a grandchild that appears above its parent.
      for (;;) {
        const before = found.size;
        for (const c of categories) {
          if (c.parent_id && found.has(c.parent_id)) found.add(c.id);
        }
        if (found.size === before) return found;
      }
    },
    [categories]
  );

  const parentOptions = useMemo(() => {
    const blocked = editing ? descendantsOf(editing.id) : new Set<string>();
    return [
      { value: '', label: 'No parent — top level' },
      ...categories
        .filter((c) => c.is_active && !blocked.has(c.id))
        .map((c) => ({
          value: c.id,
          label: c.parent_name ? `${c.parent_name} › ${c.name}` : c.name,
        })),
    ];
  }, [categories, editing, descendantsOf]);

  const mergeOptions = useMemo(() => {
    if (!editing) return [];
    return [
      { value: '', label: 'Choose where everything goes…' },
      ...categories
        .filter((c) => c.is_active && c.id !== editing.id)
        .map((c) => ({
          value: c.id,
          label: c.parent_name ? `${c.parent_name} › ${c.name}` : c.name,
        })),
    ];
  }, [categories, editing]);

  const totals = useMemo(
    () => ({
      live: categories.filter((c) => c.is_active).length,
      retired: categories.filter((c) => !c.is_active).length,
      empty: categories.filter((c) => c.is_active && !c.product_count && !c.child_count).length,
      products: categories.reduce((sum, c) => sum + (c.product_count ?? 0), 0),
    }),
    [categories]
  );

  const openNew = () => {
    setForm(BLANK);
    setEditing(null);
    setCreating(true);
    setSaveError(null);
    setConfirmOff(false);
    setMergeInto('');
  };

  const openEdit = (c: Category) => {
    setForm({
      name: c.name,
      name_hi: c.name_hi ?? '',
      name_kn: c.name_kn ?? '',
      hsn_code: c.hsn_code ?? '',
      parent_id: c.parent_id ?? '',
    });
    setEditing(c);
    setCreating(false);
    setSaveError(null);
    setConfirmOff(false);
    setMergeInto('');
  };

  const close = () => {
    setEditing(null);
    setCreating(false);
    setSaveError(null);
    setConfirmOff(false);
    setMergeInto('');
  };

  // Every write goes through this, so that a failure shows the server's own
  // complaint — "Biscuit still holds 4 products" — rather than a generic one.
  const run = async (what: string, fn: () => Promise<unknown>, after?: () => void) => {
    setSaving(true);
    setSaveError(null);
    try {
      await fn();
      after?.();
      await load();
    } catch (err: any) {
      setSaveError(err?.response?.data?.error || `Could not ${what}.`);
    } finally {
      setSaving(false);
    }
  };

  const save = () => {
    if (!form.name.trim()) {
      setSaveError('A category needs a name.');
      return;
    }
    const payload = {
      name: form.name.trim(),
      name_hi: form.name_hi.trim(),
      name_kn: form.name_kn.trim(),
      hsn_code: form.hsn_code.trim(),
      parent_id: form.parent_id || null,
    };
    return run(
      'save this category',
      () => (editing ? categoryApi.update(editing.id, payload) : categoryApi.create(payload)),
      close
    );
  };

  const merge = () => {
    if (!editing || !mergeInto) return;
    return run(
      'merge this category',
      async () => {
        const { data } = await categoryApi.merge(editing.id, mergeInto);
        const moved: string[] = [];
        if (data.products_moved) {
          moved.push(`${data.products_moved} ${data.products_moved === 1 ? 'product' : 'products'}`);
        }
        if (data.categories_moved) {
          moved.push(
            `${data.categories_moved} ${data.categories_moved === 1 ? 'sub-category' : 'sub-categories'}`
          );
        }
        setNotice(
          `${data.from} merged into ${data.into}${moved.length ? ` — ${moved.join(' and ')} moved` : ''}.` +
            (data.target_was_inside_it
              ? ` ${data.into} was inside ${data.from}, so it now sits where ${data.from} did.`
              : '')
        );
      },
      close
    );
  };

  const setActive = (active: boolean) => {
    if (!editing) return;
    return run(
      active ? 'bring this category back' : 'retire this category',
      () => (active ? categoryApi.activate(editing.id) : categoryApi.deactivate(editing.id)),
      close
    );
  };

  const columns: Column<Category>[] = [
    {
      key: 'name',
      header: 'Category',
      render: (c) => (
        <div>
          <div style={{ fontWeight: 600, display: 'flex', alignItems: 'center', gap: space.sm }}>
            {/* The parent's name is what makes a flat table read as a tree. It
                is a marker, not indentation: the table sorts, and a row indented
                under a heading that has moved away is worse than no tree. */}
            {c.parent_name && <span style={styles.parent}>{c.parent_name} ›</span>}
            <span>{c.name}</span>
          </div>
          {(c.name_hi || c.name_kn) && (
            <div style={styles.sub}>{[c.name_hi, c.name_kn].filter(Boolean).join(' · ')}</div>
          )}
        </div>
      ),
      value: (c) => `${c.parent_name ?? ''} ${c.name} ${c.name_hi ?? ''} ${c.name_kn ?? ''}`,
    },
    {
      key: 'hsn_code',
      header: 'HSN',
      render: (c) => c.hsn_code || <span style={styles.muted}>—</span>,
      value: (c) => c.hsn_code,
    },
    {
      key: 'product_count',
      header: 'Products',
      numeric: true,
      render: (c) => (c.product_count ? c.product_count : <span style={styles.muted}>—</span>),
      value: (c) => c.product_count ?? 0,
    },
    {
      key: 'child_count',
      header: 'Sub-categories',
      numeric: true,
      render: (c) => (c.child_count ? c.child_count : <span style={styles.muted}>—</span>),
      value: (c) => c.child_count ?? 0,
    },
    {
      key: 'is_active',
      header: 'Status',
      width: '7rem',
      render: (c) =>
        c.is_active ? <span style={badge('success')}>In use</span> : <span style={badge('neutral')}>Retired</span>,
      value: (c) => (c.is_active ? 1 : 0),
    },
  ];

  if (!user || user.role === 'cashier') return null;

  // What stops a category being retired outright. Discontinued products are
  // deliberately not counted here: they keep nothing in use, and a category
  // whose entire contents were withdrawn last year should be retirable.
  const inUse = editing ? (editing.product_count ?? 0) + (editing.child_count ?? 0) : 0;

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>Categories</h1>
        <button style={{ ...ui.button, marginLeft: 'auto' }} onClick={() => navigate('/products')}>
          Products →
        </button>
      </div>

      {notice && (
        <div style={styles.notice} role="status">
          {notice}
          <button style={styles.noticeClose} onClick={() => setNotice(null)} aria-label="Dismiss">
            ×
          </button>
        </div>
      )}

      <div style={styles.summary}>
        <Stat label="In use" value={String(totals.live)} />
        <Stat
          label="Products filed"
          value={String(totals.products)}
          note="Retired products are not counted."
        />
        <Stat
          label="Holding nothing"
          value={String(totals.empty)}
          note={totals.empty ? 'These can be retired outright.' : undefined}
        />
        <Stat label="Retired" value={String(totals.retired)} note={showRetired ? undefined : 'Not shown'} />
      </div>

      <DataTable
        rows={categories}
        columns={columns}
        rowKey={(c) => c.id}
        loading={loading}
        onRowClick={openEdit}
        searchPlaceholder="Search name, translation or HSN…"
        empty="No categories yet. Every product is filed under one, so make the first before adding stock."
        rowTone={(c) => (c.is_active ? undefined : 'warning')}
        toolbar={
          <>
            <label style={styles.toggle}>
              <input
                type="checkbox"
                checked={showRetired}
                onChange={(e) => setShowRetired(e.target.checked)}
              />
              Show retired
            </label>
            <button style={ui.buttonPrimary} onClick={openNew}>
              + New category
            </button>
          </>
        }
      />

      <Modal
        open={creating || !!editing}
        title={editing ? editing.name : 'New category'}
        subtitle={
          editing
            ? 'Renaming changes it everywhere at once, including on reports already run.'
            : 'Categories group the catalogue and carry the HSN code a new product starts from.'
        }
        onClose={close}
        width="42rem"
        footer={
          <>
            {editing &&
              (editing.is_active ? (
                <button
                  style={{ ...ui.buttonDanger, marginRight: 'auto' }}
                  onClick={() => (confirmOff ? setActive(false) : setConfirmOff(true))}
                  disabled={saving || inUse > 0}
                  title={inUse > 0 ? 'Merge it into another category instead.' : undefined}
                >
                  {confirmOff ? 'Confirm — retire' : 'Retire'}
                </button>
              ) : (
                <button
                  style={{ ...ui.button, marginRight: 'auto' }}
                  onClick={() => setActive(true)}
                  disabled={saving}
                >
                  Bring back
                </button>
              ))}
            <button style={ui.button} onClick={close} disabled={saving}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={save} disabled={saving}>
              {saving ? 'Saving…' : editing ? 'Save changes' : 'Add category'}
            </button>
          </>
        }
      >
        <FormError error={saveError} />

        {confirmOff && (
          <div style={styles.warn}>
            Retiring takes this category off every dropdown.{' '}
            {editing && (editing.retired_product_count ?? 0) > 0
              ? `${editing.retired_product_count} discontinued ${(editing.retired_product_count ?? 0) === 1 ? 'product stays' : 'products stay'} filed here and nothing else moves.`
              : 'It holds nothing, so nothing moves.'}
          </div>
        )}

        <FormGrid>
          <Field label="Name" required htmlFor="c-name">
            <TextInput
              id="c-name"
              value={form.name}
              autoFocus
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </Field>
          <Field
            label="Parent"
            hint="Leave at top level unless this belongs under something broader."
            htmlFor="c-parent"
          >
            <Select
              id="c-parent"
              value={form.parent_id}
              options={parentOptions}
              onChange={(e) => setForm({ ...form, parent_id: e.target.value })}
            />
          </Field>
          <Field label="Name in Hindi" htmlFor="c-hi">
            <TextInput
              id="c-hi"
              value={form.name_hi}
              onChange={(e) => setForm({ ...form, name_hi: e.target.value })}
            />
          </Field>
          <Field label="Name in Kannada" htmlFor="c-kn">
            <TextInput
              id="c-kn"
              value={form.name_kn}
              onChange={(e) => setForm({ ...form, name_kn: e.target.value })}
            />
          </Field>
          <Field
            label="HSN code"
            hint="What a new product in this category starts with. Each product keeps its own."
            htmlFor="c-hsn"
            span={2}
          >
            <TextInput
              id="c-hsn"
              value={form.hsn_code}
              inputMode="numeric"
              onChange={(e) => setForm({ ...form, hsn_code: e.target.value })}
            />
          </Field>
        </FormGrid>

        {editing && (
          <div style={styles.mergeBox}>
            <div style={styles.mergeHead}>
              <strong>Merge into another category</strong>
              <span style={styles.sub}>{describeContents(editing)}</span>
            </div>
            <div style={styles.mergeRow}>
              <Select
                value={mergeInto}
                options={mergeOptions}
                onChange={(e) => setMergeInto(e.target.value)}
                style={{ flex: 1 }}
                aria-label="Merge into"
              />
              <button style={ui.button} onClick={merge} disabled={saving || !mergeInto}>
                Merge
              </button>
            </div>
            <div style={styles.sub}>
              {editing.name} is retired afterwards. Invoices already issued keep the category they were
              issued with.
            </div>
          </div>
        )}
      </Modal>
    </div>
  );
}

/**
 * What a merge would actually move.
 *
 * Both product counts appear because both move. Showing only the live one and
 * then reporting a larger number afterwards reads as the system having done
 * something it was not asked to.
 */
function describeContents(c: Category): string {
  const live = c.product_count ?? 0;
  const retired = c.retired_product_count ?? 0;
  const children = c.child_count ?? 0;
  const parts: string[] = [];
  if (live) parts.push(`${live} ${live === 1 ? 'product' : 'products'}`);
  if (retired) parts.push(`${retired} discontinued`);
  if (children) parts.push(`${children} ${children === 1 ? 'sub-category' : 'sub-categories'}`);
  if (!parts.length) return 'Nothing is filed here, so this only retires it.';
  return `${parts.join(', ')} would move.`;
}

function Stat({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 9rem' }}>
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
  parent: { fontSize: font.size.xs, color: color.textFaint, fontWeight: 500 },
  toggle: {
    display: 'flex',
    alignItems: 'center',
    gap: space.xs,
    fontSize: font.size.sm,
    color: color.textMuted,
    whiteSpace: 'nowrap',
  },
  warn: {
    padding: space.md,
    background: color.warningSoft,
    border: `1px solid ${color.warning}33`,
    color: color.warning,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
  notice: {
    display: 'flex',
    alignItems: 'center',
    gap: space.md,
    padding: space.md,
    marginBottom: space.lg,
    background: color.successSoft,
    border: `1px solid ${color.success}33`,
    color: color.success,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
  noticeClose: {
    marginLeft: 'auto',
    background: 'none',
    border: 'none',
    color: 'inherit',
    fontSize: font.size.lg,
    cursor: 'pointer',
    lineHeight: 1,
  },
  mergeBox: {
    marginTop: space.lg,
    padding: space.lg,
    border: `1px solid ${color.border}`,
    borderRadius: '8px',
    display: 'flex',
    flexDirection: 'column',
    gap: space.sm,
  },
  mergeHead: { display: 'flex', flexDirection: 'column', gap: 2 },
  mergeRow: { display: 'flex', gap: space.sm, alignItems: 'center' },
};
