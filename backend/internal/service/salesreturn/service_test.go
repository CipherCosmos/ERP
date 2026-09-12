package salesreturn_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"gbos/internal/app"
	billingdomain "gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	domain "gbos/internal/domain/salesreturn"
	stockdomain "gbos/internal/domain/stock"
	platform "gbos/internal/platform/postgres"
	billingsvc "gbos/internal/service/billing"
	service "gbos/internal/service/salesreturn"
	"gbos/internal/testsupport"

	"github.com/google/uuid"
)

// shop is a store set up to sell and to take goods back: a store, a counter, a
// user, and products with stock on the shelf.
type shop struct {
	t          *testing.T
	db         *platform.DB
	svc        *service.Service
	svcs       app.Services
	storeID    uuid.UUID
	terminalID uuid.UUID
	userID     uuid.UUID
	uomID      uuid.UUID
	categoryID uuid.UUID
}

func newShop(t *testing.T) *shop {
	t.Helper()
	db := testsupport.OpenDB(t)

	svcs, err := app.Build(db, app.Config{JWTSecret: "salesreturn-tests-signing-secret-x"})
	if err != nil {
		t.Fatalf("build services: %v", err)
	}

	x := &shop{t: t, db: db, svc: svcs.SalesReturn, svcs: svcs}
	mustScan(t, db, `INSERT INTO stores (name, gstin) VALUES ('Returns Shop', '29ABCDE1234F1Z5') RETURNING id`, &x.storeID)
	mustScan(t, db, `INSERT INTO terminals (store_id, code, name) VALUES ($1, 'T1', 'Counter 1') RETURNING id`,
		&x.terminalID, x.storeID)
	mustScan(t, db, `INSERT INTO users (username, password_hash, role, store_id)
		VALUES ('returns-clerk', 'x', 'cashier', $1) RETURNING id`, &x.userID, x.storeID)
	// The units migration seeds the ordinary Indian retail vocabulary, so the
	// kilogram is already on file with its family and conversion factor. A test
	// that inserted its own would collide on the unique code.
	mustScan(t, db, `SELECT id FROM units_of_measure WHERE code = 'KG'`, &x.uomID)
	mustScan(t, db, `INSERT INTO categories (name) VALUES ('Groceries') RETURNING id`, &x.categoryID)
	return x
}

func mustScan(t *testing.T, db *platform.DB, query string, dest any, args ...any) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(dest); err != nil {
		t.Fatalf("seed (%s): %v", query, err)
	}
}

// otherStore adds a second shop sharing the same catalogue, so store scoping
// can be exercised for real rather than with an invented uuid.
func (x *shop) otherStore(name string) uuid.UUID {
	x.t.Helper()
	var id uuid.UUID
	mustScan(x.t, x.db, `INSERT INTO stores (name, gstin) VALUES ($1, '29ZZZZZ9999Z9Z9') RETURNING id`, &id, name)
	return id
}

// product creates a product priced at sell, with onHand units in the 'OPENING'
// batch at cost. An onHand of "0" seeds no batch at all, leaving the shelf
// genuinely empty rather than holding a zero-quantity row.
func (x *shop) product(name, sell, cost, onHand string) uuid.UUID {
	x.t.Helper()
	var productID uuid.UUID
	mustScan(x.t, x.db, `INSERT INTO products (name, category_id, uom_id, gst_rate, allow_decimal)
		VALUES ($1, $2, $3, 5, true) RETURNING id`, &productID, name, x.categoryID, x.uomID)

	// One selling unit, worth one kilo, priced for this shop. Prices are per
	// unit now — a sack is not 25 times the loose kilo — and nothing in these
	// return and write-off cases turns on bulk.
	var unitID uuid.UUID
	mustScan(x.t, x.db, `INSERT INTO product_units (product_id, uom_id, label, qty_in_base, is_default)
		VALUES ($1, $2, 'Loose', 1, true) RETURNING id`, &unitID, productID, x.uomID)

	if _, err := x.db.ExecContext(context.Background(),
		`INSERT INTO product_prices (product_id, store_id, product_unit_id, mrp, cost_price, sell_price)
		 VALUES ($1, $2, $3, $4, $5, $4)`, productID, x.storeID, unitID,
		money.MustFromString(sell), money.MustFromString(cost)); err != nil {
		x.t.Fatalf("seed price: %v", err)
	}
	if money.MustQuantityFromString(onHand).IsPositive() {
		x.batch(productID, x.storeID, "OPENING", onHand, cost)
	}
	return productID
}

