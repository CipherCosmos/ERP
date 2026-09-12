package stock_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gbos/internal/domain/money"
	domain "gbos/internal/domain/stock"
	platform "gbos/internal/platform/postgres"
	repo "gbos/internal/repo/postgres"
	service "gbos/internal/service/stock"
	"gbos/internal/testsupport"

	"github.com/google/uuid"
)

// The repository must satisfy the interface the service declares. Checked at
// compile time so a signature drift is a build failure, not a runtime nil.
var _ service.Repository = (*repo.Stock)(nil)

// fixture is a seeded shop: two stores, a user to attribute movements to, and
// one product.
type fixture struct {
	svc     *service.Service
	db      *platform.DB
	store   uuid.UUID
	other   uuid.UUID
	product uuid.UUID
	user    uuid.UUID
	// baseUnit is the product's default unit: one kilogram, the unit its single
	// stock pool is counted in and the one a bare quantity means.
	baseUnit uuid.UUID
}

// newFixture wires a real repository to a real database. These tests use
// Postgres rather than a mock because everything worth testing here — the
// guarded updates that cannot oversell, the CHECK constraints on reserved_qty,
// transaction rollback, and the cursor semantics pgx enforces — lives in the
// database, and a mock would assert only that the code calls itself.
func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()

	db, err := platform.Open(ctx, platform.Config{DSN: testsupport.DSN(t)})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	f := fixture{svc: service.New(db, repo.NewStock()), db: db}

	mustScan(t, db, `INSERT INTO stores (name) VALUES ('Main Shop') RETURNING id`, &f.store)
	mustScan(t, db, `INSERT INTO stores (name) VALUES ('Branch Shop') RETURNING id`, &f.other)
	mustScan(t, db,
		`INSERT INTO users (username, password_hash, role, store_id)
		 VALUES ('stockkeeper', 'x', 'manager', $1) RETURNING id`, &f.user, f.store)

	var categoryID, uomID uuid.UUID
	mustScan(t, db, `INSERT INTO categories (name) VALUES ('Groceries') RETURNING id`, &categoryID)
	// The units migration seeds the starter set and units_of_measure.code is
	// unique, so the kilogram is read rather than inserted.
	mustScan(t, db, `SELECT id FROM units_of_measure WHERE code = 'KG'`, &uomID)
	mustScan(t, db,
		`INSERT INTO products (name, category_id, uom_id, gst_rate, min_stock)
		 VALUES ('Basmati Rice', $1, $2, 5, 10) RETURNING id`, &f.product, categoryID, uomID)
	// Every product carries the unit a bare quantity means. The migration
	// backfills one for every product that existed; a product created here has
	// to be given one the same way.
	mustScan(t, db,
		`INSERT INTO product_units (product_id, uom_id, label, qty_in_base, is_default)
		 VALUES ($1, $2, 'Kilogram', 1, true) RETURNING id`, &f.baseUnit, f.product, uomID)

	return f
}

// addUnit defines another way of transacting the fixture's product -- a sack,
// a pouch -- and returns its id.
func (f fixture) addUnit(t *testing.T, label, qtyInBase string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	mustScan(t, f.db,
		`INSERT INTO product_units (product_id, label, qty_in_base, is_default)
		 VALUES ($1, $2, $3, false) RETURNING id`, &id, f.product, label, qtyInBase)
	return id
}

func mustScan(t *testing.T, db *platform.DB, query string, dest any, args ...any) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(dest); err != nil {
		t.Fatalf("seed (%s): %v", query, err)
	}
}

// seedBatch inserts a stock row directly, so a test controls the expiry date
// and the receipt order that FEFO sorts on.
func (f fixture) seedBatch(t *testing.T, storeID uuid.UUID, batchNo, qty, cost string, expiry *string, receivedDaysAgo int) {
	t.Helper()
	var expiryArg any
	if expiry != nil {
		expiryArg = *expiry
	}
	_, err := f.db.ExecContext(context.Background(),
		`INSERT INTO stock (product_id, store_id, batch_no, quantity, cost_price, expiry_date, created_at)
		 VALUES ($1, $2, $3, $4::numeric, $5::numeric, $6::date, now() - ($7::integer * interval '1 day'))`,
		f.product, storeID, batchNo, qty, cost, expiryArg, receivedDaysAgo)
	if err != nil {
		t.Fatalf("seed batch %q: %v", batchNo, err)
	}
}

