package billing

import (
	"errors"
	"testing"

	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

// line is a priced line sold in the product's own stocking unit — a kilo of a
// product stocked by the kilo — which is what every case here that is not about
// units wants, and what every line meant before units existed.
func line(rate string, qty string, gstPercent int64) PricedLine {
	return unitLine(rate, qty, "1", gstPercent)
}

// unitLine is a priced line sold by a unit worth qtyInBase of the stocking
// unit: "25" for a 25 kg sack, "0.25" for a 250 g pouch.
func unitLine(rate, unitQty, qtyInBase string, gstPercent int64) PricedLine {
	return PricedLine{
		LineInput: LineInput{
			ProductID: uuid.New(),
			UnitQty:   money.MustQuantityFromString(unitQty),
		},
		Unit: SellingUnit{
			ID:        uuid.New(),
			Label:     qtyInBase + " unit",
			QtyInBase: money.MustQuantityFromString(qtyInBase),
		},
		Rate:    money.MustFromString(rate),
		GSTRate: money.RateFromInt(gstPercent),
	}
}

func withLineDiscount(l PricedLine, discount string) PricedLine {
	l.Discount = money.MustFromString(discount)
	return l
}

// assertBalances checks the arithmetic identity every invoice must satisfy.
// If this ever fails, the printed invoice does not add up.
func assertBalances(t *testing.T, got Computed) {
	t.Helper()

	lineSum := money.Zero()
	lineTax := money.Zero()
	for _, l := range got.Lines {
		lineSum = lineSum.Add(l.Taxable)
		lineTax = lineTax.Add(l.Tax.CGST).Add(l.Tax.SGST).Add(l.Tax.IGST)

		if !l.Total.Equal(l.Taxable.Add(l.Tax.CGST).Add(l.Tax.SGST).Add(l.Tax.IGST)) {
			t.Errorf("line total %s is not its taxable %s plus its tax", l.Total, l.Taxable)
		}
	}
	if !lineSum.Equal(got.SubTotal) {
		t.Errorf("line taxables sum to %s, invoice sub_total is %s", lineSum, got.SubTotal)
	}

	headerTax := got.CGST.Add(got.SGST).Add(got.IGST)
	if !lineTax.Equal(headerTax) {
		t.Errorf("line taxes sum to %s, invoice tax is %s", lineTax, headerTax)
	}

	want := got.SubTotal.Add(headerTax).Add(got.RoundOff)
	if !got.Total.Equal(want) {
		t.Errorf("total = %s, but sub_total + tax + round_off = %s", got.Total, want)
	}
}

func TestSingleLineIsPricedFromRateAndQuantity(t *testing.T) {
	got, err := Compute(ComputeInput{Lines: []PricedLine{line("55.00", "1", 5)}})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}

	// The Rs.55 at 5% case: tax is 2.75, split 1.38 + 1.37 so the two halves
	// re-add to exactly what is stored. The old code produced 1.375 twice,
	// printed 1.38 + 1.38, and stored 2.75.
	if !got.SubTotal.Equal(money.MustFromString("55.00")) {
		t.Errorf("sub_total = %s, want 55.00", got.SubTotal)
	}
	if !got.CGST.Equal(money.MustFromString("1.38")) {
		t.Errorf("cgst = %s, want 1.38", got.CGST)
	}
	if !got.SGST.Equal(money.MustFromString("1.37")) {
		t.Errorf("sgst = %s, want 1.37", got.SGST)
	}
	if !got.IGST.IsZero() {
		t.Errorf("igst = %s on an intra-state supply, want 0", got.IGST)
	}
	if !got.Total.Equal(money.MustFromString("57.75")) {
		t.Errorf("total = %s, want 57.75", got.Total)
	}
	assertBalances(t, got)
}

func TestFractionalQuantityIsExact(t *testing.T) {
	// 2.5 kg of rice at Rs.450/kg. Weighed goods are why quantity stays
	// decimal and never becomes an integer count.
	got, err := Compute(ComputeInput{Lines: []PricedLine{line("450.00", "2.5", 5)}})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if !got.SubTotal.Equal(money.MustFromString("1125.00")) {
		t.Errorf("sub_total = %s, want 1125.00", got.SubTotal)
	}
	if !got.Total.Equal(money.MustFromString("1181.25")) {
		t.Errorf("total = %s, want 1181.25", got.Total)
	}
	assertBalances(t, got)
}