// batch puts a named lot of a product on a store's shelf.
func (x *shop) batch(productID, storeID uuid.UUID, batchNo, qty, cost string) {
	x.t.Helper()
	if _, err := x.db.ExecContext(context.Background(),
		`INSERT INTO stock (product_id, store_id, batch_no, quantity, total_cost)
		 VALUES ($1, $2, $3, $4, $5)`, productID, storeID, batchNo,
		money.MustQuantityFromString(qty),
		money.MustFromString(cost).Mul(money.MustQuantityFromString(qty))); err != nil {
		x.t.Fatalf("seed batch %s: %v", batchNo, err)
	}
}

// sell raises a real invoice through the billing service, which is what records
// the batch a return can go back into.
func (x *shop) sell(productID uuid.UUID, qty string) billingsvc.Result {
	x.t.Helper()
	got, err := x.svcs.Billing.Create(context.Background(), billingsvc.CreateInput{
		StoreID:     x.storeID,
		UserID:      x.userID,
		TerminalID:  x.terminalID,
		PaymentMode: billingdomain.PaymentCash,
		Lines: []billingdomain.LineInput{{
			ProductID: productID,
			UnitQty:   money.MustQuantityFromString(qty),
		}},
	})
	if err != nil {
		x.t.Fatalf("sale: %v", err)
	}
	return got
}

func (x *shop) onHand(productID, storeID uuid.UUID) money.Quantity {
	x.t.Helper()
	var q money.Quantity
	mustScan(x.t, x.db, `SELECT COALESCE(SUM(quantity), 0) FROM stock WHERE product_id = $1 AND store_id = $2`,
		&q, productID, storeID)
	return q
}

func (x *shop) batchQty(productID, storeID uuid.UUID, batchNo string) money.Quantity {
	x.t.Helper()
	var q money.Quantity
	mustScan(x.t, x.db,
		`SELECT COALESCE(SUM(quantity), 0) FROM stock WHERE product_id = $1 AND store_id = $2 AND batch_no = $3`,
		&q, productID, storeID, batchNo)
	return q
}

func (x *shop) countRows(query string, args ...any) int {
	x.t.Helper()
	var n int
	mustScan(x.t, x.db, query, &n, args...)
	return n
}

func (x *shop) statusOf(returnID uuid.UUID) string {
	x.t.Helper()
	var s string
	mustScan(x.t, x.db, `SELECT status FROM returns WHERE id = $1`, &s, returnID)
	return s
}

// takeBack records a pending return of qty against an optional invoice.
func (x *shop) takeBack(productID uuid.UUID, invoiceID *uuid.UUID, qty, amount string) domain.Return {
	x.t.Helper()
	ret, err := x.svc.CreateReturn(context.Background(), domain.ReturnInput{
		InvoiceID:  invoiceID,
		ProductID:  productID,
		StoreID:    x.storeID,
		Quantity:   money.MustQuantityFromString(qty),
		Amount:     money.MustFromString(amount),
		Reason:     "customer changed their mind",
		RefundMode: "cash",
	})
	if err != nil {
		x.t.Fatalf("create return: %v", err)
	}
	return ret
}

// --- the headline fix: approval is not replayable -----------------------------

