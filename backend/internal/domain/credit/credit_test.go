package credit_test

import (
	"errors"
	"testing"
	"time"

	"gbos/internal/domain/credit"
	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

// openIssue builds an unsettled credit sale of `amount` with `paid` already
// against it, for the allocation tests.
func openIssue(amount, paid string) credit.Entry {
	a := money.MustFromString(amount)
	p := money.MustFromString(paid)
	return credit.Entry{
		ID:         uuid.New(),
		CustomerID: uuid.New(),
		Amount:     a,
		PaidAmount: p,
		Type:       credit.TypeIssue,
		Status:     credit.StatusFor(a, p),
	}
}

func TestCountsAsDebtIsTheOnlyRule(t *testing.T) {
	t.Parallel()
	for status, want := range map[credit.Status]bool{
		credit.StatusPending: true,
		credit.StatusPartial: true,
		credit.StatusPaid:    false,
		credit.StatusVoided:  false,
	} {
		if got := status.CountsAsDebt(); got != want {
			t.Errorf("%s.CountsAsDebt() = %v, want %v", status, got, want)
		}
		if !status.Valid() {
			t.Errorf("%s should be a valid status", status)
		}
	}

	// The vocabulary the old writers used. 'completed' is not a state of a
	// credit ledger row and the Postgres enum has no such member; a reader
	// testing `status != 'paid'` counted it, and every voided row, as debt.
	for _, bogus := range []credit.Status{"completed", "open", "", "PAID"} {
		if bogus.Valid() {
			t.Errorf("%q should not be a valid status", bogus)
		}
		if bogus.CountsAsDebt() {
			t.Errorf("%q must not count as debt", bogus)
		}
	}
}

func TestStatusForIsExactAtTheBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		amount, paid string
		want         credit.Status
	}{
		{"1000.00", "0.00", credit.StatusPending},
		{"1000.00", "0.01", credit.StatusPartial},
		{"1000.00", "999.99", credit.StatusPartial},
		{"1000.00", "1000.00", credit.StatusPaid},
		{"1000.00", "1000.01", credit.StatusPaid},
		// The instalment case that float strands: ten payments of 0.13 against
		// 1.30 land on 1.2999999999999998 in float64, so `paid >= amount` is
		// false and the entry stays partial forever.
		{"1.30", "1.30", credit.StatusPaid},
		{"0.00", "0.00", credit.StatusPaid},
	} {
		got := credit.StatusFor(money.MustFromString(tc.amount), money.MustFromString(tc.paid))
		if got != tc.want {
			t.Errorf("StatusFor(%s, %s) = %s, want %s", tc.amount, tc.paid, got, tc.want)
		}
	}
}

func TestAllocatePaymentSettlesOldestFirst(t *testing.T) {
	t.Parallel()
	entries := []credit.Entry{
		openIssue("1000.00", "0.00"),
		openIssue("2000.00", "0.00"),
		openIssue("3000.00", "0.00"),
	}

	allocations, remainder := credit.AllocatePayment(entries, money.MustFromString("2500.00"))
	if len(allocations) != 2 {
		t.Fatalf("allocated across %d entries, want 2", len(allocations))
	}
	if allocations[0].EntryID != entries[0].ID || allocations[1].EntryID != entries[1].ID {
		t.Error("allocation did not run oldest-first")
	}
	if !allocations[0].PaidAmount.Equal(money.MustFromString("1000.00")) || allocations[0].Status != credit.StatusPaid {
		t.Errorf("first entry: paid %s status %s", allocations[0].PaidAmount, allocations[0].Status)
	}
	if !allocations[1].PaidAmount.Equal(money.MustFromString("1500.00")) || allocations[1].Status != credit.StatusPartial {
		t.Errorf("second entry: paid %s status %s", allocations[1].PaidAmount, allocations[1].Status)
	}
	if !remainder.IsZero() {
		t.Errorf("remainder = %s, want 0.00", remainder)
	}
}

