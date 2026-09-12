package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gbos/internal/domain/credit"
	"gbos/internal/domain/money"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// creditOpenStatuses is THE definition, in SQL, of "this ledger row is still
// owed". Every read in this file uses it and nothing in this file writes the
// predicate any other way.
//
// It exists as one constant because the implementation this replaces had the
// rule written out by hand at six call sites, in two mutually incompatible
// forms:
//
//	status IN ('pending','partial')  correct  — billing.go:317, credit.go:320,
//	                                            analytics.go:80
//	status != 'paid'                 wrong    — credit.go:98, credit.go:402,
//	                                            analytics.go:624
//
// The wrong form counts two things as debt that are not. It counts 'voided'
// rows, which are cancelled invoices. And, because the old payment insert
// wrote a bogus status = 'completed' (a value the Postgres enum deliberately
// does not have), it counted every payment ever received as a further debt —
// so a customer's balance on the customer screen and on the dashboard could
// differ by twice their total payments.
//
// It is written without a table alias so it composes with any: `ce.status IN`
// + creditOpenStatuses.
const creditOpenStatuses = `('pending', 'partial')`

// Listing bounds. Named with a credit prefix because several aggregates share
// this package.
const (
	creditDefaultCustomerLimit = 50
	creditMaxCustomerLimit     = 200
	creditDefaultEntryLimit    = 100
	creditMaxEntryLimit        = 500
)

// Credit reads and writes customers and the customer credit ledger.
type Credit struct{}

// NewCredit returns a credit repository.
func NewCredit() *Credit { return &Credit{} }

// ---------------------------------------------------------------------------
// Customers
// ---------------------------------------------------------------------------

const creditCustomerColumns = `c.id, c.name, COALESCE(c.name_hi, ''), COALESCE(c.phone, ''),
	COALESCE(c.address, ''), COALESCE(c.gstin, ''), c.credit_limit, c.is_wholesale,
	c.store_id, c.is_active`

func creditScanCustomer(s interface{ Scan(...any) error }) (credit.Customer, error) {
	var c credit.Customer
	err := s.Scan(&c.ID, &c.Name, &c.NameHi, &c.Phone, &c.Address, &c.GSTIN,
		&c.CreditLimit, &c.IsWholesale, &c.StoreID, &c.IsActive)
	return c, err
}

// creditAccountFrom is the customer read model with the two ledger totals
// attached. One LATERAL pass over the customer's entries produces both, so a
// listing of 50 customers is 51 index scans rather than 101 correlated
// subqueries.
//
// The two totals are computed differently on purpose. Outstanding is
// SUM(amount - paid_amount) over debt rows only: a payment row is written
// self-settled, so it contributes zero here even before the status filter, and
// the filter then removes voided rows. Advance is SUM(amount) over advance
// rows, because an advance's paid_amount equals its amount and the difference
// would be zero.
const creditAccountFrom = `
	FROM customers c
	LEFT JOIN LATERAL (
		SELECT
			COALESCE(SUM(ce.amount - ce.paid_amount)
				FILTER (WHERE ce.type = 'issue' AND ce.status IN ` + creditOpenStatuses + `), 0) AS outstanding,
			COALESCE(SUM(ce.amount)
				FILTER (WHERE ce.type = 'advance' AND ce.status <> 'voided'), 0) AS advance
		FROM credit_entries ce
		WHERE ce.customer_id = c.id
	) bal ON true`

func creditScanAccount(s interface{ Scan(...any) error }) (credit.Account, error) {
	var a credit.Account
	err := s.Scan(&a.ID, &a.Name, &a.NameHi, &a.Phone, &a.Address, &a.GSTIN,
		&a.CreditLimit, &a.IsWholesale, &a.StoreID, &a.IsActive,
		&a.Outstanding, &a.Advance)
	return a, err
}

// CreateCustomer inserts a customer.
func (r *Credit) CreateCustomer(ctx context.Context, q platform.Querier, c credit.Customer) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO customers (id, name, name_hi, phone, address, gstin,
			credit_limit, is_wholesale, store_id, is_active)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		c.ID, c.Name, c.NameHi, nullIfBlank(c.Phone), nullIfBlank(c.Address),
		nullIfBlank(c.GSTIN), c.CreditLimit, c.IsWholesale, c.StoreID, c.IsActive)
	return platform.MapError(err)
}