func TestApprovingTheSameReturnTwiceRestoresStockOnce(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "100")

	sale := x.sell(rice, "10")
	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("90")) {
		t.Fatalf("on hand after the sale = %s, want 90", got)
	}

	ret := x.takeBack(rice, &sale.Invoice.ID, "4", "1890.00")
	if _, err := x.svc.Approve(ctx, x.storeID, ret.ID); err != nil {
		t.Fatalf("first approval: %v", err)
	}
	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("94")) {
		t.Fatalf("on hand after approving = %s, want 94", got)
	}

	// The old handler selected only (product_id, quantity), never read the
	// status, and updated with no guard — so every click on Approve restored
	// the quantity again, inventing goods out of nothing.
	if _, err := x.svc.Approve(ctx, x.storeID, ret.ID); !errors.Is(err, service.ErrAlreadyApproved) {
		t.Fatalf("second approval: expected ErrAlreadyApproved, got %v", err)
	}
	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("94")) {
		t.Errorf("on hand after approving twice = %s, want 94 — the goods came back twice", got)
	}
}

func TestConcurrentApprovalsRestoreStockOnce(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "100")

	sale := x.sell(rice, "10")
	ret := x.takeBack(rice, &sale.Invoice.ID, "4", "1890.00")

	const attempts = 6
	var wg sync.WaitGroup
	results := make([]error, attempts)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = x.svc.Approve(ctx, x.storeID, ret.ID)
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for i, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, service.ErrAlreadyApproved):
			// The expected outcome for the losers.
		default:
			t.Errorf("approval %d failed unexpectedly: %v", i, err)
		}
	}
	if succeeded != 1 {
		t.Errorf("%d concurrent approvals succeeded, want exactly 1", succeeded)
	}
	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("94")) {
		t.Errorf("on hand after %d concurrent approvals = %s, want 94", attempts, got)
	}
}

// --- store scoping ------------------------------------------------------------

func TestApprovingAnotherStoresReturnIsRefusedAndMovesNothing(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "100")

	// A neighbouring shop that sells the same product and holds its own stock.
	other := x.otherStore("Rival Shop")
	x.batch(rice, other, "OPENING", "20", "337.50")

	sale := x.sell(rice, "10")
	ret := x.takeBack(rice, &sale.Invoice.ID, "4", "1890.00")

	// The neighbour tries to approve it. The old handler selected the return by
	// id with no store filter and then credited the store from the *query
	// string*, so this call would have moved four kilos of somebody else's rice
	// onto the rival's shelf.
	if _, err := x.svc.Approve(ctx, other, ret.ID); !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("cross-store approval: expected ErrNotFound, got %v", err)
	}

	if got := x.onHand(rice, other); !got.Equal(money.MustQuantityFromString("20")) {
		t.Errorf("the rival's stock = %s, want the original 20", got)
	}
	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("90")) {
		t.Errorf("the owning store's stock = %s, want 90 — nothing should have moved", got)
	}
	if got := x.statusOf(ret.ID); got != string(domain.StatusPending) {
		t.Errorf("the return is %q after a refused cross-store approval, want pending", got)
	}

	// And the owning store can still approve it afterwards.
	if _, err := x.svc.Approve(ctx, x.storeID, ret.ID); err != nil {
		t.Fatalf("the owning store's approval: %v", err)
	}
	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("94")) {
		t.Errorf("on hand = %s after the rightful approval, want 94", got)
	}
}

