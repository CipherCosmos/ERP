package expense_test

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "gbos/internal/domain/expense"
	"gbos/internal/domain/money"
	platform "gbos/internal/platform/postgres"
	repo "gbos/internal/repo/postgres"
	service "gbos/internal/service/expense"
	"gbos/internal/testsupport"

	"github.com/google/uuid"
)

// fixture wires a real repository to a real database. These tests run against
// Postgres rather than a mock because what is worth testing here — the
// per-category aggregation, the DATE comparison, the store boundary — lives in
// the database, and a mock would assert only that the code calls itself.
type fixture struct {
	svc     *service.Service
	db      *platform.DB
	storeID uuid.UUID
	userID  uuid.UUID
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db := testsupport.OpenDB(t)

	f := fixture{svc: service.New(db, repo.NewExpense()), db: db}
	f.storeID = seedStore(t, db, "Test Shop")
	f.userID = seedUser(t, db, f.storeID, "keeper")
	return f
}

func seedStore(t *testing.T, db *platform.DB, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.QueryRowContext(context.Background(),
		`INSERT INTO stores (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("seed store %s: %v", name, err)
	}
	return id
}

func seedUser(t *testing.T, db *platform.DB, storeID uuid.UUID, username string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.QueryRowContext(context.Background(),
		`INSERT INTO users (username, password_hash, role, store_id)
		 VALUES ($1, 'not-a-real-hash', 'manager', $2) RETURNING id`, username, storeID).Scan(&id); err != nil {
		t.Fatalf("seed user %s: %v", username, err)
	}
	return id
}

func (f fixture) category(t *testing.T, name string) uuid.UUID {
	t.Helper()
	c, err := f.svc.CreateCategory(context.Background(), name)
	if err != nil {
		t.Fatalf("create category %s: %v", name, err)
	}
	return c.ID
}

func (f fixture) record(t *testing.T, categoryID uuid.UUID, amount, date string) domain.Expense {
	t.Helper()
	day, err := time.Parse(domain.DateLayout, date)
	if err != nil {
		t.Fatalf("parse date %s: %v", date, err)
	}
	e, err := f.svc.Record(context.Background(), domain.NewExpenseInput{
		StoreID:     f.storeID,
		CategoryID:  categoryID,
		Amount:      money.MustFromString(amount),
		Description: "recorded by a test",
		ExpenseDate: day,
		UserID:      f.userID,
	})
	if err != nil {
		t.Fatalf("record expense %s: %v", amount, err)
	}
	return e
}

// TestExpenseIsRecordedAndAppearsInTheSummary is the round trip the screen
// makes: define a heading, spend under it, see it listed and totalled.
func TestExpenseIsRecordedAndAppearsInTheSummary(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	rent := f.category(t, "Rent")

	recorded := f.record(t, rent, "12000.00", "2026-04-05")

	listed, err := f.svc.List(ctx, domain.Filter{StoreID: f.storeID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d expenses, want 1", len(listed))
	}
	if listed[0].ID != recorded.ID {
		t.Errorf("listed %s, recorded %s", listed[0].ID, recorded.ID)
	}
	// The caller's copy carries the timestamp the database assigned. Returning
	// the struct that went in reports created_at as year 0001 — not a time
	// anything happened, and a value a client would render as such.
	if recorded.CreatedAt.IsZero() {
		t.Error("the recorded expense came back with a zero created_at")
	}
	if !recorded.CreatedAt.Equal(listed[0].CreatedAt) {
		t.Errorf("created_at differs between the create response (%s) and the stored row (%s)",
			recorded.CreatedAt, listed[0].CreatedAt)
	}
	// The category name rides along on the read model; without it the screen
	// shows a column of raw identifiers.
	if listed[0].CategoryName != "Rent" {
		t.Errorf("category_name = %q, want Rent", listed[0].CategoryName)
	}
	if !listed[0].Amount.Equal(money.MustFromString("12000.00")) {
		t.Errorf("amount = %s, want 12000.00", listed[0].Amount)
	}

	summary, err := f.svc.Summarise(ctx, domain.Filter{StoreID: f.storeID})
	if err != nil {
		t.Fatalf("summarise: %v", err)
	}
	if len(summary.Categories) != 1 {
		t.Fatalf("summary had %d lines, want 1", len(summary.Categories))
	}
	if summary.Categories[0].Category != "Rent" {
		t.Errorf("summary line = %q, want Rent", summary.Categories[0].Category)
	}
	if !summary.GrandTotal.Equal(money.MustFromString("12000.00")) {
		t.Errorf("grand total = %s, want 12000.00", summary.GrandTotal)
	}
}

// TestSummaryTotalsEachCategoryExactly is the arithmetic the report exists for.
// The figures are chosen so that a float64 accumulation drifts: 0.1 + 0.2 is
// not 0.3 in binary floating point, and the handler this replaces summed into
// a float64.
func TestSummaryTotalsEachCategoryExactly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	rent := f.category(t, "Rent")
	power := f.category(t, "Electricity")

	f.record(t, rent, "12000.00", "2026-04-01")
	f.record(t, rent, "0.10", "2026-04-02")
	f.record(t, rent, "0.20", "2026-04-03")
	f.record(t, power, "3450.75", "2026-04-04")
	f.record(t, power, "1099.25", "2026-04-05")

	summary, err := f.svc.Summarise(ctx, domain.Filter{StoreID: f.storeID})
	if err != nil {
		t.Fatalf("summarise: %v", err)
	}

	want := map[string]string{"Rent": "12000.30", "Electricity": "4550.00"}
	if len(summary.Categories) != len(want) {
		t.Fatalf("summary had %d lines, want %d", len(summary.Categories), len(want))
	}
	for _, line := range summary.Categories {
		expected, ok := want[line.Category]
		if !ok {
			t.Errorf("unexpected summary line %q", line.Category)
			continue
		}
		if !line.Total.Equal(money.MustFromString(expected)) {
			t.Errorf("%s total = %s, want %s", line.Category, line.Total, expected)
		}
	}

	// The biggest heading sorts first, which is what makes the summary usable.
	if summary.Categories[0].Category != "Rent" {
		t.Errorf("summary is not ordered by spend: first line is %q", summary.Categories[0].Category)
	}
	if !summary.GrandTotal.Equal(money.MustFromString("16550.30")) {
		t.Errorf("grand total = %s, want 16550.30 — it must be the sum of the lines above it", summary.GrandTotal)
	}
}

// TestAnotherStoresExpensesAreNotVisible pins the store boundary. Expense
// categories are shared across shops, so a listing that filtered on the
// category alone would show one shop's spending to another.
func TestAnotherStoresExpensesAreNotVisible(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	rent := f.category(t, "Rent")

	other := fixture{svc: f.svc, db: f.db}
	other.storeID = seedStore(t, f.db, "Somebody Elses Shop")
	other.userID = seedUser(t, f.db, other.storeID, "their-keeper")

	f.record(t, rent, "12000.00", "2026-04-01")
	other.record(t, rent, "99999.00", "2026-04-01")

	listed, err := f.svc.List(ctx, domain.Filter{StoreID: f.storeID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d expenses, want only this store's 1", len(listed))
	}
	if !listed[0].Amount.Equal(money.MustFromString("12000.00")) {
		t.Errorf("listed amount = %s — that is the other shop's expense", listed[0].Amount)
	}

	summary, err := f.svc.Summarise(ctx, domain.Filter{StoreID: f.storeID})
	if err != nil {
		t.Fatalf("summarise: %v", err)
	}
	if !summary.GrandTotal.Equal(money.MustFromString("12000.00")) {
		t.Errorf("grand total = %s, want 12000.00 — the other shop's spending leaked in", summary.GrandTotal)
	}
}

// TestExpenseWindowFiltersByDate checks the DATE comparison. A timestamptz
// bound against a DATE column promotes the column and drags the session
// timezone into a date-only comparison, which moves the boundary by a day.
func TestExpenseWindowFiltersByDate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	rent := f.category(t, "Rent")

	for _, day := range []string{"2026-03-31", "2026-04-01", "2026-04-30", "2026-05-01"} {
		f.record(t, rent, "100.00", day)
	}

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	listed, err := f.svc.List(ctx, domain.Filter{StoreID: f.storeID, From: &from, To: &to})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 {
		t.Errorf("listed %d expenses for April, want 2 — both ends of the window are inclusive", len(listed))
	}

	summary, err := f.svc.Summarise(ctx, domain.Filter{StoreID: f.storeID, From: &from, To: &to})
	if err != nil {
		t.Fatalf("summarise: %v", err)
	}
	if !summary.GrandTotal.Equal(money.MustFromString("200.00")) {
		t.Errorf("April total = %s, want 200.00", summary.GrandTotal)
	}
}

func TestExpenseWindowRejectsABackwardsRange(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	from := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	_, err := f.svc.List(context.Background(), domain.Filter{StoreID: f.storeID, From: &from, To: &to})
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation for an end date before its start, got %v", err)
	}
}

// TestExpenseNeedsAKnownCategory is why the category list has a create route:
// nothing seeds the table, so without one no expense can ever be recorded.
func TestExpenseNeedsAKnownCategory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	categories, err := f.svc.ListCategories(ctx)
	if err != nil {
		t.Fatalf("list categories: %v", err)
	}
	if len(categories) != 0 {
		t.Fatalf("a fresh installation has %d expense categories, want 0 — nothing seeds them", len(categories))
	}

	_, err = f.svc.Record(ctx, domain.NewExpenseInput{
		StoreID:     f.storeID,
		CategoryID:  uuid.New(),
		Amount:      money.MustFromString("10.00"),
		ExpenseDate: time.Now(),
		UserID:      f.userID,
	})
	if !errors.Is(err, service.ErrUnknownCategory) {
		t.Errorf("expected ErrUnknownCategory, got %v", err)
	}

	if _, err := f.svc.CreateCategory(ctx, "Rent"); err != nil {
		t.Fatalf("create category: %v", err)
	}
	categories, err = f.svc.ListCategories(ctx)
	if err != nil {
		t.Fatalf("list categories: %v", err)
	}
	if len(categories) != 1 {
		t.Errorf("after creating one, the list has %d", len(categories))
	}
}

func TestExpenseAmountMustBePositive(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	rent := f.category(t, "Rent")

	for _, amount := range []string{"0.00", "-1.00"} {
		_, err := f.svc.Record(ctx, domain.NewExpenseInput{
			StoreID:     f.storeID,
			CategoryID:  rent,
			Amount:      money.MustFromString(amount),
			ExpenseDate: time.Now(),
			UserID:      f.userID,
		})
		if !errors.Is(err, domain.ErrValidation) {
			t.Errorf("amount %s: expected ErrValidation, got %v", amount, err)
		}
	}
}

// TestExpenseListingIsBounded checks that the limit is clamped rather than
// concatenated. The handler this replaces built its LIMIT by string
// concatenation from an unvalidated query parameter.
func TestExpenseListingIsBounded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	rent := f.category(t, "Rent")

	for i := 0; i < 5; i++ {
		f.record(t, rent, "10.00", "2026-04-01")
	}

	listed, err := f.svc.List(context.Background(), domain.Filter{StoreID: f.storeID, Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 {
		t.Errorf("limit 2 returned %d rows", len(listed))
	}

	// A limit far above the ceiling is clamped, not honoured, and above all
	// not injected.
	listed, err = f.svc.List(context.Background(), domain.Filter{StoreID: f.storeID, Limit: 1_000_000})
	if err != nil {
		t.Fatalf("list with a large limit: %v", err)
	}
	if len(listed) != 5 {
		t.Errorf("clamped listing returned %d rows, want all 5", len(listed))
	}
}

func TestExpenseListingNeedsAStore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	if _, err := f.svc.List(context.Background(), domain.Filter{}); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("expected ErrStoreRequired, got %v", err)
	}
	if _, err := f.svc.Summarise(context.Background(), domain.Filter{}); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("expected ErrStoreRequired from the summary, got %v", err)
	}
}

// TestRecordedExpenseReachesTheProfitAndLossReport checks that what this
// service writes is what the analytics aggregate reads. Analytics sums the
// expenses table directly for the P&L deduction, so the two must agree on the
// store, the date column and the amount scale.
func TestRecordedExpenseReachesTheProfitAndLossReport(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	rent := f.category(t, "Rent")

	f.record(t, rent, "12000.00", "2026-04-05")
	f.record(t, rent, "500.50", "2026-04-06")

	var total money.Money
	err := f.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(SUM(e.amount), 0) FROM expenses e
		  WHERE e.store_id = $1 AND e.expense_date >= $2::date AND e.expense_date <= $3::date`,
		f.storeID, "2026-04-01", "2026-04-30").Scan(&total)
	if err != nil {
		t.Fatalf("read expenses total the way analytics does: %v", err)
	}
	if !total.Equal(money.MustFromString("12500.50")) {
		t.Errorf("profit and loss would deduct %s, want 12500.50", total)
	}
}