// onHand is the total quantity of the fixture's product in a store, read
// straight from the table so a test never trusts the code it is testing.
func (f fixture) onHand(t *testing.T, storeID uuid.UUID) money.Quantity {
	t.Helper()
	var q money.Quantity
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(SUM(quantity), 0) FROM stock WHERE product_id = $1 AND store_id = $2`,
		f.product, storeID).Scan(&q); err != nil {
		t.Fatalf("read on-hand: %v", err)
	}
	return q
}

func (f fixture) batchQty(t *testing.T, storeID uuid.UUID, batchNo string) money.Quantity {
	t.Helper()
	var q money.Quantity
	err := f.db.QueryRowContext(context.Background(),
		`SELECT quantity FROM stock WHERE product_id = $1 AND store_id = $2 AND batch_no = $3`,
		f.product, storeID, batchNo).Scan(&q)
	if err != nil {
		t.Fatalf("read batch %q: %v", batchNo, err)
	}
	return q
}

func (f fixture) countRows(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := f.db.QueryRowContext(context.Background(),
		fmt.Sprintf(`SELECT count(*) FROM %s WHERE product_id = $1`, table), f.product).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func qty(s string) money.Quantity { return money.MustQuantityFromString(s) }

func assertQty(t *testing.T, label string, got money.Quantity, want string) {
	t.Helper()
	if !got.Equal(qty(want)) {
		t.Errorf("%s = %s, want %s", label, got.StringFixed(), want)
	}
}

func dateIn(days int) *string {
	d := time.Now().AddDate(0, 0, days).Format("2006-01-02")
	return &d
}

// ---------------------------------------------------------------------------
// Lost update
// ---------------------------------------------------------------------------

// TestConcurrentAdjustmentsDoNotLoseAnUpdate is the regression test for the
// read-modify-write in inventory.go:159-232. That code read the quantity into
// Go, added the delta there and wrote `SET quantity = ?`, so two adjustments
// that interleaved both read the same starting figure and the second silently
// overwrote the first. Run under -race with real concurrent transactions, that
// implementation ends well below the expected total.
func TestConcurrentAdjustmentsDoNotLoseAnUpdate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	const adders = 12
	var wg sync.WaitGroup
	errs := make(chan error, adders)
	for range adders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.Adjust(ctx, service.AdjustInput{
				ProductID: f.product, StoreID: f.store, UserID: f.user,
				Quantity: qty("1"), Type: domain.AdjustAdd,
				CostPrice: money.MustFromString("40.00"),
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent add: %v", err)
		}
	}

	assertQty(t, "quantity after 12 concurrent additions of 1", f.onHand(t, f.store), "12")

	// Now take them back out concurrently. Each removal is a guarded relative
	// update, so none of them can remove stock another goroutine already took.
	const removers = 8
	var wg2 sync.WaitGroup
	errs2 := make(chan error, removers)
	for range removers {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			_, err := f.svc.Adjust(ctx, service.AdjustInput{
				ProductID: f.product, StoreID: f.store, UserID: f.user,
				Quantity: qty("1"), Type: domain.AdjustRemove,
			})
			errs2 <- err
		}()
	}
	wg2.Wait()
	close(errs2)
	for err := range errs2 {
		if err != nil {
			t.Fatalf("concurrent remove: %v", err)
		}
	}

	assertQty(t, "quantity after 8 concurrent removals of 1", f.onHand(t, f.store), "4")

	if n := f.countRows(t, "stock_adjustments"); n != adders+removers {
		t.Errorf("audit trail has %d rows, want %d — every movement must be recorded", n, adders+removers)
	}
}

// TestConcurrentRemovalsCannotOversell drives more removals at a batch than it
// holds. The guard decides who wins; the total removed must equal what was
// there, and the batch must never go negative.
func TestConcurrentRemovalsCannotOversell(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.seedBatch(t, f.store, "B1", "5", "40.00", nil, 0)

	const attempts = 10
	var wg sync.WaitGroup
	results := make(chan error, attempts)
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.Adjust(ctx, service.AdjustInput{
				ProductID: f.product, StoreID: f.store, UserID: f.user,
				BatchNo: "B1", Quantity: qty("1"), Type: domain.AdjustRemove,
			})
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	succeeded := 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrInsufficientStock):
			// Expected: there were only five.
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 5 {
		t.Errorf("%d of %d removals succeeded, want exactly 5", succeeded, attempts)
	}
	assertQty(t, "batch after 10 concurrent removals of 1 from 5", f.batchQty(t, f.store, "B1"), "0")
}

// ---------------------------------------------------------------------------
// Sign validation
// ---------------------------------------------------------------------------

// TestNegativeTransferQuantityDoesNotMintStock is the regression test for
// inventory.go:305. That handler ran `SET quantity = quantity - ?` with the
// request body's number and no sign check, so a quantity of -50 added 50 to the
// source and 50 to the destination, creating stock from nothing.
func TestNegativeTransferQuantityDoesNotMintStock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.seedBatch(t, f.store, "B1", "20", "40.00", nil, 0)

	err := f.svc.Transfer(ctx, service.TransferInput{
		ProductID: f.product, FromStoreID: f.store, ToStoreID: f.other,
		UserID: f.user, BatchNo: "B1", Quantity: qty("-50"),
	})
	if err == nil {
		t.Fatal("a negative transfer quantity was accepted")
	}
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation, got %v", err)
	}

	assertQty(t, "source store after a rejected negative transfer", f.onHand(t, f.store), "20")
	assertQty(t, "destination store after a rejected negative transfer", f.onHand(t, f.other), "0")
}

// TestNegativeQuantityIsRejectedEverywhere checks the same sign guard on every
// entry point that moves stock, so a later caller cannot find one that lacks it.
func TestNegativeQuantityIsRejectedEverywhere(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.seedBatch(t, f.store, "B1", "20", "40.00", nil, 0)

	negative := qty("-5")
	cases := map[string]error{
		"adjust add": func() error {
			_, err := f.svc.Adjust(ctx, service.AdjustInput{
				ProductID: f.product, StoreID: f.store, UserID: f.user,
				BatchNo: "B1", Quantity: negative, Type: domain.AdjustAdd,
			})
			return err
		}(),
		"adjust remove": func() error {
			_, err := f.svc.Adjust(ctx, service.AdjustInput{
				ProductID: f.product, StoreID: f.store, UserID: f.user,
				BatchNo: "B1", Quantity: negative, Type: domain.AdjustRemove,
			})
			return err
		}(),
		"reserve": f.svc.Reserve(ctx, service.ReservationInput{
			ProductID: f.product, StoreID: f.store, UserID: f.user,
			BatchNo: "B1", Quantity: negative,
		}),
		"release": f.svc.Release(ctx, service.ReservationInput{
			ProductID: f.product, StoreID: f.store, UserID: f.user,
			BatchNo: "B1", Quantity: negative,
		}),
		"record loss": func() error {
			_, err := f.svc.RecordLoss(ctx, service.LossInput{
				ProductID: f.product, StoreID: f.store, UserID: f.user,
				BatchNo: "B1", Quantity: negative, LossType: "damage",
			})
			return err
		}(),
	}
	for name, err := range cases {
		if !errors.Is(err, domain.ErrValidation) {
			t.Errorf("%s accepted a negative quantity (got %v)", name, err)
		}
	}
	assertQty(t, "stock after every rejected negative movement", f.onHand(t, f.store), "20")
}

// ---------------------------------------------------------------------------
// FEFO
// ---------------------------------------------------------------------------

// TestFEFOConsumesEarliestExpiringBatchFirst seeds three batches out of expiry
// order and checks that the earliest-expiring one goes first, undated stock
// last. It is also the cursor-interleaving regression test: satisfying this
// deduction requires writing to more than one batch, which the old
// implementation did with tx.Exec inside `for rows.Next()` — pgx answers
// "conn busy" to that, so this test cannot pass without draining the rows first.
func TestFEFOConsumesEarliestExpiringBatchFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	f.seedBatch(t, f.store, "LATE", "10", "40.00", dateIn(90), 30)
	f.seedBatch(t, f.store, "SOON", "10", "41.00", dateIn(5), 10)
	f.seedBatch(t, f.store, "NODATE", "10", "42.00", nil, 60)

	var deduction domain.Deduction
	err := f.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		var err error
		deduction, err = f.svc.DeductFEFO(ctx, tx, domain.DeductionRequest{
			ProductID: f.product, StoreID: f.store, Quantity: qty("25"),
		})
		return err
	})
	if err != nil {
		t.Fatalf("FEFO deduction: %v", err)
	}

	wantOrder := []struct {
		batch string
		taken string
	}{
		{"SOON", "10"},  // expires first
		{"LATE", "10"},  // expires later
		{"NODATE", "5"}, // no expiry date sorts last
	}
	if len(deduction.Batches) != len(wantOrder) {
		t.Fatalf("consumed %d batches, want %d: %+v", len(deduction.Batches), len(wantOrder), deduction.Batches)
	}
	for i, want := range wantOrder {
		got := deduction.Batches[i]
		if got.BatchNo != want.batch {
			t.Errorf("batch %d consumed was %q, want %q — FEFO order is wrong", i, got.BatchNo, want.batch)
		}
		assertQty(t, fmt.Sprintf("quantity taken from %s", want.batch), got.Quantity, want.taken)
	}

	if deduction.PrimaryBatch() != "SOON" {
		t.Errorf("primary batch = %q, want the earliest-expiring SOON", deduction.PrimaryBatch())
	}
	assertQty(t, "SOON remaining", f.batchQty(t, f.store, "SOON"), "0")
	assertQty(t, "LATE remaining", f.batchQty(t, f.store, "LATE"), "0")
	assertQty(t, "NODATE remaining", f.batchQty(t, f.store, "NODATE"), "5")
}

// TestFEFOTiesBreakOnReceiptOrder checks the FIFO half of the rule: batches
// with no expiry date are consumed oldest first.
func TestFEFOTiesBreakOnReceiptOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	f.seedBatch(t, f.store, "NEWEST", "10", "40.00", nil, 1)
	f.seedBatch(t, f.store, "OLDEST", "10", "40.00", nil, 90)
	f.seedBatch(t, f.store, "MIDDLE", "10", "40.00", nil, 45)

	var deduction domain.Deduction
	err := f.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		var err error
		deduction, err = f.svc.DeductFEFO(ctx, tx, domain.DeductionRequest{
			ProductID: f.product, StoreID: f.store, Quantity: qty("15"),
		})
		return err
	})
	if err != nil {
		t.Fatalf("FEFO deduction: %v", err)
	}
	if got := deduction.Batches[0].BatchNo; got != "OLDEST" {
		t.Errorf("first batch consumed was %q, want OLDEST", got)
	}
	if got := deduction.Batches[1].BatchNo; got != "MIDDLE" {
		t.Errorf("second batch consumed was %q, want MIDDLE", got)
	}
	assertQty(t, "NEWEST remaining", f.batchQty(t, f.store, "NEWEST"), "10")
}

// TestDeductingMoreThanExistsFailsAndLeavesStockUntouched checks the two
// halves of the failure contract: the error names the shortfall, and because
// the deduction runs inside the caller's transaction, the partial debits it had
// already made are rolled back.
func TestDeductingMoreThanExistsFailsAndLeavesStockUntouched(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	f.seedBatch(t, f.store, "A", "6", "40.00", dateIn(10), 5)
	f.seedBatch(t, f.store, "B", "4", "50.00", dateIn(20), 5)

	err := f.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		_, err := f.svc.DeductFEFO(ctx, tx, domain.DeductionRequest{
			ProductID: f.product, StoreID: f.store, Quantity: qty("11"),
		})
		return err
	})
	if err == nil {
		t.Fatal("deducting 11 units from 10 on hand succeeded")
	}
	if !errors.Is(err, domain.ErrInsufficientStock) {
		t.Errorf("expected ErrInsufficientStock, got %v", err)
	}

	// No short fill and no partial debit: the whole transaction rolled back.
	assertQty(t, "batch A after a failed oversell", f.batchQty(t, f.store, "A"), "6")
	assertQty(t, "batch B after a failed oversell", f.batchQty(t, f.store, "B"), "4")
}

// TestFEFORespectsAnEpsilonSizedShortfall is the direct regression test for
// `if remaining > 0.001` in billing.go:103. A thousandth of a unit missing is
// still missing, and no tolerance may hide it.
func TestFEFORespectsAnEpsilonSizedShortfall(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.seedBatch(t, f.store, "A", "9.999", "40.00", nil, 0)

	err := f.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		_, err := f.svc.DeductFEFO(ctx, tx, domain.DeductionRequest{
			ProductID: f.product, StoreID: f.store, Quantity: qty("10"),
		})
		return err
	})
	if !errors.Is(err, domain.ErrInsufficientStock) {
		t.Fatalf("0.001 short of the requested quantity was accepted: %v", err)
	}
	assertQty(t, "stock after the rejected deduction", f.batchQty(t, f.store, "A"), "9.999")
}

// TestWeightedAverageCostIsExact checks the COGS figure a multi-batch sale
// snapshots on its invoice line.
func TestWeightedAverageCostIsExact(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	// 10 units at 20.00 and 10 at 30.00. Taking 15 costs 10*20 + 5*30 = 350.00
	// over 15 units, which is 23.3333... a figure a float cannot hold and that
	// rounds to exactly 23.33 under half-away-from-zero.
	f.seedBatch(t, f.store, "CHEAP", "10", "20.00", dateIn(5), 0)
	f.seedBatch(t, f.store, "DEAR", "10", "30.00", dateIn(50), 0)

	var deduction domain.Deduction
	err := f.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		var err error
		deduction, err = f.svc.DeductFEFO(ctx, tx, domain.DeductionRequest{
			ProductID: f.product, StoreID: f.store, Quantity: qty("15"),
		})
		return err
	})
	if err != nil {
		t.Fatalf("FEFO deduction: %v", err)
	}

	if want := money.MustFromString("350.00"); !deduction.TotalCost.Equal(want) {
		t.Errorf("total cost = %s, want %s", deduction.TotalCost, want)
	}
	if want := money.MustFromString("23.33"); !deduction.UnitCost.Equal(want) {
		t.Errorf("weighted-average unit cost = %s, want %s", deduction.UnitCost, want)
	}
	assertQty(t, "quantity deducted", deduction.Quantity, "15")

	// And a division that comes out even must come out exactly even, with no
	// residue in the last place.
	f2 := newFixture(t)
	f2.seedBatch(t, f2.store, "CHEAP", "10", "20.00", dateIn(5), 0)
	f2.seedBatch(t, f2.store, "DEAR", "10", "30.00", dateIn(50), 0)
	var even domain.Deduction
	if err := f2.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		var err error
		even, err = f2.svc.DeductFEFO(ctx, tx, domain.DeductionRequest{
			ProductID: f2.product, StoreID: f2.store, Quantity: qty("20"),
		})
		return err
	}); err != nil {
		t.Fatalf("FEFO deduction: %v", err)
	}
	if want := money.MustFromString("25.00"); !even.UnitCost.Equal(want) {
		t.Errorf("weighted-average unit cost = %s, want exactly %s", even.UnitCost, want)
	}
}

// TestFEFOBatchHintIsAnOptimisationNotAConstraint checks that a named batch is
// used when it can serve the whole quantity and quietly bypassed when it
// cannot, rather than failing.
func TestFEFOBatchHintIsAnOptimisationNotAConstraint(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	f.seedBatch(t, f.store, "SOON", "10", "40.00", dateIn(2), 0)
	f.seedBatch(t, f.store, "HINTED", "10", "45.00", dateIn(80), 0)

	// The hint wins over FEFO when it can serve the request outright: a return
	// goes back to the batch it came from.
	var hinted domain.Deduction
	if err := f.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		var err error
		hinted, err = f.svc.DeductFEFO(ctx, tx, domain.DeductionRequest{
			ProductID: f.product, StoreID: f.store, BatchHint: "HINTED", Quantity: qty("4"),
		})
		return err
	}); err != nil {
		t.Fatalf("hinted deduction: %v", err)
	}
	if len(hinted.Batches) != 1 || hinted.Batches[0].BatchNo != "HINTED" {
		t.Errorf("the named batch was not used: %+v", hinted.Batches)
	}
	assertQty(t, "SOON untouched by the hinted deduction", f.batchQty(t, f.store, "SOON"), "10")

	// The hint cannot serve 12 on its own, so FEFO takes over and the earliest
	// expiry goes first.
	var spilled domain.Deduction
	if err := f.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		var err error
		spilled, err = f.svc.DeductFEFO(ctx, tx, domain.DeductionRequest{
			ProductID: f.product, StoreID: f.store, BatchHint: "HINTED", Quantity: qty("12"),
		})
		return err
	}); err != nil {
		t.Fatalf("spilled deduction: %v", err)
	}
	if spilled.Batches[0].BatchNo != "SOON" {
		t.Errorf("fallback did not follow FEFO: first batch was %q", spilled.Batches[0].BatchNo)
	}
	assertQty(t, "total left in the store", f.onHand(t, f.store), "4")
}

// ---------------------------------------------------------------------------
// Losses
// ---------------------------------------------------------------------------

// TestLossLargerThanAnySingleBatchDeductsAcrossBatches is the regression test
// for returns.go:226-238. That handler deducted with a subquery that selected
// one batch holding at least the whole quantity and never checked
// RowsAffected, so a loss bigger than any single batch wrote the loss row,
// moved no stock, and returned 201.
func TestLossLargerThanAnySingleBatchDeductsAcrossBatches(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	// Eight units in total, but no single batch holds more than three.
	f.seedBatch(t, f.store, "A", "3", "40.00", dateIn(5), 0)
	f.seedBatch(t, f.store, "B", "3", "40.00", dateIn(10), 0)
	f.seedBatch(t, f.store, "C", "2", "40.00", dateIn(15), 0)

	loss, err := f.svc.RecordLoss(ctx, service.LossInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		Quantity: qty("7"), LossType: "damage", Reason: "water ingress",
	})
	if err != nil {
		t.Fatalf("record loss: %v", err)
	}
	if len(loss.Consumed) != 3 {
		t.Errorf("loss spanned %d batches, want 3: %+v", len(loss.Consumed), loss.Consumed)
	}

	assertQty(t, "stock after a 7-unit loss from 8 on hand", f.onHand(t, f.store), "1")
	if n := f.countRows(t, "inventory_losses"); n != 1 {
		t.Errorf("inventory_losses has %d rows, want 1", n)
	}
}

// TestLossThatCannotBeCoveredRecordsNothing is the other half of the same
// defect: a loss must never be recorded without the stock moving. Either both
// happen or neither does.
func TestLossThatCannotBeCoveredRecordsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.seedBatch(t, f.store, "A", "3", "40.00", nil, 0)

	_, err := f.svc.RecordLoss(ctx, service.LossInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		Quantity: qty("10"), LossType: "theft",
	})
	if err == nil {
		t.Fatal("a loss larger than the stock on hand was accepted")
	}
	if !errors.Is(err, domain.ErrInsufficientStock) {
		t.Errorf("expected ErrInsufficientStock, got %v", err)
	}

	if n := f.countRows(t, "inventory_losses"); n != 0 {
		t.Errorf("inventory_losses has %d rows after a failed loss, want 0 — "+
			"a loss recorded without the stock moving puts the books permanently out of step", n)
	}
	assertQty(t, "stock after a failed loss", f.batchQty(t, f.store, "A"), "3")
}

// TestLossIsAttributedToTheBatchItActuallyCameFrom checks that the recorded
// batch is the one FEFO consumed, not the one the caller guessed at.
func TestLossIsAttributedToTheBatchItActuallyCameFrom(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.seedBatch(t, f.store, "EXPIRES-FIRST", "5", "40.00", dateIn(3), 0)
	f.seedBatch(t, f.store, "EXPIRES-LATER", "5", "40.00", dateIn(300), 0)

	loss, err := f.svc.RecordLoss(ctx, service.LossInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		Quantity: qty("2"), LossType: "expiry",
	})
	if err != nil {
		t.Fatalf("record loss: %v", err)
	}
	if loss.BatchNo != "EXPIRES-FIRST" {
		t.Errorf("loss recorded against %q, want the batch it was taken from", loss.BatchNo)
	}

	var stored string
	if err := f.db.QueryRowContext(ctx,
		`SELECT batch_no FROM inventory_losses WHERE id = $1`, loss.ID).Scan(&stored); err != nil {
		t.Fatalf("read stored loss: %v", err)
	}
	if stored != "EXPIRES-FIRST" {
		t.Errorf("stored batch_no = %q, want EXPIRES-FIRST", stored)
	}
}

// ---------------------------------------------------------------------------
// Reservations
// ---------------------------------------------------------------------------

// TestReservingMoreThanOnHandIsRefused checks the guard that replaces
// inventory.go:177-201. The schema carries CHECK (reserved_qty <= quantity), so
// the point is not only that the reservation fails but that it fails with a
// business error rather than a constraint violation.
func TestReservingMoreThanOnHandIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.seedBatch(t, f.store, "B1", "10", "40.00", nil, 0)

	err := f.svc.Reserve(ctx, service.ReservationInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("11"),
	})
	if err == nil {
		t.Fatal("reserving 11 of 10 on hand succeeded")
	}
	if !errors.Is(err, domain.ErrInsufficientStock) {
		t.Errorf("expected ErrInsufficientStock, got %v", err)
	}
	if errors.Is(err, platform.ErrConstraint) {
		t.Error("the reservation reached the database and hit a CHECK constraint; " +
			"the guard should have refused it as a business error")
	}

	// A reservation that fits is taken, and a second one that would exceed the
	// remainder is not.
	if err := f.svc.Reserve(ctx, service.ReservationInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("6"),
	}); err != nil {
		t.Fatalf("reserving 6 of 10: %v", err)
	}
	if err := f.svc.Reserve(ctx, service.ReservationInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("5"),
	}); !errors.Is(err, domain.ErrInsufficientStock) {
		t.Errorf("reserving 5 more when only 4 are free: expected ErrInsufficientStock, got %v", err)
	}
}

// TestReleasingMoreThanReservedIsRefused is the regression test for
// inventory.go:194-201, which could drive reserved_qty below zero.
func TestReleasingMoreThanReservedIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.seedBatch(t, f.store, "B1", "10", "40.00", nil, 0)

	if err := f.svc.Reserve(ctx, service.ReservationInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("3"),
	}); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	err := f.svc.Release(ctx, service.ReservationInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("4"),
	})
	if !errors.Is(err, domain.ErrInsufficientReserved) {
		t.Errorf("expected ErrInsufficientReserved, got %v", err)
	}

	var reserved money.Quantity
	if err := f.db.QueryRowContext(ctx,
		`SELECT reserved_qty FROM stock WHERE product_id = $1 AND store_id = $2 AND batch_no = 'B1'`,
		f.product, f.store).Scan(&reserved); err != nil {
		t.Fatalf("read reserved: %v", err)
	}
	assertQty(t, "reserved_qty after a rejected over-release", reserved, "3")
}

// TestReservedStockCannotBeSoldOrRemoved checks that every deduction path
// guards against the unreserved quantity rather than the raw quantity.
func TestReservedStockCannotBeSoldOrRemoved(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.seedBatch(t, f.store, "B1", "10", "40.00", nil, 0)

	if err := f.svc.Reserve(ctx, service.ReservationInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("8"),
	}); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	// Only two are free, so a sale for three must fail.
	err := f.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		_, err := f.svc.DeductFEFO(ctx, tx, domain.DeductionRequest{
			ProductID: f.product, StoreID: f.store, Quantity: qty("3"),
		})
		return err
	})
	if !errors.Is(err, domain.ErrInsufficientStock) {
		t.Errorf("a sale consumed reserved stock: %v", err)
	}

	// And so must a manual removal.
	if _, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("3"), Type: domain.AdjustRemove,
	}); !errors.Is(err, domain.ErrInsufficientStock) {
		t.Errorf("a manual removal consumed reserved stock: %v", err)
	}

	assertQty(t, "quantity after both rejected deductions", f.batchQty(t, f.store, "B1"), "10")
}

// ---------------------------------------------------------------------------
// Transfer and conversion
// ---------------------------------------------------------------------------

func TestTransferMovesStockAtomically(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	expiry := dateIn(45)
	f.seedBatch(t, f.store, "B1", "20", "40.00", expiry, 0)

	if err := f.svc.Transfer(ctx, service.TransferInput{
		ProductID: f.product, FromStoreID: f.store, ToStoreID: f.other,
		UserID: f.user, BatchNo: "B1", Quantity: qty("8"),
	}); err != nil {
		t.Fatalf("transfer: %v", err)
	}

	assertQty(t, "source after transferring 8 of 20", f.batchQty(t, f.store, "B1"), "12")
	assertQty(t, "destination after transferring 8", f.batchQty(t, f.other, "B1"), "8")

	// The batch's identity travels with it: the destination must stay on the
	// expiry report.
	var arrived time.Time
	if err := f.db.QueryRowContext(ctx,
		`SELECT expiry_date FROM stock WHERE product_id = $1 AND store_id = $2 AND batch_no = 'B1'`,
		f.product, f.other).Scan(&arrived); err != nil {
		t.Fatalf("read destination expiry: %v", err)
	}
	if arrived.Format("2006-01-02") != *expiry {
		t.Errorf("destination expiry = %s, want %s", arrived.Format("2006-01-02"), *expiry)
	}

	// Transferring more than remains must move nothing at either end.
	err := f.svc.Transfer(ctx, service.TransferInput{
		ProductID: f.product, FromStoreID: f.store, ToStoreID: f.other,
		UserID: f.user, BatchNo: "B1", Quantity: qty("13"),
	})
	if !errors.Is(err, domain.ErrInsufficientStock) {
		t.Errorf("expected ErrInsufficientStock, got %v", err)
	}
	assertQty(t, "source after a rejected transfer", f.batchQty(t, f.store, "B1"), "12")
	assertQty(t, "destination after a rejected transfer", f.batchQty(t, f.other, "B1"), "8")
}

func TestTransferToTheSameStoreIsRejected(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedBatch(t, f.store, "B1", "20", "40.00", nil, 0)

	err := f.svc.Transfer(context.Background(), service.TransferInput{
		ProductID: f.product, FromStoreID: f.store, ToStoreID: f.store,
		UserID: f.user, BatchNo: "B1", Quantity: qty("5"),
	})
	if !errors.Is(err, service.ErrSameStore) {
		t.Errorf("expected ErrSameStore, got %v", err)
	}
	assertQty(t, "stock after a rejected self-transfer", f.onHand(t, f.store), "20")
}

// ---------------------------------------------------------------------------
// Reports
// ---------------------------------------------------------------------------

// TestLowStockReport also covers two SQLite-isms that would not have survived
// the port: `is_active = 1` against a boolean, and a HAVING clause referencing
// a SELECT alias.
func TestLowStockReport(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	// The product's min_stock is 10.
	f.seedBatch(t, f.store, "A", "4", "40.00", nil, 0)
	f.seedBatch(t, f.store, "B", "3", "40.00", nil, 0)

	low, err := f.svc.LowStock(ctx, f.store)
	if err != nil {
		t.Fatalf("low stock: %v", err)
	}
	if len(low) != 1 {
		t.Fatalf("low stock returned %d products, want 1", len(low))
	}
	assertQty(t, "current stock", low[0].CurrentStock, "7")
	assertQty(t, "minimum stock", low[0].MinStock, "10")

	// Once it is stocked above the reorder level it leaves the report.
	f.seedBatch(t, f.store, "C", "20", "40.00", nil, 0)
	low, err = f.svc.LowStock(ctx, f.store)
	if err != nil {
		t.Fatalf("low stock: %v", err)
	}
	if len(low) != 0 {
		t.Errorf("a well-stocked product is still on the low-stock report: %+v", low)
	}
}

// TestExpiryReports checks that the two expiry reports partition the dated
// batches rather than overlapping.
//
// They used to overlap. "Expiring within thirty days" was expressed only as
// expiry_date <= today + 30, with no lower bound, so GONE — ten days past its
// date — was returned by both reports, showed up in both tabs of the expiry
// screen, and was counted as expiring soon while its days_until_expiry read
// -10. This test previously asserted that behaviour; it now asserts the
// partition, because a batch that has expired is not expiring.
func TestExpiryReports(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	f.seedBatch(t, f.store, "GONE", "5", "40.00", dateIn(-10), 0)
	f.seedBatch(t, f.store, "TODAY", "5", "40.00", dateIn(0), 0)
	f.seedBatch(t, f.store, "SOON", "5", "40.00", dateIn(7), 0)
	f.seedBatch(t, f.store, "LATER", "5", "40.00", dateIn(200), 0)
	f.seedBatch(t, f.store, "NODATE", "5", "40.00", nil, 0)

	expiring, err := f.svc.Expiring(ctx, f.store, 30)
	if err != nil {
		t.Fatalf("expiring: %v", err)
	}
	if len(expiring) != 2 {
		t.Fatalf("expiring within 30 days returned %d batches, want 2 (TODAY and SOON): %+v", len(expiring), expiring)
	}
	if expiring[0].BatchNo != "TODAY" {
		t.Errorf("expiring report is not in expiry order: first was %q", expiring[0].BatchNo)
	}
	// A batch expiring today is sellable for the rest of the day, so it is
	// expiring and not expired, and the count is zero rather than negative.
	if expiring[0].DaysUntilExpiry != 0 {
		t.Errorf("days until expiry for a batch expiring today = %d, want 0", expiring[0].DaysUntilExpiry)
	}
	if expiring[1].DaysUntilExpiry != 7 {
		t.Errorf("days until expiry = %d, want 7", expiring[1].DaysUntilExpiry)
	}
	for _, b := range expiring {
		if b.BatchNo == "GONE" {
			t.Errorf("an already-expired batch is on the expiring report: %+v", b)
		}
		if b.DaysUntilExpiry < 0 {
			t.Errorf("batch %q is on the expiring report with days_until_expiry %d — "+
				"a negative count means it has expired", b.BatchNo, b.DaysUntilExpiry)
		}
	}

	expired, err := f.svc.Expired(ctx, f.store)
	if err != nil {
		t.Fatalf("expired: %v", err)
	}
	if len(expired) != 1 || expired[0].BatchNo != "GONE" {
		t.Errorf("expired report = %+v, want only GONE", expired)
	}

	// The two reports must not name the same batch, or the screen's two tabs
	// show it twice and the "expiring soon" count double-counts it.
	onExpired := make(map[string]bool, len(expired))
	for _, b := range expired {
		onExpired[b.BatchNo] = true
	}
	for _, b := range expiring {
		if onExpired[b.BatchNo] {
			t.Errorf("batch %q is on both the expiring and the expired report", b.BatchNo)
		}
	}
}

func TestListStockAndProductStock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	f.seedBatch(t, f.store, "LATER", "5", "41.00", dateIn(90), 0)
	f.seedBatch(t, f.store, "SOON", "7", "40.00", dateIn(9), 0)
	f.seedBatch(t, f.store, "EMPTY", "0", "40.00", nil, 0)
	f.seedBatch(t, f.other, "ELSEWHERE", "99", "40.00", nil, 0)

	lines, err := f.svc.ListStock(ctx, f.store)
	if err != nil {
		t.Fatalf("list stock: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("listed %d batches, want 2 (an empty batch is not stock, and another store's is not ours): %+v",
			len(lines), lines)
	}
	if lines[0].ProductName != "Basmati Rice" {
		t.Errorf("product name = %q", lines[0].ProductName)
	}

	batches, err := f.svc.ProductStock(ctx, f.product, f.store)
	if err != nil {
		t.Fatalf("product stock: %v", err)
	}
	if len(batches) != 2 || batches[0].BatchNo != "SOON" {
		t.Errorf("product batches are not in FEFO order: %+v", batches)
	}
	assertQty(t, "available on the first batch", batches[0].Available(), "7")
}

func TestAdjustmentAuditTrailRecordsSignedDeltas(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("10"), Type: domain.AdjustAdd,
		CostPrice: money.MustFromString("40.00"),
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("4"), Type: domain.AdjustRemove,
	}); err != nil {
		t.Fatalf("remove: %v", err)
	}

	entries, err := f.svc.ListAdjustments(ctx, domain.AdjustmentFilter{StoreID: f.store})
	if err != nil {
		t.Fatalf("list adjustments: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("audit trail has %d entries, want 2", len(entries))
	}

	// Newest first: the removal, recorded as a negative delta so the column
	// sums to the net movement.
	assertQty(t, "removal delta", entries[0].Quantity, "-4")
	if entries[0].Type != domain.AdjustRemove {
		t.Errorf("type = %q, want remove", entries[0].Type)
	}
	if entries[0].Reason != "count_variance" {
		t.Errorf("default reason for a removal = %q, want count_variance", entries[0].Reason)
	}
	assertQty(t, "addition delta", entries[1].Quantity, "10")
	if entries[1].Reason != "received" {
		t.Errorf("default reason for an addition = %q, want received", entries[1].Reason)
	}
	if entries[0].ProductName != "Basmati Rice" {
		t.Errorf("product name = %q", entries[0].ProductName)
	}

	var net money.Quantity
	if err := f.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(quantity), 0) FROM stock_adjustments WHERE product_id = $1`,
		f.product).Scan(&net); err != nil {
		t.Fatalf("sum adjustments: %v", err)
	}
	assertQty(t, "sum of the audit trail", net, "6")
	assertQty(t, "actual stock", f.onHand(t, f.store), "6")
}