// UpdateCustomer replaces the editable fields of a customer and reports
// whether a row changed.
//
// The store is part of the WHERE clause, not merely checked beforehand. A
// caller-supplied customer id is not evidence of anything: the code this
// replaces let a cashier PATCH any customer id in the system, including one
// belonging to another store, and set its credit_limit to whatever it liked.
func (r *Credit) UpdateCustomer(ctx context.Context, q platform.Querier, storeID uuid.UUID, c credit.Customer) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE customers SET
			name = $3, name_hi = $4, phone = $5, address = $6, gstin = $7,
			credit_limit = $8, is_wholesale = $9, is_active = $10, updated_at = now()
		  WHERE id = $1 AND store_id = $2`,
		c.ID, storeID, c.Name, c.NameHi, nullIfBlank(c.Phone), nullIfBlank(c.Address),
		nullIfBlank(c.GSTIN), c.CreditLimit, c.IsWholesale, c.IsActive)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// GetCustomer returns one customer within a store, without the ledger totals.
// It returns platform.ErrNotFound when the customer does not exist in that
// store — a customer belonging to a different store is indistinguishable from
// one that does not exist, which is the point.
func (r *Credit) GetCustomer(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (credit.Customer, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+creditCustomerColumns+` FROM customers c
		  WHERE c.id = $1 AND c.store_id = $2`, id, storeID)
	c, err := creditScanCustomer(row)
	if err != nil {
		return credit.Customer{}, platform.MapError(err)
	}
	return c, nil
}

// LockCustomer is GetCustomer with SELECT ... FOR UPDATE.
//
// It is how the credit-limit check stops being a race. Reading the limit and
// the balance, then inserting an invoice that depends on both, is only sound
// if no concurrent sale can slip in between; taking the customer row's lock
// first serialises credit decisions for that one customer without escalating
// the whole transaction to SERIALIZABLE. Callers must take this lock before
// touching the customer's ledger rows, so that every path locks in the same
// order and none of them can deadlock against another.
//
// It must run inside a transaction; FOR UPDATE outside one locks nothing.
func (r *Credit) LockCustomer(ctx context.Context, tx platform.Tx, id, storeID uuid.UUID) (credit.Customer, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+creditCustomerColumns+` FROM customers c
		  WHERE c.id = $1 AND c.store_id = $2
		  FOR UPDATE`, id, storeID)
	c, err := creditScanCustomer(row)
	if err != nil {
		return credit.Customer{}, platform.MapError(err)
	}
	return c, nil
}

// GetAccount returns a customer together with what they owe and any advance
// held for them.
func (r *Credit) GetAccount(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (credit.Account, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+creditCustomerColumns+`, bal.outstanding, bal.advance`+creditAccountFrom+`
		  WHERE c.id = $1 AND c.store_id = $2`, id, storeID)
	a, err := creditScanAccount(row)
	if err != nil {
		return credit.Account{}, platform.MapError(err)
	}
	return a, nil
}

// ListAccounts returns the customers of one store with their balances.
func (r *Credit) ListAccounts(ctx context.Context, q platform.Querier, f credit.CustomerFilter) ([]credit.Account, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + creditCustomerColumns + `, bal.outstanding, bal.advance` +
		creditAccountFrom + ` WHERE c.store_id = $1`)
	args := []any{f.StoreID}

	if !f.IncludeInactive {
		sb.WriteString(` AND c.is_active`)
	}
	if f.Search != "" {
		// ILIKE, not LIKE. SQLite's LIKE is case-insensitive for ASCII and
		// Postgres's is not, so a straight port would have made every customer
		// search silently case-sensitive.
		args = append(args, "%"+escapeLike(f.Search)+"%")
		fmt.Fprintf(&sb, ` AND (c.name ILIKE $%d ESCAPE '\' OR c.phone ILIKE $%d ESCAPE '\')`, len(args), len(args))
	}

	limit := f.Limit
	if limit <= 0 {
		limit = creditDefaultCustomerLimit
	}
	if limit > creditMaxCustomerLimit {
		limit = creditMaxCustomerLimit
	}
	args = append(args, limit)
	fmt.Fprintf(&sb, ` ORDER BY c.name LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	accounts := make([]credit.Account, 0)
	for rows.Next() {
		a, err := creditScanAccount(rows)
		if err != nil {
			// Never skip a row that failed to scan. The implementation this
			// replaces discarded the scan error and appended whatever the
			// half-filled variables held, so a customer could be listed with a
			// zero credit limit that was not their credit limit.
			return nil, fmt.Errorf("scan customer account: %w", platform.MapError(err))
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return accounts, nil
}

// ---------------------------------------------------------------------------
// Ledger entries
// ---------------------------------------------------------------------------

const creditEntryColumns = `ce.id, ce.customer_id, ce.invoice_id, ce.amount, ce.paid_amount,
	ce.type, ce.due_date, ce.status, ce.created_at`

// creditScanEntry reads an entry, taking the two enum columns through plain
// strings. database/sql would assign them to the named string types directly,
// but going via string keeps this independent of how the driver decides to
// present a user-defined enum OID.
func creditScanEntry(s interface{ Scan(...any) error }) (credit.Entry, error) {
	var (
		e           credit.Entry
		entryType   string
		entryStatus string
	)
	err := s.Scan(&e.ID, &e.CustomerID, &e.InvoiceID, &e.Amount, &e.PaidAmount,
		&entryType, &e.DueDate, &entryStatus, &e.CreatedAt)
	e.Type = credit.Type(entryType)
	e.Status = credit.Status(entryStatus)
	return e, err
}

// CreateEntry inserts a ledger row and returns it with the timestamp the
// database assigned, so the caller's copy and the stored row agree.
func (r *Credit) CreateEntry(ctx context.Context, q platform.Querier, e credit.Entry) (credit.Entry, error) {
	err := q.QueryRowContext(ctx,
		`INSERT INTO credit_entries (id, customer_id, invoice_id, amount, paid_amount, type, due_date, status)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 RETURNING created_at`,
		e.ID, e.CustomerID, e.InvoiceID, e.Amount, e.PaidAmount,
		string(e.Type), e.DueDate, string(e.Status)).Scan(&e.CreatedAt)
	if err != nil {
		return credit.Entry{}, platform.MapError(err)
	}
	return e, nil
}

// GetEntry returns one ledger row within a store.
func (r *Credit) GetEntry(ctx context.Context, q platform.Querier, id, storeID uuid.UUID) (credit.EntryView, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+creditEntryColumns+`, c.name
		   FROM credit_entries ce
		   JOIN customers c ON c.id = ce.customer_id
		  WHERE ce.id = $1 AND c.store_id = $2`, id, storeID)

	var (
		v           credit.EntryView
		entryType   string
		entryStatus string
	)
	err := row.Scan(&v.ID, &v.CustomerID, &v.InvoiceID, &v.Amount, &v.PaidAmount,
		&entryType, &v.DueDate, &entryStatus, &v.CreatedAt, &v.CustomerName)
	if err != nil {
		return credit.EntryView{}, platform.MapError(err)
	}
	v.Type = credit.Type(entryType)
	v.Status = credit.Status(entryStatus)
	return v, nil
}

// ListEntries returns the ledger for one store, newest first.
func (r *Credit) ListEntries(ctx context.Context, q platform.Querier, f credit.EntryFilter) ([]credit.EntryView, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + creditEntryColumns + `, c.name
		FROM credit_entries ce
		JOIN customers c ON c.id = ce.customer_id
		WHERE c.store_id = $1`)
	args := []any{f.StoreID}

	if f.CustomerID != nil {
		args = append(args, *f.CustomerID)
		fmt.Fprintf(&sb, ` AND ce.customer_id = $%d`, len(args))
	}
	if f.Type != nil {
		args = append(args, string(*f.Type))
		fmt.Fprintf(&sb, ` AND ce.type = $%d`, len(args))
	}
	if f.OpenOnly {
		sb.WriteString(` AND ce.status IN ` + creditOpenStatuses)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = creditDefaultEntryLimit
	}
	if limit > creditMaxEntryLimit {
		limit = creditMaxEntryLimit
	}
	args = append(args, limit)
	fmt.Fprintf(&sb, ` ORDER BY ce.created_at DESC, ce.id DESC LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	entries := make([]credit.EntryView, 0)
	for rows.Next() {
		var (
			v           credit.EntryView
			entryType   string
			entryStatus string
		)
		if err := rows.Scan(&v.ID, &v.CustomerID, &v.InvoiceID, &v.Amount, &v.PaidAmount,
			&entryType, &v.DueDate, &entryStatus, &v.CreatedAt, &v.CustomerName); err != nil {
			return nil, fmt.Errorf("scan credit entry: %w", platform.MapError(err))
		}
		v.Type = credit.Type(entryType)
		v.Status = credit.Status(entryStatus)
		entries = append(entries, v)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return entries, nil
}

// LockOpenIssues returns the customer's unsettled credit sales, oldest first,
// locked for update, drained into a slice.
//
// Draining before returning is the whole contract of this method, not an
// implementation detail. The code this replaces held this exact cursor open
// and issued its UPDATE inside `for rows.Next()`. SQLite tolerated it; pgx
// returns "conn busy", because a connection cannot run a statement while a
// result set is still streaming on it. Returning a closed slice makes the
// interleaving unrepresentable rather than merely discouraged.
//
// The rows come back locked so that two cashiers taking payments from the same
// customer at once cannot both read the same due amount and both allocate
// against it. Lock the customer row first — see LockCustomer.
func (r *Credit) LockOpenIssues(ctx context.Context, tx platform.Tx, customerID uuid.UUID) ([]credit.Entry, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+creditEntryColumns+`
		   FROM credit_entries ce
		  WHERE ce.customer_id = $1
		    AND ce.type = 'issue'
		    AND ce.status IN `+creditOpenStatuses+`
		  ORDER BY ce.created_at, ce.id
		  FOR UPDATE`, customerID)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	entries := make([]credit.Entry, 0)
	for rows.Next() {
		e, err := creditScanEntry(rows)
		if err != nil {
			// A skipped row here is a debt that silently escapes settlement.
			// The old loop did `if err := rows.Scan(...); err != nil { continue }`.
			return nil, fmt.Errorf("scan open credit entry: %w", platform.MapError(err))
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return entries, nil
}

// ApplyAllocation writes one allocation: the entry's new paid_amount and the
// status that follows from it.
//
// The status filter in the WHERE clause is deliberate. It means this can only
// ever move a row that is still a debt, so a concurrent settlement or a void
// cannot be overwritten; the caller detects that from the false return rather
// than losing the write silently.
func (r *Credit) ApplyAllocation(ctx context.Context, q platform.Querier, a credit.Allocation) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE credit_entries
		    SET paid_amount = $2, status = $3, updated_at = now()
		  WHERE id = $1 AND status IN `+creditOpenStatuses,
		a.EntryID, a.PaidAmount, string(a.Status))
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// Outstanding returns what a customer still owes.
//
// This is the query the whole aggregate exists to get right. Its correctness
// rests on two writer invariants, both enforced above:
//
//  1. A payment or advance row is written with paid_amount = amount, so its
//     contribution to SUM(amount - paid_amount) is exactly zero. A payment can
//     therefore never inflate the balance, which is what it did before.
//  2. A debt row's paid_amount only ever moves toward its amount, in exact
//     decimal steps, and reaches status 'paid' at the instant the two are
//     equal — so a settled debt leaves this sum, and a partly settled one
//     contributes exactly the remainder.
//
// Given those, the sum over rows that count as debt is precisely
// (credit issued) - (credit settled) - (credit voided).
//
// The type filter is redundant under invariant 1 — a payment row can never be
// pending or partial — but it is cheap, it documents the intent, and it lets
// idx_credit_customer_type_status serve the query.
func (r *Credit) Outstanding(ctx context.Context, q platform.Querier, customerID uuid.UUID) (money.Money, error) {
	var total money.Money
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(ce.amount - ce.paid_amount), 0)
		   FROM credit_entries ce
		  WHERE ce.customer_id = $1
		    AND ce.type = 'issue'
		    AND ce.status IN `+creditOpenStatuses, customerID).Scan(&total)
	if err != nil {
		return money.Zero(), platform.MapError(err)
	}
	return total, nil
}

