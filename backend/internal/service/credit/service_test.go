package credit_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	domain "gbos/internal/domain/credit"
	"gbos/internal/domain/money"
	platform "gbos/internal/platform/postgres"
	repo "gbos/internal/repo/postgres"
	service "gbos/internal/service/credit"
	"gbos/internal/testsupport"

	"github.com/google/uuid"
)

// newService wires a real repository to a real database. These tests use
// Postgres rather than a mock because the behaviour worth testing here — the
// arithmetic of settlement as the database actually stores it, the CHECK that
// paid_amount never exceeds amount, the enum that has no 'completed' member,
// and the fact that a cursor cannot be held open across a write — lives in the
// database, and a mock would assert only that the code calls itself.
func newService(t *testing.T) (*service.Service, *platform.DB, uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	db, err := platform.Open(ctx, platform.Config{DSN: testsupport.DSN(t)})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	storeID := newStore(t, db, "Test Shop")
	return service.New(db, repo.NewCredit()), db, storeID
}

func newStore(t *testing.T, db *platform.DB, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.QueryRowContext(context.Background(),
		`INSERT INTO stores (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	return id
}

// newCustomer creates a customer with the given credit limit in rupees.
func newCustomer(t *testing.T, svc *service.Service, storeID uuid.UUID, name, limit string) domain.Customer {
	t.Helper()
	c, err := svc.CreateCustomer(context.Background(), storeID, domain.NewCustomerInput{
		Name:        name,
		Phone:       "9876500000",
		CreditLimit: money.MustFromString(limit),
	})
	if err != nil {
		t.Fatalf("create customer %s: %v", name, err)
	}
	return c
}

// issue raises a credit sale. dueDate may be nil for the default 30-day term.
func issue(t *testing.T, svc *service.Service, storeID, customerID uuid.UUID, amount string, dueDate *time.Time) domain.Entry {
	t.Helper()
	e, err := svc.IssueCredit(context.Background(), service.IssueInput{
		StoreID:    storeID,
		CustomerID: customerID,
		Amount:     money.MustFromString(amount),
		DueDate:    dueDate,
	})
	if err != nil {
		t.Fatalf("issue credit of %s: %v", amount, err)
	}
	return e
}

func pay(t *testing.T, svc *service.Service, storeID, customerID uuid.UUID, amount string) service.PaymentResult {
	t.Helper()
	res, err := svc.RecordPayment(context.Background(), service.PaymentInput{
		StoreID:    storeID,
		CustomerID: customerID,
		Amount:     money.MustFromString(amount),
	})
	if err != nil {
		t.Fatalf("record payment of %s: %v", amount, err)
	}
	return res
}

// backdate forces an entry's created_at, so that "oldest first" is a fact
// rather than a hope about clock resolution.
func backdate(t *testing.T, db *platform.DB, entryID uuid.UUID, when time.Time) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`UPDATE credit_entries SET created_at = $2 WHERE id = $1`, entryID, when); err != nil {
		t.Fatalf("backdate entry: %v", err)
	}
}

func entryRow(t *testing.T, db *platform.DB, id uuid.UUID) (amount, paid money.Money, status, entryType string) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(),
		`SELECT amount, paid_amount, status::text, type::text FROM credit_entries WHERE id = $1`, id).
		Scan(&amount, &paid, &status, &entryType); err != nil {
		t.Fatalf("read credit entry: %v", err)
	}
	return amount, paid, status, entryType
}

func mustEqual(t *testing.T, label string, got, want money.Money) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s = %s, want %s", label, got.StringFixed(), want.StringFixed())
	}
}

// ---------------------------------------------------------------------------
// The headline defect
// ---------------------------------------------------------------------------

// TestPaymentClearsTheDebtRatherThanDoublingIt is the test this whole port
// exists for.
//
// The handler being replaced inserted a payment as a new ledger row with
// amount = the payment and paid_amount = 0, while every outstanding query is
// SUM(amount - paid_amount). A customer with a Rs.5,000 credit sale who then
// paid Rs.5,000 in full ended up owing Rs.10,000: the original debt untouched
// at 5,000, plus the payment row contributing another 5,000.
func TestPaymentClearsTheDebtRatherThanDoublingIt(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Ramesh Traders", "50000.00")

	sale := issue(t, svc, storeID, customer.ID, "5000.00", nil)

	before, err := svc.Outstanding(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("outstanding before payment: %v", err)
	}
	mustEqual(t, "outstanding after the credit sale", before, money.MustFromString("5000.00"))

	result := pay(t, svc, storeID, customer.ID, "5000.00")

	after, err := svc.Outstanding(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("outstanding after payment: %v", err)
	}

	// The assertion that matters.
	if !after.IsZero() {
		t.Errorf("outstanding after paying in full = %s, want exactly 0.00", after.StringFixed())
	}
	// And the specific wrong answer, named, so a regression is unmistakable.
	if after.Equal(money.MustFromString("10000.00")) {
		t.Error("recording a payment DOUBLED the balance — the payment row is being counted as debt again")
	}
	mustEqual(t, "the result's own view of the balance", result.Outstanding, money.Zero())
	mustEqual(t, "amount applied to debt", result.Applied, money.MustFromString("5000.00"))

	// The debt is closed, not merely reduced, and the money received is on
	// the ledger as its own row.
	amount, paid, status, _ := entryRow(t, db, sale.ID)
	mustEqual(t, "paid_amount on the settled sale", paid, amount)
	if status != string(domain.StatusPaid) {
		t.Errorf("settled credit entry status = %q, want %q", status, domain.StatusPaid)
	}
	paymentAmount, paymentPaid, paymentStatus, paymentType := entryRow(t, db, result.Payment.ID)
	if paymentType != string(domain.TypePayment) {
		t.Errorf("payment row type = %q, want %q", paymentType, domain.TypePayment)
	}
	if paymentStatus != string(domain.StatusPaid) {
		t.Errorf("payment row status = %q, want %q", paymentStatus, domain.StatusPaid)
	}
	// Self-settled: amount - paid_amount is zero, so no outstanding query can
	// ever read it as a debt.
	mustEqual(t, "payment row residue", paymentAmount.Sub(paymentPaid), money.Zero())
}

// ---------------------------------------------------------------------------
// Allocation
// ---------------------------------------------------------------------------

func TestPartialPaymentLeavesExactlyTheRemainder(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Partial Payer", "50000.00")

	sale := issue(t, svc, storeID, customer.ID, "5000.00", nil)
	pay(t, svc, storeID, customer.ID, "1234.56")

	outstanding, err := svc.Outstanding(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	mustEqual(t, "outstanding after a partial payment", outstanding, money.MustFromString("3765.44"))

	_, paid, status, _ := entryRow(t, db, sale.ID)
	mustEqual(t, "paid_amount on the part-settled entry", paid, money.MustFromString("1234.56"))
	if status != string(domain.StatusPartial) {
		t.Errorf("part-settled entry status = %q, want %q", status, domain.StatusPartial)
	}
}

func TestPaymentSettlesOldestDebtsFirst(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Serial Buyer", "50000.00")

	oldest := issue(t, svc, storeID, customer.ID, "1000.00", nil)
	middle := issue(t, svc, storeID, customer.ID, "2000.00", nil)
	newest := issue(t, svc, storeID, customer.ID, "3000.00", nil)
	backdate(t, db, oldest.ID, time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	backdate(t, db, middle.ID, time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC))
	backdate(t, db, newest.ID, time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC))

	// 2,500 covers the 1,000 in full and 1,500 of the 2,000.
	result := pay(t, svc, storeID, customer.ID, "2500.00")
	if len(result.Allocations) != 2 {
		t.Fatalf("payment touched %d entries, want 2", len(result.Allocations))
	}
	if result.Allocations[0].EntryID != oldest.ID {
		t.Error("the payment did not start with the oldest debt")
	}

	for _, want := range []struct {
		label  string
		id     uuid.UUID
		paid   string
		status domain.Status
	}{
		{"oldest", oldest.ID, "1000.00", domain.StatusPaid},
		{"middle", middle.ID, "1500.00", domain.StatusPartial},
		{"newest", newest.ID, "0.00", domain.StatusPending},
	} {
		_, paid, status, _ := entryRow(t, db, want.id)
		mustEqual(t, want.label+" paid_amount", paid, money.MustFromString(want.paid))
		if status != string(want.status) {
			t.Errorf("%s status = %q, want %q", want.label, status, want.status)
		}
	}

	outstanding, err := svc.Outstanding(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	mustEqual(t, "outstanding after 2,500 against 6,000", outstanding, money.MustFromString("3500.00"))
}

// TestAwkwardDecimalsSettleExactly is the float-residue case, in the form the
// money package documents.
//
// Paying off Rs.1.30 in ten instalments of Rs.0.13 lands, in float64, on
// 1.2999999999999998 with 2.2e-16 still "due". `newPaid >= amount` is false,
// so the old code left the entry 'partial' forever: it went on consuming the
// customer's credit limit and never left the overdue report, over two
// ten-thousandths of a paisa. Exact decimal arithmetic settles it.
func TestAwkwardDecimalsSettleExactly(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Awkward Decimals", "100.00")

	sale := issue(t, svc, storeID, customer.ID, "1.30", nil)
	for i := range 10 {
		if _, err := svc.RecordPayment(ctx, service.PaymentInput{
			StoreID:    storeID,
			CustomerID: customer.ID,
			Amount:     money.MustFromString("0.13"),
		}); err != nil {
			t.Fatalf("instalment %d: %v", i+1, err)
		}
	}

	amount, paid, status, _ := entryRow(t, db, sale.ID)
	mustEqual(t, "paid_amount after ten instalments", paid, amount)
	if status != string(domain.StatusPaid) {
		t.Errorf("entry status = %q, want %q — ten payments of 0.13 must close a debt of 1.30", status, domain.StatusPaid)
	}

	outstanding, err := svc.Outstanding(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	if !outstanding.IsZero() {
		t.Errorf("outstanding = %s, want exactly 0.00", outstanding.StringFixed())
	}

	// No advance was manufactured out of residue, and no eleventh instalment
	// was needed.
	advance, err := svc.AdvanceBalance(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("advance balance: %v", err)
	}
	if !advance.IsZero() {
		t.Errorf("advance = %s, want 0.00 — exact instalments should leave nothing over", advance.StringFixed())
	}
}

func TestOverpaymentBecomesAnAdvance(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Generous Payer", "50000.00")

	issue(t, svc, storeID, customer.ID, "800.00", nil)
	result := pay(t, svc, storeID, customer.ID, "1000.00")

	if result.Advance == nil {
		t.Fatal("paying 1,000 against 800 recorded no advance")
	}
	mustEqual(t, "advance amount", result.Advance.Amount, money.MustFromString("200.00"))
	mustEqual(t, "amount applied to debt", result.Applied, money.MustFromString("800.00"))

	// An advance is self-settled, so it contributes nothing to the debt.
	amount, paid, status, entryType := entryRow(t, db, result.Advance.ID)
	mustEqual(t, "advance paid_amount", paid, amount)
	if entryType != string(domain.TypeAdvance) {
		t.Errorf("advance entry type = %q, want %q", entryType, domain.TypeAdvance)
	}
	if status != string(domain.StatusPaid) {
		t.Errorf("advance status = %q, want %q", status, domain.StatusPaid)
	}

	outstanding, err := svc.Outstanding(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	if !outstanding.IsZero() {
		t.Errorf("outstanding after overpaying = %s, want 0.00", outstanding.StringFixed())
	}
	advance, err := svc.AdvanceBalance(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("advance balance: %v", err)
	}
	mustEqual(t, "advance balance", advance, money.MustFromString("200.00"))
}

// TestPaymentWithNoDebtIsHeldEntirelyAsAdvance covers the boundary the old
// `remaining > 0.01` epsilon got wrong at both ends.
func TestPaymentWithNoDebtIsHeldEntirelyAsAdvance(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Prepayer", "50000.00")

	result := pay(t, svc, storeID, customer.ID, "0.01")
	if result.Advance == nil {
		t.Fatal("a one-paisa prepayment vanished: the old epsilon guard swallowed remainders under 0.01")
	}
	mustEqual(t, "advance", result.Advance.Amount, money.MustFromString("0.01"))
	if len(result.Allocations) != 0 {
		t.Errorf("allocated %d entries with no debt outstanding", len(result.Allocations))
	}

	outstanding, err := svc.Outstanding(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	if !outstanding.IsZero() {
		t.Errorf("outstanding = %s, want 0.00 — a prepayment is not a debt", outstanding.StringFixed())
	}
}

// ---------------------------------------------------------------------------
// The status vocabulary
// ---------------------------------------------------------------------------

// TestPaymentRowIsPaidNotCompleted pins the vocabulary. The old writer used
// 'completed', which the Postgres enum deliberately does not have, and which
// the `status != 'paid'` readers therefore counted as debt.
func TestPaymentRowIsPaidNotCompleted(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	customer := newCustomer(t, svc, storeID, "Vocabulary", "50000.00")

	issue(t, svc, storeID, customer.ID, "100.00", nil)
	result := pay(t, svc, storeID, customer.ID, "150.00")

	amount, paid, status, entryType := entryRow(t, db, result.Payment.ID)
	if entryType != string(domain.TypePayment) {
		t.Errorf("payment entry type = %q, want %q", entryType, domain.TypePayment)
	}
	if status != string(domain.StatusPaid) {
		t.Errorf("payment status = %q, want %q", status, domain.StatusPaid)
	}
	mustEqual(t, "payment paid_amount", paid, amount)

	// Whatever the ledger holds, nothing may sit outside the enum, and the
	// two readings of "still owed" must agree.
	rows, err := db.QueryContext(context.Background(),
		`SELECT DISTINCT status::text FROM credit_entries ce
		   JOIN customers c ON c.id = ce.customer_id WHERE c.store_id = $1`, storeID)
	if err != nil {
		t.Fatalf("list statuses: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan status: %v", err)
		}
		if !domain.Status(s).Valid() {
			t.Errorf("ledger holds status %q, which is not part of the vocabulary", s)
		}
		if s == "completed" {
			t.Error(`a row was written with status "completed", which does not exist`)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate statuses: %v", err)
	}
}

// TestOutstandingAgreesWithTheDomainRule walks the whole ledger in Go and
// checks the SQL sum against it. It is the belt to the braces: if a future
// query drifts back to `status != 'paid'`, this fails.
func TestOutstandingAgreesWithTheDomainRule(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Mixed Ledger", "50000.00")

	issue(t, svc, storeID, customer.ID, "1000.00", nil)
	issue(t, svc, storeID, customer.ID, "2000.00", nil)
	pay(t, svc, storeID, customer.ID, "1500.00") // settles one, part-settles the other
	pay(t, svc, storeID, customer.ID, "2000.00") // clears the rest and leaves an advance

	entries, err := svc.ListEntries(ctx, domain.EntryFilter{StoreID: storeID, Limit: 500})
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	expected := money.Zero()
	for _, e := range entries {
		expected = expected.Add(e.Outstanding())
	}

	outstanding, err := svc.Outstanding(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	mustEqual(t, "SQL outstanding against the ledger walked in Go", outstanding, expected)

	store, err := svc.StoreOutstanding(ctx, storeID)
	if err != nil {
		t.Fatalf("store outstanding: %v", err)
	}
	mustEqual(t, "store outstanding", store, expected)
}

// ---------------------------------------------------------------------------
// Void
// ---------------------------------------------------------------------------

func TestVoidedInvoiceCreditStopsCountingAsDebt(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Voided Sale", "50000.00")

	invoiceID := newInvoice(t, db, storeID)
	entry, err := svc.IssueCredit(ctx, service.IssueInput{
		StoreID:    storeID,
		CustomerID: customer.ID,
		InvoiceID:  &invoiceID,
		Amount:     money.MustFromString("2500.00"),
	})
	if err != nil {
		t.Fatalf("issue credit: %v", err)
	}

	before, err := svc.Outstanding(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("outstanding before void: %v", err)
	}
	mustEqual(t, "outstanding before the void", before, money.MustFromString("2500.00"))

	var voided int64
	if err := db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		var err error
		voided, err = svc.VoidInvoiceCredit(ctx, tx, invoiceID)
		return err
	}); err != nil {
		t.Fatalf("void invoice credit: %v", err)
	}
	if voided != 1 {
		t.Errorf("voided %d ledger rows, want 1", voided)
	}

	_, _, status, _ := entryRow(t, db, entry.ID)
	if status != string(domain.StatusVoided) {
		t.Errorf("entry status = %q, want %q", status, domain.StatusVoided)
	}

	after, err := svc.Outstanding(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("outstanding after void: %v", err)
	}
	if !after.IsZero() {
		t.Errorf("a voided invoice still counts as %s of debt — `status != 'paid'` is back", after.StringFixed())
	}

	// A voided debt must also release the credit limit.
	if err := db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		return svc.EnsureWithinCreditLimit(ctx, tx, storeID, customer.ID, money.MustFromString("50000.00"))
	}); err != nil {
		t.Errorf("a voided debt is still consuming the credit limit: %v", err)
	}
}

func newInvoice(t *testing.T, db *platform.DB, storeID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	var userID uuid.UUID
	if err := db.QueryRowContext(ctx,
		`INSERT INTO users (username, password_hash, role, store_id)
		 VALUES ($1, 'not-a-real-hash', 'cashier', $2) RETURNING id`,
		"cashier-"+uuid.NewString()[:8], storeID).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	var invoiceID uuid.UUID
	if err := db.QueryRowContext(ctx,
		`INSERT INTO invoices (invoice_no, store_id, user_id, invoice_date, total, payment_mode)
		 VALUES ($1, $2, $3, current_date, 2500.00, 'credit') RETURNING id`,
		"INV/"+uuid.NewString()[:8], storeID, userID).Scan(&invoiceID); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	return invoiceID
}

// ---------------------------------------------------------------------------
// Overdue report
// ---------------------------------------------------------------------------

func TestSettledDebtLeavesTheOverdueReport(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Late Payer", "50000.00")

	lastMonth := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	nextMonth := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	today := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	overdueEntry := issue(t, svc, storeID, customer.ID, "3000.00", &lastMonth)
	issue(t, svc, storeID, customer.ID, "4000.00", &nextMonth)

	report, err := svc.Overdue(ctx, storeID, today)
	if err != nil {
		t.Fatalf("overdue report: %v", err)
	}
	if len(report) != 1 {
		t.Fatalf("overdue report has %d lines, want 1", len(report))
	}
	// Only the past-due entry counts; the one due next month is not overdue.
	mustEqual(t, "overdue amount", report[0].Outstanding, money.MustFromString("3000.00"))
	if report[0].DueCount != 1 {
		t.Errorf("due_count = %d, want 1", report[0].DueCount)
	}
	if report[0].EarliestDue == nil || !report[0].EarliestDue.Equal(lastMonth) {
		t.Errorf("earliest due = %v, want %v", report[0].EarliestDue, lastMonth)
	}

	// Settle it in full. It must leave the report entirely, rather than
	// lingering as a stranded 'partial'.
	pay(t, svc, storeID, customer.ID, "3000.00")

	if _, _, status, _ := entryRow(t, db, overdueEntry.ID); status != string(domain.StatusPaid) {
		t.Errorf("the settled overdue entry is %q, want %q", status, domain.StatusPaid)
	}

	report, err = svc.Overdue(ctx, storeID, today)
	if err != nil {
		t.Fatalf("overdue report after payment: %v", err)
	}
	if len(report) != 0 {
		t.Errorf("a fully settled debt is still on the overdue report: %+v", report)
	}
}

// ---------------------------------------------------------------------------
// Credit limit
// ---------------------------------------------------------------------------

func TestCreditLimitPermitsExactlyReachingItAndBlocksBeyond(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "At The Limit", "10000.00")

	issue(t, svc, storeID, customer.ID, "6000.00", nil)

	// Exactly reaching the limit is allowed: a limit is a ceiling, not a
	// barrier one paisa below itself.
	if err := db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		return svc.EnsureWithinCreditLimit(ctx, tx, storeID, customer.ID, money.MustFromString("4000.00"))
	}); err != nil {
		t.Errorf("a sale that lands exactly on the limit was refused: %v", err)
	}

	// One paisa more is not.
	err := db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		return svc.EnsureWithinCreditLimit(ctx, tx, storeID, customer.ID, money.MustFromString("4000.01"))
	})
	if !errors.Is(err, domain.ErrLimitExceeded) {
		t.Errorf("expected ErrLimitExceeded one paisa over the limit, got %v", err)
	}

	// And the sale itself is refused, not merely reported on.
	if _, err := svc.IssueCredit(ctx, service.IssueInput{
		StoreID:    storeID,
		CustomerID: customer.ID,
		Amount:     money.MustFromString("4000.01"),
	}); !errors.Is(err, domain.ErrLimitExceeded) {
		t.Errorf("expected the over-limit credit sale to be rejected, got %v", err)
	}

	outstanding, err := svc.Outstanding(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	mustEqual(t, "outstanding after a refused sale", outstanding, money.MustFromString("6000.00"))
}

// TestCreditLimitCheckRefusesRatherThanFailingOpen covers the shape of the
// defect at billing.go:316. That check discarded its scan error, so anything
// that stopped it reading the balance left `outstanding` at zero and the sale
// went through. Here an unresolvable customer produces an error, and an error
// is not permission.
func TestCreditLimitCheckRefusesRatherThanFailingOpen(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()

	err := db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		return svc.EnsureWithinCreditLimit(ctx, tx, storeID, uuid.New(), money.MustFromString("1.00"))
	})
	if err == nil {
		t.Fatal("the credit check passed for a customer that does not exist")
	}
	if !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	// A zero credit limit means no credit, which is what the system has always
	// meant by it.
	cash := newCustomer(t, svc, storeID, "Cash Only", "0.00")
	err = db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		return svc.EnsureWithinCreditLimit(ctx, tx, storeID, cash.ID, money.MustFromString("0.01"))
	})
	if !errors.Is(err, domain.ErrLimitExceeded) {
		t.Errorf("expected a zero credit limit to refuse credit, got %v", err)
	}
}

func TestPaymentRestoresCreditHeadroom(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Revolving Credit", "10000.00")

	issue(t, svc, storeID, customer.ID, "10000.00", nil)

	err := db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		return svc.EnsureWithinCreditLimit(ctx, tx, storeID, customer.ID, money.MustFromString("1.00"))
	})
	if !errors.Is(err, domain.ErrLimitExceeded) {
		t.Fatalf("a customer at their limit should have no headroom, got %v", err)
	}

	pay(t, svc, storeID, customer.ID, "4000.00")

	var status domain.LimitStatus
	if err := db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		var err error
		status, err = svc.CheckCreditLimit(ctx, tx, storeID, customer.ID, money.MustFromString("4000.00"))
		return err
	}); err != nil {
		t.Fatalf("credit check: %v", err)
	}
	mustEqual(t, "headroom after a 4,000 payment", status.Available(), money.MustFromString("4000.00"))
	if !status.Allowed() {
		t.Error("a payment did not restore the customer's credit headroom")
	}
}

// ---------------------------------------------------------------------------
// Store scoping
// ---------------------------------------------------------------------------

func TestCustomerWritesAreScopedToTheStore(t *testing.T) {
	t.Parallel()
	svc, db, storeA := newService(t)
	ctx := context.Background()
	storeB := newStore(t, db, "Other Shop")

	victim := newCustomer(t, svc, storeB, "Other Store Customer", "1000.00")

	// A cashier signed in to store A must not be able to raise the credit
	// limit of a customer of store B, even holding their id. The handler this
	// replaces ran an unscoped UPDATE and would have allowed it.
	raised := money.MustFromString("99999999.00")
	_, err := svc.UpdateCustomer(ctx, storeA, victim.ID, domain.CustomerUpdate{CreditLimit: &raised})
	if !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("expected a cross-store update to be not found, got %v", err)
	}

	account, err := svc.GetAccount(ctx, storeB, victim.ID)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	mustEqual(t, "credit limit after the attempted cross-store edit", account.CreditLimit, money.MustFromString("1000.00"))

	// Reads are scoped the same way.
	if _, err := svc.GetAccount(ctx, storeA, victim.ID); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("expected a cross-store read to be not found, got %v", err)
	}

	// So are payments: a payment posted from the wrong store must not touch
	// the ledger.
	if _, err := svc.RecordPayment(ctx, service.PaymentInput{
		StoreID:    storeA,
		CustomerID: victim.ID,
		Amount:     money.MustFromString("100.00"),
	}); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("expected a cross-store payment to be not found, got %v", err)
	}

	var entries int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM credit_entries WHERE customer_id = $1`, victim.ID).Scan(&entries); err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if entries != 0 {
		t.Errorf("a refused cross-store payment wrote %d ledger rows", entries)
	}
}