// TestAdjustAddFallsBackToTheCatalogueCostPrice checks that a batch created
// without a cost is not booked in at zero. inventory.go:217 discarded the scan
// error on this read, so a failed lookup silently became a free batch.
func TestAdjustAddFallsBackToTheCatalogueCostPrice(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.db.ExecContext(ctx,
		`INSERT INTO product_prices (product_id, store_id, product_unit_id, cost_price, sell_price)
		 VALUES ($1, $2, $3, 37.50, 45.00)`, f.product, f.store, f.baseUnit); err != nil {
		t.Fatalf("seed price: %v", err)
	}

	result, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("5"), Type: domain.AdjustAdd,
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if want := money.MustFromString("37.50"); !result.Batch.CostPrice.Equal(want) {
		t.Errorf("cost price = %s, want the catalogue's %s", result.Batch.CostPrice, want)
	}

	// A second addition without a cost must not zero the batch's cost basis.
	result, err = f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("5"), Type: domain.AdjustAdd,
		CostPrice: money.Zero(),
	})
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	assertQty(t, "quantity after two additions of 5", result.Batch.Quantity, "10")
	if result.Batch.CostPrice.IsZero() {
		t.Error("a second addition wrote off the batch's cost basis")
	}
}

// TestAnAdjustmentCarriesTheTimeItWasRecorded.
//
// The response to a movement is where somebody looks to confirm when it was
// booked. Returning the struct that went in reports created_at as year 0001 —
// not a time anything happened, and a value a client renders as such.
func TestAnAdjustmentCarriesTheTimeItWasRecorded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	result, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("3"), Type: domain.AdjustAdd,
	})
	if err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if result.Entry.CreatedAt.IsZero() {
		t.Error("the adjustment came back with a zero created_at")
	}

	// And it must agree with the row the history reads back.
	entries, err := f.svc.ListAdjustments(ctx, domain.AdjustmentFilter{StoreID: f.store})
	if err != nil {
		t.Fatalf("list adjustments: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the trail is empty after an adjustment")
	}
	if !entries[0].CreatedAt.Equal(result.Entry.CreatedAt) {
		t.Errorf("created_at differs between the response (%s) and the stored row (%s)",
			result.Entry.CreatedAt, entries[0].CreatedAt)
	}
}

