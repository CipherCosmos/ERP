package billing_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	domain "gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	stockdomain "gbos/internal/domain/stock"
	platform "gbos/internal/platform/postgres"
	service "gbos/internal/service/billing"

	"github.com/google/uuid"
)

// This file is about a shop on a busy day rather than about one operation at a
// time: two counters billing at once, a till retrying a request it never heard
// the answer to, the last packet of biscuits, and the night of 31 March.
//
// Every case here is driven through the real service against real Postgres with
// real goroutines. A mocked repository cannot see any of it: the defects are
// lock ordering, row-level guards and commit visibility, which exist only in
// the database.

// --- concurrency at the counter ----------------------------------------------

// TestTwoCountersCannotSellTheLastUnit drives more simultaneous sales at a
// batch than it holds.
//
// TestConcurrentRemovalsCannotOversell in the stock package proves the guarded
// UPDATE holds for a bare adjustment. This proves it through the whole sale
// path — where the deduction is one step of a transaction that also numbers an
// invoice and writes its lines — because that is the path a cashier uses and
// the one with more to go wrong.
func TestTwoCountersCannotSellTheLastUnit(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	biscuits := x.stockProduct("Biscuits", "20.00", "14.00", "1", 18, false)

	const attempts = 8
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = x.sale(service.CreateInput{Lines: []domain.LineInput{line(biscuits, "1")}})
		}(i)
	}
	wg.Wait()

	sold := 0
	for i, err := range errs {
		switch {
		case err == nil:
			sold++
		case errors.Is(err, stockdomain.ErrInsufficientStock):
			// The expected refusal: somebody else got the last packet.
		default:
			t.Errorf("sale %d failed for the wrong reason: %v", i, err)
		}
	}
	if sold != 1 {
		t.Errorf("%d of %d simultaneous sales of the last unit succeeded, want exactly 1", sold, attempts)
	}
	if got := x.onHand(biscuits); !got.Equal(money.ZeroQuantity()) {
		t.Errorf("on hand = %s, want 0 — stock went negative or was never taken", got)
	}

	// A refused sale must leave no paperwork at all, not an invoice with no
	// goods behind it.
	var invoices int
	mustScan(t, x.db, `SELECT count(*) FROM invoices WHERE store_id = $1`, &invoices, x.storeID)
	if invoices != sold {
		t.Errorf("%d invoices exist for %d completed sales", invoices, sold)
	}
}

// TestCrossedBasketsDoNotDeadlock bills two overlapping baskets from two
// counters with the products in opposite order.
//
// This is the ordinary shape of a Saturday morning: one customer buys rice and
// then soap, the next buys soap and then rice, and the two are at the two
// counters at the same moment. The FEFO walk takes FOR UPDATE on a product's
// batches, so a sale that locks rice and then wants soap, running against one
// that locks soap and then wants rice, is a lock-order inversion — and Postgres
// resolves it by killing one of them a second later with SQLSTATE 40P01.
//
// A deadlock here is not a rare-event curiosity. It is a cashier being told the
// sale failed, with the customer standing there, for no reason the shop can see.
func TestCrossedBasketsDoNotDeadlock(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "50.00", "40.00", "2000", 5, false)
	soap := x.stockProduct("Soap", "30.00", "20.00", "2000", 18, false)
	oil := x.stockProduct("Oil", "120.00", "90.00", "2000", 5, false)

	// Six tills, each with its own order over the same three products, so no
	// two agree on which to touch first.
	baskets := [][]uuid.UUID{
		{rice, soap, oil},
		{oil, soap, rice},
		{soap, rice, oil},
		{rice, oil, soap},
		{oil, rice, soap},
		{soap, oil, rice},
	}
	const rounds = 6

	var wg sync.WaitGroup
	errs := make(chan error, len(baskets)*rounds)
	for _, basket := range baskets {
		wg.Add(1)
		go func(basket []uuid.UUID) {
			defer wg.Done()
			for range rounds {
				lines := make([]domain.LineInput, len(basket))
				for i, p := range basket {
					lines[i] = line(p, "1")
				}
				if _, err := x.sale(service.CreateInput{Lines: lines}); err != nil {
					errs <- err
				}
			}
		}(basket)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("a crossed basket failed: %v", err)
	}

	// Every sale that was not refused must have moved exactly what it billed.
	for name, product := range map[string]uuid.UUID{"rice": rice, "soap": soap, "oil": oil} {
		want := money.QuantityFromInt(2000 - int64(len(baskets)*rounds))
		if got := x.onHand(product); !got.Equal(want) {
			t.Errorf("%s on hand = %s, want %s", name, got, want)
		}
	}
}

