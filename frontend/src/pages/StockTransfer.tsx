import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { auditApi, productApi, stockApi, storeApi, uomApi } from '../services/api';
import { Store } from '../types';
import { DataTable, Column } from '../components/DataTable';
import { Modal } from '../components/Modal';
import { Field, FormError, FormGrid, NumberInput, Select } from '../components/Form';
import { badge, color, font, space, ui } from '../theme';
import { formatDate, formatMoney, formatQty } from '../utils/format';

interface StockItem {
  id: string;
  product_id: string;
  product_name?: string;
  batch_no: string;
  quantity: number;
  /** Spoken for by an order already placed. The server refuses to move it. */
  reserved_qty: number;
  expiry_date: string | null;
  cost_price: number;
}

interface Product {
  id: string;
  name: string;
  uom_id?: string;
}

interface Uom {
  id: string;
  code: string;
}

/** One row of the audit trail, which is where past transfers live. */
interface AuditEntry {
  id: string;
  action: string;
  table_name: string;
  /** The store the request was authorised against — the source of the move. */
  store_id: string;
  username: string;
  /** The request body, as JSON text. */
  new_value: string;
  created_at: string;
}

interface Transfer {
  id: string;
  at: string;
  by: string;
  from_store_id: string;
  to_store_id: string;
  product_id: string;
  batch_no: string;
  quantity: number | null;
}

/** A batch is identified by its product and its batch number, not either alone. */
function keyOf(item: Pick<StockItem, 'product_id' | 'batch_no'>): string {
  return `${item.product_id}|${item.batch_no}`;
}

/** What may actually move: reserved stock belongs to an order already taken. */
function availableOf(item: StockItem): number {
  return (item.quantity || 0) - (item.reserved_qty || 0);
}

/**
 * A past transfer, read out of one audit row.
 *
 * The source is the row's own `store_id` — the store the request was authorised
 * against, which is where the stock actually left from. The `from_store_id` in
 * the recorded body is only what the client asked for, and the server does not
 * read it, so trusting it here would reprint the same mistake in the history.
 */
function parseTransfer(entry: AuditEntry): Transfer {
  let body: Record<string, unknown> = {};
  try {
    body = JSON.parse(entry.new_value || '{}') as Record<string, unknown>;
  } catch {
    body = {};
  }
  const text = (v: unknown) => (typeof v === 'string' ? v : '');
  return {
    id: entry.id,
    at: entry.created_at,
    by: entry.username,
    from_store_id: entry.store_id,
    to_store_id: text(body.to_store_id),
    product_id: text(body.product_id),
    batch_no: text(body.batch_no),
    quantity: typeof body.quantity === 'number' ? body.quantity : null,
  };
}

/**
 * Stock transfer, rebuilt on the shared table and dialog.
 *
 * What this screen moves is a batch, and the old one asked for it through a
 * `<select>` whose options were three fields glued together — "Basmati Rice |
 * Batch: GRN-0085c50c | Qty: 4". A dropdown cannot be searched, sorted or
 * scanned, so with four batches of the same rice on hand the only way to find
 * the right one was to read every option to the end. Below it, a hand-written
 * table repeated the same rows with no search and no sorting either, and every
 * quantity on the screen was a bare number: "4" of a product stocked in kg is
 * four kilos, and nothing said so.
 *
 * So the list *is* the picker now — one table, searchable and sortable, and
 * picking a row opens the dialog that moves it.
 *
 * Two things the old screen got wrong at the counter are fixed here. It checked
 * the quantity against the batch total while the server checks it against the
 * unreserved part, so a transfer of stock promised to an open order passed
 * every check the screen made and was refused afterwards, with the reason shown
 * nowhere near the field that caused it. And once a transfer went through there
 * was no record of it on the screen at all; the audit trail is now the second
 * view.
 *
 * The third thing is not fixed here because it cannot be, and saying so is the
 * next best thing — see the note beside the source store below.
 */
