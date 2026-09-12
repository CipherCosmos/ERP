package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gbos/internal/domain/money"
	"gbos/internal/domain/stock"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// Limits for the stock audit listings.
const (
	defaultStockLimit = 100
	maxStockLimit     = 500
)

// Stock reads and writes stock batches, adjustments and inventory losses.
//
// Every quantity-reducing statement in this file is a *guarded relative*
// update: it subtracts in SQL and carries its own sufficiency test in the WHERE
// clause, and the caller checks RowsAffected. That combination is the only form
// that cannot oversell. The two failure modes it replaces both existed:
//
//   - inventory.go:159-232 read the quantity into Go, computed the new value
//     there and wrote `SET quantity = ?`. Two concurrent adjustments both read
//     the same starting figure and the second overwrote the first.
//   - inventory.go:305 and :587 subtracted relatively but tested sufficiency in
//     Go beforehand and never looked at RowsAffected, so the test was stale by
//     the time the write ran and a failed write looked like a successful one.
type Stock struct{}

// NewStock returns a stock repository.
func NewStock() *Stock { return &Stock{} }

const stockBatchColumns = `s.id, s.product_id, s.store_id, s.batch_no, s.quantity,
	s.reserved_qty, s.expiry_date, s.mfg_date, s.total_cost, s.cost_price`

// scanStockBatch reads the shared batch column list. It never discards the scan
// error: inventory.go:178, :192, :217 and :598 all dropped one, which turned a
// failed read of the figure that gates an underflow check into a silent zero.
func scanStockBatch(sc interface{ Scan(...any) error }, b *stock.Batch, extra ...any) error {
	var expiry, mfg sql.NullTime
	dest := []any{&b.ID, &b.ProductID, &b.StoreID, &b.BatchNo, &b.Quantity,
		&b.ReservedQty, &expiry, &mfg, &b.TotalCost, &b.CostPrice}
	dest = append(dest, extra...)
	if err := sc.Scan(dest...); err != nil {
		return err
	}
	b.ExpiryDate = stockTimePtr(expiry)
	b.MfgDate = stockTimePtr(mfg)
	return nil
}

func stockTimePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// stockDateArg renders an optional date for a parameter slot.
func stockDateArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// ListStock returns every batch with stock on hand in a store, product name
// included.
func (r *Stock) ListStock(ctx context.Context, q platform.Querier, storeID uuid.UUID) ([]stock.StockLine, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+stockBatchColumns+`, p.name
		   FROM stock s
		   JOIN products p ON p.id = s.product_id
		  WHERE s.store_id = $1 AND s.quantity > 0
		  ORDER BY p.name, s.expiry_date NULLS LAST, s.batch_no`, storeID)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	lines := make([]stock.StockLine, 0)
	for rows.Next() {
		var line stock.StockLine
		if err := scanStockBatch(rows, &line.Batch, &line.ProductName); err != nil {
			// Never skip a row that failed to scan. inventory.go:52 did
			// `continue` here, returning a short list that looked complete —
			// stock that exists but is invisible is worse than an error.
			return nil, fmt.Errorf("scan stock line: %w", platform.MapError(err))
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return lines, nil
}

// ProductBatches returns the batches of one product held in one store, earliest
// expiry first.
func (r *Stock) ProductBatches(ctx context.Context, q platform.Querier, productID, storeID uuid.UUID) ([]stock.Batch, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+stockBatchColumns+`
		   FROM stock s
		  WHERE s.product_id = $1 AND s.store_id = $2 AND s.quantity > 0
		  ORDER BY s.expiry_date ASC NULLS LAST, s.created_at ASC, s.id ASC`,
		productID, storeID)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	batches := make([]stock.Batch, 0)
	for rows.Next() {
		var b stock.Batch
		if err := scanStockBatch(rows, &b); err != nil {
			return nil, fmt.Errorf("scan stock batch: %w", platform.MapError(err))
		}
		batches = append(batches, b)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return batches, nil
}

// GetBatch returns one batch by its natural key, or platform.ErrNotFound.
func (r *Stock) GetBatch(ctx context.Context, q platform.Querier, productID, storeID uuid.UUID, batchNo string) (stock.Batch, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+stockBatchColumns+`
		   FROM stock s
		  WHERE s.product_id = $1 AND s.store_id = $2 AND s.batch_no = $3`,
		productID, storeID, batchNo)
	var b stock.Batch
	if err := scanStockBatch(row, &b); err != nil {
		return stock.Batch{}, platform.MapError(err)
	}
	return b, nil
}