// TestCostPriceIsStatedInTheUnitItWasCountedIn.
//
// A storekeeper booking in a delivery counts sacks and knows the price of a
// sack. Stock is costed per base unit, so a Rs.940 sack of 25 kg is Rs.37.60 a
// kilo — and storing 940 against a kilo is silent and expensive: the row
// commits, and every valuation, COGS figure and profit line downstream is out
// by the size of the pack.
//
// The receiving path has always divided (purchasing's costPerBase). This path
// did not, so the same delivery cost different money depending on which screen
// booked it in — a discrepancy nothing in the system would ever flag.
func TestCostPriceIsStatedInTheUnitItWasCountedIn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	sack := f.addUnit(t, "25 kg sack", "25")

	result, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "SEP", ProductUnitID: sack, UnitQty: qty("4"),
		Type: domain.AdjustAdd, CostPrice: money.MustFromString("940.00"),
	})
	if err != nil {
		t.Fatalf("book in 4 sacks: %v", err)
	}

	assertQty(t, "four 25 kg sacks", result.Batch.Quantity, "100")
	if want := money.MustFromString("37.60"); !result.Batch.CostPrice.Equal(want) {
		t.Errorf("cost price = %s, want %s — the sack price was stored against a kilo", result.Batch.CostPrice, want)
	}
	// The figure every report is struck from.
	value := result.Batch.CostPrice.Mul(result.Batch.Quantity)
	if want := money.MustFromString("3760.00"); !value.Equal(want) {
		t.Errorf("batch value = %s, want %s (4 sacks at 940)", value, want)
	}

	// A bare quantity carries no unit, so its cost is already per base unit and
	// must pass through untouched.
	bare, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "LOOSE", Quantity: qty("10"),
		Type: domain.AdjustAdd, CostPrice: money.MustFromString("37.60"),
	})
	if err != nil {
		t.Fatalf("book in 10 kg loose: %v", err)
	}
	if want := money.MustFromString("37.60"); !bare.Batch.CostPrice.Equal(want) {
		t.Errorf("a bare quantity's cost = %s, want %s unchanged", bare.Batch.CostPrice, want)
	}
}