export default function StockTransfer() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  const [stores, setStores] = useState<Store[]>([]);
  const [fromStoreId, setFromStoreId] = useState('');
  const [toStoreId, setToStoreId] = useState('');
  const [stockItems, setStockItems] = useState<StockItem[]>([]);
  const [products, setProducts] = useState<Product[]>([]);
  const [uoms, setUoms] = useState<Uom[]>([]);
  const [history, setHistory] = useState<Transfer[]>([]);
  const [historyBlocked, setHistoryBlocked] = useState(false);

  const [view, setView] = useState<'stock' | 'history'>('stock');
  const [movingKey, setMovingKey] = useState<string | null>(null);
  const [quantity, setQuantity] = useState('');
  const [transferring, setTransferring] = useState(false);
  const [stockLoading, setStockLoading] = useState(false);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [pageError, setPageError] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const loadStores = useCallback(async () => {
    try {
      const { data } = await storeApi.list();
      setStores(data || []);
      if (store) setFromStoreId(store.id);
    } catch (err: any) {
      setPageError(err?.response?.data?.error || 'Could not load the list of stores.');
    }
  }, [store]);

  // Takes the store as an argument and reads no state, so its identity never
  // changes and the effect below cannot re-fire on it.
  const loadStock = useCallback(async (storeId: string) => {
    setStockLoading(true);
    try {
      const { data } = await stockApi.list(storeId);
      // Only what is on a shelf: a zero row is a batch sold out, and offering
      // it to move is offering nothing.
      setStockItems(((data || []) as StockItem[]).filter((item) => item.quantity > 0));
    } catch (err: any) {
      setPageError(err?.response?.data?.error || 'Could not load stock for that store.');
      setStockItems([]);
    } finally {
      setStockLoading(false);
    }
  }, []);

  const loadProducts = useCallback(async () => {
    if (!store) return;
    try {
      const { data } = await productApi.list({ store_id: store.id });
      setProducts(data as unknown as Product[]);
    } catch (err) {
      console.error('Failed to load products:', err);
    }
  }, [store]);

  // Units of measure are global, so this closes over nothing and keeps its
  // identity across renders.
  const loadUoms = useCallback(async () => {
    try {
      const { data } = await uomApi.list();
      setUoms(data as Uom[]);
    } catch (err) {
      console.error('Failed to load units of measure:', err);
    }
  }, []);

  /**
   * Past transfers, read out of the audit trail.
   *
   * There is no transfers table: a transfer writes the two stock rows and one
   * audit entry, and that entry — which carries the whole request body — is the
   * only record that it happened. A screen that moves stock and then shows no
   * sign of having moved it is worse than one built on the trail.
   */
  const loadHistory = useCallback(async () => {
    if (!store) return;
    setHistoryLoading(true);
    try {
      const { data } = await auditApi.list({ store_id: store.id, table: 'stock', limit: '200' });
      setHistory(
        (data as AuditEntry[])
          // Both spellings: the route in service today records "transfer", the
          // older handler recorded "stock_transfer", and rows of both survive.
          .filter((e) => e.action === 'transfer' || e.action === 'stock_transfer')
          .map(parseTransfer)
      );
      setHistoryBlocked(false);
    } catch (err) {
      // Reading the trail is an elevated permission on some roles. That is not
      // a page failure — moving stock still works.
      setHistoryBlocked(true);
    } finally {
      setHistoryLoading(false);
    }
  }, [store]);

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    loadStores();
    loadProducts();
    loadUoms();
    loadHistory();
  }, [user, navigate, loadStores, loadProducts, loadUoms, loadHistory]);

  useEffect(() => {
    if (fromStoreId) {
      loadStock(fromStoreId);
    } else {
      setStockItems([]);
    }
    setMovingKey(null);
  }, [fromStoreId, loadStock]);

  const unitById = useMemo(() => {
    const codeFor: Record<string, string> = {};
    uoms.forEach((u) => {
      codeFor[u.id] = u.code;
    });
    const map: Record<string, string> = {};
    products.forEach((p) => {
      map[p.id] = p.uom_id ? (codeFor[p.uom_id] ?? '') : '';
    });
    return map;
  }, [products, uoms]);

  const productNameById = useMemo(() => {
    const map: Record<string, string> = {};
    products.forEach((p) => {
      map[p.id] = p.name;
    });
    return map;
  }, [products]);

  const storeNameById = useMemo(() => {
    const map: Record<string, string> = {};
    stores.forEach((s) => {
      map[s.id] = s.name;
    });
    return map;
  }, [stores]);

  const storeLabel = (s: Store) => (s.location_type === 'warehouse' ? `${s.name} (warehouse)` : s.name);

  const moving = useMemo(() => stockItems.find((i) => keyOf(i) === movingKey) ?? null, [stockItems, movingKey]);

  const fromStoreName = storeNameById[fromStoreId] ?? '';
  const toStoreName = storeNameById[toStoreId] ?? '';
  const destinations = useMemo(() => stores.filter((s) => s.id !== fromStoreId), [stores, fromStoreId]);

  /**
   * Whether stock can actually leave the store being looked at.
   *
   * The server takes the source of a transfer from the store the session is
   * authorised for, not from anything in the request body — so stock can only
   * ever leave the store this till is signed in to. Any other store can be
   * browsed here, and moving from it is refused, so the screen says which is
   * which rather than offering a button that quietly moves the wrong shop's
   * stock. (See the report accompanying this change: teaching `stockApi.transfer`
   * to pass the source as a `store_id` query parameter is what would lift this.)
   */
  const canMoveFromSource = !!store && fromStoreId === store.id;

  const totals = useMemo(() => {
    const value = stockItems.reduce((sum, i) => sum + (i.quantity || 0) * (i.cost_price || 0), 0);
    const locked = stockItems.filter((i) => (i.reserved_qty || 0) > 0).length;
    return { batches: stockItems.length, value, locked };
  }, [stockItems]);

  const openMove = (item: StockItem) => {
    if (!canMoveFromSource || availableOf(item) <= 0) return;
    setMovingKey(keyOf(item));
    setQuantity('');
    setFormError(null);
    setNotice(null);
  };

  const closeMove = () => {
    setMovingKey(null);
    setQuantity('');
    setFormError(null);
  };

  const transfer = async () => {
    if (!moving) return;
    if (!toStoreId) {
      setFormError('Choose where the stock is going.');
      return;
    }
    if (fromStoreId === toStoreId) {
      setFormError('A transfer needs two different stores.');
      return;
    }
    const qty = parseFloat(quantity);
    if (!Number.isFinite(qty) || qty <= 0) {
      setFormError('Enter how much to move.');
      return;
    }
    const spare = availableOf(moving);
    if (qty > spare) {
      // Names the figure the server will actually check against, rather than
      // the batch total — which is what made a refusal come as a surprise.
      setFormError(
        `Only ${formatQty(spare, unitById[moving.product_id])} of this batch can be moved${
          moving.reserved_qty > 0
            ? ` — ${formatQty(moving.reserved_qty, unitById[moving.product_id])} is reserved against an open order.`
            : '.'
        }`
      );
      return;
    }

    setTransferring(true);
    setFormError(null);
    try {
      await stockApi.transfer({
        product_id: moving.product_id,
        // Sent because the API signature asks for it, but the server takes the
        // source from the authorised store scope and ignores a store id in the
        // body. It is only ever the signed-in store here, so the two agree.
        from_store_id: fromStoreId,
        to_store_id: toStoreId,
        quantity: qty,
        batch_no: moving.batch_no || undefined,
      });
      const name = moving.product_name || productNameById[moving.product_id] || 'stock';
      setNotice(
        `Moved ${formatQty(qty, unitById[moving.product_id])} of ${name} from ${fromStoreName} to ${toStoreName}.`
      );
      closeMove();
      await loadStock(fromStoreId);
      await loadHistory();
    } catch (err: any) {
      // Flattened to a string upstream — "insufficient unreserved stock for
      // transfer" is the one worth reading, and it belongs beside the field.
      setFormError(err?.response?.data?.error || 'Could not move this stock.');
    } finally {
      setTransferring(false);
    }
  };

  const stockColumns: Column<StockItem>[] = [
    {
      key: 'product_name',
      header: 'Product',
      render: (i) => (
        <span style={{ fontWeight: 600 }}>{i.product_name || productNameById[i.product_id] || 'Unknown product'}</span>
      ),
      value: (i) => i.product_name || productNameById[i.product_id] || i.product_id,
    },
    {
      key: 'batch_no',
      header: 'Batch',
      render: (i) =>
        i.batch_no ? (
          <span style={{ fontFamily: font.mono, fontSize: font.size.sm }}>{i.batch_no}</span>
        ) : (
          <span style={styles.muted}>No batch number</span>
        ),
      value: (i) => i.batch_no,
    },
    {
      key: 'quantity',
      header: 'On hand',
      numeric: true,
      render: (i) => formatQty(i.quantity, unitById[i.product_id]),
      // The number: the cell carries a unit code, and sorting those as text
      // puts 9 kg above 10 kg.
      value: (i) => i.quantity,
    },
    {
      key: 'available',
      header: 'Can be moved',
      numeric: true,
      // The column that decides whether a transfer is accepted at all.
      render: (i) => {
        const spare = availableOf(i);
        return (
          <div>
            <div style={{ fontWeight: 600, color: spare <= 0 ? color.danger : color.text }}>
              {formatQty(spare, unitById[i.product_id])}
            </div>
            {i.reserved_qty > 0 && (
              <div style={styles.sub}>{formatQty(i.reserved_qty, unitById[i.product_id])} reserved</div>
            )}
          </div>
        );
      },
      value: (i) => availableOf(i),
    },
    {
      key: 'expiry_date',
      header: 'Expiry',
      render: (i) => {
        if (!i.expiry_date) return <span style={styles.muted}>—</span>;
        const past = new Date(i.expiry_date).getTime() < Date.now();
        return past ? <span style={badge('danger')}>{formatDate(i.expiry_date)}</span> : formatDate(i.expiry_date);
      },
      // ISO first for a chronological sort, readable form after it so a search
      // for "25-09-2026" finds the row too.
      value: (i) => (i.expiry_date ? `${i.expiry_date} ${formatDate(i.expiry_date)}` : ''),
    },
    {
      key: 'cost_price',
      header: 'Cost',
      numeric: true,
      render: (i) => formatMoney(i.cost_price),
      value: (i) => i.cost_price,
    },
    {
      key: 'actions',
      header: '',
      sortable: false,
      width: '6rem',
      render: (i) => (
        <button
          style={{ ...ui.buttonPrimary, padding: '0.25rem 0.625rem', fontSize: font.size.sm }}
          disabled={!canMoveFromSource || availableOf(i) <= 0}
          title={
            !canMoveFromSource
              ? 'Stock can only be moved out of the store this till is signed in to.'
              : availableOf(i) <= 0
                ? 'Every unit of this batch is reserved against an open order.'
                : undefined
          }
          onClick={(e) => {
            e.stopPropagation();
            openMove(i);
          }}
        >
          Move
        </button>
      ),
    },
  ];

  const historyColumns: Column<Transfer>[] = [
    {
      key: 'at',
      header: 'When',
      render: (t) => formatDate(t.at),
      // ISO first so the column sorts chronologically, readable form after it
      // so a search for "10-09-2026" matches as well.
      value: (t) => (t.at ? `${t.at} ${formatDate(t.at)}` : ''),
    },
    {
      key: 'product_id',
      header: 'Product',
      render: (t) => (
        <span style={{ fontWeight: 600 }}>{productNameById[t.product_id] || 'Product no longer listed'}</span>
      ),
      value: (t) => productNameById[t.product_id] || t.product_id,
    },
    {
      key: 'batch_no',
      header: 'Batch',
      render: (t) =>
        t.batch_no ? (
          <span style={{ fontFamily: font.mono, fontSize: font.size.sm }}>{t.batch_no}</span>
        ) : (
          <span style={styles.muted}>No batch number</span>
        ),
      value: (t) => t.batch_no,
    },
    {
      key: 'quantity',
      header: 'Moved',
      numeric: true,
      render: (t) =>
        t.quantity === null ? (
          <span style={styles.muted}>—</span>
        ) : (
          formatQty(t.quantity, unitById[t.product_id])
        ),
      value: (t) => t.quantity,
    },
    {
      key: 'from_store_id',
      header: 'From',
      render: (t) => storeNameById[t.from_store_id] || <span style={styles.muted}>Unknown store</span>,
      value: (t) => storeNameById[t.from_store_id] || t.from_store_id,
    },
    {
      key: 'to_store_id',
      header: 'To',
      render: (t) => storeNameById[t.to_store_id] || <span style={styles.muted}>Unknown store</span>,
      value: (t) => storeNameById[t.to_store_id] || t.to_store_id,
    },
    {
      key: 'by',
      header: 'By',
      render: (t) => t.by || <span style={styles.muted}>—</span>,
      value: (t) => t.by,
    },
  ];

  if (!user) return null;

  return (
    <div style={ui.page}>
      <div style={styles.header}>
        <button onClick={() => navigate('/')} style={ui.button}>
          ← Back
        </button>
        <h1 style={styles.h1}>Stock transfer</h1>
      </div>

      <div style={styles.summary}>
        <Stat label="Batches at source" value={String(totals.batches)} note={fromStoreName || 'no store chosen'} />
        <Stat label="Value on hand" value={formatMoney(totals.value)} note="at what the stock cost" />
        <Stat
          label="Batches partly reserved"
          value={String(totals.locked)}
          tone={totals.locked ? 'warning' : undefined}
          note={totals.locked ? 'reserved stock cannot be moved' : 'nothing is held back'}
        />
      </div>

      {pageError && (
        <div style={{ marginBottom: space.lg }}>
          <FormError error={pageError} />
        </div>
      )}
      {notice && (
        <div style={styles.notice} role="status">
          {notice}
        </div>
      )}

      <div style={{ ...ui.card, padding: space.lg, marginBottom: space.lg }}>
        <FormGrid>
          <Field
            label="Move stock from"
            required
            hint={
              store && fromStoreId && !canMoveFromSource
                ? `You are browsing ${fromStoreName}. Stock can only leave ${store.name}, the store this till is signed in to.`
                : 'The store this till is signed in to. Sign in elsewhere to move that store’s stock.'
            }
            htmlFor="t-from"
          >
            <Select
              id="t-from"
              value={fromStoreId}
              onChange={(e) => setFromStoreId(e.target.value)}
              placeholder="Choose a store to look at"
              options={stores.map((s) => ({ value: s.id, label: storeLabel(s) }))}
            />
          </Field>
          <Field
            label="Move stock to"
            required
            // The single-store case is the whole reason this hint exists: with
            // nowhere to send anything, an empty dropdown looks like a fault.
            hint={
              destinations.length === 0
                ? 'There is nowhere to send stock — this is the only store on the system. Add a second store or a warehouse first.'
                : undefined
            }
            htmlFor="t-to"
          >
            <Select
              id="t-to"
              value={toStoreId}
              onChange={(e) => setToStoreId(e.target.value)}
              placeholder={destinations.length ? 'Choose the destination store' : 'No other store'}
              disabled={destinations.length === 0}
              options={destinations.map((s) => ({ value: s.id, label: storeLabel(s) }))}
            />
          </Field>
        </FormGrid>
      </div>

      <div style={styles.tabs}>
        <button
          style={view === 'stock' ? styles.tabActive : styles.tab}
          onClick={() => setView('stock')}
          aria-pressed={view === 'stock'}
        >
          Stock to move ({stockItems.length})
        </button>
        <button
          style={view === 'history' ? styles.tabActive : styles.tab}
          onClick={() => setView('history')}
          aria-pressed={view === 'history'}
        >
          Past transfers ({history.length})
        </button>
      </div>

      {view === 'stock' ? (
        <>
          {fromStoreId && !canMoveFromSource && (
            <div style={styles.warn}>
              This is {fromStoreName}&rsquo;s stock, shown so you can check it. Moving it is refused by the server,
              which takes the source of a transfer from the store the session is signed in to — {store?.name}. Switch
              store to move anything from here.
            </div>
          )}
          <DataTable
            rows={stockItems}
            columns={stockColumns}
            rowKey={keyOf}
            loading={stockLoading}
            onRowClick={openMove}
            searchPlaceholder="Search product or batch…"
            // Two different facts, and only this side knows which one holds: no
            // source chosen at all, or a source whose every batch is sold down
            // to zero and filtered out above.
            empty={
              !fromStoreId
                ? 'Choose a source store to see what it holds.'
                : `${fromStoreName || 'That store'} has nothing on hand to move.`
            }
            rowTone={(i) => (availableOf(i) <= 0 ? 'danger' : i.reserved_qty > 0 ? 'warning' : undefined)}
          />
        </>
      ) : (
        <>
          {historyBlocked && (
            <div style={styles.warn}>
              Past transfers are read from the audit trail, which this account may not open. Moving stock still works.
            </div>
          )}
          <DataTable
            rows={history}
            columns={historyColumns}
            rowKey={(t) => t.id}
            loading={historyLoading}
            searchPlaceholder="Search product, batch, store or user…"
            // The trail is scoped to the signed-in store, so this is every
            // transfer out of it — not every transfer in the business.
            empty={
              historyBlocked
                ? 'The audit trail is not readable from this account.'
                : `No stock has been moved out of ${store?.name ?? 'this store'} yet.`
            }
          />
        </>
      )}

      <Modal
        open={!!moving}
        title={moving ? `Move ${moving.product_name || productNameById[moving.product_id] || 'stock'}` : ''}
        subtitle={
          moving
            ? `${moving.batch_no || 'no batch number'} · ${fromStoreName || 'source'} → ${
                toStoreName || 'choose a destination'
              }`
            : undefined
        }
        width="36rem"
        onClose={closeMove}
        footer={
          <>
            <button style={ui.button} onClick={closeMove} disabled={transferring}>
              Cancel
            </button>
            <button style={ui.buttonPrimary} onClick={transfer} disabled={transferring}>
              {transferring ? 'Moving…' : 'Move stock'}
            </button>
          </>
        }
      >
        <FormError error={formError} />

        {moving && (
          <>
            <FormGrid>
              <Field
                label={unitById[moving.product_id] ? `Quantity (${unitById[moving.product_id]})` : 'Quantity'}
                required
                hint={
                  moving.reserved_qty > 0
                    ? `${formatQty(availableOf(moving), unitById[moving.product_id])} of ${formatQty(
                        moving.quantity,
                        unitById[moving.product_id]
                      )} can move — the rest is reserved.`
                    : `${formatQty(availableOf(moving), unitById[moving.product_id])} on hand.`
                }
                htmlFor="t-qty"
              >
                <NumberInput
                  id="t-qty"
                  autoFocus
                  min={0}
                  max={availableOf(moving)}
                  value={quantity}
                  onChange={(e) => setQuantity(e.target.value)}
                />
              </Field>
              <Field
                label="Destination"
                required
                hint="The same choice as above — change it in either place."
                htmlFor="t-to-modal"
              >
                <Select
                  id="t-to-modal"
                  value={toStoreId}
                  onChange={(e) => setToStoreId(e.target.value)}
                  placeholder={destinations.length ? 'Choose the destination store' : 'No other store'}
                  disabled={destinations.length === 0}
                  options={destinations.map((s) => ({ value: s.id, label: storeLabel(s) }))}
                />
              </Field>
            </FormGrid>

            <div style={styles.detailGrid}>
              <div>
                <div style={styles.statLabel}>Batch</div>
                <div style={styles.detailValue}>{moving.batch_no || '—'}</div>
              </div>
              <div>
                <div style={styles.statLabel}>Expiry</div>
                <div style={styles.detailValue}>{formatDate(moving.expiry_date)}</div>
              </div>
              <div>
                <div style={styles.statLabel}>Cost</div>
                <div style={styles.detailValue}>{formatMoney(moving.cost_price)}</div>
              </div>
            </div>

            {parseFloat(quantity) > 0 && toStoreId && (
              <div style={styles.note}>
                Moving <strong>{formatQty(parseFloat(quantity), unitById[moving.product_id])}</strong> of{' '}
                <strong>{moving.product_name || productNameById[moving.product_id]}</strong>
                {moving.batch_no ? ` (batch ${moving.batch_no})` : ''} from <strong>{fromStoreName}</strong> to{' '}
                <strong>{toStoreName}</strong>. The batch keeps its number, its cost and its expiry date at the far end.
              </div>
            )}
          </>
        )}
      </Modal>
    </div>
  );
}