func TestAllocatePaymentNeverOverpaysAnEntry(t *testing.T) {
	t.Parallel()
	// The schema carries CHECK (paid_amount <= amount) because the old loop
	// computed paid_amount by hand and nothing stopped it overshooting.
	entries := []credit.Entry{openIssue("100.00", "40.00"), openIssue("50.00", "0.00")}

	allocations, remainder := credit.AllocatePayment(entries, money.MustFromString("500.00"))
	for i, a := range allocations {
		if a.PaidAmount.GreaterThan(entries[i].Amount) {
			t.Errorf("entry %d over-settled: paid %s against an amount of %s", i, a.PaidAmount, entries[i].Amount)
		}
		if a.Status != credit.StatusPaid {
			t.Errorf("entry %d status = %s, want paid", i, a.Status)
		}
	}
	// 500 - (60 due + 50 due) = 390 held back as an advance.
	if !remainder.Equal(money.MustFromString("390.00")) {
		t.Errorf("remainder = %s, want 390.00", remainder)
	}
}

func TestAllocatePaymentSkipsRowsThatAreNotDebts(t *testing.T) {
	t.Parallel()
	voided := openIssue("900.00", "0.00")
	voided.Status = credit.StatusVoided
	settled := openIssue("100.00", "100.00")
	live := openIssue("200.00", "0.00")

	allocations, remainder := credit.AllocatePayment([]credit.Entry{voided, settled, live}, money.MustFromString("200.00"))
	if len(allocations) != 1 || allocations[0].EntryID != live.ID {
		t.Fatalf("allocated %d entries; a voided or settled row was treated as a debt", len(allocations))
	}
	if !remainder.IsZero() {
		t.Errorf("remainder = %s, want 0.00", remainder)
	}
}

func TestAllocatePaymentAccumulatesExactlyOverInstalments(t *testing.T) {
	t.Parallel()
	// Ten instalments of 0.13 against 1.30, replayed the way the service does
	// it: each round reads the entry back and allocates against it.
	entry := openIssue("1.30", "0.00")
	for i := range 10 {
		allocations, remainder := credit.AllocatePayment([]credit.Entry{entry}, money.MustFromString("0.13"))
		if len(allocations) != 1 {
			t.Fatalf("instalment %d allocated nothing — the debt closed early", i+1)
		}
		if !remainder.IsZero() {
			t.Fatalf("instalment %d left %s over", i+1, remainder)
		}
		entry.PaidAmount = allocations[0].PaidAmount
		entry.Status = allocations[0].Status
	}
	if !entry.PaidAmount.Equal(money.MustFromString("1.30")) {
		t.Errorf("paid = %s after ten instalments, want exactly 1.30", entry.PaidAmount)
	}
	if entry.Status != credit.StatusPaid {
		t.Errorf("status = %s, want paid — float arithmetic strands this at 1.2999999999999998", entry.Status)
	}
	if !entry.Outstanding().IsZero() {
		t.Errorf("outstanding = %s, want 0.00", entry.Outstanding())
	}
}

func TestEntryOutstandingIgnoresSettledAndVoidedRows(t *testing.T) {
	t.Parallel()
	payment, err := credit.NewPayment(uuid.New(), money.MustFromString("5000.00"))
	if err != nil {
		t.Fatalf("new payment: %v", err)
	}
	// This is the whole fix in one assertion: a payment row contributes
	// nothing to the balance. The old writer stored paid_amount = 0 against
	// amount = 5000, so SUM(amount - paid_amount) added the payment to the
	// debt instead of subtracting it.
	if !payment.Outstanding().IsZero() {
		t.Errorf("a payment contributes %s to the balance, want 0.00", payment.Outstanding())
	}
	if payment.Status != credit.StatusPaid {
		t.Errorf("payment status = %s, want paid", payment.Status)
	}

	advance, err := credit.NewAdvance(uuid.New(), money.MustFromString("200.00"))
	if err != nil {
		t.Fatalf("new advance: %v", err)
	}
	if !advance.Outstanding().IsZero() {
		t.Errorf("an advance contributes %s to the balance, want 0.00", advance.Outstanding())
	}

	voided := openIssue("750.00", "0.00")
	voided.Status = credit.StatusVoided
	if !voided.Outstanding().IsZero() {
		t.Errorf("a voided sale contributes %s to the balance, want 0.00", voided.Outstanding())
	}
	if !voided.Due().Equal(money.MustFromString("750.00")) {
		t.Error("Due should still report the unsettled amount; only Outstanding applies the debt rule")
	}
}