func TestBulkIsPricedPerSellingUnitAndNotDividedBack(t *testing.T) {
	// Two 25 kg sacks at Rs.1,050 each. The bill is 2,100 exactly, and the
	// shelf loses 50 kg.
	//
	// The arithmetic this refuses is the one that looks equivalent: derive a
	// rate per kilo, 1050/25 = 42, and multiply the 50 kg back. That happens to
	// work here and does not for a Rs.1,000 three-kilo pack, where the derived
	// rate is 333.333... and no number of decimal places makes the bill come
	// back to 1,000.
	got, err := Compute(ComputeInput{Lines: []PricedLine{unitLine("1050.00", "2", "25", 5)}})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if !got.Lines[0].Gross.Equal(money.MustFromString("2100.00")) {
		t.Errorf("gross = %s, want exactly 2100.00", got.Lines[0].Gross)
	}
	if !got.SubTotal.Equal(money.MustFromString("2100.00")) {
		t.Errorf("sub_total = %s, want 2100.00", got.SubTotal)
	}
	if got := got.Lines[0].Quantity(); !got.Equal(money.MustQuantityFromString("50")) {
		t.Errorf("stock quantity = %s, want 50 — the sale must leave the shelf in base units", got)
	}
	assertBalances(t, got)
}

func TestAPackThatDoesNotDivideStillBillsExactly(t *testing.T) {
	// Three 3 kg packs at Rs.1,000. A per-kilo rate here is 333.333... and a
	// bill built from one is short; the multiplication is exact.
	got, err := Compute(ComputeInput{Lines: []PricedLine{unitLine("1000.00", "3", "3", 5)}})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if !got.SubTotal.Equal(money.MustFromString("3000.00")) {
		t.Errorf("sub_total = %s, want exactly 3000.00", got.SubTotal)
	}
	if q := got.Lines[0].Quantity(); !q.Equal(money.MustQuantityFromString("9")) {
		t.Errorf("stock quantity = %s, want 9", q)
	}
	assertBalances(t, got)
}

func TestASmallPackDeductsAFractionOfTheStockingUnit(t *testing.T) {
	// 250 g of rice, priced from the loose rate for a 250 g pouch.
	got, err := Compute(ComputeInput{Lines: []PricedLine{unitLine("12.00", "1", "0.25", 5)}})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if !got.SubTotal.Equal(money.MustFromString("12.00")) {
		t.Errorf("sub_total = %s, want 12.00", got.SubTotal)
	}
	if q := got.Lines[0].Quantity(); !q.Equal(money.MustQuantityFromString("0.25")) {
		t.Errorf("stock quantity = %s, want 0.25", q)
	}
	assertBalances(t, got)
}

func TestInterStateSupplyUsesIGSTAlone(t *testing.T) {
	got, err := Compute(ComputeInput{
		Lines:      []PricedLine{line("1000.00", "1", 18)},
		InterState: true,
	})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if !got.IGST.Equal(money.MustFromString("180.00")) {
		t.Errorf("igst = %s, want 180.00", got.IGST)
	}
	if !got.CGST.IsZero() || !got.SGST.IsZero() {
		t.Errorf("cgst/sgst = %s/%s on an inter-state supply, want 0", got.CGST, got.SGST)
	}
	assertBalances(t, got)
}

func TestLineDiscountReducesTheTaxableValue(t *testing.T) {
	// A discount given at the time of supply reduces the taxable value under
	// section 15(3)(a). Taxing the full amount would overcharge the customer.
	got, err := Compute(ComputeInput{
		Lines: []PricedLine{withLineDiscount(line("100.00", "1", 18), "10.00")},
	})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if !got.SubTotal.Equal(money.MustFromString("90.00")) {
		t.Errorf("sub_total = %s, want 90.00 after a 10.00 discount", got.SubTotal)
	}
	if tax := got.CGST.Add(got.SGST); !tax.Equal(money.MustFromString("16.20")) {
		t.Errorf("tax = %s, want 16.20 (18%% of 90, not of 100)", tax)
	}
	if !got.Discount.Equal(money.MustFromString("10.00")) {
		t.Errorf("discount = %s, want 10.00", got.Discount)
	}
	assertBalances(t, got)
}