func TestListingIsScopedToTheStore(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "100")

	sale := x.sell(rice, "10")
	x.takeBack(rice, &sale.Invoice.ID, "2", "945.00")

	mine, err := x.svc.ListReturns(ctx, domain.ReturnFilter{StoreID: x.storeID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(mine) != 1 {
		t.Errorf("own store sees %d returns, want 1", len(mine))
	}

	theirs, err := x.svc.ListReturns(ctx, domain.ReturnFilter{StoreID: x.otherStore("Rival Shop")})
	if err != nil {
		t.Fatalf("list for the other store: %v", err)
	}
	if len(theirs) != 0 {
		t.Errorf("another store sees %d of this store's returns, want 0", len(theirs))
	}
}

// --- batch linkage ------------------------------------------------------------

func TestGoodsGoBackIntoTheBatchTheyLeftOn(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "0")

	// Two lots. FEFO with no expiry dates falls back to oldest-created-first, so
	// the sale leaves on LOT-A.
	x.batch(rice, x.storeID, "LOT-A", "6", "300.00")
	x.batch(rice, x.storeID, "LOT-B", "6", "400.00")

	sale := x.sell(rice, "4")
	if got := x.batchQty(rice, x.storeID, "LOT-A"); !got.Equal(money.MustQuantityFromString("2")) {
		t.Fatalf("LOT-A after the sale = %s, want 2", got)
	}

	ret := x.takeBack(rice, &sale.Invoice.ID, "3", "1350.00")
	approved, err := x.svc.Approve(ctx, x.storeID, ret.ID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	// The goods go back where they came from, read from invoice_items.batch_no.
	// The old handler restored into "the first stock row for this product with
	// a positive quantity" — LOT-A here only by accident — and, when no such row
	// existed, into a freshly invented "RETURN-STOCK" batch at a cost of zero.
	if approved.RestoredBatch != "LOT-A" {
		t.Errorf("restored into batch %q, want LOT-A", approved.RestoredBatch)
	}
	if got := x.batchQty(rice, x.storeID, "LOT-A"); !got.Equal(money.MustQuantityFromString("5")) {
		t.Errorf("LOT-A = %s after the return, want 5", got)
	}
	if got := x.batchQty(rice, x.storeID, "LOT-B"); !got.Equal(money.MustQuantityFromString("6")) {
		t.Errorf("LOT-B = %s, want the untouched 6", got)
	}
	if got := x.countRows(`SELECT count(*) FROM stock WHERE product_id = $1 AND store_id = $2`, rice, x.storeID); got != 2 {
		t.Errorf("%d stock rows for this product, want 2 — a batch was invented", got)
	}

	// Putting goods back is not a revaluation: LOT-A keeps the cost it had.
	var cost money.Money
	mustScan(t, x.db, `SELECT cost_price FROM stock WHERE product_id = $1 AND store_id = $2 AND batch_no = 'LOT-A'`,
		&cost, rice, x.storeID)
	if !cost.Equal(money.MustFromString("300.00")) {
		t.Errorf("LOT-A cost = %s after the return, want the original 300.00", cost)
	}
}

func TestAReturnWithNoInvoiceGoesIntoTheDefaultBatch(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "10")

	// No bill to point at. The goods have to land somewhere, and that somewhere
	// is the product's untracked default lot — not a per-second invented one.
	ret := x.takeBack(rice, nil, "2", "900.00")
	approved, err := x.svc.Approve(ctx, x.storeID, ret.ID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.RestoredBatch != stockdomain.DefaultBatch {
		t.Errorf("restored into batch %q, want the default lot", approved.RestoredBatch)
	}
	if got := x.batchQty(rice, x.storeID, stockdomain.DefaultBatch); !got.Equal(money.MustQuantityFromString("2")) {
		t.Errorf("default lot = %s, want 2", got)
	}
	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("12")) {
		t.Errorf("on hand = %s, want 12", got)
	}
}

// --- returns against a voided sale --------------------------------------------