// TestVoidsBesideLiveBillingDoNotDeadlock cancels multi-line sales while other
// multi-line sales are being rung up on the same goods.
//
// A void puts every line back on the shelf, so it takes the same row locks a
// sale takes — and it happens at the counter, beside live billing, whenever a
// customer changes their mind. If a void restored its lines in the order the
// bill printed while a sale took them in some other order, the two would wait
// on each other and Postgres would kill one of them.
func TestVoidsBesideLiveBillingDoNotDeadlock(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	ctx := context.Background()
	rice := x.stockProduct("Rice", "50.00", "40.00", "5000", 5, false)
	soap := x.stockProduct("Soap", "30.00", "20.00", "5000", 18, false)
	oil := x.stockProduct("Oil", "120.00", "90.00", "5000", 5, false)

	// Raise the bills that will be cancelled, each stating its goods in a
	// different order from the sales that will run against them.
	const voids = 8
	doomed := make([]uuid.UUID, voids)
	for i := range doomed {
		got, err := x.sale(service.CreateInput{
			Lines: []domain.LineInput{line(oil, "1"), line(soap, "1"), line(rice, "1")},
		})
		if err != nil {
			t.Fatalf("seed sale %d: %v", i, err)
		}
		doomed[i] = got.Invoice.ID
	}

	var wg sync.WaitGroup
	errs := make(chan error, voids*2)
	for i := range voids {
		wg.Add(2)
		go func(id uuid.UUID) {
			defer wg.Done()
			if err := x.svcs.Billing.Void(ctx, x.storeID, id); err != nil {
				errs <- fmt.Errorf("void: %w", err)
			}
		}(doomed[i])
		go func() {
			defer wg.Done()
			if _, err := x.sale(service.CreateInput{
				Lines: []domain.LineInput{line(rice, "1"), line(soap, "1"), line(oil, "1")},
			}); err != nil {
				errs <- fmt.Errorf("sale: %w", err)
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("a void beside live billing failed: %v", err)
	}

	// Eight bills raised then cancelled, eight more raised and kept: the shelf
	// is down by exactly the eight that stand.
	for name, product := range map[string]uuid.UUID{"rice": rice, "soap": soap, "oil": oil} {
		want := money.QuantityFromInt(5000 - voids)
		if got := x.onHand(product); !got.Equal(want) {
			t.Errorf("%s on hand = %s, want %s", name, got, want)
		}
	}
}

// --- idempotency under retry -------------------------------------------------

// TestARetryBesideTheFirstRequestGetsTheOriginalBill replays an idempotency key
// while the request that owns it is still in flight.
//
// TestTheSameRequestTwiceRaisesOneSale covers the retry that arrives after the
// original committed, which is the easy half. This is the half a flaky
// connection actually produces: the till sends the sale, hears nothing, and
// sends it again while the first is still running. Both requests are then in the
// database at once and the pre-transaction replay check finds nothing for
// either.
//
// The safety property — one invoice, stock moved once — must hold. So must the
// promise the key exists to make: the second caller is told about the sale that
// happened, not handed a conflict it has no way to interpret. A till that gets
// an error after a successful sale either prints nothing or bills the customer
// twice.
func TestARetryBesideTheFirstRequestGetsTheOriginalBill(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)

	key := uuid.New().String()
	in := service.CreateInput{Lines: []domain.LineInput{line(rice, "2")}, IdempotencyKey: key}

	const attempts = 4
	results := make([]service.Result, attempts)
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = x.sale(in)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("attempt %d got an error instead of the original bill: %v", i, err)
		}
	}
	ids := map[uuid.UUID]bool{}
	for i, got := range results {
		if errs[i] != nil {
			continue
		}
		ids[got.Invoice.ID] = true
	}
	if len(ids) > 1 {
		t.Errorf("the same key produced %d different invoices", len(ids))
	}

	// Whatever the callers were told, the shop's own books must show one sale.
	var invoices int
	mustScan(t, x.db, `SELECT count(*) FROM invoices WHERE store_id = $1`, &invoices, x.storeID)
	if invoices != 1 {
		t.Errorf("%d invoices exist, want 1", invoices)
	}
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("48")) {
		t.Errorf("on hand = %s, want 48 — the stock moved more than once", got)
	}
	var revenue money.Money
	mustScan(t, x.db, `SELECT COALESCE(SUM(total), 0) FROM invoices WHERE store_id = $1 AND status = 'completed'`,
		&revenue, x.storeID)
	var one money.Money
	mustScan(t, x.db, `SELECT total FROM invoices WHERE store_id = $1 LIMIT 1`, &one, x.storeID)
	if !revenue.Equal(one) {
		t.Errorf("revenue %s counts the sale more than once (one bill is %s)", revenue, one)
	}
}

