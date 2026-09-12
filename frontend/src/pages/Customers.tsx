import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { customerApi } from '../services/api';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, NumberInput, Select, TextInput, Checkbox } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatMoney } from '../utils/format';

interface Customer {
  id: string;
  name: string;
  name_hi: string;
  phone: string;
  address: string;
  gstin: string;
  credit_limit: number;
  is_wholesale: boolean;
  is_active: boolean;
  outstanding: number;
  advance: number;
}

type Blank = Omit<Customer, 'id' | 'outstanding' | 'advance' | 'is_active'>;

const BLANK: Blank = {
  name: '',
  name_hi: '',
  phone: '',
  address: '',
  gstin: '',
  credit_limit: 0,
  is_wholesale: false,
};

/**
 * Customers, rebuilt on the shared table and form.
 *
 * The screen this replaces could list customers and add one. It could not edit
 * a customer, search beyond a server round trip per keystroke, sort by anything,
 * see who owed money, or show why a save failed — the server's field errors were
 * being dropped into JSX, where React refuses an object and unmounts the page.
 *
 * The credit columns are the reason this screen exists at all. A shop extends
 * credit from memory otherwise, and the question it needs answered on sight is
 * not "who are my customers" but "who is over their limit".
 */
export default function Customers() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [customers, setCustomers] = useState<Customer[]>([]);
  const [loading, setLoading] = useState(false);
  const [editing, setEditing] = useState<Customer | null>(null);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState<Blank>(BLANK);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [creditFilter, setCreditFilter] = useState<'all' | 'owing' | 'over'>('all');

  const loadCustomers = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const { data } = await customerApi.list(store.id);
      setCustomers(data as Customer[]);
    } catch (err) {
      console.error('Failed to load customers:', err);
    } finally {
      setLoading(false);
    }
  }, [store]);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    loadCustomers();
  }, [user, store, navigate, loadCustomers]);

  const openNew = () => {
    setForm(BLANK);
    setEditing(null);
    setCreating(true);
    setSaveError(null);
  };

  const openEdit = (c: Customer) => {
    setForm({
      name: c.name,
      name_hi: c.name_hi ?? '',
      phone: c.phone ?? '',
      address: c.address ?? '',
      gstin: c.gstin ?? '',
      credit_limit: c.credit_limit ?? 0,
      is_wholesale: !!c.is_wholesale,
    });
    setEditing(c);
    setCreating(false);
    setSaveError(null);
  };

  const close = () => {
    setEditing(null);
    setCreating(false);
    setSaveError(null);
  };

  const save = async () => {
    if (!store) return;
    if (!form.name.trim()) {
      setSaveError('A customer needs a name.');
      return;
    }
    setSaving(true);
    setSaveError(null);
    try {
      if (editing) {
        await customerApi.update(editing.id, form);
      } else {
        await customerApi.create({ ...form, store_id: store.id } as any);
      }
      close();
      await loadCustomers();
    } catch (err: any) {
      // The axios layer flattens the server envelope to a string carrying the
      // field detail, so this is the actual reason rather than "save failed".
      setSaveError(err?.response?.data?.error || 'Could not save this customer.');
    } finally {
      setSaving(false);
    }
  };

  const rows = useMemo(() => {
    if (creditFilter === 'owing') return customers.filter((c) => c.outstanding > 0);
    if (creditFilter === 'over') {
      return customers.filter((c) => c.credit_limit > 0 && c.outstanding > c.credit_limit);
    }
    return customers;
  }, [customers, creditFilter]);

  const totals = useMemo(
    () => ({
      owed: customers.reduce((sum, c) => sum + (c.outstanding || 0), 0),
      owing: customers.filter((c) => c.outstanding > 0).length,
      over: customers.filter((c) => c.credit_limit > 0 && c.outstanding > c.credit_limit).length,
    }),
    [customers]
  );

  const columns: Column<Customer>[] = [
    {
      key: 'name',
      header: 'Customer',
      render: (c) => (
        <div>
          <div style={{ fontWeight: 600 }}>{c.name}</div>
          {c.name_hi && <div style={styles.sub}>{c.name_hi}</div>}
        </div>
      ),
      value: (c) => `${c.name} ${c.name_hi ?? ''}`,
    },
    { key: 'phone', header: 'Phone', render: (c) => c.phone || '—', value: (c) => c.phone },
    { key: 'gstin', header: 'GSTIN', render: (c) => c.gstin || '—', value: (c) => c.gstin },
    {
      key: 'type',
      header: 'Type',
      render: (c) => <span style={badge(c.is_wholesale ? 'info' : 'neutral')}>{c.is_wholesale ? 'Wholesale' : 'Retail'}</span>,
      value: (c) => (c.is_wholesale ? 'Wholesale' : 'Retail'),
    },
    {
      key: 'credit_limit',
      header: 'Credit limit',
      numeric: true,
      render: (c) => (c.credit_limit > 0 ? formatMoney(c.credit_limit) : <span style={styles.muted}>No credit</span>),
      value: (c) => c.credit_limit,
    },
    {
      key: 'outstanding',
      header: 'Outstanding',
      numeric: true,
      render: (c) => {
        if (!c.outstanding) return <span style={styles.muted}>—</span>;
        const over = c.credit_limit > 0 && c.outstanding > c.credit_limit;
        return (
          <span style={{ fontWeight: 600, color: over ? color.danger : color.text }}>
            {formatMoney(c.outstanding)}
          </span>
        );
      },
      value: (c) => c.outstanding,
    },
    {
      key: 'headroom',
      header: 'Headroom',
      numeric: true,
      // What the counter actually needs at the moment of a credit sale: how much
      // more this customer may take, not what they have already taken.
      render: (c) => {
        if (!c.credit_limit) return <span style={styles.muted}>—</span>;
        const left = c.credit_limit - (c.outstanding || 0);
        return (
          <span style={{ color: left < 0 ? color.danger : left === 0 ? color.warning : color.success, fontWeight: 600 }}>
            {formatMoney(left)}
          </span>
        );
      },
      value: (c) => (c.credit_limit ? c.credit_limit - (c.outstanding || 0) : null),
    },
    {
      key: 'actions',
      header: '',
      sortable: false,
      width: '5rem',
      render: (c) => (
        <button
          style={{ ...ui.button, padding: '0.25rem 0.625rem', fontSize: font.size.sm }}
          onClick={(e) => {
            e.stopPropagation();
            openEdit(c);
          }}
        >
          Edit
        </button>
      ),
    },
  ];

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>Customers</h1>
      </div>

      <div style={styles.summary}>
        <Stat label="Customers" value={String(customers.length)} />
        <Stat label="Owe money" value={String(totals.owing)} tone={totals.owing ? 'warning' : undefined} />
        <Stat label="Over their limit" value={String(totals.over)} tone={totals.over ? 'danger' : undefined} />
        <Stat label="Total outstanding" value={formatMoney(totals.owed)} />
      </div>

      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(c) => c.id}
        loading={loading}
        onRowClick={openEdit}
        searchPlaceholder="Search name, phone or GSTIN…"
        // The filter narrows the rows before the table sees them, so the table
        // cannot tell "no customers" from "none match this filter". Saying the
        // former while two customers exist sends someone off to add a duplicate.
        empty={
          creditFilter === 'over'
            ? 'Nobody is over their credit limit.'
            : creditFilter === 'owing'
              ? 'Nobody owes anything right now.'
              : 'No customers yet. Add one to start selling on credit.'
        }
        rowTone={(c) => (c.credit_limit > 0 && c.outstanding > c.credit_limit ? 'danger' : undefined)}
        toolbar={
          <>
            <Select
              value={creditFilter}
              onChange={(e) => setCreditFilter(e.target.value as typeof creditFilter)}
              style={{ width: 'auto' }}
              options={[
                { value: 'all', label: 'All customers' },
                { value: 'owing', label: 'Owing money' },
                { value: 'over', label: 'Over their limit' },
              ]}
            />
            <button style={ui.buttonPrimary} onClick={openNew}>
              + New customer
            </button>
          </>
        }
      />

      <Modal
        open={creating || !!editing}
        title={editing ? editing.name : 'New customer'}
        subtitle={editing ? 'Changes apply to future bills; past invoices keep the details they were raised with.' : undefined}
        onClose={close}
        footer={
          <>
            <button style={ui.button} onClick={close} disabled={saving}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={save} disabled={saving}>
              {saving ? 'Saving…' : editing ? 'Save changes' : 'Add customer'}
            </button>
          </>
        }
      >
        <FormError error={saveError} />

        <FormGrid>
          <Field label="Name" required htmlFor="c-name">
            <TextInput
              id="c-name"
              value={form.name}
              autoFocus
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </Field>
          <Field label="Name (Hindi)" htmlFor="c-name-hi">
            <TextInput id="c-name-hi" value={form.name_hi} onChange={(e) => setForm({ ...form, name_hi: e.target.value })} />
          </Field>
          <Field label="Phone" htmlFor="c-phone">
            <TextInput
              id="c-phone"
              value={form.phone}
              inputMode="tel"
              onChange={(e) => setForm({ ...form, phone: e.target.value })}
            />
          </Field>
          <Field label="GSTIN" hint="Needed on a B2B invoice." htmlFor="c-gstin">
            <TextInput
              id="c-gstin"
              value={form.gstin}
              onChange={(e) => setForm({ ...form, gstin: e.target.value.toUpperCase() })}
            />
          </Field>
        </FormGrid>

        <Field label="Address" htmlFor="c-address">
          <TextInput id="c-address" value={form.address} onChange={(e) => setForm({ ...form, address: e.target.value })} />
        </Field>

        <FormGrid>
          <Field
            label="Credit limit"
            hint="Zero means cash only — a credit sale will be refused at the counter."
            htmlFor="c-limit"
          >
            <NumberInput
              id="c-limit"
              value={form.credit_limit}
              min={0}
              onChange={(e) => setForm({ ...form, credit_limit: Number(e.target.value) })}
            />
          </Field>
          <Field label="Pricing">
            <Checkbox
              label="Wholesale customer"
              hint="Charged the wholesale price wherever one is set."
              checked={form.is_wholesale}
              onChange={(v) => setForm({ ...form, is_wholesale: v })}
            />
          </Field>
        </FormGrid>

        {editing && (
          <div style={styles.ledger}>
            <div>
              <div style={styles.ledgerLabel}>Outstanding</div>
              <div style={{ ...styles.ledgerValue, color: editing.outstanding > 0 ? color.danger : color.text }}>
                {formatMoney(editing.outstanding)}
              </div>
            </div>
            <div>
              <div style={styles.ledgerLabel}>Advance held</div>
              <div style={styles.ledgerValue}>{formatMoney(editing.advance)}</div>
            </div>
            <button style={{ ...ui.button, marginLeft: 'auto' }} onClick={() => navigate('/credit')}>
              Open credit ledger →
            </button>
          </div>
        )}
      </Modal>
    </div>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'warning' | 'danger' }) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 10rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div
        style={{
          ...styles.statValue,
          color: tone === 'danger' ? color.danger : tone === 'warning' ? color.warning : color.text,
        }}
      >
        {value}
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  header: { display: 'flex', alignItems: 'center', gap: space.lg, marginBottom: space.lg },
  h1: { margin: 0, fontSize: font.size.xxl },
  summary: { display: 'flex', gap: space.lg, marginBottom: space.lg, flexWrap: 'wrap' },
  statLabel: { fontSize: font.size.xs, textTransform: 'uppercase', letterSpacing: '0.04em', color: color.textMuted, fontWeight: 700 },
  statValue: { marginTop: space.xs, fontSize: font.size.xl, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
  sub: { fontSize: font.size.xs, color: color.textMuted },
  muted: { color: color.textFaint },
  ledger: {
    display: 'flex',
    gap: space.xl,
    alignItems: 'center',
    padding: space.lg,
    background: color.surfaceAlt,
    borderRadius: '10px',
    border: `1px solid ${color.border}`,
  },
  ledgerLabel: { fontSize: font.size.xs, textTransform: 'uppercase', letterSpacing: '0.04em', color: color.textMuted, fontWeight: 700 },
  ledgerValue: { marginTop: space.xs, fontSize: font.size.lg, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
};
