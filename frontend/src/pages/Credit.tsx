import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { creditApi, customerApi } from '../services/api';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, NumberInput, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatDate, formatMoney } from '../utils/format';

interface CreditEntry {
  id: string;
  customer_id: string;
  customer_name: string;
  invoice_id: string;
  amount: number;
  paid_amount: number;
  type: string;
  due_date: string;
  status: string;
  created_at: string;
}

interface Customer {
  id: string;
  name: string;
  credit_limit: number;
  outstanding: number;
}

const todayISO = () => new Date().toISOString().slice(0, 10);

/**
 * Credit management.
 *
 * The screen this replaces listed ledger rows and offered a payment box. What
 * it could not answer is the question the shop actually opens it for: who is
 * overdue, and by how long. So it is organised around the customer rather than
 * around the entry — one row per person who owes money, with the ledger a click
 * away — and the overdue rows carry their age.
 *
 * Balance is `amount - paid_amount` throughout. That matters: an earlier
 * implementation recorded a payment as a *new row* with amount = the payment
 * and paid_amount = 0, so settling a bill INCREASED what the customer owed.
 * Reading the balance one way, in one place, is what stops that returning.
 */
export default function Credit() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [entries, setEntries] = useState<CreditEntry[]>([]);
  const [customers, setCustomers] = useState<Customer[]>([]);
  const [loading, setLoading] = useState(false);
  const [view, setView] = useState<'customers' | 'entries'>('customers');
  const [statusFilter, setStatusFilter] = useState<'open' | 'overdue' | 'all'>('open');

  const [payingFor, setPayingFor] = useState<{ id: string; name: string; due: number } | null>(null);
  const [payment, setPayment] = useState({ amount: '', method: 'cash', notes: '' });
  const [payError, setPayError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [ledgerFor, setLedgerFor] = useState<{ id: string; name: string } | null>(null);

  const load = useCallback(async () => {
    if (!store) return;
    setLoading(true);
    try {
      const [entryRes, custRes] = await Promise.all([
        creditApi.list({ store_id: store.id }),
        customerApi.list(store.id),
      ]);
      setEntries(entryRes.data as CreditEntry[]);
      setCustomers(custRes.data as Customer[]);
    } catch (err) {
      console.error('Failed to load credit:', err);
    } finally {
      setLoading(false);
    }
  }, [store]);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    load();
  }, [user, store, navigate, load]);

  const balanceOf = (e: CreditEntry) => (e.amount || 0) - (e.paid_amount || 0);

  /** Days past due, negative when still in date. */
  const daysOverdue = useCallback((due: string) => {
    if (!due) return 0;
    const d = due.slice(0, 10);
    const today = todayISO();
    if (d >= today) return 0;
    return Math.round((Date.parse(today) - Date.parse(d)) / 86_400_000);
  }, []);

  /** Credit limits, so a row can say whether this debt is within what was agreed. */
  const limitOf = useMemo(() => {
    const m = new Map<string, number>();
    for (const c of customers) m.set(c.id, c.credit_limit || 0);
    return m;
  }, [customers]);

  /** One row per customer who owes something, which is how the shop thinks. */
  const owing = useMemo(() => {
    const byCustomer = new Map<
      string,
      {
        id: string;
        name: string;
        due: number;
        oldestDays: number;
        openCount: number;
        nextDue: string;
        limit: number;
      }
    >();
    for (const e of entries) {
      const bal = balanceOf(e);
      if (bal <= 0) continue;
      const row = byCustomer.get(e.customer_id) ?? {
        id: e.customer_id,
        name: e.customer_name,
        due: 0,
        oldestDays: 0,
        openCount: 0,
        nextDue: e.due_date,
        limit: limitOf.get(e.customer_id) ?? 0,
      };
      row.due += bal;
      row.openCount += 1;
      row.oldestDays = Math.max(row.oldestDays, daysOverdue(e.due_date));
      if (!row.nextDue || (e.due_date && e.due_date < row.nextDue)) row.nextDue = e.due_date;
      byCustomer.set(e.customer_id, row);
    }
    const rows = [...byCustomer.values()];
    if (statusFilter === 'overdue') return rows.filter((r) => r.oldestDays > 0);
    return rows;
  }, [entries, statusFilter, daysOverdue, limitOf]);

  const visibleEntries = useMemo(() => {
    let rows = entries;
    if (ledgerFor) rows = rows.filter((e) => e.customer_id === ledgerFor.id);
    if (statusFilter === 'open') rows = rows.filter((e) => balanceOf(e) > 0);
    if (statusFilter === 'overdue') rows = rows.filter((e) => balanceOf(e) > 0 && daysOverdue(e.due_date) > 0);
    return rows;
  }, [entries, statusFilter, ledgerFor, daysOverdue]);

  const totals = useMemo(() => {
    const open = entries.filter((e) => balanceOf(e) > 0);
    return {
      outstanding: open.reduce((s, e) => s + balanceOf(e), 0),
      overdue: open.filter((e) => daysOverdue(e.due_date) > 0).reduce((s, e) => s + balanceOf(e), 0),
      customers: new Set(open.map((e) => e.customer_id)).size,
    };
  }, [entries, daysOverdue]);

  const openPayment = (customerId: string, name: string, due: number) => {
    setPayingFor({ id: customerId, name, due });
    setPayment({ amount: String(due.toFixed(2)), method: 'cash', notes: '' });
    setPayError(null);
  };

  const takePayment = async () => {
    if (!payingFor) return;
    const amount = Number(payment.amount);
    if (!Number.isFinite(amount) || amount <= 0) {
      setPayError('Enter the amount collected.');
      return;
    }
    // Compared in whole paise, not with a tolerance: 0.1 + 0.2 is not 0.3 in
    // binary floating point, and a tolerance would quietly accept a rupee more
    // than the customer owes.
    if (Math.round(amount * 100) > Math.round(payingFor.due * 100)) {
      setPayError(`That is more than the ${formatMoney(payingFor.due)} outstanding.`);
      return;
    }
    setSaving(true);
    setPayError(null);
    try {
      await creditApi.payment({
        customer_id: payingFor.id,
        amount,
        method: payment.method,
        notes: payment.notes,
      });
      setPayingFor(null);
      await load();
    } catch (err: any) {
      setPayError(err?.response?.data?.error || 'Could not record this payment.');
    } finally {
      setSaving(false);
    }
  };

  const agingTone = (days: number) => (days > 60 ? 'danger' : days > 30 ? 'warning' : days > 0 ? 'warning' : 'success');

  const customerColumns: Column<(typeof owing)[number]>[] = [
    { key: 'name', header: 'Customer', render: (r) => <span style={{ fontWeight: 600 }}>{r.name}</span>, value: (r) => r.name },
    { key: 'openCount', header: 'Open bills', numeric: true, value: (r) => r.openCount },
    { key: 'nextDue', header: 'Earliest due', render: (r) => formatDate(r.nextDue), value: (r) => r.nextDue },
    {
      key: 'oldestDays',
      header: 'Age',
      render: (r) =>
        r.oldestDays > 0 ? (
          <span style={badge(agingTone(r.oldestDays))}>{r.oldestDays} days overdue</span>
        ) : (
          <span style={badge('success')}>In date</span>
        ),
      value: (r) => r.oldestDays,
    },
    {
      key: 'due',
      header: 'Outstanding',
      numeric: true,
      render: (r) => <span style={{ fontWeight: 700 }}>{formatMoney(r.due)}</span>,
      value: (r) => r.due,
    },
    {
      // Whether this debt is inside what the shop agreed to. A customer can be
      // in date and still over their limit, which is the case the counter needs
      // warning about before it puts another bill on the account.
      key: 'limit',
      header: 'Against limit',
      numeric: true,
      render: (r) => {
        if (!r.limit) return <span style={styles.mutedCell}>No limit set</span>;
        const over = r.due > r.limit;
        return (
          <span style={{ color: over ? color.danger : color.textMuted, fontWeight: over ? 700 : 400 }}>
            {over ? `${formatMoney(r.due - r.limit)} over` : `of ${formatMoney(r.limit)}`}
          </span>
        );
      },
      value: (r) => (r.limit ? r.due - r.limit : null),
    },
    {
      key: 'actions',
      header: '',
      sortable: false,
      width: '11rem',
      render: (r) => (
        <div style={{ display: 'flex', gap: space.xs, justifyContent: 'flex-end' }}>
          <button
            style={{ ...ui.button, padding: '0.25rem 0.5rem', fontSize: font.size.sm }}
            onClick={(e) => {
              e.stopPropagation();
              setLedgerFor({ id: r.id, name: r.name });
              setView('entries');
            }}
          >
            Ledger
          </button>
          <button
            style={{ ...ui.buttonPrimary, padding: '0.25rem 0.625rem', fontSize: font.size.sm }}
            onClick={(e) => {
              e.stopPropagation();
              openPayment(r.id, r.name, r.due);
            }}
          >
            Collect
          </button>
        </div>
      ),
    },
  ];

  const entryColumns: Column<CreditEntry>[] = [
    { key: 'created_at', header: 'Raised', render: (e) => formatDate(e.created_at), value: (e) => e.created_at },
    { key: 'customer_name', header: 'Customer', value: (e) => e.customer_name },
    {
      key: 'type',
      header: 'Type',
      render: (e) => <span style={badge(e.type === 'issue' ? 'neutral' : 'info')}>{e.type}</span>,
      value: (e) => e.type,
    },
    { key: 'amount', header: 'Amount', numeric: true, render: (e) => formatMoney(e.amount), value: (e) => e.amount },
    { key: 'paid_amount', header: 'Paid', numeric: true, render: (e) => formatMoney(e.paid_amount), value: (e) => e.paid_amount },
    {
      key: 'balance',
      header: 'Balance',
      numeric: true,
      render: (e) => {
        const bal = balanceOf(e);
        return <span style={{ fontWeight: 700, color: bal > 0 ? color.text : color.textFaint }}>{formatMoney(bal)}</span>;
      },
      value: (e) => balanceOf(e),
    },
    {
      key: 'due_date',
      header: 'Due',
      render: (e) => {
        const days = daysOverdue(e.due_date);
        return (
          <span style={{ color: days > 0 && balanceOf(e) > 0 ? color.danger : color.text }}>
            {formatDate(e.due_date)}
            {days > 0 && balanceOf(e) > 0 && <span style={styles.sub}> · {days}d late</span>}
          </span>
        );
      },
      value: (e) => e.due_date,
    },
    {
      key: 'status',
      header: 'Status',
      render: (e) => {
        const bal = balanceOf(e);
        const tone = bal <= 0 ? 'success' : daysOverdue(e.due_date) > 0 ? 'danger' : 'warning';
        return <span style={badge(tone)}>{bal <= 0 ? 'settled' : e.status}</span>;
      },
      value: (e) => e.status,
    },
  ];

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>Credit</h1>
      </div>

      <div style={styles.summary}>
        <Stat label="Outstanding" value={formatMoney(totals.outstanding)} />
        <Stat label="Of which overdue" value={formatMoney(totals.overdue)} tone={totals.overdue > 0 ? 'danger' : undefined} />
        <Stat label="Customers owing" value={String(totals.customers)} />
        <Stat label="Aging report" value="Open →" onClick={() => navigate('/credit-aging')} />
      </div>

      {ledgerFor && (
        <div style={styles.crumb}>
          Showing the ledger for <strong>{ledgerFor.name}</strong>
          <button
            style={{ ...ui.button, marginLeft: space.md, padding: '0.125rem 0.5rem', fontSize: font.size.sm }}
            onClick={() => setLedgerFor(null)}
          >
            Show everyone
          </button>
        </div>
      )}

      {view === 'customers' && !ledgerFor ? (
        <DataTable
          rows={owing}
          columns={customerColumns}
          rowKey={(r) => r.id}
          loading={loading}
          searchPlaceholder="Search customer…"
          empty={statusFilter === 'overdue' ? 'Nobody is overdue.' : 'Nobody owes anything right now.'}
          rowTone={(r) =>
            r.oldestDays > 60 || (r.limit > 0 && r.due > r.limit) ? 'danger' : r.oldestDays > 0 ? 'warning' : undefined
          }
          toolbar={<Toolbar {...{ statusFilter, setStatusFilter, view, setView, setLedgerFor }} />}
        />
      ) : (
        <DataTable
          rows={visibleEntries}
          columns={entryColumns}
          rowKey={(e) => e.id}
          loading={loading}
          searchPlaceholder="Search customer…"
          empty={
            statusFilter === 'overdue'
              ? 'No overdue entries.'
              : statusFilter === 'open'
                ? 'Nothing outstanding.'
                : 'No credit entries yet.'
          }
          rowTone={(e) => (balanceOf(e) > 0 && daysOverdue(e.due_date) > 0 ? 'danger' : undefined)}
          toolbar={<Toolbar {...{ statusFilter, setStatusFilter, view, setView, setLedgerFor }} />}
        />
      )}

      <Modal
        open={!!payingFor}
        title={`Collect from ${payingFor?.name ?? ''}`}
        subtitle={payingFor ? `${formatMoney(payingFor.due)} outstanding` : undefined}
        width="34rem"
        onClose={() => setPayingFor(null)}
        footer={
          <>
            <button style={ui.button} onClick={() => setPayingFor(null)} disabled={saving}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={takePayment} disabled={saving}>
              {saving ? 'Recording…' : 'Record payment'}
            </button>
          </>
        }
      >
        <FormError error={payError} />
        <FormGrid>
          <Field label="Amount collected" required htmlFor="pay-amount">
            <NumberInput
              id="pay-amount"
              autoFocus
              min={0}
              value={payment.amount}
              onChange={(e) => setPayment({ ...payment, amount: e.target.value })}
            />
          </Field>
          <Field label="Method" htmlFor="pay-method">
            <Select
              id="pay-method"
              value={payment.method}
              onChange={(e) => setPayment({ ...payment, method: e.target.value })}
              options={[
                { value: 'cash', label: 'Cash' },
                { value: 'upi', label: 'UPI' },
                { value: 'card', label: 'Card' },
                { value: 'bank', label: 'Bank transfer' },
                { value: 'cheque', label: 'Cheque' },
              ]}
            />
          </Field>
        </FormGrid>
        <Field label="Note" hint="A cheque number, or who collected it.">
          <TextInput value={payment.notes} onChange={(e) => setPayment({ ...payment, notes: e.target.value })} />
        </Field>
        {payingFor && Number(payment.amount) > 0 && Number(payment.amount) < payingFor.due && (
          <div style={styles.remainder}>
            {formatMoney(payingFor.due - Number(payment.amount))} will remain outstanding.
          </div>
        )}
      </Modal>
    </div>
  );
}

