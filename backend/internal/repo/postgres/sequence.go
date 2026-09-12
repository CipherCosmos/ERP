package postgres

import (
	"context"
	"fmt"

	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// nextDocumentNumber allocates the next value in a terminal's series for a
// document type and financial year, and must be called inside the same
// transaction as the document it numbers.
//
// The row is taken with SELECT ... FOR UPDATE so concurrent tills serialise on
// it, and the increment rolls back with the rest of the transaction if the
// document is not written — a Postgres SEQUENCE would leave a gap there, and
// CGST Rule 46(b) requires the series to be consecutive.
//
// Invoices and purchase orders share this because they share the rule. Two
// copies of it would eventually disagree about what "consecutive" means, and a
// series is only meaningful while it is unbroken.
func nextDocumentNumber(ctx context.Context, q platform.Querier, storeID, terminalID uuid.UUID, docType, finYear string) (int64, error) {
	// Create the counter if this is the terminal's first document of the year.
	// DO NOTHING rather than DO UPDATE: a concurrent creator blocks here on the
	// index tuple and then finds the row already present, which is the intended
	// outcome.
	if _, err := q.ExecContext(ctx,
		`INSERT INTO document_sequences (store_id, terminal_id, doc_type, fin_year, next_value)
		 VALUES ($1, $2, $3::document_type, $4, 1)
		 ON CONFLICT (store_id, terminal_id, doc_type, fin_year) DO NOTHING`,
		storeID, terminalID, docType, finYear); err != nil {
		return 0, platform.MapError(err)
	}

	var next int64
	if err := q.QueryRowContext(ctx,
		`SELECT next_value FROM document_sequences
		  WHERE store_id = $1 AND terminal_id = $2 AND doc_type = $3::document_type AND fin_year = $4
		  FOR UPDATE`,
		storeID, terminalID, docType, finYear).Scan(&next); err != nil {
		return 0, platform.MapError(err)
	}

	res, err := q.ExecContext(ctx,
		`UPDATE document_sequences SET next_value = next_value + 1, updated_at = now()
		  WHERE store_id = $1 AND terminal_id = $2 AND doc_type = $3::document_type AND fin_year = $4`,
		storeID, terminalID, docType, finYear)
	if err != nil {
		return 0, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, platform.MapError(err)
	}
	if n != 1 {
		return 0, fmt.Errorf("document sequence for terminal %s: expected 1 row updated, got %d", terminalID, n)
	}
	return next, nil
}

// claimDocumentNumber records that a terminal has used a number its own till
// allocated, and moves the counter past it.
//
// A till that bills while the connection is down has to hand the customer a tax
// invoice, and a tax invoice carries a number, so the till allocates from its own
// terminal's series. The counter here stops being the allocator and becomes a
// high-water mark: whatever arrives, the next server-side allocation starts after
// it.
//
// GREATEST rather than assignment. Documents arrive out of order — a till syncs a
// backlog oldest-first, but two tills on one store sync whenever each reconnects —
// and a lower number arriving later must not wind the counter back.
//
// Uniqueness is not this function's job and must not be: invoices_store_no_key
// already refuses a duplicate, atomically, in the same transaction as the
// document. A check here would be a read before a write, which two tills syncing
// at once would race straight through.
func claimDocumentNumber(ctx context.Context, q platform.Querier,
	storeID, terminalID uuid.UUID, docType, finYear string, seq int64) error {
	if seq < 1 {
		return fmt.Errorf("document sequence number must be positive, got %d", seq)
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO document_sequences (store_id, terminal_id, doc_type, fin_year, next_value)
		 VALUES ($1, $2, $3::document_type, $4, $5)
		 ON CONFLICT (store_id, terminal_id, doc_type, fin_year) DO UPDATE
		 SET next_value = GREATEST(document_sequences.next_value, excluded.next_value),
		     updated_at = now()`,
		storeID, terminalID, docType, finYear, seq+1); err != nil {
		return platform.MapError(err)
	}
	return nil
}
