import React, { useCallback, useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAppStore } from '../stores/appStore';
import { authApi, storeApi, terminalApi } from '../services/api';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';

/**
 * Settings, meaning the things a shop owner has to be able to change without
 * calling anyone.
 *
 * The screen this replaces had Store, Users and Language. Two capabilities were
 * missing outright rather than merely awkward:
 *
 *   - **Terminals had no interface at all.** A terminal owns an invoice series
 *     (INV/T1/2026-27/00001), so opening a second counter is a business event
 *     the software simply refused to allow. The API had supported it all along.
 *
 *   - **A user could be created but never disabled.** The deactivate and
 *     activate endpoints existed and nothing called them, so a departing
 *     cashier kept their login.
 */

const MIN_PASSWORD_LENGTH = 10;

type Tab = 'store' | 'terminals' | 'users' | 'security' | 'language';

interface StaffUser {
  id: string;
  username: string;
  role: string;
  is_active: boolean;
}

interface TerminalRow {
  id: string;
  code: string;
  name: string;
  is_active: boolean;
}

/** What each role may do, in the words a shop owner uses. */
const ROLE_NOTES: Record<string, string> = {
  owner: 'Everything, including users and settings.',
  manager: 'Everything except managing users.',
  cashier: 'Billing and customers. Cannot see cost prices or reports.',
  warehouse: 'Stock, purchases and receiving. No billing.',
  helper: 'Billing only, under supervision.',
};