// TestRemovingFromAnUnknownBatchFailsCleanly checks that a guarded update that
// matched nothing produces a business error rather than a silent success.
func TestRemovingFromAnUnknownBatchFailsCleanly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	_, err := f.svc.Adjust(context.Background(), service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "NOT-A-BATCH", Quantity: qty("1"), Type: domain.AdjustRemove,
	})
	if !errors.Is(err, domain.ErrInsufficientStock) {
		t.Errorf("expected ErrInsufficientStock, got %v", err)
	}
	if n := f.countRows(t, "stock_adjustments"); n != 0 {
		t.Errorf("a failed removal wrote %d audit rows, want 0", n)
	}
}

// TestMovementsRequireAnAttributedUser checks that nothing moves anonymously.
// Every one of these tables has a NOT NULL user_id, so this is the difference
// between a clear message and a foreign-key violation.
func TestMovementsRequireAnAttributedUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedBatch(t, f.store, "B1", "10", "40.00", nil, 0)

	_, err := f.svc.Adjust(context.Background(), service.AdjustInput{
		ProductID: f.product, StoreID: f.store,
		BatchNo: "B1", Quantity: qty("1"), Type: domain.AdjustAdd,
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation for an unattributed adjustment, got %v", err)
	}
	assertQty(t, "stock after a rejected unattributed adjustment", f.onHand(t, f.store), "10")
}

