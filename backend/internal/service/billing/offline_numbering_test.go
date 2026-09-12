package billing_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"gbos/internal/domain/billing"
	"gbos/internal/domain/document"
	"gbos/internal/domain/money"
	service "gbos/internal/service/billing"
)

// TestATillMayNumberItsOwnSale is the change that makes offline billing possible.
//
// A till that cannot reach the server still has to hand the customer a tax
// invoice, and a tax invoice carries a number. So the till draws from its own
// terminal's series and states it. The server records it, and the counter moves
// past it so a later server-side allocation cannot reissue it.
func TestATillMayNumberItsOwnSale(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)
	year := document.FinancialYear(time.Now())

	stated := document.Number(document.PrefixInvoice, "T1", year, 7)
	got, err := x.sale(service.CreateInput{
		Lines:     []billing.LineInput{line(rice, "1")},
		InvoiceNo: stated,
	})
	if err != nil {
		t.Fatalf("a sale numbered by the till: %v", err)
	}
	if got.Invoice.InvoiceNo != stated {
		t.Fatalf("invoice number = %s, want the till's %s", got.Invoice.InvoiceNo, stated)
	}

	// The counter moved past it: the next sale the server numbers is 8, not 1.
	next, err := x.sale(service.CreateInput{Lines: []billing.LineInput{line(rice, "1")}})
	if err != nil {
		t.Fatalf("the next sale: %v", err)
	}
	want := document.Number(document.PrefixInvoice, "T1", year, 8)
	if next.Invoice.InvoiceNo != want {
		t.Errorf("the server then allocated %s, want %s — the counter did not move past the till's number",
			next.Invoice.InvoiceNo, want)
	}
}

// TestTheSameNumberTwiceIsRefused: two tills billing on one terminal code is a
// misconfiguration, and it has to fail loudly on sync rather than quietly issue
// one number twice. The unique index is what catches it.
func TestTheSameNumberTwiceIsRefused(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)
	stated := document.Number(document.PrefixInvoice, "T1", document.FinancialYear(time.Now()), 3)

	if _, err := x.sale(service.CreateInput{
		Lines: []billing.LineInput{line(rice, "1")}, InvoiceNo: stated,
	}); err != nil {
		t.Fatalf("the first sale: %v", err)
	}
	if _, err := x.sale(service.CreateInput{
		Lines: []billing.LineInput{line(rice, "1")}, InvoiceNo: stated,
	}); err == nil {
		t.Fatal("the same invoice number was issued twice")
	}

	// And the shelf is untouched by the refused sale: 50 less the one that sold.
	if got := x.onHand(rice); !got.Equal(money.MustQuantityFromString("49")) {
		t.Errorf("on hand = %s, want 49 — the refused sale moved stock", got)
	}
}

// TestATillCannotInventANumber covers what the unique index cannot.
//
// A duplicate is caught by the index. A number belonging to another counter, or
// to a closed financial year, or simply not in the form this system issues, is
// unique and would be accepted — and each one breaks the consecutive series
// Rule 46(b) requires in a way nobody would notice until a return was filed.
func TestATillCannotInventANumber(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)
	year := document.FinancialYear(time.Now())

	cases := map[string]string{
		"another counter's series":  document.Number(document.PrefixInvoice, "T9", year, 1),
		"a closed financial year":   document.Number(document.PrefixInvoice, "T1", "2019-20", 1),
		"a purchase order's series": document.Number(document.PrefixPurchaseOrder, "T1", year, 1),
		"not a number at all":       "hand-written-42",
		"an unpadded sequence":      "INV/T1/" + year + "/7",
		"a zero sequence":           "INV/T1/" + year + "/00000",
	}
	for name, stated := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := x.sale(service.CreateInput{
				Lines: []billing.LineInput{line(rice, "1")}, InvoiceNo: stated,
			})
			if err == nil {
				t.Fatalf("%q was accepted", stated)
			}
			if !strings.Contains(err.Error(), "validation") {
				t.Errorf("%q: %v — this should be a validation error the till can act on", stated, err)
			}
		})
	}
}

// TestAnOfflineSaleIsDatedWhenItHappened: the till's clock decides the document
// date, because that is when the customer was served. Dating it on arrival would
// put a Tuesday's takings in Wednesday's column, and a shop reconciles its
// drawer nightly.
func TestAnOfflineSaleIsDatedWhenItHappened(t *testing.T) {
	t.Parallel()
	x := newTill(t)
	rice := x.stockProduct("Rice", "100.00", "75.00", "50", 5, false)

	served := time.Now().In(document.Location).Add(-26 * time.Hour)
	got, err := x.sale(service.CreateInput{
		Lines:         []billing.LineInput{line(rice, "1")},
		BilledOffline: true,
		BilledAt:      &served,
	})
	if err != nil {
		t.Fatalf("an offline sale synced later: %v", err)
	}
	if !got.Invoice.BilledOffline {
		t.Error("the invoice does not record that it was billed offline")
	}
	if delta := got.Invoice.InvoiceDate.Sub(served); delta > time.Second || delta < -time.Second {
		t.Errorf("invoice_date = %s, want the till's %s", got.Invoice.InvoiceDate, served)
	}

	// And it reads back that way, not just in the response.
	reread, err := x.svc.Get(context.Background(), x.storeID, got.Invoice.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !reread.Invoice.BilledOffline {
		t.Error("billed_offline did not survive the round trip")
	}
}
