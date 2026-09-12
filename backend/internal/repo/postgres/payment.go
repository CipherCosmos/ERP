package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	"gbos/internal/domain/payment"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// Payment reads and writes how invoices were settled.
type Payment struct{}

// NewPayment returns a payment repository.
func NewPayment() *Payment { return &Payment{} }

// SettleableInvoice is what settling an invoice needs to know about it: how
// much is owed, and whether the sale still stands.
type SettleableInvoice struct {
	ID      uuid.UUID
	StoreID uuid.UUID
	Total   money.Money
	Status  billing.Status
}

const settleableInvoiceColumns = `i.id, i.store_id, i.total, i.status`

// GetInvoiceToSettle reads the invoice a payment is being recorded against,
// scoped to a store.
//
// The store is a parameter, not something inferred from the invoice id. An id
// alone is a bearer token: anyone holding one could otherwise read or settle
// another shop's sale.
func (r *Payment) GetInvoiceToSettle(ctx context.Context, q platform.Querier, storeID, invoiceID uuid.UUID) (SettleableInvoice, error) {
	return scanSettleableInvoice(q.QueryRowContext(ctx,
		`SELECT `+settleableInvoiceColumns+` FROM invoices i WHERE i.id = $1 AND i.store_id = $2`,
		invoiceID, storeID))
}

// LockInvoiceToSettle is GetInvoiceToSettle with the invoice row locked FOR
// UPDATE, and it is the guard that makes settling an invoice happen at most
// once.
//
// The lock is what the implementation this replaces was missing. That one ran
// `SELECT COUNT(*) FROM invoice_payments WHERE invoice_id = ?` with the scan
// error discarded, so a failed count read as zero and let a second settlement
// straight through; and even with the error checked, two concurrent requests
// under READ COMMITTED would both see a count of zero and both write. Taking
// the invoice row first serialises them: the second request waits, and by the
// time it reads the count the first has committed its payments.
func (r *Payment) LockInvoiceToSettle(ctx context.Context, tx platform.Tx, storeID, invoiceID uuid.UUID) (SettleableInvoice, error) {
	return scanSettleableInvoice(tx.QueryRowContext(ctx,
		`SELECT `+settleableInvoiceColumns+` FROM invoices i WHERE i.id = $1 AND i.store_id = $2 FOR UPDATE`,
		invoiceID, storeID))
}

func scanSettleableInvoice(row interface{ Scan(...any) error }) (SettleableInvoice, error) {
	var inv SettleableInvoice
	if err := row.Scan(&inv.ID, &inv.StoreID, &inv.Total, &inv.Status); err != nil {
		return SettleableInvoice{}, platform.MapError(err)
	}
	return inv, nil
}

// HasPayments reports whether an invoice has already been settled.
//
// The error is returned rather than discarded. That is the whole difference
// between this and the guard it replaces: a failed read there produced a zero
// count, which read as "not yet settled" and allowed the write. A guard that
// fails open is not a guard.
func (r *Payment) HasPayments(ctx context.Context, q platform.Querier, invoiceID uuid.UUID) (bool, error) {
	var exists bool
	if err := q.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM invoice_payments WHERE invoice_id = $1)`,
		invoiceID).Scan(&exists); err != nil {
		return false, platform.MapError(err)
	}
	return exists, nil
}

// InsertPayments writes the tenders that settle an invoice.
func (r *Payment) InsertPayments(ctx context.Context, q platform.Querier, payments []payment.Payment) error {
	for _, p := range payments {
		if _, err := q.ExecContext(ctx,
			`INSERT INTO invoice_payments (id, invoice_id, payment_mode, amount, reference, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6)`,
			p.ID, p.InvoiceID, string(p.Mode), p.Amount, nullIfBlank(p.Reference), p.CreatedAt); err != nil {
			return platform.MapError(err)
		}
	}
	return nil
}

// PaymentsForInvoice returns how an invoice was settled, scoped to a store.
//
// The join to invoices is the scoping. Without it the store parameter would be
// decoration and the invoice id would be enough to read another shop's takings.
func (r *Payment) PaymentsForInvoice(ctx context.Context, q platform.Querier, storeID, invoiceID uuid.UUID) ([]payment.Payment, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT p.id, p.invoice_id, p.payment_mode, p.amount, COALESCE(p.reference, ''), p.created_at
		   FROM invoice_payments p
		   JOIN invoices i ON i.id = p.invoice_id
		  WHERE p.invoice_id = $1 AND i.store_id = $2
		  ORDER BY p.created_at, p.id`, invoiceID, storeID)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	out := make([]payment.Payment, 0)
	for rows.Next() {
		var p payment.Payment
		var mode string
		// The error is checked. The implementation this replaces discarded it,
		// so a row that failed to decode was appended as a zero-value payment
		// and reported to the caller as real.
		if err := rows.Scan(&p.ID, &p.InvoiceID, &mode, &p.Amount, &p.Reference, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan invoice payment: %w", platform.MapError(err))
		}
		p.Mode = billing.PaymentMode(mode)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return out, nil
}

