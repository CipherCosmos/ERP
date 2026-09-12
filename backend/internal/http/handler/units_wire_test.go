package handler

import (
	"encoding/json"
	"testing"

	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

// The wire shapes a till and a receiving screen post are the contract this
// change is for, and they have to keep two promises at once: a client that
// knows about units can name one, and a client written before units existed
// keeps working because a bare `quantity` still means the product's default
// unit. Binding is where both promises are kept or quietly broken, so it is
// pinned here rather than only exercised end to end.

func TestASaleLineNamesItsUnitOrFallsBackToTheDefault(t *testing.T) {
	sackID := uuid.New()

	cases := map[string]struct {
		body     string
		wantUnit uuid.UUID
		wantQty  string
	}{
		"unit named": {
			body:     `{"product_id":"p","product_unit_id":"` + sackID.String() + `","unit_qty":2}`,
			wantUnit: sackID,
			wantQty:  "2",
		},
		// The spelling every existing client sends. It has always meant "how
		// many of the thing you are buying", and the default unit is what it
		// meant it in.
		"legacy quantity": {
			body:    `{"product_id":"p","quantity":2.5}`,
			wantQty: "2.5",
		},
		// A blank unit is an absent unit, not a malformed one: a form that
		// submits every field sends "" for the one nobody chose.
		"blank unit": {
			body:    `{"product_id":"p","product_unit_id":"","unit_qty":1}`,
			wantQty: "1",
		},
		// unit_qty wins, so a client that sends both while migrating is not at
		// the mercy of field order.
		"both spellings": {
			body:    `{"product_id":"p","unit_qty":3,"quantity":9}`,
			wantQty: "3",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var got saleLineRequest
			if err := json.Unmarshal([]byte(tc.body), &got); err != nil {
				t.Fatalf("bind: %v", err)
			}
			unitID, err := got.unitID()
			if err != nil {
				t.Fatalf("parse unit: %v", err)
			}
			if unitID != tc.wantUnit {
				t.Errorf("product unit = %s, want %s", unitID, tc.wantUnit)
			}
			if q := got.qty(); !q.Equal(money.MustQuantityFromString(tc.wantQty)) {
				t.Errorf("quantity = %s, want %s", q, tc.wantQty)
			}
		})
	}

	// A malformed id is a bad request, not a silent fall back to the default:
	// selling at the default unit's price because an id failed to parse is the
	// kind of quiet substitution this whole change exists to remove.
	var bad saleLineRequest
	if err := json.Unmarshal([]byte(`{"product_id":"p","product_unit_id":"not-a-uuid"}`), &bad); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, err := bad.unitID(); err == nil {
		t.Error("a malformed product_unit_id should be refused")
	}
}

func TestAPurchaseLineNamesItsUnitOrFallsBackToTheDefault(t *testing.T) {
	sackID := uuid.New()

	var named purchasingOrderLineRequest
	if err := json.Unmarshal([]byte(
		`{"product_id":"p","product_unit_id":"`+sackID.String()+`","unit_qty":10,"rate":900}`), &named); err != nil {
		t.Fatalf("bind: %v", err)
	}
	unitID, err := purchasingUnitID(named.ProductUnitID)
	if err != nil {
		t.Fatalf("parse unit: %v", err)
	}
	if unitID != sackID {
		t.Errorf("product unit = %s, want %s", unitID, sackID)
	}
	if !named.qty().Equal(money.QuantityFromInt(10)) {
		t.Errorf("unit_qty = %s, want 10", named.qty())
	}
	// The rate is per buying unit, so the line total is a multiplication and
	// ten sacks at Rs.900 is Rs.9,000 rather than nine thousand and something.
	if !named.Rate.Equal(money.MustFromString("900.00")) {
		t.Errorf("rate = %s, want 900.00", named.Rate)
	}

	var legacy purchasingOrderLineRequest
	if err := json.Unmarshal([]byte(`{"product_id":"p","quantity":4}`), &legacy); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got, err := purchasingUnitID(legacy.ProductUnitID); err != nil || got != uuid.Nil {
		t.Errorf("unit = %s, err = %v; want the nil uuid meaning the default unit", got, err)
	}
	if !legacy.qty().Equal(money.QuantityFromInt(4)) {
		t.Errorf("quantity = %s, want 4", legacy.qty())
	}
}

func TestAReceiptLineCountsInTheUnitItNames(t *testing.T) {
	looseID := uuid.New()

	// The three spellings a receiving screen may send, in the order they win.
	// received_qty is what /purchases/receive posted, quantity is what /grn
	// posted, and unit_qty is the name the units work gave the same field.
	cases := map[string]struct {
		body string
		want string
	}{
		"received_qty": {`{"product_id":"p","received_qty":10}`, "10"},
		"unit_qty":     {`{"product_id":"p","unit_qty":7}`, "7"},
		"quantity":     {`{"product_id":"p","quantity":3}`, "3"},
		"none stated":  {`{"product_id":"p"}`, "0"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var got purchasingReceiptLineRequest
			if err := json.Unmarshal([]byte(tc.body), &got); err != nil {
				t.Fatalf("bind: %v", err)
			}
			if q := got.received(); !q.Equal(money.MustQuantityFromString(tc.want)) {
				t.Errorf("received = %s, want %s", q, tc.want)
			}
		})
	}

	// An accepted quantity of zero is a stated fact and must survive binding as
	// one. A delivery with everything rejected books nothing into stock, and
	// that is only expressible if a stated zero differs from an absent value.
	var allRejected purchasingReceiptLineRequest
	if err := json.Unmarshal([]byte(
		`{"product_id":"p","product_unit_id":"`+looseID.String()+
			`","received_qty":10,"rejected_qty":10,"accepted_qty":0}`), &allRejected); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if allRejected.AcceptedQty == nil {
		t.Fatal("a stated accepted_qty of 0 was lost in binding")
	}
	if !allRejected.AcceptedQty.IsZero() {
		t.Errorf("accepted = %s, want 0", *allRejected.AcceptedQty)
	}
	if got, err := purchasingUnitID(allRejected.ProductUnitID); err != nil || got != looseID {
		t.Errorf("unit = %s, err = %v; want %s", got, err, looseID)
	}
}