function Stat({ label, value, note, tone }: { label: string; value: string; note?: string; tone?: 'warning' }) {
  return (
    <div style={{ ...ui.card, padding: space.lg, flex: '1 1 10rem' }}>
      <div style={styles.statLabel}>{label}</div>
      <div style={{ ...styles.statValue, color: tone === 'warning' ? color.warning : color.text }}>{value}</div>
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
  sub: { marginTop: space.xs, fontSize: font.size.xs, color: color.textMuted },
  muted: { color: color.textFaint },
  tabs: { display: 'flex', gap: space.xs, marginBottom: space.md },
  tab: { ...ui.button, borderRadius: '999px', padding: '0.375rem 0.875rem' },
  tabActive: { ...ui.buttonPrimary, borderRadius: '999px', padding: '0.375rem 0.875rem' },
  notice: {
    padding: space.md,
    marginBottom: space.lg,
    background: color.successSoft,
    border: `1px solid ${color.success}33`,
    color: color.success,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
  note: {
    padding: space.md,
    background: color.primarySoft,
    border: `1px solid ${color.primary}22`,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
  warn: {
    padding: space.md,
    marginBottom: space.lg,
    background: color.warningSoft,
    border: `1px solid ${color.warning}33`,
    color: color.warning,
    borderRadius: '6px',
    fontSize: font.size.md,
  },
  detailGrid: { display: 'flex', gap: space.xl, flexWrap: 'wrap' },
  detailValue: { marginTop: space.xs, fontSize: font.size.lg, fontWeight: 700, fontVariantNumeric: 'tabular-nums' },
};