// BatchesForDeduction returns the batches a FEFO deduction may consume, in
// consumption order: earliest expiry first, undated batches last, then oldest
// first.
//
// The rows are fully drained into a slice before this returns, and the caller
// writes only afterwards. That is mandatory rather than tidy. The old
// implementation (billing.go:54-98) held this cursor open and called tx.Exec
// inside `for rows.Next()`. pgx refuses a second operation on a connection that
// still has an open cursor — it reports "conn busy", which database/sql
// surfaces as `driver: bad connection` — so a literal port of that loop fails
// on the first sale that spans more than one batch. It survived only because
// SQLite buffered the result set.
//
// FOR UPDATE locks the candidate rows for the caller's transaction, so two
// concurrent sales of the same product queue instead of one of them losing the
// guarded update and failing spuriously. The lock order is the ORDER BY, which
// is identical for identical arguments, so two FEFO walks cannot deadlock each
// other.
func (r *Stock) BatchesForDeduction(ctx context.Context, q platform.Querier, productID, storeID uuid.UUID) ([]stock.Batch, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+stockBatchColumns+`
		   FROM stock s
		  WHERE s.product_id = $1 AND s.store_id = $2 AND s.quantity > s.reserved_qty
		  ORDER BY s.expiry_date ASC NULLS LAST, s.created_at ASC, s.id ASC
		    FOR UPDATE`, productID, storeID)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	batches := make([]stock.Batch, 0)
	for rows.Next() {
		var b stock.Batch
		if err := scanStockBatch(rows, &b); err != nil {
			return nil, fmt.Errorf("scan deduction candidate: %w", platform.MapError(err))
		}
		batches = append(batches, b)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return batches, nil
}