// DaySales is a day's completed trading, before it is broken down by how it
// was settled.
type DaySales struct {
	Total money.Money
	Count int
}

// SalesForDay returns what a store's completed invoices came to on one day.
//
// `day` is text in payment.DateLayout, not a timestamp. A timestamp would be
// cast to a date using the session's time zone, which would make the day a
// report covers depend on which connection served it.
//
// status = 'completed' is preserved from the query this replaces, and it is
// the one thing that query got right: a voided sale must not appear in the
// day's takings.
func (r *Payment) SalesForDay(ctx context.Context, q platform.Querier, storeID uuid.UUID, day string) (DaySales, error) {
	var out DaySales
	if err := q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(i.total), 0), COUNT(*)
		   FROM invoices i
		  WHERE i.store_id = $1 AND i.invoice_date = $2::date AND i.status = 'completed'`,
		storeID, day).Scan(&out.Total, &out.Count); err != nil {
		return DaySales{}, platform.MapError(err)
	}
	return out, nil
}

// TendersForDay returns a store's settlement for one day, grouped by recorded
// mode.
//
// One query replaces the two the implementation this supersedes ran, and the
// eleven accumulators it folded them into by hand. The union is what makes it
// one query: an invoice is accounted for either by its own tender rows or, if
// it has none, by the mode on its header — never by both, which is what the
// NOT EXISTS guarantees.
//
// The raw mode is lowered and trimmed here and folded onto the vocabulary in
// the domain, so the closed list of payment modes lives in exactly one place.
func (r *Payment) TendersForDay(ctx context.Context, q platform.Querier, storeID uuid.UUID, day string) ([]payment.TenderGroup, error) {
	rows, err := q.QueryContext(ctx,
		`WITH day_invoices AS (
		     SELECT i.id, i.total, i.payment_mode
		       FROM invoices i
		      WHERE i.store_id = $1
		        AND i.invoice_date = $2::date
		        AND i.status = 'completed'
		 ),
		 tenders AS (
		     SELECT lower(btrim(p.payment_mode)) AS mode, p.amount
		       FROM invoice_payments p
		       JOIN day_invoices d ON d.id = p.invoice_id
		     UNION ALL
		     SELECT lower(btrim(d.payment_mode)), d.total
		       FROM day_invoices d
		      WHERE NOT EXISTS (SELECT 1 FROM invoice_payments p WHERE p.invoice_id = d.id)
		 )
		 SELECT mode, SUM(amount), COUNT(*) FROM tenders GROUP BY mode ORDER BY mode`,
		storeID, day)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	out := make([]payment.TenderGroup, 0)
	for rows.Next() {
		var g payment.TenderGroup
		if err := rows.Scan(&g.Mode, &g.Amount, &g.Count); err != nil {
			return nil, fmt.Errorf("scan settlement group: %w", platform.MapError(err))
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return out, nil
}

// RefundsForDay returns what a store paid back over the counter on one day.
//
// returns.created_at is a timestamptz while invoices.invoice_date is a date,
// so the range is half-open against the same $2::date the sales query uses.
// Comparing that way also lets the index do the work, which `created_at::date
// = $2` would prevent.
//
// This is a read into the returns aggregate. It is reached through payment's
// RefundReader port so that when returns grows its own service, the wiring
// changes and nothing here does.
func (r *Payment) RefundsForDay(ctx context.Context, q platform.Querier, storeID uuid.UUID, day string) (money.Money, error) {
	var total money.Money
	if err := q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(r.amount), 0)
		   FROM returns r
		  WHERE r.store_id = $1
		    AND r.created_at >= $2::date
		    AND r.created_at < $2::date + 1
		    AND r.status = 'completed'`,
		storeID, day).Scan(&total); err != nil {
		return money.Zero(), platform.MapError(err)
	}
	return total, nil
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------
//
// processed_requests is shared with billing. The statements are repeated here
// rather than reached for across repositories, because each aggregate owns the
// SQL it sends; the table is the shared thing, not the code.

// FindProcessedPayment returns a previously recorded response for an
// idempotency key.
func (r *Payment) FindProcessedPayment(ctx context.Context, q platform.Querier, key string) ([]byte, bool, error) {
	var body []byte
	err := q.QueryRowContext(ctx,
		`SELECT response_body FROM processed_requests WHERE key = $1`, key).Scan(&body)
	if err != nil {
		mapped := platform.MapError(err)
		if errors.Is(mapped, platform.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, mapped
	}
	return body, true, nil
}

// RecordProcessedPayment stores the response for an idempotency key, inside
// the same transaction as the payments themselves.
func (r *Payment) RecordProcessedPayment(ctx context.Context, q platform.Querier, key, path string, status int, response any) error {
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode idempotent response: %w", err)
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO processed_requests (key, request_path, response_status, response_body)
		 VALUES ($1, $2, $3, $4)`, key, path, status, body)
	return platform.MapError(err)
}