func TestApprovingAReturnAgainstAVoidedInvoiceIsRefused(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "100")

	sale := x.sell(rice, "10")
	ret := x.takeBack(rice, &sale.Invoice.ID, "4", "1890.00")

	// Voiding the sale already puts all ten kilos back.
	if err := x.svcs.Billing.Void(ctx, x.storeID, sale.Invoice.ID); err != nil {
		t.Fatalf("void: %v", err)
	}
	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("100")) {
		t.Fatalf("on hand after the void = %s, want 100", got)
	}

	// Approving the return would put four of those kilos back a second time and
	// refund a customer whose receivable the void already cancelled.
	if _, err := x.svc.Approve(ctx, x.storeID, ret.ID); !errors.Is(err, service.ErrInvoiceVoided) {
		t.Fatalf("expected ErrInvoiceVoided, got %v", err)
	}
	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("100")) {
		t.Errorf("on hand = %s after the refused approval, want 100", got)
	}
	// The guarded UPDATE ran before the check, so the refusal has to have rolled
	// the status change back too.
	if got := x.statusOf(ret.ID); got != string(domain.StatusPending) {
		t.Errorf("the return is %q after a refused approval, want pending", got)
	}
}

// --- what a return may claim --------------------------------------------------

func TestReturningMoreThanWasSoldIsRefused(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "100")

	sale := x.sell(rice, "3")

	_, err := x.svc.CreateReturn(ctx, domain.ReturnInput{
		InvoiceID: &sale.Invoice.ID,
		ProductID: rice,
		StoreID:   x.storeID,
		Quantity:  money.MustQuantityFromString("5"),
		Amount:    money.MustFromString("2250.00"),
		Reason:    "wants more back than they bought",
	})
	if !errors.Is(err, service.ErrOverReturn) {
		t.Fatalf("expected ErrOverReturn, got %v", err)
	}

	// Two partial returns that together exceed the sale are refused on the
	// second one, not on the first.
	x.takeBack(rice, &sale.Invoice.ID, "2", "900.00")
	_, err = x.svc.CreateReturn(ctx, domain.ReturnInput{
		InvoiceID: &sale.Invoice.ID,
		ProductID: rice,
		StoreID:   x.storeID,
		Quantity:  money.MustQuantityFromString("2"),
		Amount:    money.MustFromString("900.00"),
		Reason:    "and the rest",
	})
	if !errors.Is(err, service.ErrOverReturn) {
		t.Fatalf("expected the second partial return to exceed the sale, got %v", err)
	}
	if got := x.countRows(`SELECT count(*) FROM returns WHERE store_id = $1`, x.storeID); got != 1 {
		t.Errorf("%d returns recorded, want 1", got)
	}
}

func TestAReturnForAProductThatWasNotOnTheInvoiceIsRefused(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "100")
	soap := x.product("Soap", "60.00", "45.00", "100")

	sale := x.sell(rice, "3")

	_, err := x.svc.CreateReturn(ctx, domain.ReturnInput{
		InvoiceID: &sale.Invoice.ID,
		ProductID: soap,
		StoreID:   x.storeID,
		Quantity:  money.MustQuantityFromString("1"),
		Amount:    money.MustFromString("60.00"),
		Reason:    "soap that this bill never sold",
	})
	if !errors.Is(err, service.ErrNotOnInvoice) {
		t.Fatalf("expected ErrNotOnInvoice, got %v", err)
	}
}

func TestAReturnAgainstAnotherStoresInvoiceIsRefused(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "100")
	sale := x.sell(rice, "3")

	other := x.otherStore("Rival Shop")
	x.batch(rice, other, "OPENING", "20", "337.50")

	_, err := x.svc.CreateReturn(ctx, domain.ReturnInput{
		InvoiceID: &sale.Invoice.ID,
		ProductID: rice,
		StoreID:   other,
		Quantity:  money.MustQuantityFromString("1"),
		Amount:    money.MustFromString("450.00"),
		Reason:    "borrowing somebody else's bill",
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected a validation error for a cross-store invoice, got %v", err)
	}
}

