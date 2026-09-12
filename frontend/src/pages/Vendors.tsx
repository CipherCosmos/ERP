import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { vendorApi } from '../services/api';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, NumberInput, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatMoney } from '../utils/format';

interface Vendor {
  id: string;
  name: string;
  phone: string;
  address: string;
  gstin: string;
  state: string;
  credit_days?: number;
  credit_limit?: number;
  is_active?: boolean;
}

/**
 * What a vendor is owed. `GET /vendors/:id` answers with
 * `{vendor, exposure: {ordered, paid}, outstanding}` — everything ordered and
 * not cancelled, less everything paid.
 */
interface Exposure {
  ordered: number;
  paid: number;
  outstanding: number;
}

type Blank = Omit<Vendor, 'id' | 'is_active'>;

const BLANK: Blank = {
  name: '',
  phone: '',
  address: '',
  gstin: '',
  state: '',
  credit_days: 0,
  credit_limit: 0,
};

/** How many balance lookups to have in flight at once. */
const BALANCE_BATCH = 6;

/**
 * What `GET /vendors` returns when nobody asks for a page size — the server's
 * own default, and `vendorApi.list` sends no `limit`. The search box below
 * filters the rows that arrived, so on a book longer than this the screen has
 * to say so rather than let a search come back empty for a vendor that exists.
 */
const LIST_CAP = 100;

/**
 * Vendors, rebuilt on the shared table and form.
 *
 * The screen this replaces was a card grid. It could list vendors and add one;
 * it could not edit a vendor, sort by anything, deactivate one, or show what
 * any of them were owed — and its "Total Purchase" line read a `total_purchase`
 * field the server has never sent on the list endpoint, so it never appeared.
 *
 * The money is the reason to open this screen. A buyer about to raise a purchase
 * order needs to know what this vendor is already owed and whether another order
 * fits inside their limit — the server refuses the order otherwise, and finding
 * that out at the order screen is finding it out too late.
 *
 * The list endpoint carries no balance, so each vendor's exposure is fetched
 * once after the list arrives, a few at a time. That is a bounded fan-out on
 * load rather than a poll: it settles and does not run again until the list is
 * reloaded.
 */