export default function Settings() {
  const navigate = useNavigate();
  const { t, i18n } = useTranslation();
  const { user, store, setStore } = useAppStore();

  const [tab, setTab] = useState<Tab>('store');

  // Store ------------------------------------------------------------------
  const [allStores, setAllStores] = useState<{ id: string; name: string }[]>([]);
  const [storeForm, setStoreForm] = useState({ name: '', address: '', phone: '', gstin: '', location_type: 'store' });
  const [storeError, setStoreError] = useState<string | null>(null);
  const [storeSaved, setStoreSaved] = useState(false);

  // Terminals ---------------------------------------------------------------
  const [terminals, setTerminals] = useState<TerminalRow[]>([]);
  const [terminalsLoading, setTerminalsLoading] = useState(false);
  const [newTerminal, setNewTerminal] = useState<{ code: string; name: string } | null>(null);
  const [terminalError, setTerminalError] = useState<string | null>(null);

  // Users -------------------------------------------------------------------
  const [users, setUsers] = useState<StaffUser[]>([]);
  const [usersLoading, setUsersLoading] = useState(false);
  const [newUser, setNewUser] = useState<{ username: string; password: string; role: string } | null>(null);
  const [userError, setUserError] = useState<string | null>(null);

  // Security ----------------------------------------------------------------
  const [pw, setPw] = useState({ old: '', next: '', confirm: '' });
  const [pwError, setPwError] = useState<string | null>(null);
  const [pwDone, setPwDone] = useState(false);

  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!user || !store) {
      navigate('/login');
      return;
    }
    setStoreForm({
      name: store.name ?? '',
      address: (store as any).address ?? '',
      phone: (store as any).phone ?? '',
      gstin: (store as any).gstin ?? '',
      location_type: (store as any).location_type ?? 'store',
    });
  }, [user, store, navigate]);

  const loadStores = useCallback(async () => {
    try {
      const { data } = await storeApi.list();
      setAllStores(data as { id: string; name: string }[]);
    } catch (err) {
      console.error('Failed to load stores:', err);
    }
  }, []);

  const loadTerminals = useCallback(async () => {
    if (!store) return;
    setTerminalsLoading(true);
    try {
      const { data } = await terminalApi.list(store.id);
      setTerminals(data as TerminalRow[]);
    } catch (err) {
      console.error('Failed to load terminals:', err);
    } finally {
      setTerminalsLoading(false);
    }
  }, [store]);

  const loadUsers = useCallback(async () => {
    if (!store) return;
    setUsersLoading(true);
    try {
      const { data } = await authApi.listUsers(store.id);
      setUsers(data as StaffUser[]);
    } catch (err) {
      console.error('Failed to load users:', err);
    } finally {
      setUsersLoading(false);
    }
  }, [store]);

  useEffect(() => {
    if (tab === 'store') loadStores();
    if (tab === 'terminals') loadTerminals();
    if (tab === 'users') loadUsers();
  }, [tab, loadStores, loadTerminals, loadUsers]);

  const saveStore = async () => {
    if (!store) return;
    setBusy(true);
    setStoreError(null);
    setStoreSaved(false);
    try {
      await storeApi.update(store.id, storeForm as any);
      const { data } = await storeApi.get(store.id);
      setStore(data);
      setStoreSaved(true);
    } catch (err: any) {
      setStoreError(err?.response?.data?.error || 'Could not save the store details.');
    } finally {
      setBusy(false);
    }
  };

  const createTerminal = async () => {
    if (!store || !newTerminal) return;
    if (!newTerminal.code.trim() || !newTerminal.name.trim()) {
      setTerminalError('A counter needs both a code and a name.');
      return;
    }
    setBusy(true);
    setTerminalError(null);
    try {
      await terminalApi.create(store.id, {
        code: newTerminal.code.trim().toUpperCase(),
        name: newTerminal.name.trim(),
      });
      setNewTerminal(null);
      await loadTerminals();
    } catch (err: any) {
      setTerminalError(err?.response?.data?.error || 'Could not add this counter.');
    } finally {
      setBusy(false);
    }
  };

  const retireTerminal = async (row: TerminalRow) => {
    if (!store) return;
    // A terminal's code is printed on every bill it raised, so retiring one is
    // not reversible from this screen — worth a confirmation.
    if (!window.confirm(`Retire ${row.code} — ${row.name}? Bills it already raised keep their numbers.`)) return;
    try {
      await terminalApi.deactivate(store.id, row.id);
      await loadTerminals();
    } catch (err: any) {
      window.alert(err?.response?.data?.error || 'Could not retire this counter.');
    }
  };

  const createUser = async () => {
    if (!store || !newUser) return;
    if (!newUser.username.trim()) {
      setUserError('A username is required.');
      return;
    }
    if (newUser.password.length < MIN_PASSWORD_LENGTH) {
      setUserError(`The password must be at least ${MIN_PASSWORD_LENGTH} characters.`);
      return;
    }
    setBusy(true);
    setUserError(null);
    try {
      await authApi.register(newUser.username.trim(), newUser.password, newUser.role, store.id);
      setNewUser(null);
      await loadUsers();
    } catch (err: any) {
      setUserError(err?.response?.data?.error || 'Could not create this user.');
    } finally {
      setBusy(false);
    }
  };

  const toggleUser = async (row: StaffUser) => {
    try {
      if (row.is_active) {
        await authApi.deactivateUser(row.id);
      } else {
        await authApi.activateUser(row.id);
      }
      await loadUsers();
    } catch (err: any) {
      window.alert(err?.response?.data?.error || 'Could not change this user.');
    }
  };

  const changePassword = async () => {
    setPwError(null);
    setPwDone(false);
    if (pw.next.length < MIN_PASSWORD_LENGTH) {
      setPwError(`The new password must be at least ${MIN_PASSWORD_LENGTH} characters.`);
      return;
    }
    if (pw.next !== pw.confirm) {
      setPwError('The two new passwords do not match.');
      return;
    }
    setBusy(true);
    try {
      await authApi.changePassword(pw.old, pw.next);
      setPw({ old: '', next: '', confirm: '' });
      setPwDone(true);
    } catch (err: any) {
      setPwError(err?.response?.data?.error || 'Could not change the password.');
    } finally {
      setBusy(false);
    }
  };

  const terminalColumns: Column<TerminalRow>[] = [
    { key: 'code', header: 'Code', render: (r) => <span style={{ fontFamily: font.mono, fontWeight: 700 }}>{r.code}</span>, value: (r) => r.code },
    { key: 'name', header: 'Counter', value: (r) => r.name },
    {
      key: 'series',
      header: 'Invoice series',
      sortable: false,
      render: (r) => <span style={{ fontFamily: font.mono, color: color.textMuted }}>INV/{r.code}/…</span>,
    },
    {
      key: 'is_active',
      header: 'Status',
      render: (r) => <span style={badge(r.is_active ? 'success' : 'neutral')}>{r.is_active ? 'In use' : 'Retired'}</span>,
      value: (r) => (r.is_active ? 'In use' : 'Retired'),
    },
    {
      key: 'actions',
      header: '',
      sortable: false,
      width: '7rem',
      render: (r) =>
        r.is_active ? (
          <button style={{ ...ui.buttonDanger, padding: '0.25rem 0.625rem', fontSize: font.size.sm }} onClick={() => retireTerminal(r)}>
            Retire
          </button>
        ) : null,
    },
  ];

  const userColumns: Column<StaffUser>[] = [
    { key: 'username', header: 'Username', render: (r) => <span style={{ fontWeight: 600 }}>{r.username}</span>, value: (r) => r.username },
    {
      key: 'role',
      header: 'Role',
      render: (r) => (
        <div>
          <span style={badge(r.role === 'owner' ? 'info' : 'neutral')}>{r.role}</span>
          <div style={styles.roleNote}>{ROLE_NOTES[r.role] ?? ''}</div>
        </div>
      ),
      value: (r) => r.role,
    },
    {
      key: 'is_active',
      header: 'Status',
      render: (r) => <span style={badge(r.is_active ? 'success' : 'danger')}>{r.is_active ? 'Active' : 'Disabled'}</span>,
      value: (r) => (r.is_active ? 'Active' : 'Disabled'),
    },
    {
      key: 'actions',
      header: '',
      sortable: false,
      width: '8rem',
      render: (r) =>
        // Disabling yourself locks you out of the screen you are standing on.
        r.id === user?.id ? (
          <span style={styles.roleNote}>That’s you</span>
        ) : (
          <button
            style={{
              ...(r.is_active ? ui.buttonDanger : ui.button),
              padding: '0.25rem 0.625rem',
              fontSize: font.size.sm,
            }}
            onClick={() => toggleUser(r)}
          >
            {r.is_active ? 'Disable' : 'Enable'}
          </button>
        ),
    },
  ];

  const TABS: { id: Tab; label: string }[] = [
    { id: 'store', label: 'Store' },
    { id: 'terminals', label: 'Counters' },
    { id: 'users', label: 'Users' },
    { id: 'security', label: 'Security' },
    { id: 'language', label: 'Language' },
  ];

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>Settings</h1>
      </div>

      <div style={styles.tabs}>
        {TABS.map((x) => (
          <button
            key={x.id}
            style={tab === x.id ? styles.tabActive : styles.tab}
            onClick={() => setTab(x.id)}
          >
            {x.label}
          </button>
        ))}
      </div>

      {tab === 'store' && (
        <div style={{ ...ui.card, padding: space.xl, display: 'grid', gap: space.lg, maxWidth: '48rem' }}>
          <FormError error={storeError} />
          {storeSaved && <div style={styles.ok}>Saved.</div>}

          <FormGrid>
            <Field label="Store name" required htmlFor="s-name">
              <TextInput id="s-name" value={storeForm.name} onChange={(e) => setStoreForm({ ...storeForm, name: e.target.value })} />
            </Field>
            <Field label="Phone" htmlFor="s-phone">
              <TextInput id="s-phone" value={storeForm.phone} onChange={(e) => setStoreForm({ ...storeForm, phone: e.target.value })} />
            </Field>
            <Field label="GSTIN" hint="Printed on every tax invoice this store raises." htmlFor="s-gstin">
              <TextInput
                id="s-gstin"
                value={storeForm.gstin}
                onChange={(e) => setStoreForm({ ...storeForm, gstin: e.target.value.toUpperCase() })}
              />
            </Field>
            <Field label="Type" hint="A warehouse holds stock but does not bill." htmlFor="s-type">
              <Select
                id="s-type"
                value={storeForm.location_type}
                onChange={(e) => setStoreForm({ ...storeForm, location_type: e.target.value })}
                options={[
                  { value: 'store', label: 'Store' },
                  { value: 'warehouse', label: 'Warehouse' },
                ]}
              />
            </Field>
          </FormGrid>

          <Field label="Address" htmlFor="s-addr">
            <TextInput id="s-addr" value={storeForm.address} onChange={(e) => setStoreForm({ ...storeForm, address: e.target.value })} />
          </Field>

          <div style={{ display: 'flex', gap: space.sm }}>
            <button style={ui.buttonPrimary} onClick={saveStore} disabled={busy}>
              {busy ? 'Saving…' : 'Save store details'}
            </button>
          </div>

          {allStores.length > 1 && (
            <div style={styles.section}>
              <div style={ui.label}>Working in</div>
              <Select
                value={store?.id ?? ''}
                onChange={async (e) => {
                  const { data } = await storeApi.get(e.target.value);
                  setStore(data);
                }}
                options={allStores.map((s) => ({ value: s.id, label: s.name }))}
                style={{ maxWidth: '20rem' }}
              />
              <div style={styles.hint}>Everything else in the app applies to the store selected here.</div>
            </div>
          )}
        </div>
      )}

      {tab === 'terminals' && (
        <>
          <p style={styles.lead}>
            Each counter issues its own run of invoice numbers, so two tills can bill at once without ever colliding —
            and each run stays unbroken, which is what the GST rules require.
          </p>
          <DataTable
            rows={terminals}
            columns={terminalColumns}
            rowKey={(r) => r.id}
            loading={terminalsLoading}
            searchable={false}
            empty="No counters yet."
            toolbar={
              <button style={ui.buttonPrimary} onClick={() => { setNewTerminal({ code: '', name: '' }); setTerminalError(null); }}>
                + Add counter
              </button>
            }
          />
        </>
      )}

      {tab === 'users' && (
        <DataTable
          rows={users}
          columns={userColumns}
          rowKey={(r) => r.id}
          loading={usersLoading}
          searchPlaceholder="Search username or role…"
          empty="No staff accounts yet."
          rowTone={(r) => (r.is_active ? undefined : 'warning')}
          toolbar={
            <button style={ui.buttonPrimary} onClick={() => { setNewUser({ username: '', password: '', role: 'cashier' }); setUserError(null); }}>
              + Add user
            </button>
          }
        />
      )}

      {tab === 'security' && (
        <div style={{ ...ui.card, padding: space.xl, display: 'grid', gap: space.lg, maxWidth: '32rem' }}>
          <FormError error={pwError} />
          {pwDone && <div style={styles.ok}>Password changed.</div>}
          <Field label="Current password" required htmlFor="pw-old">
            <TextInput id="pw-old" type="password" value={pw.old} onChange={(e) => setPw({ ...pw, old: e.target.value })} />
          </Field>
          <Field label="New password" required hint={`At least ${MIN_PASSWORD_LENGTH} characters.`} htmlFor="pw-new">
            <TextInput id="pw-new" type="password" value={pw.next} onChange={(e) => setPw({ ...pw, next: e.target.value })} />
          </Field>
          <Field label="Confirm new password" required htmlFor="pw-confirm">
            <TextInput id="pw-confirm" type="password" value={pw.confirm} onChange={(e) => setPw({ ...pw, confirm: e.target.value })} />
          </Field>
          <div>
            <button style={ui.buttonPrimary} onClick={changePassword} disabled={busy}>
              {busy ? 'Changing…' : 'Change password'}
            </button>
          </div>
          <div style={styles.hint}>
            Changing your password signs out every other device using this account.
          </div>
        </div>
      )}

      {tab === 'language' && (
        <div style={{ ...ui.card, padding: space.xl, display: 'grid', gap: space.lg, maxWidth: '32rem' }}>
          <Field label="Language" hint="Applies to this device only.">
            <Select
              value={i18n.language}
              onChange={(e) => i18n.changeLanguage(e.target.value)}
              options={[
                { value: 'en', label: 'English' },
                { value: 'hi', label: 'हिन्दी' },
                { value: 'kn', label: 'ಕನ್ನಡ' },
              ]}
            />
          </Field>
          <div style={styles.hint}>{t('common.appName', 'GBOS')}</div>
        </div>
      )}

      <Modal
        open={!!newTerminal}
        title="Add a counter"
        subtitle="The code becomes part of every invoice number this counter raises, so keep it short."
        width="32rem"
        onClose={() => setNewTerminal(null)}
        footer={
          <>
            <button style={ui.button} onClick={() => setNewTerminal(null)} disabled={busy}>Cancel</button>
            <button style={ui.buttonPrimary} onClick={createTerminal} disabled={busy}>
              {busy ? 'Adding…' : 'Add counter'}
            </button>
          </>
        }
      >
        <FormError error={terminalError} />
        <FormGrid>
          <Field label="Code" required hint="T1, T2, FRONT — letters and digits." htmlFor="t-code">
            <TextInput
              id="t-code"
              autoFocus
              value={newTerminal?.code ?? ''}
              onChange={(e) => setNewTerminal({ ...(newTerminal ?? { code: '', name: '' }), code: e.target.value.toUpperCase() })}
            />
          </Field>
          <Field label="Name" required hint="What staff call it." htmlFor="t-name">
            <TextInput
              id="t-name"
              value={newTerminal?.name ?? ''}
              onChange={(e) => setNewTerminal({ ...(newTerminal ?? { code: '', name: '' }), name: e.target.value })}
            />
          </Field>
        </FormGrid>
        {newTerminal?.code && (
          <div style={styles.preview}>
            Bills from this counter will be numbered{' '}
            <strong style={{ fontFamily: font.mono }}>INV/{newTerminal.code}/2026-27/00001</strong>
          </div>
        )}
      </Modal>

      <Modal
        open={!!newUser}
        title="Add a user"
        width="34rem"
        onClose={() => setNewUser(null)}
        footer={
          <>
            <button style={ui.button} onClick={() => setNewUser(null)} disabled={busy}>Cancel</button>
            <button style={ui.buttonPrimary} onClick={createUser} disabled={busy}>
              {busy ? 'Creating…' : 'Create user'}
            </button>
          </>
        }
      >
        <FormError error={userError} />
        <FormGrid>
          <Field label="Username" required htmlFor="u-name">
            <TextInput
              id="u-name"
              autoFocus
              value={newUser?.username ?? ''}
              onChange={(e) => setNewUser({ ...(newUser ?? { username: '', password: '', role: 'cashier' }), username: e.target.value })}
            />
          </Field>
          <Field label="Password" required hint={`At least ${MIN_PASSWORD_LENGTH} characters.`} htmlFor="u-pw">
            <TextInput
              id="u-pw"
              type="password"
              value={newUser?.password ?? ''}
              onChange={(e) => setNewUser({ ...(newUser ?? { username: '', password: '', role: 'cashier' }), password: e.target.value })}
            />
          </Field>
        </FormGrid>
        <Field label="Role" hint={ROLE_NOTES[newUser?.role ?? 'cashier']}>
          <Select
            value={newUser?.role ?? 'cashier'}
            onChange={(e) => setNewUser({ ...(newUser ?? { username: '', password: '', role: 'cashier' }), role: e.target.value })}
            options={[
              { value: 'manager', label: 'Manager' },
              { value: 'cashier', label: 'Cashier' },
              { value: 'warehouse', label: 'Warehouse' },
              { value: 'helper', label: 'Helper' },
            ]}
          />
        </Field>
      </Modal>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  header: { display: 'flex', alignItems: 'center', gap: space.lg, marginBottom: space.lg },
  h1: { margin: 0, fontSize: font.size.xxl },
  tabs: { display: 'flex', gap: space.xs, marginBottom: space.lg, flexWrap: 'wrap' },
  tab: { ...ui.button, borderRadius: '999px' },
  tabActive: { ...ui.buttonPrimary, borderRadius: '999px' },
  lead: { color: color.textMuted, fontSize: font.size.md, marginTop: 0, marginBottom: space.md, maxWidth: '46rem' },
  section: { paddingTop: space.lg, borderTop: `1px solid ${color.border}` },
  hint: { fontSize: font.size.sm, color: color.textFaint, marginTop: space.xs },
  roleNote: { fontSize: font.size.xs, color: color.textFaint, marginTop: 2 },
  ok: {
    padding: space.md,
    background: color.successSoft,
    border: `1px solid ${color.success}33`,
    borderRadius: '6px',
    color: color.success,
    fontSize: font.size.md,
  },
  preview: {
    padding: space.md,
    background: color.primarySoft,
    border: `1px solid ${color.primary}22`,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
};
