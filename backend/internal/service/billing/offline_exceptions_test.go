package billing_test

import (
	"context"
	"testing"
	"time"

	"gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	"gbos/internal/domain/reconcile"
	repo "gbos/internal/repo/postgres"
	service "gbos/internal/service/billing"

	"github.com/google/uuid"
)

// TestASaleThatAlreadyHappenedIsNeverRefused is the rule offline billing rests
// on.
//
// The till sold five kilos while the connection was down. The shelf now says
// three, because another counter sold the same goods. Both sales are real: the
// rice left the shop twice over and the money is in two drawers. Refusing this
// one on sync would leave a shop whose books deny a sale its cash drawer
// records, which is the one thing this system exists to prevent.
//
// So the sale lands, the shelf goes to zero rather than negative — a shelf
// cannot hold minus two kilos — and an exception says two kilos are unaccounted
// for, which is a stock count waiting to happen.
func TestASaleThatAlreadyHappenedIsNeverRefused(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "3", 5, true)

	served := time.Now().Add(-2 * time.Hour)
	got, err := x.sale(service.CreateInput{
		Lines:         []billing.LineInput{line(rice, "5")},
		BilledOffline: true,
		BilledAt:      &served,
	})
	if err != nil {
		t.Fatalf("a sale that already happened was refused: %v", err)
	}
	// The customer paid for five kilos, so the bill is for five.
	if !got.Invoice.SubTotal.Equal(money.MustFromString("500.00")) {
		t.Errorf("sub total = %s, want 500.00 for the five kilos the customer took", got.Invoice.SubTotal)
	}
	if got.Items[0].Shortfall.StringFixed() != "2.000" {
		t.Errorf("shortfall = %s, want 2.000", got.Items[0].Shortfall)
	}
	if got := x.onHand(rice); !got.IsZero() {
		t.Errorf("on hand = %s, want zero — a shelf cannot hold a negative quantity", got)
	}

	// And somebody is told to count.
	open := x.openExceptions(t)
	if len(open) != 1 {
		t.Fatalf("%d exceptions raised, want 1: %+v", len(open), open)
	}
	if open[0].Kind != reconcile.Oversold {
		t.Errorf("kind = %s, want oversold", open[0].Kind)
	}
	if open[0].InvoiceID == nil || *open[0].InvoiceID != got.Invoice.ID {
		t.Error("the exception does not name the sale it is about")
	}
	if open[0].Detail == "" {
		t.Error("the exception does not say what to do about it")
	}
}

// TestTheCounterStillRefusesToSellWhatIsNotThere is the other half. A shortfall
// is allowed for a sale that has already happened and for nothing else: at the
// counter, stock going short is something a cashier can act on, and silently
// selling air is not.
func TestTheCounterStillRefusesToSellWhatIsNotThere(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "3", 5, true)

	if _, err := x.sale(service.CreateInput{Lines: []billing.LineInput{line(rice, "5")}}); err == nil {
		t.Fatal("a sale being made now was allowed to take more than the shelf holds")
	}
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("3")) {
		t.Errorf("on hand = %s, want the untouched 3", got)
	}
	if n := len(x.openExceptions(t)); n != 0 {
		t.Errorf("%d exceptions raised by a refused sale, want none", n)
	}
}

// TestAnOfflineCreditSalePastTheLimitIsRecordedAndFlagged: the till could not
// check the limit, the goods have gone and the customer has left. The debt is
// real whatever the limit says, and a shop that refused it would have a
// receivable its own books deny.
func TestAnOfflineCreditSalePastTheLimitIsRecordedAndFlagged(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)

	var customer uuid.UUID
	mustScan(t, x.db,
		`INSERT INTO customers (store_id, name, credit_limit) VALUES ($1, 'Rahul', 100) RETURNING id`,
		&customer, x.storeID)

	served := time.Now().Add(-time.Hour)
	got, err := x.sale(service.CreateInput{
		CustomerID:    &customer,
		PaymentMode:   billing.PaymentCredit,
		Lines:         []billing.LineInput{line(rice, "10")},
		BilledOffline: true,
		BilledAt:      &served,
	})
	if err != nil {
		t.Fatalf("an offline credit sale past the limit was refused: %v", err)
	}
	if !got.Invoice.Total.Equal(money.MustFromString("1050.00")) {
		t.Errorf("total = %s, want 1050.00", got.Invoice.Total)
	}

	// The receivable exists, because the debt does.
	var outstanding money.Money
	mustScan(t, x.db,
		`SELECT COALESCE(SUM(amount - paid_amount), 0) FROM credit_entries WHERE customer_id = $1`,
		&outstanding, customer)
	if !outstanding.Equal(money.MustFromString("1050.00")) {
		t.Errorf("outstanding = %s, want the 1050.00 the customer owes", outstanding)
	}

	open := x.openExceptions(t)
	if len(open) != 1 || open[0].Kind != reconcile.CreditLimitExceeded {
		t.Fatalf("exceptions = %+v, want one credit_limit_exceeded", open)
	}
	if open[0].CustomerID == nil || *open[0].CustomerID != customer {
		t.Error("the exception does not name the customer to ring")
	}

	// The counter still refuses the same sale when it can check.
	if _, err := x.sale(service.CreateInput{
		CustomerID:  &customer,
		PaymentMode: billing.PaymentCredit,
		Lines:       []billing.LineInput{line(rice, "10")},
	}); err == nil {
		t.Error("a credit sale past the limit was allowed at the counter")
	}
}

// openExceptions reads the store's unresolved reconciliation exceptions.
func (x *till) openExceptions(t *testing.T) []reconcile.Exception {
	t.Helper()
	got, err := repo.NewReconcile().List(context.Background(), x.db, reconcile.Filter{StoreID: x.storeID})
	if err != nil {
		t.Fatalf("read exceptions: %v", err)
	}
	return got
}
