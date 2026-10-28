import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { unitFamilyApi, uomApi } from '../services/api';
import { UnitFamily, UnitOfMeasure } from '../types';
import { AppLayout } from '../components/Layout/AppLayout';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, NumberInput, Select, TextInput } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';

interface FamilyDraft {
  code: string;
  name: string;
}

interface UnitDraft {
  code: string;
  name: string;
  family_id: string;
  factor_to_base: string;
  decimal_places: string;
}

const BLANK_FAMILY: FamilyDraft = { code: '', name: '' };
const BLANK_UNIT: UnitDraft = { code: '', name: '', family_id: '', factor_to_base: '1', decimal_places: '3' };

/**
 * Units of measure, and the families they convert within.
 *
 * These have been rows rather than an enum since the units migration, precisely
 * so that a trade the shop takes up next year needs an INSERT and not a deploy.
 * There was no way to do that INSERT: the API has been complete from the start
 * and nothing in the app called it, so the "data not enum" decision bought
 * nothing a hardcoded list would not have.
 *
 * A family is what refuses nonsense. Units convert only within one, so rice
 * held in kilograms can never be sold in litres by arithmetic accident — the
 * conversion does not exist to be got wrong. Every member states its size in
 * the family's canonical unit, which is what makes n factors do the work of n
 * squared and is why the canonical unit cannot be retired while anything still
 * converts through it.
 */