func TestUpdateCustomerLeavesUnmentionedFieldsAlone(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Careful Edit", "7500.00")

	phone := "9000000001"
	updated, err := svc.UpdateCustomer(ctx, storeID, customer.ID, domain.CustomerUpdate{Phone: &phone})
	if err != nil {
		t.Fatalf("update customer: %v", err)
	}
	if updated.Phone != phone {
		t.Errorf("phone = %q, want %q", updated.Phone, phone)
	}
	// Editing the phone number must not reset the credit limit to zero, which
	// a naive full-row update would do.
	mustEqual(t, "credit limit after editing only the phone", updated.CreditLimit, money.MustFromString("7500.00"))
	if updated.Name != "Careful Edit" {
		t.Errorf("name = %q, want it unchanged", updated.Name)
	}

	negative := money.MustFromString("-1.00")
	if _, err := svc.UpdateCustomer(ctx, storeID, customer.ID, domain.CustomerUpdate{CreditLimit: &negative}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected a negative credit limit to be rejected, got %v", err)
	}
}

func TestListAccountsCarriesTheBalanceAndIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Balance Carrier", "50000.00")
	newCustomer(t, svc, storeID, "Someone Else", "1000.00")

	issue(t, svc, storeID, customer.ID, "1200.00", nil)
	pay(t, svc, storeID, customer.ID, "200.00")

	// SQLite's LIKE is case-insensitive for ASCII and Postgres's is not, so a
	// literal port of the customer search would return nothing here.
	for _, term := range []string{"balance", "BALANCE", "Balance Carrier"} {
		found, err := svc.ListAccounts(ctx, domain.CustomerFilter{StoreID: storeID, Search: term})
		if err != nil {
			t.Fatalf("search %q: %v", term, err)
		}
		if len(found) != 1 {
			t.Fatalf("search %q returned %d customers, want 1", term, len(found))
		}
		mustEqual(t, "listed outstanding", found[0].Outstanding, money.MustFromString("1000.00"))
		mustEqual(t, "listed headroom", found[0].Available(), money.MustFromString("49000.00"))
	}

	all, err := svc.ListAccounts(ctx, domain.CustomerFilter{StoreID: storeID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("listed %d customers, want 2", len(all))
	}
}