// ---------------------------------------------------------------------------
// Moving stock in a unit other than the base one
// ---------------------------------------------------------------------------
//
// There is no stock/convert any more, and these are why it is not missed. Stock
// is ONE pool in the product's base unit; a movement states its own unit and is
// multiplied into that pool at the moment it happens. Nothing is ever repacked
// from one balance into another, because there is only one balance.

// TestReceivingTenSacksAddsTwoHundredAndFiftyKilograms.
func TestReceivingTenSacksAddsTwoHundredAndFiftyKilograms(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	sack := f.addUnit(t, "25 kg sack", "25")

	result, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", ProductUnitID: sack, UnitQty: qty("10"),
		Type: domain.AdjustAdd, CostPrice: money.MustFromString("38.00"),
	})
	if err != nil {
		t.Fatalf("receive ten sacks: %v", err)
	}

	assertQty(t, "the pool after ten 25 kg sacks", f.onHand(t, f.store), "250")
	assertQty(t, "the batch after ten 25 kg sacks", f.batchQty(t, f.store, "B1"), "250")

	// The response says what was asked for as well as what moved, so a till can
	// show "10 sacks = 250.000 kg" and a reader can check the arithmetic.
	if result.Unit == nil {
		t.Fatal("the result does not report the unit the movement was stated in")
	}
	assertQty(t, "the unit's size", result.Unit.QtyInBase, "25")
	assertQty(t, "what the shopkeeper asked for", result.UnitQty, "10")
	assertQty(t, "the audit delta", result.Entry.Quantity, "250")

	// The audit trail keeps what was actually said. An entry reading "250.000"
	// alone gives no hint that somebody counted ten sacks, and a stock-take
	// dispute is settled on exactly that.
	if !strings.Contains(result.Entry.Reason, "10 x 25 kg sack") {
		t.Errorf("audit reason = %q, want it to record the units counted", result.Entry.Reason)
	}
}

