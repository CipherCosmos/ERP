package payment_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gbos/internal/app"
	billingdomain "gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	domain "gbos/internal/domain/payment"
	platform "gbos/internal/platform/postgres"
	billingsvc "gbos/internal/service/billing"
	service "gbos/internal/service/payment"
	"gbos/internal/testsupport"

	"github.com/google/uuid"
)

// counter is a shop set up to take money: stores with counters and cashiers,
// products on the shelf, and the wired services that sell and settle.
type counter struct {
	t    *testing.T
	db   *platform.DB
	svcs app.Services
	svc  *service.Service

	uomID      uuid.UUID
	categoryID uuid.UUID

	// shop is the shop under test. A second one is seeded by newShop when a
	// test needs to prove that store scoping holds.
	shop *shop
}

type shop struct {
	storeID    uuid.UUID
	terminalID uuid.UUID
	userID     uuid.UUID
}

func newCounter(t *testing.T) *counter {
	t.Helper()
	db := testsupport.OpenDB(t)

	svcs, err := app.Build(db, app.Config{JWTSecret: "payment-tests-signing-secret-value-x"})
	if err != nil {
		t.Fatalf("build services: %v", err)
	}

	x := &counter{t: t, db: db, svcs: svcs, svc: svcs.Payment}
	// A unit of measure now belongs to a family, because a unit nothing can
	// convert is useless for the one job the table has. "Each" is a count, so
	// it goes in the COUNT family at a factor of one.
	mustScan(t, db, `INSERT INTO units_of_measure (code, name, decimal_places, family_id, factor_to_base)
		SELECT 'EA', 'Each', 0, id, 1 FROM unit_families WHERE code = 'COUNT' RETURNING id`, &x.uomID)
	mustScan(t, db, `INSERT INTO categories (name) VALUES ('Groceries') RETURNING id`, &x.categoryID)
	x.shop = x.newShop("Corner Shop", "T1", "corner-cashier")
	return x
}

func (x *counter) newShop(name, terminalCode, username string) *shop {
	x.t.Helper()
	s := &shop{}
	mustScan(x.t, x.db, `INSERT INTO stores (name, gstin) VALUES ($1, '29ABCDE1234F1Z5') RETURNING id`, &s.storeID, name)
	mustScan(x.t, x.db, `INSERT INTO terminals (store_id, code, name) VALUES ($1, $2, 'Counter') RETURNING id`,
		&s.terminalID, s.storeID, terminalCode)
	mustScan(x.t, x.db, `INSERT INTO users (username, password_hash, role, store_id)
		VALUES ($1, 'x', 'cashier', $2) RETURNING id`, &s.userID, username, s.storeID)
	return s
}

func mustScan(t *testing.T, db *platform.DB, query string, dest any, args ...any) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(dest); err != nil {
		t.Fatalf("seed (%s): %v", query, err)
	}
}

func (x *counter) mustExec(query string, args ...any) {
	x.t.Helper()
	if _, err := x.db.ExecContext(context.Background(), query, args...); err != nil {
		x.t.Fatalf("seed (%s): %v", query, err)
	}
}