func TestStoreIsRequiredEverywhere(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService(t)
	ctx := context.Background()

	if _, err := svc.CreateCustomer(ctx, uuid.Nil, domain.NewCustomerInput{Name: "No Store"}); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("CreateCustomer without a store: %v", err)
	}
	if _, err := svc.ListAccounts(ctx, domain.CustomerFilter{}); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("ListAccounts without a store: %v", err)
	}
	if _, err := svc.RecordPayment(ctx, service.PaymentInput{CustomerID: uuid.New(), Amount: money.FromInt(1)}); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("RecordPayment without a store: %v", err)
	}
	if _, err := svc.Overdue(ctx, uuid.Nil, time.Now()); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("Overdue without a store: %v", err)
	}
}

func TestNonPositivePaymentIsRejected(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Zero Payer", "1000.00")
	issue(t, svc, storeID, customer.ID, "500.00", nil)

	for _, amount := range []string{"0.00", "-100.00"} {
		if _, err := svc.RecordPayment(ctx, service.PaymentInput{
			StoreID: storeID, CustomerID: customer.ID, Amount: money.MustFromString(amount),
		}); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("payment of %s: expected ErrValidation, got %v", amount, err)
		}
	}

	var payments int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM credit_entries WHERE customer_id = $1 AND type = 'payment'`,
		customer.ID).Scan(&payments); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if payments != 0 {
		t.Errorf("a rejected payment wrote %d rows", payments)
	}
}

// ---------------------------------------------------------------------------
// Two cashiers at once
// ---------------------------------------------------------------------------

// TestConcurrentPaymentsSettleTheSameDebtOnce takes money from one customer at
// two counters at the same moment.
//
// Recording a payment is a multi-step write — lock the customer, read every open
// debt, decide the whole allocation, write the payment row, raise each entry's
// paid_amount, hold any remainder as an advance — and the allocation is computed
// in Go from rows read a moment earlier. Two of those running at once against
// one ledger is the case where a read-then-write either serialises or double-
// spends: both could see the same Rs.1,000 outstanding, both allocate Rs.600 to
// it, and the debt would be settled twice over while Rs.200 of the customer's
// money went nowhere.
//
// The customer's row lock is what makes it safe, and it is taken first and
// always first. This asserts the outcome rather than the mechanism: whatever
// order the two payments land in, every rupee received is accounted for exactly
// once, as settlement or as advance.
func TestConcurrentPaymentsSettleTheSameDebtOnce(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Two Counters", "10000.00")
	issue(t, svc, storeID, customer.ID, "1000.00", nil)

	const payments = 4
	var wg sync.WaitGroup
	results := make([]service.PaymentResult, payments)
	errs := make([]error, payments)
	for i := range payments {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.RecordPayment(ctx, service.PaymentInput{
				StoreID: storeID, CustomerID: customer.ID, Amount: money.MustFromString("600.00"),
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("payment %d: %v", i, err)
		}
	}

	// Rs.2,400 arrived against a Rs.1,000 debt: the debt is cleared and the
	// rest is held.
	outstanding, err := svc.Outstanding(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	mustEqual(t, "outstanding after four concurrent payments", outstanding, money.Zero())

	advance, err := svc.AdvanceBalance(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	mustEqual(t, "advance held", advance, money.MustFromString("1400.00"))

	// Applied plus advance, summed over the callers, is exactly what was
	// handed over. A lost update would show up here as applied money with no
	// debt behind it.
	applied := money.Zero()
	for _, r := range results {
		applied = applied.Add(r.Applied)
	}
	mustEqual(t, "total applied to debts", applied, money.MustFromString("1000.00"))

	// And no entry was over-settled, which the schema's CHECK would also have
	// refused — the point is that it never had to.
	var overpaid int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM credit_entries WHERE customer_id = $1 AND paid_amount > amount`,
		customer.ID).Scan(&overpaid); err != nil {
		t.Fatalf("check for over-settled entries: %v", err)
	}
	if overpaid != 0 {
		t.Errorf("%d ledger entries are settled beyond their amount", overpaid)
	}

	// Four payment rows, four hundred-rupee notes' worth of evidence: the
	// ledger records every one of them.
	var received money.Money
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM credit_entries WHERE customer_id = $1 AND type = 'payment'`,
		customer.ID).Scan(&received); err != nil {
		t.Fatalf("sum payments: %v", err)
	}
	mustEqual(t, "money recorded as received", received, money.MustFromString("2400.00"))
}

// TestAPaymentThatCannotCompleteLeavesTheLedgerUntouched forces a failure after
// the payment row and its allocations have already been written.
//
// Recording a payment is four writes that have to stand or fall together: the
// self-settled payment row, one update per debt it settles, the advance holding
// any remainder, and the balance read that reports the outcome. A payment that
// landed partly would be the worst possible state — money recorded as received
// against debts that were never reduced, or debts reduced with no record of what
// reduced them — and no report would show either.
//
// The advance is the last write, so refusing it is how this reaches the state
// where everything before it has happened. Nothing may survive.
func TestAPaymentThatCannotCompleteLeavesTheLedgerUntouched(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()
	customer := newCustomer(t, svc, storeID, "Overpayer", "10000.00")
	debt := issue(t, svc, storeID, customer.ID, "1000.00", nil)

	if _, err := db.ExecContext(ctx,
		`ALTER TABLE credit_entries ADD CONSTRAINT credit_advance_probe CHECK (type <> 'advance')`); err != nil {
		t.Fatalf("add probe constraint: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(),
			`ALTER TABLE credit_entries DROP CONSTRAINT IF EXISTS credit_advance_probe`)
	})

	// Rs.1,500 against a Rs.1,000 debt: the debt settles, and the Rs.500
	// remainder becomes the advance the probe refuses.
	if _, err := svc.RecordPayment(ctx, service.PaymentInput{
		StoreID: storeID, CustomerID: customer.ID, Amount: money.MustFromString("1500.00"),
	}); err == nil {
		t.Fatal("the payment should have failed on the probe constraint")
	}

	// The debt is exactly as it was.
	amount, paid, status, _ := entryRow(t, db, debt.ID)
	mustEqual(t, "debt amount", amount, money.MustFromString("1000.00"))
	mustEqual(t, "debt settled so far", paid, money.Zero())
	if status != string(domain.StatusPending) {
		t.Errorf("debt status = %q after a failed payment, want pending", status)
	}

	outstanding, err := svc.Outstanding(ctx, storeID, customer.ID)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	mustEqual(t, "outstanding after a failed payment", outstanding, money.MustFromString("1000.00"))

	// And no evidence the money ever arrived, which is the honest answer: it
	// did not, as far as this system is concerned.
	var payments int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM credit_entries WHERE customer_id = $1 AND type = 'payment'`,
		customer.ID).Scan(&payments); err != nil {
		t.Fatalf("count payment rows: %v", err)
	}
	if payments != 0 {
		t.Errorf("%d payment rows survived a rolled-back payment, want none", payments)
	}
}