// LowStock returns the products in a store at or below their reorder level.
//
// Two SQLite-isms are corrected here. `p.is_active = 1` is a type error against
// a boolean column, and the HAVING clause referenced the SELECT alias
// `current_stock`, which SQLite tolerates and Postgres rejects.
// It carries the supplier, the unit the shop buys in and what one last cost,
// because a list of names and numbers is not something a buyer can act on: each
// line would otherwise need three lookups, on a list that is forty lines long
// the morning after a good weekend.
//
// The buying unit is the largest purchasable one. Buying is done in bulk: a
// shop reordering rice orders sacks, and an order for 648 kilos is not
// something anybody places. A shop that genuinely buys by the kilo has only the
// kilo marked purchasable, so the same rule gives the kilo — which is why it
// needs no special case for the product's default unit. Preferring the default
// was tried and is wrong exactly when it matters: the default is the unit the
// shop SELLS in, and a product sold loose and bought by the sack is the case
// the whole units model exists for. The buyer can change the figure on the
// line.
func (r *Stock) LowStock(ctx context.Context, q platform.Querier, storeID uuid.UUID) ([]stock.LowStockItem, error) {
	rows, err := q.QueryContext(ctx,
		`WITH on_hand AS (
		    SELECT p.id AS product_id, COALESCE(SUM(s.quantity), 0) AS current_stock
		      FROM products p
		      LEFT JOIN stock s ON s.product_id = p.id AND s.store_id = $1
		     WHERE p.is_active AND p.min_stock > 0
		     GROUP BY p.id
		), buying_unit AS (
		    SELECT DISTINCT ON (pu.product_id)
		           pu.product_id, pu.id, pu.label, pu.qty_in_base
		      FROM product_units pu
		     WHERE pu.is_active AND pu.is_purchasable
		     ORDER BY pu.product_id, pu.qty_in_base DESC, pu.sort_order
		)
		 SELECT p.id, p.name, p.min_stock, h.current_stock,
		        p.preferred_vendor_id, COALESCE(v.name, ''),
		        COALESCE(bu.id, '00000000-0000-0000-0000-000000000000'::uuid),
		        COALESCE(bu.label, ''), COALESCE(bu.qty_in_base, 0),
		        COALESCE(pp.cost_price, 0)
		   FROM products p
		   JOIN on_hand h ON h.product_id = p.id
		   LEFT JOIN vendors v ON v.id = p.preferred_vendor_id
		   LEFT JOIN buying_unit bu ON bu.product_id = p.id
		   LEFT JOIN product_prices pp
		          ON pp.product_id = p.id AND pp.product_unit_id = bu.id AND pp.store_id = $1
		  WHERE h.current_stock <= p.min_stock
		  ORDER BY COALESCE(v.name, ''), h.current_stock ASC, p.name`, storeID)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	items := make([]stock.LowStockItem, 0)
	for rows.Next() {
		var it stock.LowStockItem
		if err := rows.Scan(&it.ProductID, &it.Name, &it.MinStock, &it.CurrentStock,
			&it.PreferredVendorID, &it.PreferredVendorName,
			&it.PurchaseUnitID, &it.PurchaseUnitLabel, &it.QtyInBase,
			&it.LastCost); err != nil {
			return nil, fmt.Errorf("scan low stock item: %w", platform.MapError(err))
		}
		// Derived in Go rather than in SQL: rounding up to a whole buying unit
		// is a rule about how a shop orders, and it belongs with the rule that
		// says what level to order back to.
		it.SuggestedUnits = stock.SuggestOrder(it.MinStock, it.CurrentStock, it.QtyInBase)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return items, nil
}

// expiringColumns adds the product name and the day count to the batch columns.
const expiringColumns = stockBatchColumns + `, p.name, (s.expiry_date - CURRENT_DATE)`

func (r *Stock) scanExpiring(ctx context.Context, q platform.Querier, query string, args ...any) ([]stock.ExpiringBatch, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	batches := make([]stock.ExpiringBatch, 0)
	for rows.Next() {
		var b stock.ExpiringBatch
		if err := scanStockBatch(rows, &b.Batch, &b.ProductName, &b.DaysUntilExpiry); err != nil {
			return nil, fmt.Errorf("scan expiring batch: %w", platform.MapError(err))
		}
		batches = append(batches, b)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return batches, nil
}

// ExpiringBatches returns batches that expire within the next `days` days and
// have not expired yet.
//
// The lower bound is the fix for a report that contradicted itself. The window
// was open at the bottom - every batch with expiry_date <= today + N - so a
// batch forty days past its date came back from this query with
// days_until_expiry of -40, at the same time as it came back from
// ExpiredBatches. The expiry screen puts the two in separate tabs, so one batch
// appeared in both, and the "Expiring Soon" count included stock that is not
// expiring but expired.
//
// "Expiring within thirty days" has to mean a batch that is still sellable and
// will stop being sellable inside the window: it is a worklist for discounting
// and moving stock, and something already off the shelf cannot be discounted,
// only written off. That is the other report. The two sets are now disjoint by
// construction - expiry_date < today there, expiry_date >= today here - so
// every dated batch with stock appears in exactly one of them and the counts
// add up.
//
// A batch expiring today counts as expiring rather than expired: it is
// sellable for the rest of the day, and today is inside every window of one
// day or more. It reads as days_until_expiry 0.
//
// The date arithmetic is native: SQLite's julianday() and
// date('now', '+'||?||' days') do not exist in Postgres, and the string
// concatenation the old query used to build the interval was a place a
// non-numeric `days` parameter reached the SQL text.
func (r *Stock) ExpiringBatches(ctx context.Context, q platform.Querier, storeID uuid.UUID, days int) ([]stock.ExpiringBatch, error) {
	return r.scanExpiring(ctx, q,
		`SELECT `+expiringColumns+`
		   FROM stock s
		   JOIN products p ON p.id = s.product_id
		  WHERE s.store_id = $1 AND s.quantity > 0 AND s.expiry_date IS NOT NULL
		    AND s.expiry_date >= CURRENT_DATE
		    AND s.expiry_date <= CURRENT_DATE + $2::integer
		  ORDER BY s.expiry_date ASC, p.name`, storeID, days)
}

// ExpiredBatches returns batches whose expiry date has already passed and which
// still hold stock.
func (r *Stock) ExpiredBatches(ctx context.Context, q platform.Querier, storeID uuid.UUID) ([]stock.ExpiringBatch, error) {
	return r.scanExpiring(ctx, q,
		`SELECT `+expiringColumns+`
		   FROM stock s
		   JOIN products p ON p.id = s.product_id
		  WHERE s.store_id = $1 AND s.quantity > 0 AND s.expiry_date IS NOT NULL
		    AND s.expiry_date < CURRENT_DATE
		  ORDER BY s.expiry_date DESC, p.name`, storeID)
}

// ProductUnitFactor returns how much of a product's base unit one of a given
// product unit amounts to, or platform.ErrNotFound.
//
// The product is part of the key rather than trusted from the caller: a unit id
// belonging to a different product would otherwise let "ten sacks" of one
// product move stock of another.
//
// It replaces GetConversion, which read the qty_in_parent column the units
// migration dropped. The two are not the same question. That one asked "how do
// I turn a batch of cases into a batch of pieces"; this one asks "how much does
// one sack take out of the single pool", which is the only question left once
// stock is held in one place.
func (r *Stock) ProductUnitFactor(ctx context.Context, q platform.Querier, unitID, productID uuid.UUID) (stock.UnitFactor, error) {
	f := stock.UnitFactor{UnitID: unitID, ProductID: productID}
	err := q.QueryRowContext(ctx,
		`SELECT label, qty_in_base FROM product_units WHERE id = $1 AND product_id = $2`,
		unitID, productID).Scan(&f.Label, &f.QtyInBase)
	if err != nil {
		return stock.UnitFactor{}, platform.MapError(err)
	}
	return f, nil
}

// ProductCostPrice returns the catalogue cost price for a product in a store,
// used as the opening cost of a batch created without one. A product with no
// price row yields zero rather than an error.
//
// The cost is read from the DEFAULT unit's price row and divided by that unit's
// size, which makes it a cost per BASE unit — the same unit the batch quantity
// is counted in.
//
// Both halves are load-bearing since the units migration. product_prices is now
// unique on (product_id, store_id, product_unit_id) rather than
// (product_id, store_id), so a bare two-column WHERE matches one row per priced
// unit and QueryRow would take an arbitrary one: a 25 kg sack at Rs.1,000 booked
// in as the cost of a single kilo, silently, on whichever row Postgres returned
// first. The division then covers the rarer case of a default unit that is not
// itself worth one of the base unit.
func (r *Stock) ProductCostPrice(ctx context.Context, q platform.Querier, productID, storeID uuid.UUID) (money.Money, error) {
	var cost money.Money
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(pp.cost_price, 0) / pu.qty_in_base
		   FROM product_prices pp
		   JOIN product_units pu
		     ON pu.id = pp.product_unit_id AND pu.product_id = pp.product_id AND pu.is_default
		  WHERE pp.product_id = $1 AND pp.store_id = $2`, productID, storeID).Scan(&cost)
	if err != nil {
		mapped := platform.MapError(err)
		if errors.Is(mapped, platform.ErrNotFound) {
			return money.Zero(), nil
		}
		// The error is returned, not swallowed. inventory.go:217 wrote
		// `_ = tx.QueryRow(...).Scan(&costPrice)`, so a failed read left
		// costPrice at zero and the batch was booked in at no cost at all.
		return money.Zero(), mapped
	}
	return cost, nil
}

// ---------------------------------------------------------------------------
// Writes
// ---------------------------------------------------------------------------

// AddQuantity adds stock to a batch, creating the batch if it does not exist.
//
// The upsert adds relatively — `quantity = stock.quantity + excluded.quantity`
// — so two concurrent receipts of the same batch both land. The row-level lock
// the ON CONFLICT path takes serialises them; neither reads a figure into Go,
// so neither can overwrite the other's work.
//
// Value adds the same way quantity does — `total_cost + excluded.total_cost` —
// because a second delivery into the same batch adds its own cost to the pile
// rather than replacing it. The old column was a per-unit rate and the upsert
// *overwrote* it, so a cheaper second delivery silently repriced the stock
// already on the shelf.
//
// A total cost of zero leaves the stored value alone rather than zeroing it: a
// caller adding stock without stating a cost should not write off the batch's
// cost basis. Dates behave the same way, through COALESCE.
func (r *Stock) AddQuantity(ctx context.Context, q platform.Querier, in stock.Receipt) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO stock (product_id, store_id, batch_no, quantity, total_cost, expiry_date, mfg_date)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (product_id, store_id, batch_no) DO UPDATE SET
			quantity    = stock.quantity + excluded.quantity,
			total_cost  = stock.total_cost + excluded.total_cost,
			expiry_date = COALESCE(excluded.expiry_date, stock.expiry_date),
			mfg_date    = COALESCE(excluded.mfg_date, stock.mfg_date),
			updated_at  = now()`,
		in.ProductID, in.StoreID, in.BatchNo, in.Quantity, in.TotalCost,
		stockDateArg(in.ExpiryDate), stockDateArg(in.MfgDate))
	return platform.MapError(err)
}

// DeductBatch removes qty from one batch identified by its natural key and
// reports whether the deduction actually happened.
//
// This is the idiom from billing.go:39-47, which is the one place the old code
// got right: subtract in SQL, carry the sufficiency test in the WHERE clause,
// and check RowsAffected. Nothing here reads a quantity into Go, so nothing can
// act on a figure that another transaction has already changed.
//
// The guard tests `quantity - reserved_qty >= $4` rather than `quantity >= $4`.
// Deducting into the reserved portion would either sell goods that are already
// promised or violate CHECK (reserved_qty <= quantity).
func (r *Stock) DeductBatch(ctx context.Context, q platform.Querier,
	productID, storeID uuid.UUID, batchNo string, qty money.Quantity) (bool, money.Money, error) {
	return deductBatch(ctx, q,
		`SELECT id, `+chargeExpr+` AS charged FROM stock
		  WHERE product_id = $2 AND store_id = $3 AND batch_no = $4`,
		qty, productID, storeID, batchNo)
}

// DeductBatchByID is DeductBatch addressed by primary key, for the FEFO walk
// which has already resolved the row.
func (r *Stock) DeductBatchByID(ctx context.Context, q platform.Querier,
	id uuid.UUID, qty money.Quantity) (bool, money.Money, error) {
	return deductBatch(ctx, q,
		`SELECT id, `+chargeExpr+` AS charged FROM stock WHERE id = $2`,
		qty, id)
}

// chargeExpr is the value a deduction of $1 takes out of a batch.
//
// Taking the whole batch takes exactly what is left in it, with no division, so
// the last sale of a delivery closes its value to zero. Taking part takes a
// proportional share rounded to paise, and because the same figure is subtracted
// from total_cost, the residue of that rounding stays on the shelf with the
// goods instead of evaporating. Sell the rest later and the two halves add back
// to what was paid.
//
// LEAST is a belt: under READ COMMITTED a second deducting statement can be
// planned against the pre-update snapshot and applied to the post-update row, so
// its share can be a paisa larger than what is left. Conservation still holds —
// the subtraction is from the live value, and the residue keeps the difference —
// but the clamp is what guarantees the non-negative constraint cannot be
// tripped by that paisa.
const chargeExpr = `LEAST(total_cost, CASE
	WHEN quantity <= 0 THEN 0
	WHEN quantity <= $1 THEN total_cost
	ELSE round(total_cost * $1 / quantity, 2)
END)`

// deductBatch subtracts quantity and the value that goes with it in one
// statement, and reports what it charged.
//
// The charge is computed in the database from the row being debited, never read
// into Go and computed there: a figure read before the write is a figure another
// till can have changed, and this is the hottest row in the system.
//
// locator must select (id, charged) for exactly the batch to debit, with the
// quantity as $1 and its own parameters from $2 on.
func deductBatch(ctx context.Context, q platform.Querier, locator string,
	qty money.Quantity, args ...any) (bool, money.Money, error) {
	params := append([]any{qty}, args...)
	var charged money.Money
	err := q.QueryRowContext(ctx,
		`WITH target AS (`+locator+`)
		 UPDATE stock s
		    SET quantity   = s.quantity - $1,
		        total_cost = s.total_cost - t.charged,
		        updated_at = now()
		   FROM target t
		  WHERE s.id = t.id AND s.quantity - s.reserved_qty >= $1
		 RETURNING t.charged`, params...).Scan(&charged)
	if errors.Is(err, sql.ErrNoRows) {
		// The guard refused the write, or there is no such batch. Nothing was
		// oversold and nothing was charged.
		return false, money.Zero(), nil
	}
	if err != nil {
		return false, money.Zero(), platform.MapError(err)
	}
	return true, charged, nil
}

// Reserve holds qty of a batch against a pending order and reports whether the
// reservation was taken.
//
// The guard is what keeps reserved_qty inside CHECK (reserved_qty <= quantity).
// The old handler read both figures into Go and compared them there
// (inventory.go:177-184), so a concurrent reservation or a concurrent removal
// between the read and the write produced a constraint violation — a 500 with a
// SQLSTATE in it — rather than "that stock is already spoken for".
func (r *Stock) Reserve(ctx context.Context, q platform.Querier,
	productID, storeID uuid.UUID, batchNo string, qty money.Quantity) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE stock SET reserved_qty = reserved_qty + $4, updated_at = now()
		  WHERE product_id = $1 AND store_id = $2 AND batch_no = $3
		    AND reserved_qty + $4 <= quantity`,
		productID, storeID, batchNo, qty)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n == 1, nil
}

// Release returns qty of a batch to general availability and reports whether
// there was that much reserved to release. The guard keeps reserved_qty at or
// above zero, which inventory.go:194-201 could not.
func (r *Stock) Release(ctx context.Context, q platform.Querier,
	productID, storeID uuid.UUID, batchNo string, qty money.Quantity) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE stock SET reserved_qty = reserved_qty - $4, updated_at = now()
		  WHERE product_id = $1 AND store_id = $2 AND batch_no = $3
		    AND reserved_qty >= $4`,
		productID, storeID, batchNo, qty)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n == 1, nil
}