func TestLimitStatusAllowsExactlyReachingTheLimit(t *testing.T) {
	t.Parallel()
	at := credit.LimitStatus{
		Limit:       money.MustFromString("10000.00"),
		Outstanding: money.MustFromString("6000.00"),
		Requested:   money.MustFromString("4000.00"),
	}
	if !at.Allowed() || at.Err() != nil {
		t.Error("a sale landing exactly on the limit must be allowed")
	}

	over := at
	over.Requested = money.MustFromString("4000.01")
	if over.Allowed() {
		t.Error("a sale one paisa over the limit must be refused")
	}
	if !errors.Is(over.Err(), credit.ErrLimitExceeded) {
		t.Errorf("Err() = %v, want ErrLimitExceeded", over.Err())
	}
	if !over.Available().Equal(money.MustFromString("4000.00")) {
		t.Errorf("Available() = %s, want 4000.00", over.Available())
	}
}

func TestNewCustomerValidation(t *testing.T) {
	t.Parallel()
	storeID := uuid.New()

	if _, err := credit.NewCustomer(uuid.Nil, credit.NewCustomerInput{Name: "X"}); !errors.Is(err, credit.ErrValidation) {
		t.Errorf("a customer with no store should be rejected, got %v", err)
	}
	if _, err := credit.NewCustomer(storeID, credit.NewCustomerInput{Name: "  "}); !errors.Is(err, credit.ErrValidation) {
		t.Errorf("a blank name should be rejected, got %v", err)
	}
	if _, err := credit.NewCustomer(storeID, credit.NewCustomerInput{
		Name: "Negative", CreditLimit: money.MustFromString("-1.00"),
	}); !errors.Is(err, credit.ErrValidation) {
		t.Errorf("a negative credit limit should be rejected, got %v", err)
	}
	if _, err := credit.NewCustomer(storeID, credit.NewCustomerInput{Name: "Bad GST", GSTIN: "29ABCDE"}); !errors.Is(err, credit.ErrValidation) {
		t.Errorf("a malformed GSTIN should be rejected, got %v", err)
	}

	c, err := credit.NewCustomer(storeID, credit.NewCustomerInput{
		Name: "  Ramesh Traders  ", GSTIN: "29aabbccddeeffz", CreditLimit: money.MustFromString("5000.00"),
	})
	if err != nil {
		t.Fatalf("new customer: %v", err)
	}
	if c.Name != "Ramesh Traders" {
		t.Errorf("name = %q, want it trimmed", c.Name)
	}
	if c.GSTIN != "29AABBCCDDEEFFZ" {
		t.Errorf("gstin = %q, want it upper-cased", c.GSTIN)
	}
	if !c.IsActive || c.StoreID != storeID {
		t.Error("a new customer should be active and belong to the store it was created in")
	}
}

func TestIsOverdueNeedsBothADueDateAndADebt(t *testing.T) {
	t.Parallel()
	due := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	today := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	e := openIssue("500.00", "0.00")
	e.DueDate = &due
	if !e.IsOverdue(today) {
		t.Error("an unpaid sale past its due date is overdue")
	}

	e.Status = credit.StatusPaid
	if e.IsOverdue(today) {
		t.Error("a settled sale is never overdue, however old")
	}

	notYet := openIssue("500.00", "0.00")
	later := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	notYet.DueDate = &later
	if notYet.IsOverdue(today) {
		t.Error("a sale due next month is not overdue")
	}

	noDate := openIssue("500.00", "0.00")
	if noDate.IsOverdue(today) {
		t.Error("a sale with no due date is not overdue")
	}
}

func TestDefaultDueDateIsThirtyDays(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	if got := credit.DefaultDueDate(from); !got.Equal(want) {
		t.Errorf("DefaultDueDate(%v) = %v, want %v", from, got, want)
	}
}
