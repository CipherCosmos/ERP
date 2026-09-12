package postgres

import (
	"context"
	"fmt"
	"strings"

	"gbos/internal/domain/reconcile"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// Reconcile reads and writes the exceptions raised when a sale the server could
// not fully verify had to be accepted anyway.
type Reconcile struct{}

// NewReconcile returns a reconciliation repository.
func NewReconcile() *Reconcile { return &Reconcile{} }

const exceptionColumns = `e.id, e.store_id, e.terminal_id, e.invoice_id, e.product_id, e.customer_id,
	e.kind, e.expected, e.actual, e.detail,
	e.resolved_at, e.resolved_by, e.resolved_note, e.created_at`

// Insert records an exception.
//
// It runs inside the transaction that writes the sale, deliberately. An
// exception written afterwards can be lost to a crash between the two, and the
// sale would then stand with nothing saying it needs checking — which is worse
// than either outcome on its own, because nobody would know to look.
func (r *Reconcile) Insert(ctx context.Context, q platform.Querier, e reconcile.Exception) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO sync_exceptions
			(id, store_id, terminal_id, invoice_id, product_id, customer_id,
			 kind, expected, actual, detail)
		 VALUES ($1,$2,$3,$4,$5,$6,$7::sync_exception_kind,$8,$9,$10)`,
		e.ID, e.StoreID, e.TerminalID, e.InvoiceID, e.ProductID, e.CustomerID,
		string(e.Kind), e.Expected, e.Actual, e.Detail)
	return platform.MapError(err)
}

// List returns a store's exceptions, newest first.
func (r *Reconcile) List(ctx context.Context, q platform.Querier, f reconcile.Filter) ([]reconcile.Exception, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + exceptionColumns + `,
		COALESCE(p.name, ''), COALESCE(i.invoice_no, '')
	   FROM sync_exceptions e
	   LEFT JOIN products p ON p.id = e.product_id
	   LEFT JOIN invoices i ON i.id = e.invoice_id
	  WHERE e.store_id = $1`)
	args := []any{f.StoreID}

	if !f.IncludeResolved {
		sb.WriteString(` AND e.resolved_at IS NULL`)
	}
	if f.Kind != "" {
		args = append(args, string(f.Kind))
		fmt.Fprintf(&sb, ` AND e.kind = $%d::sync_exception_kind`, len(args))
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	args = append(args, limit)
	fmt.Fprintf(&sb, ` ORDER BY e.created_at DESC LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	out := make([]reconcile.Exception, 0)
	for rows.Next() {
		var e reconcile.Exception
		if err := rows.Scan(&e.ID, &e.StoreID, &e.TerminalID, &e.InvoiceID, &e.ProductID, &e.CustomerID,
			&e.Kind, &e.Expected, &e.Actual, &e.Detail,
			&e.ResolvedAt, &e.ResolvedBy, &e.ResolvedNote, &e.CreatedAt,
			&e.ProductName, &e.InvoiceNo); err != nil {
			return nil, fmt.Errorf("scan sync exception: %w", platform.MapError(err))
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return out, nil
}

// Resolve marks one exception dealt with, and reports whether it was still open.
//
// The guard carries both the store and the open predicate, so resolving twice
// changes nothing and resolving another shop's exception is not possible. It is
// an UPDATE and not a DELETE on purpose: how many of these a shop raises in a
// month is the signal that its offline window is too long or its counts are
// wrong, and that signal disappears if they can be tidied away.
func (r *Reconcile) Resolve(ctx context.Context, q platform.Querier,
	storeID, id, userID uuid.UUID, note string) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE sync_exceptions
		    SET resolved_at = now(), resolved_by = $3, resolved_note = $4
		  WHERE id = $1 AND store_id = $2 AND resolved_at IS NULL`,
		id, storeID, userID, note)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n == 1, nil
}

// CountOpen is the badge on the reconciliation screen: how many things are
// waiting for somebody.
func (r *Reconcile) CountOpen(ctx context.Context, q platform.Querier, storeID uuid.UUID) (int, error) {
	var n int
	if err := q.QueryRowContext(ctx,
		`SELECT count(*) FROM sync_exceptions WHERE store_id = $1 AND resolved_at IS NULL`,
		storeID).Scan(&n); err != nil {
		return 0, platform.MapError(err)
	}
	return n, nil
}