// ---------------------------------------------------------------------------
// Audit trail
// ---------------------------------------------------------------------------

// InsertAdjustment records one movement in the stock audit trail and stamps the
// entry with the time the database assigned.
//
// It takes a pointer so that time reaches the caller. Returning the struct that
// went in reports created_at as year 0001, which is not a time anything
// happened — and the response to a movement is exactly where somebody looks to
// confirm when it was booked.
func (r *Stock) InsertAdjustment(ctx context.Context, q platform.Querier, a *stock.Adjustment) error {
	err := q.QueryRowContext(ctx,
		`INSERT INTO stock_adjustments
			(id, product_id, store_id, batch_no, quantity, adjustment_type, reason, user_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING created_at`,
		a.ID, a.ProductID, a.StoreID, a.BatchNo, a.Quantity, string(a.Type), a.Reason, a.UserID).
		Scan(&a.CreatedAt)
	return platform.MapError(err)
}

// ListAdjustments returns the audit trail for a store, newest first.
func (r *Stock) ListAdjustments(ctx context.Context, q platform.Querier, f stock.AdjustmentFilter) ([]stock.Adjustment, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT sa.id, sa.product_id, p.name, sa.store_id, sa.batch_no, sa.quantity,
		sa.adjustment_type, COALESCE(sa.reason, ''), sa.user_id, sa.created_at
		FROM stock_adjustments sa
		JOIN products p ON p.id = sa.product_id
		WHERE sa.store_id = $1`)
	args := []any{f.StoreID}
	if f.ProductID != nil {
		args = append(args, *f.ProductID)
		fmt.Fprintf(&sb, ` AND sa.product_id = $%d`, len(args))
	}
	// The limit is a bound parameter, not string concatenation. The old handler
	// pasted it into the SQL text (inventory.go:659).
	args = append(args, clampStockLimit(f.Limit))
	fmt.Fprintf(&sb, ` ORDER BY sa.created_at DESC, sa.id DESC LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	adjustments := make([]stock.Adjustment, 0)
	for rows.Next() {
		var a stock.Adjustment
		var kind string
		if err := rows.Scan(&a.ID, &a.ProductID, &a.ProductName, &a.StoreID, &a.BatchNo,
			&a.Quantity, &kind, &a.Reason, &a.UserID, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan adjustment: %w", platform.MapError(err))
		}
		a.Type = stock.AdjustmentType(kind)
		adjustments = append(adjustments, a)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return adjustments, nil
}

// InsertLoss records an inventory write-off.
func (r *Stock) InsertLoss(ctx context.Context, q platform.Querier, l stock.Loss) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO inventory_losses
			(id, product_id, store_id, batch_no, quantity, loss_type, reason, user_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		l.ID, l.ProductID, l.StoreID, l.BatchNo, l.Quantity, l.LossType, l.Reason, l.UserID)
	return platform.MapError(err)
}

// ListLosses returns recorded losses for a store, newest first.
func (r *Stock) ListLosses(ctx context.Context, q platform.Querier, f stock.LossFilter) ([]stock.Loss, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT id, product_id, store_id, batch_no, quantity, loss_type,
		COALESCE(reason, ''), user_id, created_at
		FROM inventory_losses WHERE store_id = $1`)
	args := []any{f.StoreID}
	if f.LossType != "" {
		args = append(args, f.LossType)
		fmt.Fprintf(&sb, ` AND loss_type = $%d`, len(args))
	}
	args = append(args, clampStockLimit(f.Limit))
	fmt.Fprintf(&sb, ` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	losses := make([]stock.Loss, 0)
	for rows.Next() {
		var l stock.Loss
		if err := rows.Scan(&l.ID, &l.ProductID, &l.StoreID, &l.BatchNo, &l.Quantity,
			&l.LossType, &l.Reason, &l.UserID, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan loss: %w", platform.MapError(err))
		}
		losses = append(losses, l)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return losses, nil
}

func clampStockLimit(limit int) int {
	if limit <= 0 {
		return defaultStockLimit
	}
	if limit > maxStockLimit {
		return maxStockLimit
	}
	return limit
}