// stockProduct puts a product on one shop's shelf at a price.
func (x *counter) stockProduct(s *shop, name, sell, onHand string, gstPercent int64) uuid.UUID {
	x.t.Helper()
	var productID uuid.UUID
	// The catalogue is shared across stores — products carry no store_id, only
	// their prices and stock do — and a name is unique inside its category. So
	// two shops in one test cannot both stock a product called "Rice": that
	// would be one product, not two, which is exactly what the index says.
	// The store id disambiguates the row without changing what the case is
	// about.
	mustScan(x.t, x.db, `INSERT INTO products (name, category_id, uom_id, gst_rate, allow_decimal)
		VALUES ($1, $2, $3, $4, false) RETURNING id`,
		&productID, fmt.Sprintf("%s %s", name, s.storeID.String()[:8]),
		x.categoryID, x.uomID, gstPercent)

	// One selling unit, worth one of the product's stocking unit, priced for
	// this shop. Prices are per unit now — a sack is not 25 times the loose
	// kilo — and nothing in these settlement cases turns on bulk, so every
	// product here is sold the one way.
	var unitID uuid.UUID
	mustScan(x.t, x.db, `INSERT INTO product_units (product_id, uom_id, label, qty_in_base, is_default)
		VALUES ($1, $2, 'Each', 1, true) RETURNING id`, &unitID, productID, x.uomID)

	x.mustExec(`INSERT INTO product_prices (product_id, store_id, product_unit_id, mrp, cost_price, sell_price)
		VALUES ($1, $2, $3, $4, $4, $4)`, productID, s.storeID, unitID, money.MustFromString(sell))
	// total_cost, not a rate: stock holds what a batch is worth and the
	// per-unit figure is generated from it.
	x.mustExec(`INSERT INTO stock (product_id, store_id, batch_no, quantity, total_cost)
		VALUES ($1, $2, 'OPENING', $3, $4)`, productID, s.storeID,
		money.MustQuantityFromString(onHand),
		money.MustFromString(sell).Mul(money.MustQuantityFromString(onHand)))
	return productID
}

// sale raises a real invoice through the billing service, so the totals the
// payment service settles against are the ones the shop actually computed.
func (x *counter) sale(s *shop, mode billingdomain.PaymentMode, customer *uuid.UUID, lines ...billingdomain.LineInput) billingsvc.Result {
	x.t.Helper()
	got, err := x.svcs.Billing.Create(context.Background(), billingsvc.CreateInput{
		StoreID:     s.storeID,
		UserID:      s.userID,
		TerminalID:  s.terminalID,
		CustomerID:  customer,
		Lines:       lines,
		PaymentMode: mode,
	})
	if err != nil {
		x.t.Fatalf("sale: %v", err)
	}
	return got
}

func line(productID uuid.UUID, qty string) billingdomain.LineInput {
	return billingdomain.LineInput{ProductID: productID, UnitQty: money.MustQuantityFromString(qty)}
}

func tender(mode billingdomain.PaymentMode, amount string) domain.TenderInput {
	return domain.TenderInput{Mode: mode, Amount: money.MustFromString(amount)}
}

// invoiceDay reads the trading day the database recorded an invoice on, rather
// than the one Go would compute, so a test run near midnight cannot disagree
// with the row it is about to report on.
func (x *counter) invoiceDay(invoiceID uuid.UUID) time.Time {
	x.t.Helper()
	var day time.Time
	mustScan(x.t, x.db, `SELECT invoice_date FROM invoices WHERE id = $1`, &day, invoiceID)
	return day
}