export default function Vendors() {
  const navigate = useNavigate();
  const { user } = useAppStore();

  const [vendors, setVendors] = useState<Vendor[]>([]);
  const [balances, setBalances] = useState<Record<string, Exposure | null>>({});
  const [balancesPending, setBalancesPending] = useState(false);
  const [loading, setLoading] = useState(false);
  const [editing, setEditing] = useState<Vendor | null>(null);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState<Blank>(BLANK);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [confirmOff, setConfirmOff] = useState(false);
  const [creditFilter, setCreditFilter] = useState<'all' | 'owing' | 'over'>('all');

  // A reload started while balances are still arriving must not have the old
  // run's answers land on top of the new list.
  const runRef = useRef(0);

  const loadVendors = useCallback(async () => {
    setLoading(true);
    const run = runRef.current + 1;
    runRef.current = run;
    try {
      const { data } = await vendorApi.list();
      const rows = (data as Vendor[]) || [];
      setVendors(rows);
      setBalances({});
      setLoading(false);

      // Balances, a few at a time, so a hundred-vendor book does not open a
      // hundred sockets at once. Each batch lands as it finishes, so the column
      // fills in rather than waiting on the slowest lookup.
      setBalancesPending(rows.length > 0);
      for (let i = 0; i < rows.length; i += BALANCE_BATCH) {
        if (runRef.current !== run) return;
        const batch = rows.slice(i, i + BALANCE_BATCH);
        const settled = await Promise.allSettled(batch.map((v) => vendorApi.get(v.id)));
        if (runRef.current !== run) return;
        setBalances((prev) => {
          const next = { ...prev };
          settled.forEach((result, index) => {
            const id = batch[index].id;
            if (result.status !== 'fulfilled') {
              // null is "we asked and could not find out", which the cell says
              // out loud. Showing a dash there would read as "owes nothing".
              next[id] = null;
              return;
            }
            const body = result.value.data as any;
            next[id] = {
              ordered: Number(body?.exposure?.ordered ?? 0),
              paid: Number(body?.exposure?.paid ?? 0),
              outstanding: Number(body?.outstanding ?? 0),
            };
          });
          return next;
        });
      }
      if (runRef.current === run) setBalancesPending(false);
    } catch (err) {
      console.error('Failed to load vendors:', err);
      setLoading(false);
      setBalancesPending(false);
    }
  }, []);

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
      return;
    }
    loadVendors();
  }, [user, navigate, loadVendors]);

  const openNew = () => {
    setForm(BLANK);
    setEditing(null);
    setCreating(true);
    setSaveError(null);
    setConfirmOff(false);
  };

  const openEdit = (v: Vendor) => {
    setForm({
      name: v.name,
      phone: v.phone ?? '',
      address: v.address ?? '',
      gstin: v.gstin ?? '',
      state: v.state ?? '',
      credit_days: v.credit_days ?? 0,
      credit_limit: v.credit_limit ?? 0,
    });
    setEditing(v);
    setCreating(false);
    setSaveError(null);
    setConfirmOff(false);
  };

  const close = () => {
    setEditing(null);
    setCreating(false);
    setSaveError(null);
    setConfirmOff(false);
  };

  const save = async () => {
    if (!form.name.trim()) {
      setSaveError('A vendor needs a name.');
      return;
    }
    setSaving(true);
    setSaveError(null);
    const payload = {
      name: form.name.trim(),
      phone: form.phone,
      address: form.address,
      gstin: form.gstin,
      state: form.state,
      credit_days: Number(form.credit_days) || 0,
      credit_limit: Number(form.credit_limit) || 0,
    };
    try {
      if (editing) {
        await vendorApi.update(editing.id, payload);
      } else {
        await vendorApi.create(payload);
      }
      close();
      await loadVendors();
    } catch (err: any) {
      // The axios layer flattens `{error:{message,fields}}` to a string, so this
      // is the server's actual complaint — "name: this field is required" —
      // rather than a generic failure.
      setSaveError(err?.response?.data?.error || 'Could not save this vendor.');
    } finally {
      setSaving(false);
    }
  };

  const deactivate = async () => {
    if (!editing) return;
    setSaving(true);
    setSaveError(null);
    try {
      await vendorApi.deactivate(editing.id);
      close();
      await loadVendors();
    } catch (err: any) {
      setSaveError(err?.response?.data?.error || 'Could not deactivate this vendor.');
    } finally {
      setSaving(false);
    }
  };

  const outstandingOf = useCallback(
    (v: Vendor) => {
      const balance = balances[v.id];
      return balance ? balance.outstanding : undefined;
    },
    [balances]
  );

  const isOverLimit = useCallback(
    (v: Vendor) => {
      const owed = outstandingOf(v);
      return owed !== undefined && (v.credit_limit ?? 0) > 0 && owed > (v.credit_limit ?? 0);
    },
    [outstandingOf]
  );

  const rows = useMemo(() => {
    if (creditFilter === 'owing') return vendors.filter((v) => (outstandingOf(v) ?? 0) > 0);
    if (creditFilter === 'over') return vendors.filter(isOverLimit);
    return vendors;
  }, [vendors, creditFilter, outstandingOf, isOverLimit]);

  const totals = useMemo(() => {
    let owed = 0;
    let known = 0;
    vendors.forEach((v) => {
      const balance = balances[v.id];
      if (balance) {
        owed += balance.outstanding;
        known += 1;
      }
    });
    return {
      owed,
      known,
      over: vendors.filter(isOverLimit).length,
      onCredit: vendors.filter((v) => (v.credit_days ?? 0) > 0).length,
      // What the trade has agreed to carry for this shop, which is the ceiling
      // the outstanding figure beside it is spending against.
      extended: vendors.reduce((sum, v) => sum + (v.credit_limit ?? 0), 0),
      // Lookups that came back as a failure rather than a figure. A credit
      // filter cannot speak for these, and the empty state has to admit it.
      unknown: vendors.filter((v) => balances[v.id] === null).length,
    };
  }, [vendors, balances, isOverLimit]);

  /**
   * What to say when a credit filter leaves nothing on screen.
   *
   * The filters run over balances fetched one vendor at a time, so an empty
   * result can mean three different things: still arriving, asked and refused,
   * or genuinely nobody. "Nothing is owed to any vendor" while six lookups
   * failed is the same false all-clear as an empty state that hides a filter.
   */
  const emptyMessage = () => {
    if (creditFilter === 'all') return 'No vendors yet. Add one before raising a purchase order.';
    if (balancesPending) return 'Still working out what each vendor is owed…';
    const caveat = totals.unknown
      ? ` — ${totals.unknown} ${totals.unknown === 1 ? 'balance' : 'balances'} could not be loaded, so this does not cover ${totals.unknown === 1 ? 'that vendor' : 'those vendors'}.`
      : '';
    return creditFilter === 'over'
      ? `No vendor is over their credit limit${caveat || '.'}`
      : `Nothing is owed to any vendor right now${caveat || '.'}`;
  };

  const columns: Column<Vendor>[] = [
    {
      key: 'name',
      header: 'Vendor',
      render: (v) => (
        <div>
          <div style={{ fontWeight: 600 }}>{v.name}</div>
          {v.address && <div style={styles.sub}>{v.address}</div>}
        </div>
      ),
      value: (v) => `${v.name} ${v.address ?? ''}`,
    },
    { key: 'phone', header: 'Phone', render: (v) => v.phone || '—', value: (v) => v.phone },
    { key: 'gstin', header: 'GSTIN', render: (v) => v.gstin || '—', value: (v) => v.gstin },
    { key: 'state', header: 'State', render: (v) => v.state || '—', value: (v) => v.state },
    {
      key: 'credit_days',
      header: 'Terms',
      render: (v) =>
        (v.credit_days ?? 0) > 0 ? (
          <span style={badge('info')}>{v.credit_days} days</span>
        ) : (
          <span style={badge('neutral')}>Cash</span>
        ),
      value: (v) => v.credit_days ?? 0,
    },
    {
      key: 'credit_limit',
      header: 'Credit limit',
      numeric: true,
      render: (v) =>
        (v.credit_limit ?? 0) > 0 ? formatMoney(v.credit_limit) : <span style={styles.muted}>No limit</span>,
      value: (v) => v.credit_limit ?? 0,
    },
    {
      key: 'outstanding',
      header: 'Outstanding',
      numeric: true,
      render: (v) => {
        const balance = balances[v.id];
        if (balance === undefined) return <span style={styles.muted}>…</span>;
        if (balance === null) return <span style={styles.muted} title="Balance could not be loaded">n/a</span>;
        if (!balance.outstanding) return <span style={styles.muted}>—</span>;
        return (
          <span style={{ fontWeight: 600, color: isOverLimit(v) ? color.danger : color.text }}>
            {formatMoney(balance.outstanding)}
          </span>
        );
      },
      value: (v) => outstandingOf(v) ?? null,
    },
    {
      key: 'headroom',
      header: 'Headroom',
      numeric: true,
      // What the buyer needs at the moment of raising an order: how much more
      // this vendor will take before the server refuses it.
      render: (v) => {
        const limit = v.credit_limit ?? 0;
        if (!limit) return <span style={styles.muted}>—</span>;
        const owed = outstandingOf(v);
        if (owed === undefined) return <span style={styles.muted}>…</span>;
        const left = limit - owed;
        return (
          <span style={{ fontWeight: 600, color: left < 0 ? color.danger : left === 0 ? color.warning : color.success }}>
            {formatMoney(left)}
          </span>
        );
      },
      value: (v) => {
        const limit = v.credit_limit ?? 0;
        const owed = outstandingOf(v);
        return limit && owed !== undefined ? limit - owed : null;
      },
    },
    {
      key: 'actions',
      header: '',
      sortable: false,
      width: '5rem',
      render: (v) => (
        <button
          style={{ ...ui.button, padding: '0.25rem 0.625rem', fontSize: font.size.sm }}
          onClick={(e) => {
            e.stopPropagation();
            openEdit(v);
          }}
        >
          Edit
        </button>
      ),
    },
  ];

  if (!user || user.role === 'cashier') return null;

  const editingBalance = editing ? balances[editing.id] : undefined;

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>Vendors</h1>
      </div>

      <div style={styles.summary}>
        <Stat
          label="Vendors"
          value={String(vendors.length)}
          // The list arrives capped. Saying "100 vendors" on a book of 140, with
          // a search box that only sees the hundred, is the same lie as an empty
          // state that hides a filter.
          note={vendors.length >= LIST_CAP ? `first ${LIST_CAP} — search covers these` : undefined}
        />
        <Stat
          label="Credit extended"
          value={formatMoney(totals.extended)}
          note={`${totals.onCredit} of ${vendors.length} on credit terms`}
        />
        <Stat
          label="Over their limit"
          value={balancesPending && !totals.over ? '…' : String(totals.over)}
          tone={totals.over ? 'danger' : undefined}
        />
        <Stat
          label="Owed to vendors"
          value={formatMoney(totals.owed)}
          note={
            balancesPending
              ? 'still loading balances'
              : totals.known < vendors.length
                ? `${totals.known} of ${vendors.length} balances loaded`
                : undefined
          }
        />
      </div>

      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(v) => v.id}
        loading={loading}
        onRowClick={openEdit}
        searchPlaceholder="Search name, phone, GSTIN or state…"
        // The filter narrows the rows before the table sees them, so the table
        // cannot tell "no vendors" from "none match this filter" — and the
        // second dressed up as the first sends someone off to add a duplicate.
        empty={emptyMessage()}
        rowTone={(v) => (isOverLimit(v) ? 'danger' : undefined)}
        toolbar={
          <>
            <Select
              value={creditFilter}
              onChange={(e) => setCreditFilter(e.target.value as typeof creditFilter)}
              style={{ width: 'auto' }}
              options={[
                { value: 'all', label: 'All vendors' },
                { value: 'owing', label: 'Owed money' },
                { value: 'over', label: 'Over their limit' },
              ]}
            />
            <button style={ui.buttonPrimary} onClick={openNew}>
              + New vendor
            </button>
          </>
        }
      />

      <Modal
        open={creating || !!editing}
        title={editing ? editing.name : 'New vendor'}
        subtitle={
          editing
            ? 'Changes apply to future orders; purchase orders already raised keep the details they were raised with.'
            : undefined
        }
        onClose={close}
        footer={
          <>
            {editing && (
              <button
                style={{ ...ui.buttonDanger, marginRight: 'auto' }}
                onClick={() => (confirmOff ? deactivate() : setConfirmOff(true))}
                disabled={saving}
              >
                {confirmOff ? 'Confirm — deactivate' : 'Deactivate'}
              </button>
            )}
            <button style={ui.button} onClick={close} disabled={saving}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={save} disabled={saving}>
              {saving ? 'Saving…' : editing ? 'Save changes' : 'Add vendor'}
            </button>
          </>
        }
      >
        <FormError error={saveError} />

        {confirmOff && (
          <div style={styles.warn}>
            Deactivating takes this vendor off the list for new orders. Purchase orders and payments already
            recorded against them are untouched, and the listing has no way to bring a deactivated vendor back.
          </div>
        )}

        <FormGrid>
          <Field label="Name" required htmlFor="v-name">
            <TextInput
              id="v-name"
              value={form.name}
              autoFocus
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </Field>
          <Field label="Phone" htmlFor="v-phone">
            <TextInput
              id="v-phone"
              value={form.phone}
              inputMode="tel"
              onChange={(e) => setForm({ ...form, phone: e.target.value })}
            />
          </Field>
          <Field label="GSTIN" hint="Decides IGST versus CGST+SGST on a purchase." htmlFor="v-gstin">
            <TextInput
              id="v-gstin"
              value={form.gstin}
              onChange={(e) => setForm({ ...form, gstin: e.target.value.toUpperCase() })}
            />
          </Field>
          <Field label="State" htmlFor="v-state">
            <TextInput id="v-state" value={form.state} onChange={(e) => setForm({ ...form, state: e.target.value })} />
          </Field>
        </FormGrid>

        <Field label="Address" htmlFor="v-address">
          <TextInput
            id="v-address"
            value={form.address}
            onChange={(e) => setForm({ ...form, address: e.target.value })}
          />
        </Field>

        <FormGrid>
          <Field label="Credit days" hint="Zero means payment on delivery." htmlFor="v-days">
            <NumberInput
              id="v-days"
              value={form.credit_days}
              min={0}
              step={1}
              onChange={(e) => setForm({ ...form, credit_days: parseInt(e.target.value, 10) || 0 })}
            />
          </Field>
          <Field
            label="Credit limit"
            hint="Zero means no limit. Above it, the server refuses a new purchase order."
            htmlFor="v-limit"
          >
            <NumberInput
              id="v-limit"
              value={form.credit_limit}
              min={0}
              onChange={(e) => setForm({ ...form, credit_limit: parseFloat(e.target.value) || 0 })}
            />
          </Field>
        </FormGrid>

        {editing && (
          <div style={styles.ledger}>
            <div>
              <div style={styles.ledgerLabel}>Ordered</div>
              <div style={styles.ledgerValue}>
                {editingBalance ? formatMoney(editingBalance.ordered) : editingBalance === null ? 'n/a' : '…'}
              </div>
            </div>
            <div>
              <div style={styles.ledgerLabel}>Paid</div>
              <div style={styles.ledgerValue}>
                {editingBalance ? formatMoney(editingBalance.paid) : editingBalance === null ? 'n/a' : '…'}
              </div>
            </div>
            <div>
              <div style={styles.ledgerLabel}>Outstanding</div>
              <div
                style={{
                  ...styles.ledgerValue,
                  color: editingBalance && editingBalance.outstanding > 0 ? color.danger : color.text,
                }}
              >
                {editingBalance ? formatMoney(editingBalance.outstanding) : editingBalance === null ? 'n/a' : '…'}
              </div>
            </div>
            <button style={{ ...ui.button, marginLeft: 'auto' }} onClick={() => navigate('/vendor-payments')}>
              Vendor payments →
            </button>
          </div>
        )}
      </Modal>
    </div>
  );
}

function Stat({
  label,
  value,
  tone,
  note,
}: {
  label: string;
  value: string;
  tone?: 'warning' | 'danger';
  note?: string;
}) {
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
  warn: {
    padding: space.md,
    background: color.warningSoft,
    border: `1px solid ${color.warning}33`,
    color: color.warning,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
  ledger: {
    display: 'flex',
    gap: space.xl,
    alignItems: 'center',
    padding: space.lg,
    background: color.surfaceAlt,
    borderRadius: '10px',
    border: `1px solid ${color.border}`,
    flexWrap: 'wrap',
  },
  ledgerLabel: {
    fontSize: font.size.xs,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    color: color.textMuted,
    fontWeight: 700,
  },
  ledgerValue: { marginTop: space.xs, fontSize: font.size.lg, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
};