func TestAReturnNeedsAQuantityAndAReason(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "100")

	cases := []struct {
		name string
		in   domain.ReturnInput
	}{
		{"zero quantity", domain.ReturnInput{ProductID: rice, StoreID: x.storeID,
			Quantity: money.ZeroQuantity(), Reason: "nothing at all"}},
		{"negative quantity", domain.ReturnInput{ProductID: rice, StoreID: x.storeID,
			Quantity: money.MustQuantityFromString("-5"), Reason: "minting stock"}},
		{"negative refund", domain.ReturnInput{ProductID: rice, StoreID: x.storeID,
			Quantity: money.MustQuantityFromString("1"), Amount: money.MustFromString("-1.00"), Reason: "owed money"}},
		{"no reason", domain.ReturnInput{ProductID: rice, StoreID: x.storeID,
			Quantity: money.MustQuantityFromString("1"), Reason: "   "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := x.svc.CreateReturn(ctx, c.in); !errors.Is(err, domain.ErrValidation) {
				t.Errorf("expected ErrValidation, got %v", err)
			}
		})
	}
	if got := x.countRows(`SELECT count(*) FROM returns WHERE store_id = $1`, x.storeID); got != 0 {
		t.Errorf("%d returns recorded from refused requests, want 0", got)
	}
}

// --- inventory losses ---------------------------------------------------------

func (x *shop) writeOff(productID uuid.UUID, batchHint, qty, lossType string) (domain.Loss, error) {
	return x.svc.RecordLoss(context.Background(), domain.LossInput{
		ProductID: productID,
		StoreID:   x.storeID,
		UserID:    x.userID,
		BatchNo:   batchHint,
		Quantity:  money.MustQuantityFromString(qty),
		LossType:  lossType,
		Reason:    "a mouse got in",
	})
}

func TestALossLargerThanAnySingleBatchIsSpreadAcrossThem(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	rice := x.product("Rice", "450.00", "337.50", "0")

	// Three lots of four. No single one holds the ten being written off, which
	// is precisely the case the old `WHERE rowid = (SELECT ... quantity >= ?
	// ... LIMIT 1)` subquery matched nothing for: zero rows changed, err was
	// nil, and the API answered 201 with the goods still on the shelf.
	x.batch(rice, x.storeID, "LOT-A", "4", "300.00")
	x.batch(rice, x.storeID, "LOT-B", "4", "310.00")
	x.batch(rice, x.storeID, "LOT-C", "4", "320.00")

	loss, err := x.writeOff(rice, "", "10", "damage")
	if err != nil {
		t.Fatalf("record loss: %v", err)
	}

	// Exactly ten units left, and they left the lots they were actually in.
	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("2")) {
		t.Errorf("on hand = %s after writing off 10 of 12, want 2", got)
	}
	if len(loss.Batches) != 3 {
		t.Fatalf("the write-off names %d lots, want 3", len(loss.Batches))
	}
	spread := money.ZeroQuantity()
	for _, a := range loss.Batches {
		spread = spread.Add(a.Quantity)
	}
	if !spread.Equal(money.MustQuantityFromString("10")) {
		t.Errorf("the named lots total %s, want 10", spread)
	}
	if loss.BatchNo != "LOT-A" {
		t.Errorf("batch_no recorded as %q, want the first lot consumed (LOT-A)", loss.BatchNo)
	}
	// 4x300 + 4x310 + 2x320 = 3080.00
	if want := money.MustFromString("3080.00"); !loss.Value().Equal(want) {
		t.Errorf("the write-off is valued at %s, want %s", loss.Value(), want)
	}
	if got := x.countRows(`SELECT count(*) FROM inventory_losses WHERE store_id = $1`, x.storeID); got != 1 {
		t.Errorf("%d loss rows, want 1", got)
	}
}

func TestALossLargerThanTheTotalOnHandIsRefusedAndRecordsNothing(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	rice := x.product("Rice", "450.00", "337.50", "0")
	x.batch(rice, x.storeID, "LOT-A", "4", "300.00")
	x.batch(rice, x.storeID, "LOT-B", "3", "310.00")

	_, err := x.writeOff(rice, "", "10", "wastage")
	if !errors.Is(err, stockdomain.ErrInsufficientStock) {
		t.Fatalf("expected ErrInsufficientStock, got %v", err)
	}

	// This is the assertion the old code could not make. A write-off that
	// cannot be satisfied must leave both the books and the shelf exactly as
	// they were — never the loss on the books with the goods still there.
	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("7")) {
		t.Errorf("on hand = %s after a refused write-off, want the original 7", got)
	}
	if got := x.countRows(`SELECT count(*) FROM inventory_losses WHERE store_id = $1`, x.storeID); got != 0 {
		t.Errorf("%d loss rows after a refused write-off, want 0", got)
	}
}

