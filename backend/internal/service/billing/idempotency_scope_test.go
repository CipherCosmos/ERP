package billing_test

import (
	"context"
	"testing"

	"gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	service "gbos/internal/service/billing"

	"github.com/google/uuid"
)

// TestTwoShopsMayUseTheSameIdempotencyKey is what scoping the key to the store
// buys, and it is not an abstraction: a till numbers its requests from its own
// counter, so "till-1-0001" is the first key every shop ever sends.
//
// The key space used to be global. The second shop's sale was refused at the
// counter, with a customer waiting, for a collision it could neither see nor
// avoid — and a till with a counter has no other key to offer. Both shops now
// raise their own sale, and each still gets exactly one.
func TestTwoShopsMayUseTheSameIdempotencyKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Two shops in one database, as a chain is.
	first := newTill(t)
	second := first.anotherShop(t)

	firstRice := first.stockProduct("Rice", "100.00", "75.00", "50", 5, false)
	secondRice := second.stockProduct("Rice", "100.00", "75.00", "50", 5, false)

	const sharedKey = "till-1-0001"

	a, err := first.sale(service.CreateInput{
		Lines: []billing.LineInput{line(firstRice, "2")}, IdempotencyKey: sharedKey,
	})
	if err != nil {
		t.Fatalf("the first shop's sale: %v", err)
	}
	b, err := second.sale(service.CreateInput{
		Lines: []billing.LineInput{line(secondRice, "3")}, IdempotencyKey: sharedKey,
	})
	if err != nil {
		t.Fatalf("the second shop's sale was refused for a key the first shop had used: %v", err)
	}

	if a.Invoice.ID == b.Invoice.ID {
		t.Fatal("both shops were given the same invoice")
	}
	if b.Replayed {
		t.Error("the second shop's sale was answered as a replay of the first's")
	}
	if a.Invoice.StoreID == b.Invoice.StoreID {
		t.Fatal("the fixture did not create two stores; this test proves nothing")
	}
	// Each shop sold its own goods, in its own quantity, off its own shelf.
	if got := a.Invoice.SubTotal; !got.Equal(money.MustFromString("200.00")) {
		t.Errorf("first shop sub total = %s, want 200.00", got)
	}
	if got := b.Invoice.SubTotal; !got.Equal(money.MustFromString("300.00")) {
		t.Errorf("second shop sub total = %s, want 300.00", got)
	}
	if got := first.onHand(firstRice); !got.Equal(money.MustQuantityFromString("48")) {
		t.Errorf("first shop on hand = %s, want 48", got)
	}
	if got := second.onHand(secondRice); !got.Equal(money.MustQuantityFromString("47")) {
		t.Errorf("second shop on hand = %s, want 47", got)
	}

	// And the key still does its job within each shop.
	replay, err := second.sale(service.CreateInput{
		Lines: []billing.LineInput{line(secondRice, "3")}, IdempotencyKey: sharedKey,
	})
	if err != nil {
		t.Fatalf("replay in the second shop: %v", err)
	}
	if replay.Invoice.ID != b.Invoice.ID || !replay.Replayed {
		t.Errorf("a retry inside one shop raised a second sale: %s then %s",
			b.Invoice.ID, replay.Invoice.ID)
	}
	if got := second.onHand(secondRice); !got.Equal(money.MustQuantityFromString("47")) {
		t.Errorf("second shop on hand after the retry = %s, want 47 — the stock moved twice", got)
	}
	_ = ctx
}

// anotherShop returns a till in a second store, sharing the same database and
// services. Chains are the normal case this system is built for, so a test about
// two shops should not need a second process.
func (x *till) anotherShop(t *testing.T) *till {
	t.Helper()
	other := &till{t: t, db: x.db, svc: x.svc, svcs: x.svcs, uomID: x.uomID}
	mustScan(t, x.db,
		`INSERT INTO stores (name, gstin) VALUES ('Second Shop', '29ZZZZZ1234F1Z5') RETURNING id`,
		&other.storeID)
	mustScan(t, x.db,
		`INSERT INTO terminals (store_id, code, name) VALUES ($1, 'T1', 'Counter 1') RETURNING id`,
		&other.terminalID, other.storeID)
	mustScan(t, x.db,
		`INSERT INTO users (username, password_hash, role, store_id)
		 VALUES ($1, 'x', 'cashier', $2) RETURNING id`,
		&other.userID, "second-shop-cashier-"+uuid.NewString()[:8], other.storeID)
	mustScan(t, x.db, `INSERT INTO categories (name) VALUES ('Groceries') RETURNING id`, &other.categoryID)
	return other
}
