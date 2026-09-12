package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"gbos/internal/domain/money"
	"gbos/internal/domain/salesreturn"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// Limits for the returns and losses listings.
const (
	salesReturnDefaultLimit = 50
	salesReturnMaxLimit     = 500
)

// SalesReturn reads and writes customer returns and inventory losses.
//
// It writes no stock SQL. Both documents in this aggregate cause stock to move,
// but choosing a batch, layering cost and keeping the non-negative invariant
// belong to the stock aggregate; this file only records what happened.
type SalesReturn struct{}

// NewSalesReturn returns a returns-and-losses repository.
func NewSalesReturn() *SalesReturn { return &SalesReturn{} }

// ---------------------------------------------------------------------------
// Customer returns
// ---------------------------------------------------------------------------

const salesReturnColumns = `r.id, r.invoice_id, r.product_id, r.store_id, r.quantity,
	r.amount, r.reason, COALESCE(r.refund_mode, ''), r.status, r.created_at`

func scanSalesReturn(s interface{ Scan(...any) error }) (salesreturn.Return, error) {
	var ret salesreturn.Return
	var status string
	err := s.Scan(&ret.ID, &ret.InvoiceID, &ret.ProductID, &ret.StoreID, &ret.Quantity,
		&ret.Amount, &ret.Reason, &ret.RefundMode, &status, &ret.CreatedAt)
	ret.Status = salesreturn.Status(status)
	return ret, err
}

// InsertReturn writes a customer return.
//
// The status is cast to the enum explicitly rather than left to the driver's
// inference, and refund_mode goes in as NULL when blank so that "no refund mode
// recorded" is not stored as an empty string that reads back as a value.
func (r *SalesReturn) InsertReturn(ctx context.Context, q platform.Querier, ret salesreturn.Return) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO returns (id, invoice_id, product_id, store_id, quantity, amount,
			reason, refund_mode, status)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::return_status)`,
		ret.ID, ret.InvoiceID, ret.ProductID, ret.StoreID, ret.Quantity, ret.Amount,
		ret.Reason, nullIfBlank(ret.RefundMode), string(ret.Status))
	return platform.MapError(err)
}

// GetReturn returns one customer return, scoped to a store.
//
// The store filter is not decoration. Approval reads this row to decide where
// the goods go, and the implementation this replaces selected on `id` alone
// (returns.go:131) and then restored the stock into whichever store the caller
// named in the query string — so one shop could approve another shop's return
// and have the goods land on its own shelf.
func (r *SalesReturn) GetReturn(ctx context.Context, q platform.Querier, storeID, id uuid.UUID) (salesreturn.Return, error) {
	ret, err := scanSalesReturn(q.QueryRowContext(ctx,
		`SELECT `+salesReturnColumns+` FROM returns r WHERE r.id = $1 AND r.store_id = $2`, id, storeID))
	if err != nil {
		return salesreturn.Return{}, platform.MapError(err)
	}
	return ret, nil
}

// ApproveReturn moves a return from pending to completed and reports whether it
// was this call that did it.
//
// The guard is in the UPDATE, not in a preceding read. Approval is the event
// that puts goods back on the shelf, so an approval that ran twice would put
// them back twice and invent stock out of nothing. The implementation this
// replaces issued `UPDATE returns SET status = 'completed' WHERE id = ?` with
// no status predicate and never looked at the status at all, which made the
// whole operation replayable: every click on Approve restored the quantity
// again.
//
// The store predicate is part of the same guard, so a caller cannot approve a
// return belonging to a store it has no business in.
func (r *SalesReturn) ApproveReturn(ctx context.Context, q platform.Querier, storeID, id uuid.UUID) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE returns SET status = 'completed'
		  WHERE id = $1 AND store_id = $2 AND status = 'pending'`, id, storeID)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n == 1, nil
}