// TestAnIdempotencyKeyCannotReplayAnotherShopsBill guards the replay across a
// store boundary.
//
// processed_requests is keyed on the client's string alone — text, primary key,
// nothing about who sent it — so the keys have to be globally unique for the
// table to mean what it says. Nothing enforces that. A till sending a
// per-terminal counter, or two shops running the same build of the same client,
// both land on one row; the second shop is then handed the first shop's invoice
// with a 200 and raises no sale of its own, which loses the sale and hands over
// another business's figures at the same time.
//
// The replay is refused when it does not belong to the store asking. The other
// store's counter and shelf never come into it: the check runs before anything
// else, which is the point — there is no path from another shop's key to this
// shop's answer.
func TestAnIdempotencyKeyCannotReplayAnotherShopsBill(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)

	key := "till-1-0001" // the sort of key a client invents when left to itself
	first, err := x.sale(service.CreateInput{
		Lines: []domain.LineInput{line(rice, "2")}, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("first shop's sale: %v", err)
	}

	var otherStore uuid.UUID
	mustScan(t, x.db, `INSERT INTO stores (name) VALUES ('Second Shop') RETURNING id`, &otherStore)

	got, err := x.sale(service.CreateInput{
		StoreID:        otherStore,
		Lines:          []domain.LineInput{line(rice, "2")},
		IdempotencyKey: key,
	})
	if err == nil {
		t.Fatalf("the second shop was handed invoice %s", got.Invoice.InvoiceNo)
	}
	if !errors.Is(err, platform.ErrConflict) {
		t.Errorf("expected a conflict telling the caller to use its own key, got %v", err)
	}
	if got.Invoice.ID == first.Invoice.ID {
		t.Error("the second shop received the first shop's invoice")
	}
}

// --- partial failure mid-transaction ------------------------------------------

// TestASaleThatFailsAfterTheStockLeavesPutsItBack forces a failure at the one
// step that comes after the goods have already moved.
//
// The terminal's code is resolved after the stock has been deducted and after
// the invoice number has been drawn, so deactivating the counter mid-shift is
// the natural way to fail there. Nothing may survive: not the stock movement,
// not the drawn number, not an idempotency key that would make the retry
// replay a sale that never happened.
func TestASaleThatFailsAfterTheStockLeavesPutsItBack(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)
	key := uuid.New().String()

	if _, err := x.db.ExecContext(context.Background(),
		`UPDATE terminals SET is_active = false WHERE id = $1`, x.terminalID); err != nil {
		t.Fatalf("deactivate terminal: %v", err)
	}

	if _, err := x.sale(service.CreateInput{
		Lines:          []domain.LineInput{line(rice, "5")},
		IdempotencyKey: key,
	}); err == nil {
		t.Fatal("a sale from a deactivated counter should be refused")
	}

	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("50")) {
		t.Errorf("on hand = %s after a failed sale, want the original 50", got)
	}
	var adjustments, invoices, items, sequences, processed int
	mustScan(t, x.db, `SELECT count(*) FROM invoices WHERE store_id = $1`, &invoices, x.storeID)
	mustScan(t, x.db, `SELECT count(*) FROM invoice_items`, &items)
	mustScan(t, x.db, `SELECT count(*) FROM stock_adjustments WHERE store_id = $1`, &adjustments, x.storeID)
	// A rolled-back sale returns its number rather than leaving a gap in a
	// statutory series, so the counter must not exist at all yet.
	mustScan(t, x.db, `SELECT count(*) FROM document_sequences WHERE store_id = $1`, &sequences, x.storeID)
	mustScan(t, x.db, `SELECT count(*) FROM processed_requests WHERE key = $1`, &processed, key)
	for _, c := range []struct {
		what string
		got  int
	}{
		{"invoices", invoices}, {"invoice lines", items}, {"stock adjustments", adjustments},
		{"document sequences", sequences}, {"idempotency records", processed},
	} {
		if c.got != 0 {
			t.Errorf("%d %s survived a rolled-back sale, want none", c.got, c.what)
		}
	}

	// And the key is still usable, because nothing was done under it.
	if _, err := x.db.ExecContext(context.Background(),
		`UPDATE terminals SET is_active = true WHERE id = $1`, x.terminalID); err != nil {
		t.Fatalf("reactivate terminal: %v", err)
	}
	got, err := x.sale(service.CreateInput{
		Lines:          []domain.LineInput{line(rice, "5")},
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("retrying the sale after the counter came back: %v", err)
	}
	if got.Replayed {
		t.Error("the retry replayed a sale that never happened")
	}
}

