package billing_test

import (
	"context"
	"testing"

	"gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	service "gbos/internal/service/billing"
)

// TestAReprintShowsTheDocumentNotTodaysCatalogue is the reason the description,
// HSN code and tax rate are written onto each line instead of joined from the
// product.
//
// A shop renames goods, reclassifies them, and has rates changed under it by
// notification. None of that may reach an invoice already issued: CGST Rule 46
// requires those particulars to be on the document, and a bill that reprints
// with a rate the customer was not charged misstates tax already collected and
// remitted.
func TestAReprintShowsTheDocumentNotTodaysCatalogue(t *testing.T) {
	t.Parallel()
	x := newTill(t)

	rice := x.product("Sona Masoori Rice 25kg", 5, true)
	x.sellingUnit(rice, "Loose", "1", "450.00", true)
	x.stockOnHand(rice, "100", "337.50")
	if _, err := x.db.ExecContext(context.Background(),
		`UPDATE products SET hsn_code = '1006' WHERE id = $1`, rice); err != nil {
		t.Fatalf("set hsn: %v", err)
	}

	sold, err := x.sale(service.CreateInput{Lines: []billing.LineInput{line(rice, "2")}})
	if err != nil {
		t.Fatalf("sale: %v", err)
	}
	assertLine(t, "at the moment of sale", sold.Items[0], "Sona Masoori Rice 25kg", "1006", "5")

	// The shop now renames the product, reclassifies it, and the rate changes.
	if _, err := x.db.ExecContext(context.Background(),
		`UPDATE products SET name = 'Premium Sona Masoori 25kg', hsn_code = '1006300', gst_rate = 12
		  WHERE id = $1`, rice); err != nil {
		t.Fatalf("amend catalogue: %v", err)
	}

	reprint, err := x.svc.Get(context.Background(), x.storeID, sold.Invoice.ID)
	if err != nil {
		t.Fatalf("reprint: %v", err)
	}
	assertLine(t, "on reprint", reprint.Items[0], "Sona Masoori Rice 25kg", "1006", "5")

	// The money on the document is unchanged too: 2 x 450 at 5% is 945.00, and
	// it must not become 1008.00 because the rate moved.
	if !reprint.Invoice.Total.Equal(money.MustFromString("945.00")) {
		t.Errorf("reprinted total = %s, want 945.00", reprint.Invoice.Total)
	}
}

func assertLine(t *testing.T, when string, got billing.Item, name, hsn, gst string) {
	t.Helper()
	if got.ProductName != name {
		t.Errorf("%s: description = %q, want %q", when, got.ProductName, name)
	}
	if got.HSNCode != hsn {
		t.Errorf("%s: hsn_code = %q, want %q", when, got.HSNCode, hsn)
	}
	if got.GSTRate.String() != gst {
		t.Errorf("%s: gst_rate = %s, want %s", when, got.GSTRate, gst)
	}
}