// ListReturns returns customer returns matching a filter, newest first.
func (r *SalesReturn) ListReturns(ctx context.Context, q platform.Querier, f salesreturn.ReturnFilter) ([]salesreturn.Return, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + salesReturnColumns + ` FROM returns r WHERE r.store_id = $1`)
	args := []any{f.StoreID}

	if f.ProductID != nil {
		args = append(args, *f.ProductID)
		fmt.Fprintf(&sb, ` AND r.product_id = $%d`, len(args))
	}
	if f.InvoiceID != nil {
		args = append(args, *f.InvoiceID)
		fmt.Fprintf(&sb, ` AND r.invoice_id = $%d`, len(args))
	}
	if f.Status != "" {
		args = append(args, string(f.Status))
		fmt.Fprintf(&sb, ` AND r.status = $%d::return_status`, len(args))
	}

	// The limit is a bound parameter, not string concatenation into the SQL
	// text, which is what the old handler did (returns.go:82).
	args = append(args, clampSalesReturnLimit(f.Limit))
	fmt.Fprintf(&sb, ` ORDER BY r.created_at DESC, r.id DESC LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	returns := make([]salesreturn.Return, 0)
	for rows.Next() {
		ret, err := scanSalesReturn(rows)
		if err != nil {
			// Never skipped. The old listing discarded the scan error entirely
			// (returns.go:98), so a row that failed to read was emitted as a
			// zero-valued return with a blank id.
			return nil, fmt.Errorf("scan return: %w", platform.MapError(err))
		}
		returns = append(returns, ret)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return returns, nil
}

// SoldFrom is what the sale a return refers to can tell us about it.
type SoldFrom struct {
	// InvoiceStatus is the invoice's own status, which decides whether
	// approving this return would restore goods a void has already restored.
	InvoiceStatus string

	// LineFound reports whether the invoice actually carried this product. A
	// return naming an invoice that never sold the product is a data error, not
	// a return.
	LineFound bool

	// BatchNo is the lot the goods left on, taken from the invoice line. It is
	// the empty string for goods that are not batch-tracked, which is a real
	// value and not an absence — LineFound says which.
	BatchNo string

	// SoldQuantity is how much of this product the invoice sold.
	SoldQuantity money.Quantity

	// ReturnedQuantity is how much of it has already been recorded as returned
	// against this invoice, whatever the status of those returns.
	ReturnedQuantity money.Quantity

	// SoldCost is what the goods on those lines cost the shop, in full. A
	// return puts back a proportional share of it, so goods coming back are
	// worth what they were worth going out and the shop's inventory value does
	// not move because a customer changed their mind.
	SoldCost money.Money
}

// SoldFrom looks up the sale a customer return refers to.
//
// It answers three questions in one round trip: is the invoice still live, did
// it actually sell this product, and which lot did the goods leave on. The
// third is what lets an approved return go back into the batch it came from
// rather than into whichever row the old handler's
// `SELECT batch_no FROM stock ... LIMIT 1` happened to find first.
//
// The invoice is looked up scoped to the store, so a return cannot borrow
// another shop's invoice to name a batch with.
func (r *SalesReturn) SoldFrom(ctx context.Context, q platform.Querier,
	storeID, invoiceID, productID uuid.UUID) (SoldFrom, error) {
	var out SoldFrom
	var batch sql.NullString
	err := q.QueryRowContext(ctx,
		`SELECT i.status,
		        (SELECT ii.batch_no FROM invoice_items ii
		          WHERE ii.invoice_id = i.id AND ii.product_id = $3
		          ORDER BY ii.created_at, ii.id LIMIT 1),
		        COALESCE((SELECT SUM(ii.quantity) FROM invoice_items ii
		                   WHERE ii.invoice_id = i.id AND ii.product_id = $3), 0),
		        COALESCE((SELECT SUM(rr.quantity) FROM returns rr
		                   WHERE rr.invoice_id = i.id AND rr.product_id = $3), 0),
		        COALESCE((SELECT SUM(ii.cost_of_goods) FROM invoice_items ii
		                   WHERE ii.invoice_id = i.id AND ii.product_id = $3), 0)
		   FROM invoices i
		  WHERE i.id = $1 AND i.store_id = $2`,
		invoiceID, storeID, productID).
		Scan(&out.InvoiceStatus, &batch, &out.SoldQuantity, &out.ReturnedQuantity, &out.SoldCost)
	if err != nil {
		return SoldFrom{}, platform.MapError(err)
	}
	out.LineFound = batch.Valid
	out.BatchNo = batch.String
	return out, nil
}

// ---------------------------------------------------------------------------
// Inventory losses
// ---------------------------------------------------------------------------

const salesLossColumns = `l.id, l.product_id, l.store_id, l.batch_no, l.quantity,
	l.loss_type, COALESCE(l.reason, ''), l.user_id, l.created_at`

// InsertLoss records an inventory write-off.
//
// Writing this row is the *only* thing this method does. The goods leave
// through the stock aggregate, inside the same transaction, and this insert is
// deliberately powerless to record a loss that did not move any stock — the
// service will not reach it unless the deduction has already succeeded.
func (r *SalesReturn) InsertLoss(ctx context.Context, q platform.Querier, l salesreturn.Loss) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO inventory_losses
			(id, product_id, store_id, batch_no, quantity, loss_type, reason, user_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		l.ID, l.ProductID, l.StoreID, l.BatchNo, l.Quantity, l.LossType,
		nullIfBlank(l.Reason), l.UserID)
	return platform.MapError(err)
}

// ListLosses returns recorded write-offs for a store, newest first.
func (r *SalesReturn) ListLosses(ctx context.Context, q platform.Querier, f salesreturn.LossFilter) ([]salesreturn.Loss, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + salesLossColumns + ` FROM inventory_losses l WHERE l.store_id = $1`)
	args := []any{f.StoreID}

	if f.ProductID != nil {
		args = append(args, *f.ProductID)
		fmt.Fprintf(&sb, ` AND l.product_id = $%d`, len(args))
	}
	if f.LossType != "" {
		args = append(args, f.LossType)
		fmt.Fprintf(&sb, ` AND l.loss_type = $%d`, len(args))
	}
	args = append(args, clampSalesReturnLimit(f.Limit))
	fmt.Fprintf(&sb, ` ORDER BY l.created_at DESC, l.id DESC LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	losses := make([]salesreturn.Loss, 0)
	for rows.Next() {
		var l salesreturn.Loss
		if err := rows.Scan(&l.ID, &l.ProductID, &l.StoreID, &l.BatchNo, &l.Quantity,
			&l.LossType, &l.Reason, &l.UserID, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan inventory loss: %w", platform.MapError(err))
		}
		losses = append(losses, l)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return losses, nil
}

func clampSalesReturnLimit(limit int) int {
	if limit <= 0 {
		return salesReturnDefaultLimit
	}
	if limit > salesReturnMaxLimit {
		return salesReturnMaxLimit
	}
	return limit
}