// TestAVoidThatCannotCompleteRestoresNothing pairs with the above on the way
// back out: a void either returns every line to stock and cancels the
// receivable, or it does none of it.
func TestAVoidThatCannotCompleteRestoresNothing(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	ctx := context.Background()
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)
	soap := x.stockProduct("Soap", "60.00", "45.00", "50", 18, false)
	customer := x.customer("Corner Cafe", "50000.00", false)

	sold, err := x.sale(service.CreateInput{
		Lines:       []domain.LineInput{line(rice, "5"), line(soap, "5")},
		CustomerID:  &customer,
		PaymentMode: domain.PaymentCredit,
	})
	if err != nil {
		t.Fatalf("credit sale: %v", err)
	}

	// Drop the soap batch's row out from under the void by putting the product
	// beyond the reach of the restoring upsert: an ON DELETE RESTRICT foreign
	// key on the products table means deleting the product is refused, so
	// instead make the stock insert fail on its own CHECK by leaving the batch
	// at a quantity the restore cannot legally produce. The simplest honest
	// lever is a constraint the restore must violate.
	if _, err := x.db.ExecContext(ctx,
		`ALTER TABLE stock ADD CONSTRAINT stock_void_probe CHECK (quantity < 50)`); err != nil {
		t.Fatalf("add probe constraint: %v", err)
	}
	t.Cleanup(func() {
		_, _ = x.db.ExecContext(context.Background(), `ALTER TABLE stock DROP CONSTRAINT IF EXISTS stock_void_probe`)
	})

	if err := x.svcs.Billing.Void(ctx, x.storeID, sold.Invoice.ID); err == nil {
		t.Fatal("the void should have failed on the probe constraint")
	}

	// Nothing partial: the invoice is still completed, both batches are still
	// down by five, and the customer still owes the money.
	var status string
	mustScan(t, x.db, `SELECT status FROM invoices WHERE id = $1`, &status, sold.Invoice.ID)
	if status != string(domain.StatusCompleted) {
		t.Errorf("invoice status = %q after a failed void, want completed", status)
	}
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("45")) {
		t.Errorf("rice on hand = %s after a failed void, want 45", got)
	}
	if got := x.onHand(soap); !got.Equal(money.MustQuantityFromString("45")) {
		t.Errorf("soap on hand = %s after a failed void, want 45", got)
	}
	outstanding, err := x.svcs.Credit.Outstanding(ctx, x.storeID, customer)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	if !outstanding.Equal(sold.Invoice.Total) {
		t.Errorf("outstanding = %s after a failed void, want the unchanged %s", outstanding, sold.Invoice.Total)
	}
}

// --- boundary values that occur daily ----------------------------------------

// TestAZeroQuantityLineIsRefused covers the scanner that fired twice and the
// cashier who cleared the quantity box.
func TestAZeroQuantityLineIsRefused(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, true)

	if _, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(rice, "0")}}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("a zero-quantity line should be a validation error, got %v", err)
	}
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("50")) {
		t.Errorf("on hand = %s, want the untouched 50", got)
	}
}

// TestAQuantityFinerThanTheShopCanStoreIsRefused covers a quantity with more
// decimal places than a quantity column holds.
//
// Quantities are numeric(14,3) — grams within a kilogram, which is as fine as
// any stocked unit of measure is sold in. A request for 0.0006 kg is not a
// smaller sale, it is a number the shop cannot record: the bill would be
// computed from 0.0006 and stored as 0.001, so the line's own total would not
// equal its rate times its printed quantity, and the two figures would disagree
// for ever afterwards.
//
// stock.ValidateMovement already refuses this on the movement side. The sale
// has to refuse it on the way in, by name, rather than let it through to a
// confusing failure deeper down or to a rounded line nobody asked for.
func TestAQuantityFinerThanTheShopCanStoreIsRefused(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, true)

	if _, err := x.sale(service.CreateInput{
		Lines: []domain.LineInput{line(rice, "0.0006")},
	}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("a quantity of 0.0006 should be a validation error, got %v", err)
	}

	// The same rule applies to what the customer asked for, not only to what
	// leaves the shelf: a pouch of 0.25 kg asked for 1.0006 times is 0.25 kg
	// once rounded, and the shelf would never notice.
	pouch := x.product("Tea", 5, true)
	pouchUnit := x.sellingUnit(pouch, "250 g pouch", "0.25", "40.00", true)
	x.stockOnHand(pouch, "50", "30.00")
	if _, err := x.sale(service.CreateInput{
		Lines: []domain.LineInput{unitLine(pouch, pouchUnit, "1.0006")},
	}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("1.0006 pouches should be a validation error, got %v", err)
	}
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("50")) {
		t.Errorf("rice on hand = %s, want the untouched 50", got)
	}
}

// TestAFullyDiscountedLineIsBilledAtNothingAndStillMovesTheGoods is the free
// replacement handed over the counter: the goods leave, the paperwork says
// zero, and the GST on zero is zero.
func TestAFullyDiscountedLineIsBilledAtNothingAndStillMovesTheGoods(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)

	got, err := x.sale(service.CreateInput{
		Lines: []domain.LineInput{{
			ProductID: rice,
			UnitQty:   money.MustQuantityFromString("2"),
			Discount:  money.MustFromString("200.00"),
		}},
	})
	if err != nil {
		t.Fatalf("a hundred per cent discount should be allowed: %v", err)
	}
	if !got.Invoice.Total.IsZero() {
		t.Errorf("total = %s on a fully discounted bill, want 0", got.Invoice.Total)
	}
	if !got.Invoice.SubTotal.IsZero() || !got.Invoice.CGST.IsZero() || !got.Invoice.SGST.IsZero() {
		t.Errorf("a zero taxable value carries tax: sub %s cgst %s sgst %s",
			got.Invoice.SubTotal, got.Invoice.CGST, got.Invoice.SGST)
	}
	if !got.Invoice.Discount.Equal(money.MustFromString("200.00")) {
		t.Errorf("discount on the invoice = %s, want the 200.00 the customer was given", got.Invoice.Discount)
	}
	// The goods left the shelf even though no money changed hands: that is the
	// whole point of recording it as a sale rather than as a loss.
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("48")) {
		t.Errorf("on hand = %s, want 48", got)
	}
}