// recorded returns how many payment rows an invoice has and what they sum to,
// read straight from the table rather than through the service.
func (x *counter) recorded(invoiceID uuid.UUID) (int, money.Money) {
	x.t.Helper()
	var count int
	var total money.Money
	if err := x.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*), COALESCE(SUM(amount), 0) FROM invoice_payments WHERE invoice_id = $1`,
		invoiceID).Scan(&count, &total); err != nil {
		x.t.Fatalf("read back payments: %v", err)
	}
	return count, total
}

// --- a split must sum exactly ------------------------------------------------

func TestASplitThatSumsExactlyIsAccepted(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	rice := x.stockProduct(x.shop, "Rice", "100.00", "50", 5)

	sold := x.sale(x.shop, billingdomain.PaymentSplit, nil, line(rice, "1"))
	if !sold.Invoice.Total.Equal(money.MustFromString("105.00")) {
		t.Fatalf("invoice total = %s, want 105.00", sold.Invoice.Total)
	}

	got, err := x.svc.Settle(context.Background(), service.SettleInput{
		StoreID:   x.shop.storeID,
		InvoiceID: sold.Invoice.ID,
		Tenders: []domain.TenderInput{
			tender(billingdomain.PaymentCash, "50.00"),
			tender(billingdomain.PaymentCard, "30.00"),
			tender(billingdomain.PaymentUPI, "15.00"),
			tender(billingdomain.PaymentBank, "10.00"),
		},
	})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if len(got.Payments) != 4 {
		t.Errorf("wrote %d payments, want 4", len(got.Payments))
	}

	// The handler this replaces discarded the result of tx.Commit() and then
	// answered 201 regardless, so the rows might never have landed. Read them
	// back from the table, not from the response.
	count, total := x.recorded(sold.Invoice.ID)
	if count != 4 {
		t.Errorf("%d payment rows committed, want 4", count)
	}
	if !total.Equal(sold.Invoice.Total) {
		t.Errorf("committed payments sum to %s, want the invoice total %s", total, sold.Invoice.Total)
	}
}

func TestASplitOnePaisaOutIsRefusedEitherWay(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	rice := x.stockProduct(x.shop, "Rice", "100.00", "50", 5)
	ctx := context.Background()

	cases := []struct {
		name    string
		tenders []domain.TenderInput
		// direction is the word the message must carry, because "short" and
		// "over" call for different actions at the counter.
		direction string
	}{
		{
			name:      "a paisa short",
			tenders:   []domain.TenderInput{tender(billingdomain.PaymentCash, "52.00"), tender(billingdomain.PaymentCard, "52.99")},
			direction: "short",
		},
		{
			name:      "a paisa over",
			tenders:   []domain.TenderInput{tender(billingdomain.PaymentCash, "52.00"), tender(billingdomain.PaymentCard, "53.01")},
			direction: "over",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sold := x.sale(x.shop, billingdomain.PaymentSplit, nil, line(rice, "1"))

			// The handler this replaces accepted anything within five paise, so
			// both of these committed and the drawer was out by a paisa.
			_, err := x.svc.Settle(ctx, service.SettleInput{
				StoreID:   x.shop.storeID,
				InvoiceID: sold.Invoice.ID,
				Tenders:   tc.tenders,
			})
			if !errors.Is(err, domain.ErrAmountMismatch) {
				t.Fatalf("expected ErrAmountMismatch, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.direction) {
				t.Errorf("message %q does not say which way it is out (%q)", err.Error(), tc.direction)
			}
			if count, _ := x.recorded(sold.Invoice.ID); count != 0 {
				t.Errorf("a refused settlement left %d payment rows behind", count)
			}
		})
	}
}

func TestATenderMustBeAWayMoneyMoves(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	rice := x.stockProduct(x.shop, "Rice", "100.00", "50", 5)
	ctx := context.Background()

	for _, mode := range []billingdomain.PaymentMode{billingdomain.PaymentSplit, billingdomain.PaymentCredit, "bitcoin"} {
		sold := x.sale(x.shop, billingdomain.PaymentSplit, nil, line(rice, "1"))
		_, err := x.svc.Settle(ctx, service.SettleInput{
			StoreID:   x.shop.storeID,
			InvoiceID: sold.Invoice.ID,
			Tenders:   []domain.TenderInput{{Mode: mode, Amount: sold.Invoice.Total}},
		})
		if !errors.Is(err, domain.ErrValidation) {
			t.Errorf("settling with %q: expected ErrValidation, got %v", mode, err)
		}
	}
}

// --- settling happens at most once -------------------------------------------

func TestSettlingTwiceDoesNotDoubleCount(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	rice := x.stockProduct(x.shop, "Rice", "100.00", "50", 5)
	ctx := context.Background()

	sold := x.sale(x.shop, billingdomain.PaymentCash, nil, line(rice, "1"))
	in := service.SettleInput{
		StoreID:   x.shop.storeID,
		InvoiceID: sold.Invoice.ID,
		Tenders:   []domain.TenderInput{tender(billingdomain.PaymentCash, "105.00")},
	}

	if _, err := x.svc.Settle(ctx, in); err != nil {
		t.Fatalf("first settlement: %v", err)
	}
	// The guard this replaces was a SELECT COUNT(*) whose scan error was
	// discarded, so a failed count read as zero and let this through.
	if _, err := x.svc.Settle(ctx, in); !errors.Is(err, domain.ErrAlreadySettled) {
		t.Fatalf("second settlement: expected ErrAlreadySettled, got %v", err)
	}

	count, total := x.recorded(sold.Invoice.ID)
	if count != 1 {
		t.Errorf("%d payment rows exist, want 1", count)
	}
	if !total.Equal(money.MustFromString("105.00")) {
		t.Errorf("payments sum to %s, want 105.00 — the settlement was counted twice", total)
	}
}

func TestConcurrentSettlementsRecordTheMoneyOnce(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	rice := x.stockProduct(x.shop, "Rice", "100.00", "50", 5)
	ctx := context.Background()

	sold := x.sale(x.shop, billingdomain.PaymentCash, nil, line(rice, "1"))
	in := service.SettleInput{
		StoreID:   x.shop.storeID,
		InvoiceID: sold.Invoice.ID,
		Tenders:   []domain.TenderInput{tender(billingdomain.PaymentCash, "105.00")},
	}

	// Without the row lock, two requests under READ COMMITTED both see no
	// existing payments and both write, which is the same defect as the
	// discarded error by a different route.
	var wg sync.WaitGroup
	results := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = x.svc.Settle(ctx, in)
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrAlreadySettled):
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Errorf("%d concurrent settlements succeeded, want exactly 1", succeeded)
	}
	count, total := x.recorded(sold.Invoice.ID)
	if count != 1 || !total.Equal(money.MustFromString("105.00")) {
		t.Errorf("%d rows summing to %s, want 1 row of 105.00", count, total)
	}
}

func TestARetriedRequestReplaysItsOriginalResult(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	rice := x.stockProduct(x.shop, "Rice", "100.00", "50", 5)
	ctx := context.Background()

	sold := x.sale(x.shop, billingdomain.PaymentCash, nil, line(rice, "1"))
	in := service.SettleInput{
		StoreID:        x.shop.storeID,
		InvoiceID:      sold.Invoice.ID,
		Tenders:        []domain.TenderInput{tender(billingdomain.PaymentCash, "105.00")},
		IdempotencyKey: uuid.New().String(),
	}

	first, err := x.svc.Settle(ctx, in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// A till whose connection dropped after the write retries. It should get
	// the original answer, not a conflict.
	second, err := x.svc.Settle(ctx, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !second.Replayed {
		t.Error("the replayed response should say so")
	}
	if len(second.Payments) != 1 || second.Payments[0].ID != first.Payments[0].ID {
		t.Errorf("replay returned different payments from the original")
	}
	if count, _ := x.recorded(sold.Invoice.ID); count != 1 {
		t.Errorf("%d payment rows exist after a replay, want 1", count)
	}
}

// TestARetryBesideTheFirstSettlementReplaysRatherThanConflicting is the retry
// that arrives while the settlement it repeats is still in flight.
//
// TestARetriedRequestReplaysItsOriginalResult covers the retry that comes after
// the original committed, which the pre-transaction key lookup answers. This is
// the one a dropped connection actually produces: the till sends the tender,
// hears nothing, and sends it again straight away. Neither request can see the
// other's key yet, so both do the work and the invoice row lock decides.
//
// The loser must be told about the settlement that happened. ErrAlreadySettled
// is the right answer to a second, different attempt to take money for the same
// bill; it is the wrong answer to the same attempt under the same key, because
// a till that gets an error after the money was recorded will either ask the
// customer to pay again or leave the sale looking unsettled.
func TestARetryBesideTheFirstSettlementReplaysRatherThanConflicting(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	rice := x.stockProduct(x.shop, "Rice", "100.00", "50", 5)
	ctx := context.Background()

	sold := x.sale(x.shop, billingdomain.PaymentCash, nil, line(rice, "1"))
	in := service.SettleInput{
		StoreID:        x.shop.storeID,
		InvoiceID:      sold.Invoice.ID,
		Tenders:        []domain.TenderInput{tender(billingdomain.PaymentCash, "105.00")},
		IdempotencyKey: uuid.New().String(),
	}

	const attempts = 4
	results := make([]service.Result, attempts)
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = x.svc.Settle(ctx, in)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("attempt %d got an error instead of the settlement: %v", i, err)
		}
	}
	reported := map[uuid.UUID]bool{}
	for i, got := range results {
		if errs[i] != nil {
			continue
		}
		if len(got.Payments) == 0 {
			t.Errorf("attempt %d reported no payments at all", i)
			continue
		}
		reported[got.Payments[0].ID] = true
	}
	if len(reported) > 1 {
		t.Errorf("the same key reported %d different payments", len(reported))
	}

	// And the money was taken once.
	count, total := x.recorded(sold.Invoice.ID)
	if count != 1 || !total.Equal(money.MustFromString("105.00")) {
		t.Errorf("%d payment rows summing to %s, want 1 row of 105.00", count, total)
	}
}

func TestAVoidedInvoiceCannotBeSettled(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	rice := x.stockProduct(x.shop, "Rice", "100.00", "50", 5)
	ctx := context.Background()

	sold := x.sale(x.shop, billingdomain.PaymentCash, nil, line(rice, "1"))
	if err := x.svcs.Billing.Void(ctx, x.shop.storeID, sold.Invoice.ID); err != nil {
		t.Fatalf("void: %v", err)
	}

	_, err := x.svc.Settle(ctx, service.SettleInput{
		StoreID:   x.shop.storeID,
		InvoiceID: sold.Invoice.ID,
		Tenders:   []domain.TenderInput{tender(billingdomain.PaymentCash, "105.00")},
	})
	if !errors.Is(err, domain.ErrNotSettleable) {
		t.Fatalf("expected ErrNotSettleable, got %v", err)
	}
}

// --- store scoping -----------------------------------------------------------

func TestAPaymentAgainstAnotherStoresInvoiceIsRefused(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	ctx := context.Background()

	other := x.newShop("Rival Shop", "T9", "rival-cashier")
	theirRice := x.stockProduct(other, "Rice", "100.00", "50", 5)
	theirs := x.sale(other, billingdomain.PaymentCash, nil, line(theirRice, "1"))

	// The invoice id alone must not be enough. The store is a parameter, and
	// the answer for another shop's sale is the same as for a sale that does
	// not exist — confirming it exists would leak it.
	_, err := x.svc.Settle(ctx, service.SettleInput{
		StoreID:   x.shop.storeID,
		InvoiceID: theirs.Invoice.ID,
		Tenders:   []domain.TenderInput{tender(billingdomain.PaymentCash, "105.00")},
	})
	if !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("expected ErrNotFound settling another store's invoice, got %v", err)
	}
	if count, _ := x.recorded(theirs.Invoice.ID); count != 0 {
		t.Errorf("a cross-store settlement wrote %d payment rows", count)
	}

	if _, err := x.svc.ForInvoice(ctx, x.shop.storeID, theirs.Invoice.ID); !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("expected ErrNotFound reading another store's payments, got %v", err)
	}
}

func TestPaymentsReadBackAsTheyWereRecorded(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	rice := x.stockProduct(x.shop, "Rice", "100.00", "50", 5)
	ctx := context.Background()

	sold := x.sale(x.shop, billingdomain.PaymentSplit, nil, line(rice, "1"))
	if _, err := x.svc.Settle(ctx, service.SettleInput{
		StoreID:   x.shop.storeID,
		InvoiceID: sold.Invoice.ID,
		Tenders: []domain.TenderInput{
			{Mode: billingdomain.PaymentCash, Amount: money.MustFromString("55.00")},
			{Mode: billingdomain.PaymentCard, Amount: money.MustFromString("50.00"), Reference: "SLIP-4417"},
		},
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}

	got, err := x.svc.ForInvoice(ctx, x.shop.storeID, sold.Invoice.ID)
	if err != nil {
		t.Fatalf("read payments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read back %d payments, want 2", len(got))
	}
	sum := money.Zero()
	for _, p := range got {
		sum = sum.Add(p.Amount)
	}
	if !sum.Equal(sold.Invoice.Total) {
		t.Errorf("payments read back sum to %s, want %s", sum, sold.Invoice.Total)
	}
	// The listing this replaces discarded rows.Scan, so a row that failed to
	// decode was reported as a zero-value payment.
	var slip string
	for _, p := range got {
		if p.Mode == billingdomain.PaymentCard {
			slip = p.Reference
		}
	}
	if slip != "SLIP-4417" {
		t.Errorf("card reference read back as %q, want SLIP-4417", slip)
	}
}

// --- the settlement report ---------------------------------------------------

// tradingDay sells a fixed day at one shop and returns the day to report on.
// The figures are chosen so every expected total below is exact.
func (x *counter) tradingDay() time.Time {
	x.t.Helper()
	ctx := context.Background()
	s := x.shop

	rice := x.stockProduct(s, "Rice", "100.00", "100", 5) // 105.00 each
	soap := x.stockProduct(s, "Soap", "50.00", "100", 18) // 59.00 each

	var customer uuid.UUID
	mustScan(x.t, x.db, `INSERT INTO customers (name, store_id, credit_limit)
		VALUES ('Corner Cafe', $1, 50000) RETURNING id`, &customer, s.storeID)

	first := x.sale(s, billingdomain.PaymentCash, nil, line(rice, "1")) // 105.00 cash
	x.sale(s, billingdomain.PaymentUPI, nil, line(soap, "2"))           // 118.00 upi
	x.sale(s, billingdomain.PaymentCard, nil, line(rice, "1"), line(soap, "1"))
	x.sale(s, billingdomain.PaymentCredit, &customer, line(soap, "1")) // 59.00 credit

	// A split settled through this service: 105.00 across four tenders.
	split := x.sale(s, billingdomain.PaymentSplit, nil, line(rice, "1"))
	if _, err := x.svc.Settle(ctx, service.SettleInput{
		StoreID:   s.storeID,
		InvoiceID: split.Invoice.ID,
		Tenders: []domain.TenderInput{
			tender(billingdomain.PaymentCash, "50.00"),
			tender(billingdomain.PaymentCard, "30.00"),
			tender(billingdomain.PaymentUPI, "15.00"),
			tender(billingdomain.PaymentBank, "10.00"),
		},
	}); err != nil {
		x.t.Fatalf("settle the split: %v", err)
	}

	// A voided cash sale, and a voided sale that was settled before it was
	// cancelled. Neither may reach the day's takings — one through the invoice
	// header, the other through its tender rows.
	voidedPlain := x.sale(s, billingdomain.PaymentCash, nil, line(rice, "1"))
	voidedSettled := x.sale(s, billingdomain.PaymentSplit, nil, line(rice, "1"))
	if _, err := x.svc.Settle(ctx, service.SettleInput{
		StoreID:   s.storeID,
		InvoiceID: voidedSettled.Invoice.ID,
		Tenders:   []domain.TenderInput{tender(billingdomain.PaymentCash, "105.00")},
	}); err != nil {
		x.t.Fatalf("settle the sale that will be voided: %v", err)
	}
	for _, v := range []uuid.UUID{voidedPlain.Invoice.ID, voidedSettled.Invoice.ID} {
		if err := x.svcs.Billing.Void(ctx, s.storeID, v); err != nil {
			x.t.Fatalf("void: %v", err)
		}
	}

	day := x.invoiceDay(first.Invoice.ID)

	// Two refunds: one on the day, one the evening before. Only the first
	// belongs in this day's net.
	x.mustExec(`INSERT INTO returns (product_id, store_id, quantity, amount, reason, status, created_at)
		VALUES ($1, $2, 1, 30.00, 'damaged', 'completed', $3::date + interval '10 hours')`,
		rice, s.storeID, day.Format(domain.DateLayout))
	x.mustExec(`INSERT INTO returns (product_id, store_id, quantity, amount, reason, status, created_at)
		VALUES ($1, $2, 1, 12.00, 'damaged', 'completed', $3::date - interval '2 hours')`,
		rice, s.storeID, day.Format(domain.DateLayout))

	return day
}

func TestTheSettlementReportTotalsTheDayByMode(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	day := x.tradingDay()

	got, err := x.svc.Settlement(context.Background(), x.shop.storeID, day)
	if err != nil {
		t.Fatalf("settlement report: %v", err)
	}

	want := map[billingdomain.PaymentMode]string{
		// 105.00 header cash + 50.00 of the split
		billingdomain.PaymentCash: "155.00",
		// 164.00 header card + 30.00 of the split
		billingdomain.PaymentCard: "194.00",
		// 118.00 header upi + 15.00 of the split
		billingdomain.PaymentUPI: "133.00",
		// only the split's bank leg
		billingdomain.PaymentBank:   "10.00",
		billingdomain.PaymentCheque: "0.00",
		billingdomain.PaymentCredit: "59.00",
	}
	for mode, amount := range want {
		if g := got.Amount(mode); !g.Equal(money.MustFromString(amount)) {
			t.Errorf("%s total = %s, want %s", mode, g, amount)
		}
	}

	if got.Invoices != 5 {
		t.Errorf("invoice count = %d, want 5", got.Invoices)
	}
	if !got.TotalSales.Equal(money.MustFromString("551.00")) {
		t.Errorf("total sales = %s, want 551.00", got.TotalSales)
	}
	if !got.Refunds.Equal(money.MustFromString("30.00")) {
		t.Errorf("refunds = %s, want 30.00 — yesterday's refund leaked in", got.Refunds)
	}
	if got.Date != day.Format(domain.DateLayout) {
		t.Errorf("report date = %q, want %q", got.Date, day.Format(domain.DateLayout))
	}
}

func TestVoidedSalesAreNotInTheDaysTakings(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	day := x.tradingDay()

	got, err := x.svc.Settlement(context.Background(), x.shop.storeID, day)
	if err != nil {
		t.Fatalf("settlement report: %v", err)
	}

	// Two cash sales of 105.00 were voided — one settled through its header,
	// one through a tender row. Counting either would tell the shopkeeper to
	// expect cash the drawer does not hold.
	if !got.Amount(billingdomain.PaymentCash).Equal(money.MustFromString("155.00")) {
		t.Errorf("cash = %s, want 155.00 — a voided sale reached the takings", got.Amount(billingdomain.PaymentCash))
	}
	if got.Count(billingdomain.PaymentCash) != 2 {
		t.Errorf("cash tender count = %d, want 2", got.Count(billingdomain.PaymentCash))
	}
	if !got.TotalSales.Equal(money.MustFromString("551.00")) {
		t.Errorf("total sales = %s, want 551.00 — voided sales were counted", got.TotalSales)
	}
}

func TestTheReportsPartsSumToItsNetFigureExactly(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	day := x.tradingDay()

	got, err := x.svc.Settlement(context.Background(), x.shop.storeID, day)
	if err != nil {
		t.Fatalf("settlement report: %v", err)
	}

	// Every line, added up, is what was settled.
	settled := money.Zero()
	for _, line := range got.Modes {
		settled = settled.Add(line.Amount)
	}
	if !settled.Equal(got.Settled) {
		t.Errorf("the lines sum to %s but Settled says %s", settled, got.Settled)
	}
	// What was settled, less what was not settled today, less what went back
	// over the counter, is the net — exactly, with no epsilon anywhere.
	wantNet := settled.Sub(got.Amount(billingdomain.PaymentCredit)).Sub(got.Refunds)
	if !got.Net.Equal(wantNet) {
		t.Errorf("net = %s but the parts come to %s", got.Net, wantNet)
	}
	if !got.Collected.Add(got.OnCredit).Equal(got.Settled) {
		t.Errorf("collected %s + on credit %s != settled %s", got.Collected, got.OnCredit, got.Settled)
	}
	if !got.Net.Equal(money.MustFromString("462.00")) {
		t.Errorf("net = %s, want 462.00", got.Net)
	}

	// Every completed sale is accounted for by exactly one recorded mode, so
	// nothing is left over.
	if !got.Unsettled.IsZero() {
		t.Errorf("unsettled = %s, want 0 — the day does not reconcile", got.Unsettled)
	}
}

func TestAnUnrecognisedModeIsNotQuietlyCountedAsCash(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	ctx := context.Background()
	rice := x.stockProduct(x.shop, "Rice", "100.00", "50", 5)

	sold := x.sale(x.shop, billingdomain.PaymentSplit, nil, line(rice, "1"))
	// Free text is what the column allows, and what legacy rows contain.
	// "phonepe" is UPI; "gift_voucher" is nothing the system knows.
	x.mustExec(`INSERT INTO invoice_payments (invoice_id, payment_mode, amount) VALUES ($1, ' PhonePe ', 60.00)`, sold.Invoice.ID)
	x.mustExec(`INSERT INTO invoice_payments (invoice_id, payment_mode, amount) VALUES ($1, 'gift_voucher', 45.00)`, sold.Invoice.ID)

	got, err := x.svc.Settlement(ctx, x.shop.storeID, x.invoiceDay(sold.Invoice.ID))
	if err != nil {
		t.Fatalf("settlement report: %v", err)
	}
	if !got.Amount(billingdomain.PaymentUPI).Equal(money.MustFromString("60.00")) {
		t.Errorf("upi = %s, want 60.00 — the alias was not folded in", got.Amount(billingdomain.PaymentUPI))
	}
	// The report this replaces added unrecognised modes to the cash total "for
	// settlement visibility", raising the cash the drawer was expected to hold
	// by an amount that was never cash.
	if !got.Amount(billingdomain.PaymentCash).IsZero() {
		t.Errorf("cash = %s, want 0 — an unknown mode was counted as cash", got.Amount(billingdomain.PaymentCash))
	}
	if !got.Amount(domain.ModeOther).Equal(money.MustFromString("45.00")) {
		t.Errorf("other = %s, want 45.00", got.Amount(domain.ModeOther))
	}
}

func TestASaleMarkedSplitWithNoTendersShowsAsUnsettled(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	rice := x.stockProduct(x.shop, "Rice", "100.00", "50", 5)

	// The report this replaces silently ignored these, so the day appeared to
	// balance while 105.00 of sales had no settlement recorded at all.
	sold := x.sale(x.shop, billingdomain.PaymentSplit, nil, line(rice, "1"))

	got, err := x.svc.Settlement(context.Background(), x.shop.storeID, x.invoiceDay(sold.Invoice.ID))
	if err != nil {
		t.Fatalf("settlement report: %v", err)
	}
	if !got.Settled.IsZero() {
		t.Errorf("settled = %s, want 0", got.Settled)
	}
	if !got.Unsettled.Equal(money.MustFromString("105.00")) {
		t.Errorf("unsettled = %s, want 105.00", got.Unsettled)
	}
}

func TestTheReportIsScopedToOneStore(t *testing.T) {
	t.Parallel()
	x := newCounter(t)
	day := x.tradingDay()

	other := x.newShop("Rival Shop", "T9", "rival-cashier")
	theirRice := x.stockProduct(other, "Rice", "100.00", "50", 5)
	x.sale(other, billingdomain.PaymentCash, nil, line(theirRice, "1"))

	got, err := x.svc.Settlement(context.Background(), x.shop.storeID, day)
	if err != nil {
		t.Fatalf("settlement report: %v", err)
	}
	if !got.Amount(billingdomain.PaymentCash).Equal(money.MustFromString("155.00")) {
		t.Errorf("cash = %s, want 155.00 — another store's takings leaked in", got.Amount(billingdomain.PaymentCash))
	}
	if got.Invoices != 5 {
		t.Errorf("invoice count = %d, want 5", got.Invoices)
	}
}

func TestAQuietDayReportsZerosRatherThanFailing(t *testing.T) {
	t.Parallel()
	x := newCounter(t)

	got, err := x.svc.SettlementOn(context.Background(), x.shop.storeID, "2020-01-02")
	if err != nil {
		t.Fatalf("settlement report: %v", err)
	}
	if got.Invoices != 0 || !got.TotalSales.IsZero() || !got.Net.IsZero() || !got.Unsettled.IsZero() {
		t.Errorf("a day with no trading reported %+v", got)
	}
	if len(got.Modes) != len(domain.CanonicalModes) {
		t.Errorf("%d mode lines, want the %d canonical ones", len(got.Modes), len(domain.CanonicalModes))
	}
}