function Toolbar({
  statusFilter,
  setStatusFilter,
  view,
  setView,
  setLedgerFor,
}: {
  statusFilter: string;
  setStatusFilter: (v: any) => void;
  view: string;
  setView: (v: any) => void;
  setLedgerFor: (v: null) => void;
}) {
  return (
    <>
      <Select
        value={statusFilter}
        onChange={(e) => setStatusFilter(e.target.value)}
        style={{ width: 'auto' }}
        options={[
          { value: 'open', label: 'Outstanding' },
          { value: 'overdue', label: 'Overdue only' },
          { value: 'all', label: 'All entries' },
        ]}
      />
      <Select
        value={view}
        onChange={(e) => {
          setView(e.target.value);
          setLedgerFor(null);
        }}
        style={{ width: 'auto' }}
        options={[
          { value: 'customers', label: 'By customer' },
          { value: 'entries', label: 'Every entry' },
        ]}
      />
    </>
  );
}

function Stat({
  label,
  value,
  tone,
  onClick,
}: {
  label: string;
  value: string;
  tone?: 'danger';
  onClick?: () => void;
}) {
  return (
    <div
      style={{ ...ui.card, padding: space.lg, flex: '1 1 10rem', cursor: onClick ? 'pointer' : 'default' }}
      onClick={onClick}
    >
      <div style={styles.statLabel}>{label}</div>
      <div style={{ ...styles.statValue, color: tone === 'danger' ? color.danger : color.text }}>{value}</div>
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
  mutedCell: { color: color.textFaint },
  crumb: {
    padding: space.md,
    marginBottom: space.md,
    background: color.primarySoft,
    border: `1px solid ${color.primary}22`,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
  remainder: {
    padding: space.md,
    background: color.warningSoft,
    border: `1px solid ${color.warning}33`,
    borderRadius: '6px',
    color: color.warning,
    fontSize: font.size.md,
  },
};