// TestABillOfExactlyZeroIsStillARecordedSale is the same event reached the
// other way — every line free — and it must still produce a numbered invoice,
// because a shop that hands goods over without paperwork cannot account for
// them.
func TestABillOfExactlyZeroIsStillARecordedSale(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	sample := x.product("Sample Sachet", 0, false)
	x.sellingUnit(sample, "Sachet", "1", "0.00", true)
	x.stockOnHand(sample, "20", "2.00")

	got, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(sample, "3")}})
	if err != nil {
		t.Fatalf("a free sample should still be billable: %v", err)
	}
	if !got.Invoice.Total.IsZero() {
		t.Errorf("total = %s, want 0", got.Invoice.Total)
	}
	if got.Invoice.InvoiceNo == "" {
		t.Error("a zero bill was not given an invoice number")
	}
	if got := x.onHand(sample); !got.Equal(money.MustQuantityFromString("17")) {
		t.Errorf("on hand = %s, want 17", got)
	}
	// The COGS snapshot is what makes the giveaway visible in the profit
	// figures rather than invisible.
	if len(got.Items) != 1 || !got.Items[0].UnitCostAtSale.Equal(money.MustFromString("2.00")) {
		t.Errorf("cost at sale = %v, want 2.00 so the giveaway shows up as a cost", got.Items)
	}
}

// TestACustomerExactlyOnTheirCreditLimitMayStillBuy proves a limit is a ceiling
// rather than a barrier one paisa below itself. The bill is built to land on
// the limit exactly.
func TestACustomerExactlyOnTheirCreditLimitMayStillBuy(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	ctx := context.Background()
	// Nil-rated goods, so the bill is the line value with no tax to complicate
	// the arithmetic: 10 x 100 is exactly 1,000.
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 0, false)
	customer := x.customer("Exactly", "1000.00", false)

	got, err := x.sale(service.CreateInput{
		Lines:       []domain.LineInput{line(rice, "10")},
		CustomerID:  &customer,
		PaymentMode: domain.PaymentCredit,
	})
	if err != nil {
		t.Fatalf("a sale landing exactly on the limit should be allowed: %v", err)
	}
	if !got.Invoice.Total.Equal(money.MustFromString("1000.00")) {
		t.Fatalf("the fixture did not land on the limit: total %s", got.Invoice.Total)
	}

	// One paisa more, and it is refused.
	if _, err := x.sale(service.CreateInput{
		Lines:       []domain.LineInput{line(rice, "1")},
		CustomerID:  &customer,
		PaymentMode: domain.PaymentCredit,
	}); err == nil {
		t.Error("a sale past the limit should be refused")
	}
	outstanding, err := x.svcs.Credit.Outstanding(ctx, x.storeID, customer)
	if err != nil {
		t.Fatalf("outstanding: %v", err)
	}
	if !outstanding.Equal(money.MustFromString("1000.00")) {
		t.Errorf("outstanding = %s, want 1000.00 — the refused sale left a receivable", outstanding)
	}
}

// TestSellingExactlyWhatIsOnTheShelfEmptiesIt covers the last packet sold on
// purpose rather than by a race: asking for exactly the quantity on hand must
// succeed, and asking for a paisa-weight more must not.
func TestSellingExactlyWhatIsOnTheShelfEmptiesIt(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "7.5", 5, true)

	if _, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(rice, "7.5")}}); err != nil {
		t.Fatalf("selling exactly what is on the shelf: %v", err)
	}
	if got := x.onHand(rice); !got.Equal(money.ZeroQuantity()) {
		t.Errorf("on hand = %s, want 0", got)
	}
	if _, err := x.sale(service.CreateInput{
		Lines: []domain.LineInput{line(rice, "0.001")},
	}); !errors.Is(err, stockdomain.ErrInsufficientStock) {
		t.Errorf("selling from an empty shelf should be refused, got %v", err)
	}
}

