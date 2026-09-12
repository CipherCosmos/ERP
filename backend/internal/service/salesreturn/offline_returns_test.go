package salesreturn_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gbos/internal/domain/money"
	"gbos/internal/domain/reconcile"
	domain "gbos/internal/domain/salesreturn"
	repo "gbos/internal/repo/postgres"
	service "gbos/internal/service/salesreturn"
)

// TestARefundAlreadyPaidIsRecordedAndFlagged.
//
// A return taken while the till was offline is a claim the cashier has already
// settled: the goods are on the counter and the money has left the drawer. When
// it syncs, the invoice behind it may not support it — more coming back than
// went out, or a product that invoice never sold. Refusing then leaves the shop
// having paid against a record it does not hold.
//
// So it lands, and the doubt is written down for somebody to decide.
func TestARefundAlreadyPaidIsRecordedAndFlagged(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "100.00", "75.00", "50")

	sale := x.sell(rice, "2")
	served := time.Now().Add(-90 * time.Minute)

	// Five kilos coming back against a bill for two.
	ret, err := x.svc.CreateReturn(ctx, domain.ReturnInput{
		InvoiceID:     &sale.Invoice.ID,
		ProductID:     rice,
		StoreID:       x.storeID,
		Quantity:      money.MustQuantityFromString("5"),
		Amount:        money.MustFromString("500.00"),
		Reason:        "customer returned five",
		RefundMode:    "cash",
		RaisedOffline: true,
		RaisedAt:      &served,
	})
	if err != nil {
		t.Fatalf("a refund already paid was refused on sync: %v", err)
	}
	if !ret.RaisedOffline {
		t.Error("the return does not record that the till was offline")
	}
	if delta := ret.RaisedAt.Sub(served); delta > time.Second || delta < -time.Second {
		t.Errorf("raised_at = %s, want the till's %s", ret.RaisedAt, served)
	}
	// Still pending: approval is what moves stock, and it is a separate decision
	// made against what the shelf and the invoice say.
	if ret.Status != domain.StatusPending {
		t.Errorf("status = %s, want pending — nothing moves until somebody approves", ret.Status)
	}

	open := x.openExceptions(t)
	if len(open) != 1 || open[0].Kind != reconcile.OverReturned {
		t.Fatalf("exceptions = %+v, want one over_returned", open)
	}
	if open[0].InvoiceID == nil || *open[0].InvoiceID != sale.Invoice.ID {
		t.Error("the exception does not name the invoice it is about")
	}
	if open[0].Detail == "" || open[0].Actual == "" {
		t.Errorf("the exception does not say what happened or what to do: %+v", open[0])
	}
}

// TestTheCounterStillRefusesAnImpossibleReturn is the other half. With a
// connection, this is something the cashier can act on before handing money
// over, so it is refused rather than recorded.
func TestTheCounterStillRefusesAnImpossibleReturn(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "100.00", "75.00", "50")
	sale := x.sell(rice, "2")

	_, err := x.svc.CreateReturn(ctx, domain.ReturnInput{
		InvoiceID:  &sale.Invoice.ID,
		ProductID:  rice,
		StoreID:    x.storeID,
		Quantity:   money.MustQuantityFromString("5"),
		Amount:     money.MustFromString("500.00"),
		Reason:     "customer returned five",
		RefundMode: "cash",
	})
	if err == nil {
		t.Fatal("a return for more than the invoice sold was accepted at the counter")
	}
	if !errors.Is(err, service.ErrOverReturn) {
		t.Errorf("expected an over-return error the cashier can act on, got %v", err)
	}
	if n := len(x.openExceptions(t)); n != 0 {
		t.Errorf("%d exceptions raised by a refused return, want none", n)
	}
	if got := x.countRows(`SELECT count(*) FROM returns WHERE store_id = $1`, x.storeID); got != 0 {
		t.Errorf("%d returns recorded by a refused request, want none", got)
	}
}

// TestAnOfflineReturnAgainstNoInvoiceIsFine: a goodwill return with no bill
// behind it has nothing to contradict, so it raises no exception.
func TestAnOfflineReturnAgainstNoInvoiceIsFine(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()
	rice := x.product("Rice", "100.00", "75.00", "50")
	served := time.Now().Add(-time.Hour)

	if _, err := x.svc.CreateReturn(ctx, domain.ReturnInput{
		ProductID:     rice,
		StoreID:       x.storeID,
		Quantity:      money.MustQuantityFromString("1"),
		Amount:        money.MustFromString("100.00"),
		Reason:        "goodwill",
		RefundMode:    "cash",
		RaisedOffline: true,
		RaisedAt:      &served,
	}); err != nil {
		t.Fatalf("a goodwill return taken offline: %v", err)
	}
	if n := len(x.openExceptions(t)); n != 0 {
		t.Errorf("%d exceptions raised, want none — there is no invoice to disagree with", n)
	}
}

func (x *shop) openExceptions(t *testing.T) []reconcile.Exception {
	t.Helper()
	got, err := repo.NewReconcile().List(context.Background(), x.db, reconcile.Filter{StoreID: x.storeID})
	if err != nil {
		t.Fatalf("read exceptions: %v", err)
	}
	return got
}