func TestALossHonoursTheBatchItIsToldAboutAndFallsBackWhenItCannot(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	rice := x.product("Rice", "450.00", "337.50", "0")
	x.batch(rice, x.storeID, "LOT-A", "5", "300.00")
	x.batch(rice, x.storeID, "LOT-B", "5", "310.00")

	// A named lot that can cover the whole write-off is used as named, even
	// though FEFO would otherwise reach for LOT-A first.
	if _, err := x.writeOff(rice, "LOT-B", "3", "expiry"); err != nil {
		t.Fatalf("write off from LOT-B: %v", err)
	}
	if got := x.batchQty(rice, x.storeID, "LOT-B"); !got.Equal(money.MustQuantityFromString("2")) {
		t.Errorf("LOT-B = %s, want 2", got)
	}
	if got := x.batchQty(rice, x.storeID, "LOT-A"); !got.Equal(money.MustQuantityFromString("5")) {
		t.Errorf("LOT-A = %s, want the untouched 5", got)
	}

	// A named lot that cannot cover the whole quantity is a hint, not a
	// constraint. LOT-B holds only two, so the write-off falls through to the
	// first-expired-first-out walk and takes all four from LOT-A rather than
	// refusing — which is what the old single-batch subquery did.
	loss, err := x.writeOff(rice, "LOT-B", "4", "expiry")
	if err != nil {
		t.Fatalf("write off more than LOT-B holds: %v", err)
	}
	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("3")) {
		t.Errorf("on hand = %s, want 3", got)
	}
	if got := x.batchQty(rice, x.storeID, "LOT-A"); !got.Equal(money.MustQuantityFromString("1")) {
		t.Errorf("LOT-A = %s, want 1 — the walk should have taken all four from it", got)
	}
	if got := x.batchQty(rice, x.storeID, "LOT-B"); !got.Equal(money.MustQuantityFromString("2")) {
		t.Errorf("LOT-B = %s, want the untouched 2", got)
	}
	if loss.BatchNo != "LOT-A" {
		t.Errorf("batch_no recorded as %q, want LOT-A — the lot it actually came from, not the one asked for", loss.BatchNo)
	}
}

func TestALossNeedsAUserAQuantityAndAType(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "10")

	good := domain.LossInput{
		ProductID: rice, StoreID: x.storeID, UserID: x.userID,
		Quantity: money.MustQuantityFromString("1"), LossType: "damage",
	}
	cases := map[string]func(in domain.LossInput) domain.LossInput{
		// An unattributed write-off is indistinguishable from theft.
		"no user":      func(in domain.LossInput) domain.LossInput { in.UserID = uuid.Nil; return in },
		"no loss type": func(in domain.LossInput) domain.LossInput { in.LossType = "  "; return in },
		"zero quantity": func(in domain.LossInput) domain.LossInput {
			in.Quantity = money.ZeroQuantity()
			return in
		},
		// A negative write-off would mint stock out of a loss.
		"negative quantity": func(in domain.LossInput) domain.LossInput {
			in.Quantity = money.MustQuantityFromString("-3")
			return in
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := x.svc.RecordLoss(ctx, mutate(good)); !errors.Is(err, domain.ErrValidation) {
				t.Errorf("expected ErrValidation, got %v", err)
			}
		})
	}
	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("10")) {
		t.Errorf("on hand = %s after refused write-offs, want 10", got)
	}
	if got := x.countRows(`SELECT count(*) FROM inventory_losses WHERE store_id = $1`, x.storeID); got != 0 {
		t.Errorf("%d loss rows from refused requests, want 0", got)
	}
}