// TestStockExpiringTodayIsStillSoldAndSoldFirst states what a batch dated today
// is for. It is sellable until midnight — the expiry reports already say so,
// putting today's batch on the "expiring" list and not on the "expired" one —
// and first-expired-first-out must reach for it before the batch that is good
// for another month.
func TestStockExpiringTodayIsStillSoldAndSoldFirst(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	ctx := context.Background()
	milk := x.product("Milk", 5, false)
	x.sellingUnit(milk, "Packet", "1", "30.00", true)

	today := time.Now().Truncate(24 * time.Hour)
	x.batch(milk, "FRESH", "10", "22.00", today.AddDate(0, 1, 0))
	x.batch(milk, "TODAY", "4", "22.00", today)

	got, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(milk, "4")}})
	if err != nil {
		t.Fatalf("selling a batch that expires today: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].BatchNo != "TODAY" {
		t.Errorf("the sale took batch %q, want TODAY — the batch closest to expiry", got.Items[0].BatchNo)
	}
	if q := x.batchQty(milk, "TODAY"); !q.IsZero() {
		t.Errorf("today's batch still holds %s", q)
	}
	if q := x.batchQty(milk, "FRESH"); !q.Equal(money.MustQuantityFromString("10")) {
		t.Errorf("the fresh batch was touched: %s left, want 10", q)
	}

	// And the two expiry reports agree with the sale: today's batch is on the
	// worklist for moving, never on the write-off one.
	expiring, err := x.svcs.Stock.Expiring(ctx, x.storeID, 30)
	if err != nil {
		t.Fatalf("expiring report: %v", err)
	}
	for _, b := range expiring {
		if b.BatchNo == "TODAY" {
			t.Error("today's batch is on the expiring report but holds no stock")
		}
	}
	expired, err := x.svcs.Stock.Expired(ctx, x.storeID)
	if err != nil {
		t.Fatalf("expired report: %v", err)
	}
	if len(expired) != 0 {
		t.Errorf("%d batches are reported expired, want none — today has not passed", len(expired))
	}
}

// --- clock and financial-year edges -------------------------------------------

// TestTheSeriesRollsAtMidnightOnThirtyFirstMarch bills either side of the
// financial-year boundary from one counter.
//
// The year runs 1 April to 31 March, so a sale at 23:59 on 31 March belongs to
// the old year and one at 00:01 on 1 April starts a new series at 1 — both
// numbered INV/T1/<year>/..., both consecutive within their own year, and
// neither colliding with the other.
//
// The boundary is an Indian one. It falls at midnight in India whatever
// timezone the server happens to run in, so this test states the times in IST
// and expects the year that an Indian shopkeeper would write on the bill.
func TestTheSeriesRollsAtMidnightOnThirtyFirstMarch(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "500", 5, false)

	ist := time.FixedZone("IST", int((5*time.Hour + 30*time.Minute).Seconds()))
	lastNight := time.Date(2026, time.March, 31, 23, 59, 0, 0, ist)
	newYear := time.Date(2026, time.April, 1, 0, 1, 0, 0, ist)

	// Two sales before midnight, two after.
	for i, at := range []time.Time{lastNight, lastNight.Add(30 * time.Second), newYear, newYear.Add(time.Minute)} {
		got, err := x.saleAt(at, service.CreateInput{Lines: []domain.LineInput{line(rice, "1")}})
		if err != nil {
			t.Fatalf("sale %d at %s: %v", i+1, at, err)
		}
		want := []string{
			"INV/T1/2025-26/00001",
			"INV/T1/2025-26/00002",
			"INV/T1/2026-27/00001",
			"INV/T1/2026-27/00002",
		}[i]
		if got.Invoice.InvoiceNo != want {
			t.Errorf("sale %d at %s numbered %q, want %q", i+1, at.Format(time.RFC3339), got.Invoice.InvoiceNo, want)
		}
	}

	// Two counters, two years, four numbers, no collision — and the shop's
	// numbering table holds one counter per terminal per year, not a shared one
	// that could restart mid-year.
	var series int
	mustScan(t, x.db,
		`SELECT count(*) FROM document_sequences WHERE store_id = $1 AND doc_type = 'invoice'`,
		&series, x.storeID)
	if series != 2 {
		t.Errorf("%d invoice series exist for one counter across a year boundary, want 2", series)
	}
	var numbers int
	mustScan(t, x.db, `SELECT count(DISTINCT invoice_no) FROM invoices WHERE store_id = $1`, &numbers, x.storeID)
	if numbers != 4 {
		t.Errorf("%d distinct invoice numbers for 4 sales", numbers)
	}
}

// TestAYearAlreadyUnderWayDoesNotRestart guards the other half of the rule: a
// series is only meaningful if it is unbroken, so a sale in the middle of a
// year must continue the year's numbering rather than open a fresh one.
func TestAYearAlreadyUnderWayDoesNotRestart(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "500", 5, false)

	ist := time.FixedZone("IST", int((5*time.Hour + 30*time.Minute).Seconds()))
	days := []time.Time{
		time.Date(2026, time.April, 1, 9, 0, 0, 0, ist),
		time.Date(2026, time.December, 31, 20, 0, 0, 0, ist),
		time.Date(2027, time.January, 1, 9, 0, 0, 0, ist), // a calendar year rolls, the financial one does not
		time.Date(2027, time.March, 31, 22, 0, 0, 0, ist),
	}
	for i, at := range days {
		got, err := x.saleAt(at, service.CreateInput{Lines: []domain.LineInput{line(rice, "1")}})
		if err != nil {
			t.Fatalf("sale on %s: %v", at.Format("2 Jan 2006"), err)
		}
		want := fmt.Sprintf("INV/T1/2026-27/%05d", i+1)
		if got.Invoice.InvoiceNo != want {
			t.Errorf("sale on %s numbered %q, want %q", at.Format("2 Jan 2006"), got.Invoice.InvoiceNo, want)
		}
	}
}