func TestBillDiscountIsApportionedAndNeverLosesPaise(t *testing.T) {
	// Three lines that do not divide evenly, and a discount that cannot be
	// split into whole paise proportionally.
	got, err := Compute(ComputeInput{
		Lines: []PricedLine{
			line("33.33", "1", 5),
			line("66.67", "1", 12),
			line("100.00", "1", 18),
		},
		BillDiscount: money.MustFromString("10.00"),
	})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}

	apportioned := money.Zero()
	for _, l := range got.Lines {
		apportioned = apportioned.Add(l.ApportionedDiscount)
	}
	// Every paisa of the discount granted must land on some line. If the
	// shares did not sum exactly, the invoice would not add up.
	if !apportioned.Equal(money.MustFromString("10.00")) {
		t.Errorf("apportioned shares sum to %s, want exactly the 10.00 granted", apportioned)
	}
	if !got.Discount.Equal(money.MustFromString("10.00")) {
		t.Errorf("invoice discount = %s, want 10.00", got.Discount)
	}
	// Taxable is the pre-discount value less the apportioned share.
	if !got.SubTotal.Equal(money.MustFromString("190.00")) {
		t.Errorf("sub_total = %s, want 190.00 (200.00 less the 10.00 discount)", got.SubTotal)
	}
	assertBalances(t, got)
}

func TestBillDiscountAcrossManyAwkwardSplits(t *testing.T) {
	// A sweep, so the apportionment is not merely correct for one lucky case.
	for _, discount := range []string{"0.01", "0.07", "1.00", "3.33", "9.99", "17.77"} {
		for _, lineCount := range []int{2, 3, 5, 7} {
			lines := make([]PricedLine, lineCount)
			for i := range lines {
				lines[i] = line("13.37", "3", 12)
			}
			got, err := Compute(ComputeInput{
				Lines:        lines,
				BillDiscount: money.MustFromString(discount),
			})
			if err != nil {
				t.Fatalf("discount %s across %d lines: %v", discount, lineCount, err)
			}

			apportioned := money.Zero()
			for _, l := range got.Lines {
				apportioned = apportioned.Add(l.ApportionedDiscount)
				if l.ApportionedDiscount.IsNegative() {
					t.Errorf("discount %s across %d lines: a negative share %s", discount, lineCount, l.ApportionedDiscount)
				}
			}
			if !apportioned.Equal(money.MustFromString(discount)) {
				t.Errorf("discount %s across %d lines: shares sum to %s", discount, lineCount, apportioned)
			}
			assertBalances(t, got)
		}
	}
}

func TestRoundOffAbsorbsTheFraction(t *testing.T) {
	got, err := Compute(ComputeInput{
		Lines:      []PricedLine{line("55.00", "1", 5)},
		RoundTotal: true,
	})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	// 57.75 rounds to 58, so round_off is +0.25.
	if !got.Total.Equal(money.MustFromString("58.00")) {
		t.Errorf("total = %s, want 58.00", got.Total)
	}
	if !got.RoundOff.Equal(money.MustFromString("0.25")) {
		t.Errorf("round_off = %s, want 0.25", got.RoundOff)
	}
	assertBalances(t, got)
}

func TestImpossibleDiscountsAreRejected(t *testing.T) {
	cases := map[string]ComputeInput{
		"line discount exceeds the line": {
			Lines: []PricedLine{withLineDiscount(line("50.00", "1", 5), "60.00")},
		},
		"bill discount exceeds the bill": {
			Lines:        []PricedLine{line("50.00", "1", 5)},
			BillDiscount: money.MustFromString("60.00"),
		},
		"negative bill discount": {
			Lines:        []PricedLine{line("50.00", "1", 5)},
			BillDiscount: money.MustFromString("-5.00"),
		},
		"negative line discount": {
			Lines: []PricedLine{withLineDiscount(line("50.00", "1", 5), "-5.00")},
		},
	}
	for name, in := range cases {
		if _, err := Compute(in); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: expected ErrValidation, got %v", name, err)
		}
	}
}