// TestSellingTwoSacksTakesFiftyKilogramsFromOnePool. Two sacks out and a 250 g
// pouch out come from the same kilograms, so the shop can never be out of loose
// rice while sacks sit on the shelf.
func TestSellingTwoSacksTakesFiftyKilogramsFromOnePool(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	sack := f.addUnit(t, "25 kg sack", "25")
	pouch := f.addUnit(t, "250 g pouch", "0.25")

	f.seedBatch(t, f.store, "B1", "100", "38.00", nil, 0)

	if _, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", ProductUnitID: sack, UnitQty: qty("2"),
		Type: domain.AdjustRemove, Reason: "wholesale collection",
	}); err != nil {
		t.Fatalf("remove two sacks: %v", err)
	}
	// Exactly fifty, from the same batch. Not 49.999 and not a second balance.
	assertQty(t, "the pool after two sacks left", f.onHand(t, f.store), "50")

	// And a quarter kilo from the same pool the sacks came out of.
	if _, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", ProductUnitID: pouch, UnitQty: qty("1"),
		Type: domain.AdjustRemove,
	}); err != nil {
		t.Fatalf("remove one pouch: %v", err)
	}
	assertQty(t, "the pool after a 250 g sale", f.onHand(t, f.store), "49.75")

	// Four pouches is one kilo, exactly.
	if _, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", ProductUnitID: pouch, UnitQty: qty("4"),
		Type: domain.AdjustRemove,
	}); err != nil {
		t.Fatalf("remove four pouches: %v", err)
	}
	assertQty(t, "the pool after four more pouches", f.onHand(t, f.store), "48.75")

	// One row in one table holds all of it. A "sack balance" alongside a "loose
	// balance" is precisely what this model does not have.
	var batches int
	if err := f.db.QueryRowContext(ctx,
		`SELECT count(*) FROM stock WHERE product_id = $1 AND store_id = $2`,
		f.product, f.store).Scan(&batches); err != nil {
		t.Fatalf("count batches: %v", err)
	}
	if batches != 1 {
		t.Errorf("selling in three different units produced %d stock rows, want 1 pool", batches)
	}
}