// --- money precision under accumulation ---------------------------------------

// TestManySmallLinesReconcileToThePaisa bills a basket of the sort that
// actually crosses a counter — dozens of small odd-priced lines at mixed GST
// rates — and insists the invoice's own figures, the stored figures and the
// lines all agree exactly.
//
// Accumulation is where a per-line rounding policy either holds or drifts. The
// figure that matters is not any one line but the sum: sub_total + cgst + sgst +
// igst + round_off must equal total, and the line totals must add up to the same
// place.
func TestManySmallLinesReconcileToThePaisa(t *testing.T) {
	t.Parallel()
	x := newTill(t)

	// Prices chosen so that half the tax is an odd number of paise: Rs.0.35 at
	// 5% is 1.75 paise of tax, and Rs.55 at 5% is Rs.2.75, whose halves are
	// 1.375 — the figure that used to print as 1.38 + 1.38 = 2.76 against a
	// stored 2.75.
	sweets := x.stockProduct("Sweets", "0.35", "0.20", "10000", 5, true)
	strips := x.stockProduct("Strip", "55.00", "40.00", "10000", 5, false)
	soap := x.stockProduct("Soap", "17.77", "12.00", "10000", 18, false)

	lines := make([]domain.LineInput, 0, 60)
	for i := range 20 {
		lines = append(lines,
			line(sweets, fmt.Sprintf("%d", i+1)),
			line(strips, "1"),
			line(soap, "3"))
	}

	got, err := x.sale(service.CreateInput{Lines: lines, RoundTotal: true})
	if err != nil {
		t.Fatalf("the basket: %v", err)
	}

	inv := got.Invoice
	if want := inv.SubTotal.Add(inv.CGST).Add(inv.SGST).Add(inv.IGST).Add(inv.RoundOff); !inv.Total.Equal(want) {
		t.Errorf("total %s but sub_total + tax + round_off = %s", inv.Total, want)
	}
	// The round-off is what it claims to be: the distance from the unrounded
	// bill to a whole rupee, and no more than fifty paise in either direction.
	if inv.RoundOff.Abs().GreaterThan(money.MustFromString("0.50")) {
		t.Errorf("round_off = %s, which is more than half a rupee", inv.RoundOff)
	}
	if !inv.Total.Equal(inv.Total.Round()) || inv.Total.Paise()%100 != 0 {
		t.Errorf("a rounded total of %s is not a whole number of rupees", inv.Total)
	}

	// The lines add up to the header, before the round-off is applied.
	lineSub, lineCGST, lineSGST, lineTotal := money.Zero(), money.Zero(), money.Zero(), money.Zero()
	for _, it := range got.Items {
		lineCGST = lineCGST.Add(it.CGST)
		lineSGST = lineSGST.Add(it.SGST)
		lineTotal = lineTotal.Add(it.Total)
		lineSub = lineSub.Add(it.Total.Sub(it.CGST).Sub(it.SGST).Sub(it.IGST))
	}
	if !lineSub.Equal(inv.SubTotal) {
		t.Errorf("lines are taxable on %s, the invoice says %s", lineSub, inv.SubTotal)
	}
	if !lineCGST.Equal(inv.CGST) || !lineSGST.Equal(inv.SGST) {
		t.Errorf("line tax %s/%s, invoice tax %s/%s", lineCGST, lineSGST, inv.CGST, inv.SGST)
	}
	if want := lineTotal.Add(inv.RoundOff); !inv.Total.Equal(want) {
		t.Errorf("line totals plus round-off = %s, invoice total %s", want, inv.Total)
	}

	// And the same figures come back out of the database, which is where a
	// numeric(12,2) column would quietly truncate a third decimal place.
	var storedSub, storedCGST, storedSGST, storedRound, storedTotal money.Money
	mustScan(t, x.db, `SELECT sub_total FROM invoices WHERE id = $1`, &storedSub, inv.ID)
	mustScan(t, x.db, `SELECT cgst FROM invoices WHERE id = $1`, &storedCGST, inv.ID)
	mustScan(t, x.db, `SELECT sgst FROM invoices WHERE id = $1`, &storedSGST, inv.ID)
	mustScan(t, x.db, `SELECT round_off FROM invoices WHERE id = $1`, &storedRound, inv.ID)
	mustScan(t, x.db, `SELECT total FROM invoices WHERE id = $1`, &storedTotal, inv.ID)
	if !storedSub.Equal(inv.SubTotal) || !storedCGST.Equal(inv.CGST) || !storedSGST.Equal(inv.SGST) ||
		!storedRound.Equal(inv.RoundOff) || !storedTotal.Equal(inv.Total) {
		t.Errorf("stored %s/%s/%s/%s/%s differs from computed %s/%s/%s/%s/%s",
			storedSub, storedCGST, storedSGST, storedRound, storedTotal,
			inv.SubTotal, inv.CGST, inv.SGST, inv.RoundOff, inv.Total)
	}
	if !storedTotal.Equal(storedSub.Add(storedCGST).Add(storedSGST).Add(storedRound)) {
		t.Error("the stored invoice does not add up to itself")
	}
}