func TestInvalidLinesAreRejected(t *testing.T) {
	zeroQty := line("50.00", "0", 5)
	negativeQty := line("50.00", "-1", 5)
	negativeRate := line("-50.00", "1", 5)
	badGST := line("50.00", "1", 5)
	badGST.GSTRate = money.RateFromInt(120)
	noProduct := line("50.00", "1", 5)
	noProduct.ProductID = uuid.Nil
	noUnit := line("50.00", "1", 5)
	noUnit.Unit.ID = uuid.Nil
	// A unit worth nothing of the stocking unit would price a sale and move no
	// stock at all, which is the one way a units bug can pass a bill and empty
	// nothing off the shelf.
	weightlessUnit := unitLine("50.00", "1", "0", 5)

	for name, l := range map[string]PricedLine{
		"zero quantity":     zeroQty,
		"negative quantity": negativeQty,
		"negative rate":     negativeRate,
		"impossible GST":    badGST,
		"no product":        noProduct,
		"no unit":           noUnit,
		"weightless unit":   weightlessUnit,
	} {
		if _, err := Compute(ComputeInput{Lines: []PricedLine{l}}); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: expected ErrValidation, got %v", name, err)
		}
	}

	if _, err := Compute(ComputeInput{}); !errors.Is(err, ErrValidation) {
		t.Errorf("empty invoice: expected ErrValidation, got %v", err)
	}
}

func TestMixedRatesKeepTheirOwnTax(t *testing.T) {
	// A real basket: rice at 5%, soap at 18%, an exempt item at 0%.
	got, err := Compute(ComputeInput{
		Lines: []PricedLine{
			line("450.00", "2", 5),
			line("60.00", "3", 18),
			line("25.00", "4", 0),
		},
	})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if !got.SubTotal.Equal(money.MustFromString("1180.00")) {
		t.Errorf("sub_total = %s, want 1180.00", got.SubTotal)
	}
	// 900 @ 5% = 45.00, 180 @ 18% = 32.40, 100 @ 0% = 0. Total 77.40.
	if tax := got.CGST.Add(got.SGST); !tax.Equal(money.MustFromString("77.40")) {
		t.Errorf("tax = %s, want 77.40", tax)
	}
	if !got.Lines[2].Tax.CGST.IsZero() {
		t.Errorf("an exempt line carried tax: %s", got.Lines[2].Tax.CGST)
	}
	assertBalances(t, got)
}

func TestInterStateSupplyDependsOnStateCode(t *testing.T) {
	cases := map[string]struct {
		store, customer string
		want            bool
	}{
		"same state":                 {"29ABCDE1234F1Z5", "29XYZAB5678G1Z2", false},
		"different state":            {"29ABCDE1234F1Z5", "27XYZAB5678G1Z2", true},
		"unregistered customer":      {"29ABCDE1234F1Z5", "", false},
		"unregistered store":         {"", "27XYZAB5678G1Z2", false},
		"neither registered":         {"", "", false},
		"malformed customer treated": {"29ABCDE1234F1Z5", "2", false},
	}
	for name, c := range cases {
		if got := InterStateSupply(c.store, c.customer); got != c.want {
			t.Errorf("%s: InterStateSupply(%q, %q) = %v, want %v", name, c.store, c.customer, got, c.want)
		}
	}
}

func TestPaymentModeVocabularyIsClosed(t *testing.T) {
	for _, m := range ValidPaymentModes {
		if !m.Valid() {
			t.Errorf("%q is in ValidPaymentModes but reports invalid", m)
		}
	}
	for _, m := range []PaymentMode{"", "barter", "COD", "Cash"} {
		if m.Valid() {
			t.Errorf("%q should not be a valid payment mode", m)
		}
	}
	if !PaymentCredit.OnCredit() {
		t.Error("credit sales must be recognised as creating a receivable")
	}
	if PaymentCash.OnCredit() {
		t.Error("a cash sale must not create a receivable")
	}
}