// AdvanceBalance returns the total of overpayments held for a customer.
func (r *Credit) AdvanceBalance(ctx context.Context, q platform.Querier, customerID uuid.UUID) (money.Money, error) {
	var total money.Money
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(ce.amount), 0)
		   FROM credit_entries ce
		  WHERE ce.customer_id = $1 AND ce.type = 'advance' AND ce.status <> 'voided'`,
		customerID).Scan(&total)
	if err != nil {
		return money.Zero(), platform.MapError(err)
	}
	return total, nil
}

// StoreOutstanding returns the total credit outstanding across one store. This
// is the dashboard figure.
func (r *Credit) StoreOutstanding(ctx context.Context, q platform.Querier, storeID uuid.UUID) (money.Money, error) {
	var total money.Money
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(ce.amount - ce.paid_amount), 0)
		   FROM credit_entries ce
		   JOIN customers c ON c.id = ce.customer_id
		  WHERE c.store_id = $1
		    AND ce.type = 'issue'
		    AND ce.status IN `+creditOpenStatuses, storeID).Scan(&total)
	if err != nil {
		return money.Zero(), platform.MapError(err)
	}
	return total, nil
}

// Overdue returns the customers of one store with credit past its due date as
// of asOf, largest balance first.
//
// asOf is a parameter rather than now() so that the report is deterministic
// and testable; the caller passes today's date.
func (r *Credit) Overdue(ctx context.Context, q platform.Querier, storeID uuid.UUID, asOf time.Time) ([]credit.OverdueCustomer, error) {
	// The HAVING clause repeats the SUM rather than naming the output column.
	// SQLite accepts `HAVING outstanding > 0` against a SELECT alias and
	// Postgres does not — an alias is not in scope in HAVING — so the literal
	// port of this query fails outright at 42703.
	rows, err := q.QueryContext(ctx,
		`SELECT c.id, c.name, COALESCE(c.phone, ''), c.credit_limit,
		        COALESCE(SUM(ce.amount - ce.paid_amount), 0) AS outstanding,
		        COUNT(ce.id) AS due_count,
		        MIN(ce.due_date) AS earliest_due
		   FROM customers c
		   JOIN credit_entries ce ON ce.customer_id = c.id
		  WHERE c.store_id = $1
		    AND ce.type = 'issue'
		    AND ce.status IN `+creditOpenStatuses+`
		    AND ce.due_date IS NOT NULL
		    AND ce.due_date < $2
		  GROUP BY c.id, c.name, c.phone, c.credit_limit
		 HAVING SUM(ce.amount - ce.paid_amount) > 0
		  ORDER BY outstanding DESC, c.name`, storeID, asOf)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	overdue := make([]credit.OverdueCustomer, 0)
	for rows.Next() {
		var o credit.OverdueCustomer
		if err := rows.Scan(&o.CustomerID, &o.Name, &o.Phone, &o.CreditLimit,
			&o.Outstanding, &o.DueCount, &o.EarliestDue); err != nil {
			return nil, fmt.Errorf("scan overdue customer: %w", platform.MapError(err))
		}
		overdue = append(overdue, o)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return overdue, nil
}

// VoidInvoiceEntries marks the credit raised against an invoice as voided and
// returns how many rows changed. A voided row is not a debt: see
// credit.Status.CountsAsDebt.
func (r *Credit) VoidInvoiceEntries(ctx context.Context, q platform.Querier, invoiceID uuid.UUID) (int64, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE credit_entries
		    SET status = 'voided', updated_at = now()
		  WHERE invoice_id = $1 AND status IN `+creditOpenStatuses, invoiceID)
	if err != nil {
		return 0, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, platform.MapError(err)
	}
	return n, nil
}