// TestSellingMoreSacksThanThePoolHoldsIsRefused. The guard sees base units, so
// the shortfall is reported against the pool rather than against a sack count
// that does not exist.
func TestSellingMoreSacksThanThePoolHoldsIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	sack := f.addUnit(t, "25 kg sack", "25")

	f.seedBatch(t, f.store, "B1", "40", "38.00", nil, 0)

	// Forty kilos is one sack and change, so two are not there to be sold.
	_, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", ProductUnitID: sack, UnitQty: qty("2"),
		Type: domain.AdjustRemove,
	})
	if !errors.Is(err, domain.ErrInsufficientStock) {
		t.Errorf("expected ErrInsufficientStock, got %v", err)
	}
	assertQty(t, "the pool after a refused removal", f.onHand(t, f.store), "40")
}

// TestAMovementInAnUnknownOrForeignUnitIsRefused. The multiplier is read from
// the database and never taken from the caller: a client that could name its
// own qty_in_base could book in one sack and be credited with a tonne.
func TestAMovementInAnUnknownOrForeignUnitIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.seedBatch(t, f.store, "B1", "20", "38.00", nil, 0)

	// A unit belonging to another product entirely.
	var otherProduct, foreignUnit uuid.UUID
	var categoryID uuid.UUID
	mustScan(t, f.db, `SELECT id FROM categories LIMIT 1`, &categoryID)
	mustScan(t, f.db,
		`INSERT INTO products (name, category_id, gst_rate) VALUES ('Toor Dal', $1, 5) RETURNING id`,
		&otherProduct, categoryID)
	mustScan(t, f.db,
		`INSERT INTO product_units (product_id, label, qty_in_base, is_default)
		 VALUES ($1, 'Kilogram', 1, true) RETURNING id`, &foreignUnit, otherProduct)

	for name, in := range map[string]service.AdjustInput{
		"unknown unit": {
			ProductID: f.product, StoreID: f.store, UserID: f.user, BatchNo: "B1",
			ProductUnitID: uuid.New(), UnitQty: qty("1"), Type: domain.AdjustRemove,
		},
		"another product's unit": {
			ProductID: f.product, StoreID: f.store, UserID: f.user, BatchNo: "B1",
			ProductUnitID: foreignUnit, UnitQty: qty("1"), Type: domain.AdjustRemove,
		},
		"no quantity of the unit": {
			ProductID: f.product, StoreID: f.store, UserID: f.user, BatchNo: "B1",
			ProductUnitID: f.baseUnit, UnitQty: qty("0"), Type: domain.AdjustRemove,
		},
		"a negative number of units": {
			ProductID: f.product, StoreID: f.store, UserID: f.user, BatchNo: "B1",
			ProductUnitID: f.baseUnit, UnitQty: qty("-3"), Type: domain.AdjustRemove,
		},
	} {
		if _, err := f.svc.Adjust(ctx, in); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("%s: expected ErrValidation, got %v", name, err)
		}
	}
	assertQty(t, "the pool after every refused movement", f.onHand(t, f.store), "20")
}

// TestABareQuantityStillMeansBaseUnits. Naming no unit is the ordinary case and
// must go on meaning exactly what it did before any of this.
func TestABareQuantityStillMeansBaseUnits(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.addUnit(t, "25 kg sack", "25")

	result, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("7.5"), Type: domain.AdjustAdd,
	})
	if err != nil {
		t.Fatalf("add a bare quantity: %v", err)
	}
	assertQty(t, "the pool after a bare quantity", f.onHand(t, f.store), "7.5")
	if result.Unit != nil {
		t.Errorf("a bare quantity reported unit %+v, want none", result.Unit)
	}
}

// TestCatalogueCostIsReadFromTheDefaultUnit.
//
// product_prices is unique on (product_id, store_id, product_unit_id) now, so a
// product can carry several price rows in one store. A read filtered on product
// and store alone takes an arbitrary one, and a sack's Rs.950 booked in as the
// cost of a single kilo is a silent, permanent error in COGS.
func TestCatalogueCostIsReadFromTheDefaultUnit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	sack := f.addUnit(t, "25 kg sack", "25")

	// The sack is priced first and priced higher, so a query that takes
	// whichever row comes back first is likely to take the wrong one.
	for _, row := range []struct {
		unit uuid.UUID
		cost string
	}{
		{sack, "950.0000"},
		{f.baseUnit, "37.5000"},
	} {
		if _, err := f.db.ExecContext(ctx,
			`INSERT INTO product_prices (product_id, store_id, product_unit_id, cost_price, sell_price)
			 VALUES ($1, $2, $3, $4, 0)`, f.product, f.store, row.unit, row.cost); err != nil {
			t.Fatalf("seed price: %v", err)
		}
	}

	result, err := f.svc.Adjust(ctx, service.AdjustInput{
		ProductID: f.product, StoreID: f.store, UserID: f.user,
		BatchNo: "B1", Quantity: qty("5"), Type: domain.AdjustAdd,
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if want := money.MustFromString("37.50"); !result.Batch.CostPrice.Equal(want) {
		t.Errorf("batch cost = %s, want the default unit's %s and not the sack's 950.00",
			result.Batch.CostPrice, want)
	}
}