// TestAnOddPaisaOfTaxIsSplitWithoutInventingOrLosingIt is the half-paise case
// stated at invoice level rather than at the level of money.SplitTax.
//
// A Rs.55 line at 5% carries Rs.2.75 of tax and half of that is 1.375, which is
// not payable. CGST takes the stray paisa, always and deterministically, and
// SGST takes the rest — so the two halves sum to the whole by construction and
// recomputing the invoice a year later reproduces it exactly.
func TestAnOddPaisaOfTaxIsSplitWithoutInventingOrLosingIt(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	strip := x.stockProduct("Strip", "55.00", "40.00", "500", 5, false)

	got, err := x.sale(service.CreateInput{Lines: []domain.LineInput{line(strip, "1")}})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	if !got.Invoice.CGST.Equal(money.MustFromString("1.38")) {
		t.Errorf("cgst = %s, want 1.38 — the stray paisa goes to the centre", got.Invoice.CGST)
	}
	if !got.Invoice.SGST.Equal(money.MustFromString("1.37")) {
		t.Errorf("sgst = %s, want 1.37", got.Invoice.SGST)
	}
	if want := money.MustFromString("2.75"); !got.Invoice.CGST.Add(got.Invoice.SGST).Equal(want) {
		t.Errorf("cgst + sgst = %s, want the whole tax %s",
			got.Invoice.CGST.Add(got.Invoice.SGST), want)
	}

	// Twenty such lines on one bill: the odd paisa lands the same way twenty
	// times rather than drifting, so the invoice's halves still sum to the
	// whole and the difference between them is exactly twenty paise.
	lines := make([]domain.LineInput, 20)
	for i := range lines {
		lines[i] = line(strip, "1")
	}
	many, err := x.sale(service.CreateInput{Lines: lines})
	if err != nil {
		t.Fatalf("twenty lines: %v", err)
	}
	if want := money.MustFromString("55.00"); !many.Invoice.CGST.Add(many.Invoice.SGST).Equal(want) {
		t.Errorf("cgst + sgst over twenty lines = %s, want %s",
			many.Invoice.CGST.Add(many.Invoice.SGST), want)
	}
	if want := money.MustFromString("0.20"); !many.Invoice.CGST.Sub(many.Invoice.SGST).Equal(want) {
		t.Errorf("cgst - sgst = %s, want %s — one paisa per line, consistently",
			many.Invoice.CGST.Sub(many.Invoice.SGST), want)
	}
}

// --- fixtures ----------------------------------------------------------------

// batch puts a dated lot on the shelf, for the expiry cases.
func (x *till) batch(productID uuid.UUID, batchNo, qty, cost string, expiry time.Time) {
	x.t.Helper()
	if _, err := x.db.ExecContext(context.Background(),
		`INSERT INTO stock (product_id, store_id, batch_no, quantity, cost_price, expiry_date)
		 VALUES ($1, $2, $3, $4, $5, $6)`, productID, x.storeID, batchNo,
		money.MustQuantityFromString(qty), money.MustFromString(cost), expiry); err != nil {
		x.t.Fatalf("seed batch %s: %v", batchNo, err)
	}
}

func (x *till) batchQty(productID uuid.UUID, batchNo string) money.Quantity {
	x.t.Helper()
	var q money.Quantity
	mustScan(x.t, x.db, `SELECT quantity FROM stock WHERE product_id = $1 AND store_id = $2 AND batch_no = $3`,
		&q, productID, x.storeID, batchNo)
	return q
}

// saleAt bills as though the counter's clock read `at`, which is how the
// financial-year cases reach 31 March without waiting for it.
func (x *till) saleAt(at time.Time, in service.CreateInput) (service.Result, error) {
	if in.StoreID == uuid.Nil {
		in.StoreID = x.storeID
	}
	if in.UserID == uuid.Nil {
		in.UserID = x.userID
	}
	if in.TerminalID == uuid.Nil {
		in.TerminalID = x.terminalID
	}
	if in.PaymentMode == "" {
		in.PaymentMode = domain.PaymentCash
	}
	return x.svc.WithClock(func() time.Time { return at }).Create(context.Background(), in)
}