export default function Units() {
  const navigate = useNavigate();
  const { user } = useAppStore();

  const [families, setFamilies] = useState<UnitFamily[]>([]);
  const [units, setUnits] = useState<UnitOfMeasure[]>([]);
  const [loading, setLoading] = useState(false);
  const [showRetired, setShowRetired] = useState(false);
  const [familyFilter, setFamilyFilter] = useState('');

  const [editingFamily, setEditingFamily] = useState<UnitFamily | null>(null);
  const [creatingFamily, setCreatingFamily] = useState(false);
  const [familyForm, setFamilyForm] = useState<FamilyDraft>(BLANK_FAMILY);

  const [editingUnit, setEditingUnit] = useState<UnitOfMeasure | null>(null);
  const [creatingUnit, setCreatingUnit] = useState(false);
  const [unitForm, setUnitForm] = useState<UnitDraft>(BLANK_UNIT);

  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmOff, setConfirmOff] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [f, u] = await Promise.all([
        unitFamilyApi.list(showRetired),
        uomApi.list(undefined, showRetired),
      ]);
      setFamilies(f.data || []);
      setUnits(u.data || []);
    } catch (err) {
      console.error('Failed to load units:', err);
    } finally {
      setLoading(false);
    }
  }, [showRetired]);

  useEffect(() => {
    if (!user || user.role === 'cashier') {
      navigate('/billing');
      return;
    }
    load();
  }, [user, navigate, load]);

  const familyName = useCallback(
    (id: string) => families.find((f) => f.id === id)?.name ?? '—',
    [families]
  );
  const familyOf = useCallback((id: string) => families.find((f) => f.id === id), [families]);

  const rows = useMemo(
    () => (familyFilter ? units.filter((u) => u.family_id === familyFilter) : units),
    [units, familyFilter]
  );

  const run = async (what: string, fn: () => Promise<unknown>, after?: () => void) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      after?.();
      await load();
    } catch (err: any) {
      setError(err?.response?.data?.error || `Could not ${what}.`);
    } finally {
      setBusy(false);
    }
  };

  const closeAll = () => {
    setEditingFamily(null);
    setCreatingFamily(false);
    setEditingUnit(null);
    setCreatingUnit(false);
    setError(null);
    setConfirmOff(false);
  };

  const saveFamily = () => {
    if (!familyForm.name.trim() || (!editingFamily && !familyForm.code.trim())) {
      setError('A family needs a code and a name.');
      return;
    }
    return run(
      'save this family',
      () =>
        editingFamily
          ? unitFamilyApi.update(editingFamily.id, { name: familyForm.name.trim() })
          : unitFamilyApi.create({ code: familyForm.code.trim(), name: familyForm.name.trim() }),
      closeAll
    );
  };

  const saveUnit = () => {
    if (!unitForm.name.trim() || (!editingUnit && !unitForm.code.trim())) {
      setError('A unit needs a code and a name.');
      return;
    }
    const factor = Number(unitForm.factor_to_base);
    if (!(factor > 0)) {
      setError('A unit has to be worth something. A gram is 0.001 of a kilogram.');
      return;
    }
    const places = Number(unitForm.decimal_places);
    return run(
      'save this unit',
      () =>
        editingUnit
          ? uomApi.update(editingUnit.id, {
              name: unitForm.name.trim(),
              decimal_places: places,
              factor_to_base: factor,
            })
          : uomApi.create({
              code: unitForm.code.trim().toUpperCase(),
              name: unitForm.name.trim(),
              decimal_places: places,
              family_id: unitForm.family_id,
              factor_to_base: factor,
            }),
      closeAll
    );
  };

  const setUnitActive = (active: boolean) => {
    if (!editingUnit) return;
    return run(
      active ? 'bring this unit back' : 'retire this unit',
      () => (active ? uomApi.activate(editingUnit.id) : uomApi.deactivate(editingUnit.id)),
      closeAll
    );
  };

  const setFamilyActive = (active: boolean) => {
    if (!editingFamily) return;
    return run(
      active ? 'bring this family back' : 'retire this family',
      () => (active ? unitFamilyApi.activate(editingFamily.id) : unitFamilyApi.deactivate(editingFamily.id)),
      closeAll
    );
  };

  const makeBase = (unit: UnitOfMeasure) =>
    run('make this the base unit', () => unitFamilyApi.setBaseUnit(unit.family_id, unit.id), closeAll);

  const openNewUnit = () => {
    setUnitForm({ ...BLANK_UNIT, family_id: familyFilter || families[0]?.id || '' });
    setEditingUnit(null);
    setCreatingUnit(true);
    setError(null);
    setConfirmOff(false);
  };

  const openUnit = (u: UnitOfMeasure) => {
    setUnitForm({
      code: u.code,
      name: u.name,
      family_id: u.family_id,
      factor_to_base: String(u.factor_to_base),
      decimal_places: String(u.decimal_places),
    });
    setEditingUnit(u);
    setCreatingUnit(false);
    setError(null);
    setConfirmOff(false);
  };

  const openFamily = (f: UnitFamily) => {
    setFamilyForm({ code: f.code, name: f.name });
    setEditingFamily(f);
    setCreatingFamily(false);
    setError(null);
    setConfirmOff(false);
  };

  const familyColumns: Column<UnitFamily>[] = [
    {
      key: 'name',
      header: 'Measures',
      render: (f) => (
        <div>
          <div style={{ fontWeight: 600 }}>{f.name}</div>
          <div style={styles.sub}>{f.code}</div>
        </div>
      ),
      value: (f) => `${f.name} ${f.code}`,
    },
    {
      key: 'base_uom_code',
      header: 'Converts through',
      render: (f) =>
        f.base_uom_code ? (
          <span style={badge('info')}>{f.base_uom_code}</span>
        ) : (
          <span style={styles.warn}>none yet</span>
        ),
      value: (f) => f.base_uom_code ?? '',
    },
    {
      key: 'unit_count',
      header: 'Units',
      numeric: true,
      render: (f) => (f.unit_count ? f.unit_count : <span style={styles.muted}>—</span>),
      value: (f) => f.unit_count,
    },
    {
      key: 'is_active',
      header: 'Status',
      width: '7rem',
      render: (f) =>
        f.is_active ? <span style={badge('success')}>In use</span> : <span style={badge('neutral')}>Retired</span>,
      value: (f) => (f.is_active ? 1 : 0),
    },
  ];

  const unitColumns: Column<UnitOfMeasure>[] = [
    {
      key: 'code',
      header: 'Unit',
      render: (u) => (
        <div>
          <div style={{ fontWeight: 600, fontFamily: font.mono }}>{u.code}</div>
          <div style={styles.sub}>{u.name}</div>
        </div>
      ),
      value: (u) => `${u.code} ${u.name}`,
    },
    {
      key: 'family_id',
      header: 'Measures',
      render: (u) => familyName(u.family_id),
      value: (u) => familyName(u.family_id),
    },
    {
      key: 'factor_to_base',
      header: 'Worth',
      numeric: true,
      // The factor on its own is a number without a meaning. "0.001 KG" is the
      // sentence somebody can check against what they know.
      render: (u) => {
        const base = familyOf(u.family_id)?.base_uom_code;
        return (
          <span style={{ fontVariantNumeric: 'tabular-nums' }}>
            {u.factor_to_base}
            {base ? <span style={styles.muted}> {base}</span> : null}
          </span>
        );
      },
      value: (u) => u.factor_to_base,
    },
    {
      key: 'decimal_places',
      header: 'Decimals',
      numeric: true,
      // Zero places is what refuses half an egg. It is a rule, not a display
      // preference, so it belongs on the listing.
      render: (u) =>
        u.decimal_places === 0 ? <span style={badge('warning')}>whole only</span> : u.decimal_places,
      value: (u) => u.decimal_places,
    },
    {
      key: 'base',
      header: '',
      sortable: false,
      width: '7rem',
      render: (u) =>
        familyOf(u.family_id)?.base_uom_id === u.id ? <span style={badge('info')}>base</span> : null,
    },
    {
      key: 'is_active',
      header: 'Status',
      width: '7rem',
      render: (u) =>
        u.is_active ? <span style={badge('success')}>In use</span> : <span style={badge('neutral')}>Retired</span>,
      value: (u) => (u.is_active ? 1 : 0),
    },
  ];

  if (!user || user.role === 'cashier') return null;

  const editingUnitIsBase = editingUnit && familyOf(editingUnit.family_id)?.base_uom_id === editingUnit.id;
  const baseCodeFor = (familyId: string) => familyOf(familyId)?.base_uom_code || 'the base unit';

  return (
    <AppLayout>
      <div style={{ ...ui.page, padding: 0 }}>
        <div style={styles.header}>
          <h1 style={styles.h1}>Units of Measure & Conversions</h1>
          <button style={{ ...ui.button, marginLeft: 'auto' }} onClick={() => navigate('/products')}>
            Products →
          </button>
        </div>

      <p style={styles.lead}>
        A family groups the units that convert between each other. Each unit states how much of its
        family&rsquo;s base unit it is worth, so a gram is 0.001 of a kilogram. Units never convert across
        families, which is what makes selling rice by the litre impossible rather than merely discouraged.
      </p>

      <FormError error={error} />

      <section style={styles.section}>
        <div style={styles.sectionHead}>
          <h2 style={styles.h2}>What the shop measures</h2>
          <label style={styles.toggle}>
            <input type="checkbox" checked={showRetired} onChange={(e) => setShowRetired(e.target.checked)} />
            Show retired
          </label>
        </div>
        <DataTable
          rows={families}
          columns={familyColumns}
          rowKey={(f) => f.id}
          loading={loading}
          onRowClick={openFamily}
          searchable={false}
          empty="No unit families yet."
          rowTone={(f) => (f.is_active ? undefined : 'warning')}
          toolbar={
            <button
              style={ui.buttonPrimary}
              onClick={() => {
                setFamilyForm(BLANK_FAMILY);
                setEditingFamily(null);
                setCreatingFamily(true);
                setError(null);
              }}
            >
              + New family
            </button>
          }
        />
      </section>

      <section style={styles.section}>
        <h2 style={styles.h2}>Units</h2>
        <DataTable
          rows={rows}
          columns={unitColumns}
          rowKey={(u) => u.id}
          loading={loading}
          onRowClick={openUnit}
          searchPlaceholder="Search code or name…"
          empty={familyFilter ? 'No units in this family.' : 'No units yet.'}
          rowTone={(u) => (u.is_active ? undefined : 'warning')}
          toolbar={
            <>
              <Select
                value={familyFilter}
                onChange={(e) => setFamilyFilter(e.target.value)}
                placeholder="All families"
                options={families.map((f) => ({ value: f.id, label: f.name }))}
                style={{ width: 'auto' }}
              />
              <button style={ui.buttonPrimary} onClick={openNewUnit} disabled={!families.length}>
                + New unit
              </button>
            </>
          }
        />
      </section>

      {/* Family editor -------------------------------------------------- */}
      <Modal
        open={creatingFamily || !!editingFamily}
        title={editingFamily ? editingFamily.name : 'New family'}
        subtitle={
          editingFamily
            ? `${editingFamily.unit_count} ${editingFamily.unit_count === 1 ? 'unit' : 'units'}, converting through ${editingFamily.base_uom_code || 'nothing yet'}.`
            : 'Weight, volume, count, length, area — or whatever this trade measures in.'
        }
        onClose={closeAll}
        width="34rem"
        footer={
          <>
            {editingFamily &&
              (editingFamily.is_active ? (
                <button
                  style={{ ...ui.buttonDanger, marginRight: 'auto' }}
                  onClick={() => (confirmOff ? setFamilyActive(false) : setConfirmOff(true))}
                  disabled={busy || editingFamily.unit_count > 0}
                  title={editingFamily.unit_count > 0 ? 'Retire its units first.' : undefined}
                >
                  {confirmOff ? 'Confirm — retire' : 'Retire'}
                </button>
              ) : (
                <button
                  style={{ ...ui.button, marginRight: 'auto' }}
                  onClick={() => setFamilyActive(true)}
                  disabled={busy}
                >
                  Bring back
                </button>
              ))}
            <button style={ui.button} onClick={closeAll} disabled={busy}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={saveFamily} disabled={busy}>
              {busy ? 'Saving…' : editingFamily ? 'Save changes' : 'Add family'}
            </button>
          </>
        }
      >
        <FormError error={error} />
        <FormGrid>
          <Field
            label="Code"
            required={!editingFamily}
            hint={editingFamily ? 'A code cannot change once units reference the family.' : 'Short and upper case: WEIGHT, VOLUME.'}
            htmlFor="f-code"
          >
            <TextInput
              id="f-code"
              value={familyForm.code}
              disabled={!!editingFamily}
              autoFocus={!editingFamily}
              onChange={(e) => setFamilyForm({ ...familyForm, code: e.target.value.toUpperCase() })}
            />
          </Field>
          <Field label="Name" required htmlFor="f-name">
            <TextInput
              id="f-name"
              value={familyForm.name}
              autoFocus={!!editingFamily}
              onChange={(e) => setFamilyForm({ ...familyForm, name: e.target.value })}
            />
          </Field>
        </FormGrid>
        {creatingFamily && (
          <div style={styles.note}>
            The first unit added to a family becomes what the rest convert through, so add its base unit
            next — the kilogram for weight, the litre for volume.
          </div>
        )}
      </Modal>

      {/* Unit editor ---------------------------------------------------- */}
      <Modal
        open={creatingUnit || !!editingUnit}
        title={editingUnit ? `${editingUnit.code} — ${editingUnit.name}` : 'New unit'}
        subtitle={
          editingUnit
            ? `Measures ${familyName(editingUnit.family_id)}.`
            : 'A unit belongs to one family and states its size in that family\u2019s base unit.'
        }
        onClose={closeAll}
        width="38rem"
        footer={
          <>
            {editingUnit &&
              (editingUnit.is_active ? (
                <button
                  style={{ ...ui.buttonDanger, marginRight: 'auto' }}
                  onClick={() => (confirmOff ? setUnitActive(false) : setConfirmOff(true))}
                  disabled={busy}
                >
                  {confirmOff ? 'Confirm — retire' : 'Retire'}
                </button>
              ) : (
                <button
                  style={{ ...ui.button, marginRight: 'auto' }}
                  onClick={() => setUnitActive(true)}
                  disabled={busy}
                >
                  Bring back
                </button>
              ))}
            {editingUnit && editingUnit.is_active && !editingUnitIsBase && (
              <button style={ui.button} onClick={() => makeBase(editingUnit)} disabled={busy}>
                Make it the base
              </button>
            )}
            <button style={ui.button} onClick={closeAll} disabled={busy}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={saveUnit} disabled={busy}>
              {busy ? 'Saving…' : editingUnit ? 'Save changes' : 'Add unit'}
            </button>
          </>
        }
      >
        <FormError error={error} />

        {confirmOff && (
          <div style={styles.note}>
            Retiring takes this unit off every form. The server refuses if products are stocked in it, if
            anything is sold or bought in it, or if it is what its family converts through.
          </div>
        )}

        <FormGrid>
          <Field
            label="Code"
            required={!editingUnit}
            hint={editingUnit ? 'A code cannot change once quantities are recorded in it.' : 'KG, G, NOS, DOZ.'}
            htmlFor="u-code"
          >
            <TextInput
              id="u-code"
              value={unitForm.code}
              disabled={!!editingUnit}
              autoFocus={!editingUnit}
              onChange={(e) => setUnitForm({ ...unitForm, code: e.target.value.toUpperCase() })}
            />
          </Field>
          <Field label="Name" required htmlFor="u-name">
            <TextInput
              id="u-name"
              value={unitForm.name}
              autoFocus={!!editingUnit}
              onChange={(e) => setUnitForm({ ...unitForm, name: e.target.value })}
            />
          </Field>
          <Field
            label="Measures"
            required={!editingUnit}
            hint={editingUnit ? 'A unit cannot change families once anything is measured in it.' : undefined}
            htmlFor="u-family"
          >
            <Select
              id="u-family"
              value={unitForm.family_id}
              disabled={!!editingUnit}
              options={families.filter((f) => f.is_active).map((f) => ({ value: f.id, label: f.name }))}
              onChange={(e) => setUnitForm({ ...unitForm, family_id: e.target.value })}
            />
          </Field>
          <Field
            label={`Worth in ${baseCodeFor(unitForm.family_id)}`}
            required
            hint="A gram is 0.001 of a kilogram. The base unit itself is 1."
            htmlFor="u-factor"
          >
            <NumberInput
              id="u-factor"
              min={0}
              step="any"
              value={unitForm.factor_to_base}
              onChange={(e) => setUnitForm({ ...unitForm, factor_to_base: e.target.value })}
            />
          </Field>
          <Field
            label="Decimal places"
            hint="Zero is what refuses half an egg. Three suits anything weighed or poured."
            htmlFor="u-places"
            span={2}
          >
            <NumberInput
              id="u-places"
              min={0}
              max={6}
              step={1}
              value={unitForm.decimal_places}
              onChange={(e) => setUnitForm({ ...unitForm, decimal_places: e.target.value })}
            />
          </Field>
        </FormGrid>

        {editingUnitIsBase && (
          <div style={styles.note}>
            This is what {familyName(editingUnit!.family_id)} converts through. Every other unit in the
            family states its size against this one, so changing what it is worth changes what all of them
            mean.
          </div>
        )}
      </Modal>
      </div>
    </AppLayout>
  );
}

const styles: Record<string, React.CSSProperties> = {
  header: { display: 'flex', alignItems: 'center', gap: space.lg, marginBottom: space.lg },
  h1: { margin: 0, fontSize: font.size.xxl },
  h2: { margin: 0, fontSize: font.size.lg },
  section: { marginTop: space.xl },
  sectionHead: { display: 'flex', alignItems: 'center', gap: space.lg, marginBottom: space.sm },
  lead: { margin: 0, color: color.textMuted, fontSize: font.size.md, maxWidth: '52rem' },
  sub: { fontSize: font.size.xs, color: color.textMuted },
  muted: { color: color.textFaint },
  warn: { color: color.warning, fontSize: font.size.sm },
  toggle: {
    display: 'flex',
    alignItems: 'center',
    gap: space.xs,
    fontSize: font.size.sm,
    color: color.textMuted,
    marginLeft: 'auto',
    whiteSpace: 'nowrap',
  },
  note: {
    marginTop: space.lg,
    padding: space.md,
    background: color.surfaceAlt,
    borderRadius: '6px',
    fontSize: font.size.sm,
    color: color.textMuted,
  },
};