func TestLossesAreListedNewestFirstAndFilteredByType(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "50")

	if _, err := x.writeOff(rice, "", "2", "damage"); err != nil {
		t.Fatalf("damage: %v", err)
	}
	if _, err := x.writeOff(rice, "", "3", "expiry"); err != nil {
		t.Fatalf("expiry: %v", err)
	}

	all, err := x.svc.ListLosses(ctx, domain.LossFilter{StoreID: x.storeID})
	if err != nil {
		t.Fatalf("list losses: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("%d losses listed, want 2", len(all))
	}

	expiry, err := x.svc.ListLosses(ctx, domain.LossFilter{StoreID: x.storeID, LossType: "expiry"})
	if err != nil {
		t.Fatalf("list expiry losses: %v", err)
	}
	if len(expiry) != 1 || expiry[0].LossType != "expiry" {
		t.Errorf("expiry filter returned %d rows, want 1", len(expiry))
	}
	if !expiry[0].Quantity.Equal(money.MustQuantityFromString("3")) {
		t.Errorf("expiry loss quantity = %s, want 3", expiry[0].Quantity)
	}
}

// --- store is mandatory -------------------------------------------------------

func TestEveryOperationNeedsAStore(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	if _, err := x.svc.CreateReturn(ctx, domain.ReturnInput{ProductID: uuid.New()}); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("CreateReturn without a store: got %v", err)
	}
	if _, err := x.svc.Approve(ctx, uuid.Nil, uuid.New()); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("Approve without a store: got %v", err)
	}
	if _, err := x.svc.RecordLoss(ctx, domain.LossInput{ProductID: uuid.New()}); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("RecordLoss without a store: got %v", err)
	}
	if _, err := x.svc.ListReturns(ctx, domain.ReturnFilter{}); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("ListReturns without a store: got %v", err)
	}
	if _, err := x.svc.ListLosses(ctx, domain.LossFilter{}); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("ListLosses without a store: got %v", err)
	}
}

// --- the whole cycle ----------------------------------------------------------

func TestASaleAReturnAndAWriteOffReconcile(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "450.00", "337.50", "100")

	sale := x.sell(rice, "20") // 100 -> 80
	ret := x.takeBack(rice, &sale.Invoice.ID, "5", "2250.00")
	if _, err := x.svc.Approve(ctx, x.storeID, ret.ID); err != nil {
		t.Fatalf("approve: %v", err)
	} // 80 -> 85
	if _, err := x.writeOff(rice, "", "6", "damage"); err != nil {
		t.Fatalf("write off: %v", err)
	} // 85 -> 79

	if got := x.onHand(rice, x.storeID); !got.Equal(money.MustQuantityFromString("79")) {
		t.Errorf("on hand = %s, want 79 (100 - 20 + 5 - 6)", got)
	}

	// And the paperwork says the same thing the shelf does.
	var sold, returned, lost money.Quantity
	mustScan(t, x.db, `SELECT COALESCE(SUM(quantity), 0) FROM invoice_items ii
		JOIN invoices i ON i.id = ii.invoice_id
		WHERE i.store_id = $1 AND i.status = 'completed'`, &sold, x.storeID)
	mustScan(t, x.db, `SELECT COALESCE(SUM(quantity), 0) FROM returns
		WHERE store_id = $1 AND status = 'completed'`, &returned, x.storeID)
	mustScan(t, x.db, `SELECT COALESCE(SUM(quantity), 0) FROM inventory_losses WHERE store_id = $1`, &lost, x.storeID)

	net := money.MustQuantityFromString("100").Sub(sold).Add(returned).Sub(lost)
	if !net.Equal(x.onHand(rice, x.storeID)) {
		t.Errorf("the books say %s on hand, the shelf says %s", net, x.onHand(rice, x.storeID))
	}
}
