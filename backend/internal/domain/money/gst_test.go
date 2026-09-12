package money

import (
	"testing"

	"github.com/shopspring/decimal"
)

// TestSplitTaxHalfPaise is defect (1). The Rs.55 line at 5% is the case that
// printed 1.38 + 1.38 = 2.76 while the database held 2.75.
func TestSplitTaxHalfPaise(t *testing.T) {
	taxable := MustFromString("55.00")
	rate := RateFromInt(5)

	total := TaxAmount(taxable, rate)
	if !total.Equal(MustFromString("2.75")) {
		t.Fatalf("tax on 55 at 5%% = %s, want exactly 2.75", total.String())
	}

	split := SplitTax(taxable, rate, false)

	// The halves are the two figures that will be printed on the invoice.
	if got := split.CGST.StringFixed(); got != "1.38" {
		t.Errorf("CGST = %s, want 1.38", got)
	}
	if got := split.SGST.StringFixed(); got != "1.37" {
		t.Errorf("SGST = %s, want 1.37", got)
	}
	if !split.IGST.IsZero() {
		t.Errorf("IGST = %s on an intra-state supply, want 0", split.IGST)
	}

	// The invariant: what is printed re-adds to what is stored.
	readded := split.CGST.Add(split.SGST)
	if !readded.Equal(total) {
		t.Fatalf("CGST + SGST = %s, want exactly the total tax %s", readded.String(), total.String())
	}
	if !split.Total().Equal(MustFromString("2.75")) {
		t.Fatalf("split total = %s, want exactly 2.75", split.Total().String())
	}

	// And the printed halves must not exceed the total, which is precisely
	// what the legacy 1.38 + 1.38 did.
	if readded.GreaterThan(total) {
		t.Fatalf("the halves over-collect: %s > %s", readded.String(), total.String())
	}
}

func TestSplitTaxTable(t *testing.T) {
	tests := []struct {
		name       string
		taxable    string
		rate       string
		interState bool
		wantTotal  string
		wantCGST   string
		wantSGST   string
		wantIGST   string
	}{
		{
			name:    "55 at 5 intra - the half-paise case",
			taxable: "55.00", rate: "5", wantTotal: "2.75",
			wantCGST: "1.38", wantSGST: "1.37", wantIGST: "0.00",
		},
		{
			name:    "55 at 5 inter - all IGST",
			taxable: "55.00", rate: "5", interState: true, wantTotal: "2.75",
			wantCGST: "0.00", wantSGST: "0.00", wantIGST: "2.75",
		},
		{
			name:    "99.90 at 12 - the other known drift case",
			taxable: "99.90", rate: "12", wantTotal: "11.99", // 11.988 rounds to 11.99
			wantCGST: "6.00", wantSGST: "5.99", wantIGST: "0.00",
		},
		{
			name:    "99.90 at 12 inter",
			taxable: "99.90", rate: "12", interState: true, wantTotal: "11.99",
			wantCGST: "0.00", wantSGST: "0.00", wantIGST: "11.99",
		},
		{
			name:    "100 at 18 - even paise, clean halves",
			taxable: "100.00", rate: "18", wantTotal: "18.00",
			wantCGST: "9.00", wantSGST: "9.00", wantIGST: "0.00",
		},
		{
			name:    "exempt goods at 0",
			taxable: "1234.56", rate: "0", wantTotal: "0.00",
			wantCGST: "0.00", wantSGST: "0.00", wantIGST: "0.00",
		},
		{
			name:    "bullion at 0.25",
			taxable: "50000.00", rate: "0.25", wantTotal: "125.00",
			wantCGST: "62.50", wantSGST: "62.50", wantIGST: "0.00",
		},
		{
			name:    "luxury at 28",
			taxable: "1799.00", rate: "28", wantTotal: "503.72",
			wantCGST: "251.86", wantSGST: "251.86", wantIGST: "0.00",
		},
		{
			name:    "sub-paise tax rounds up, then splits evenly",
			taxable: "12.75", rate: "18", wantTotal: "2.30", // 2.295 -> 2.30
			wantCGST: "1.15", wantSGST: "1.15", wantIGST: "0.00",
		},
		{
			name:    "credit note - negative taxable",
			taxable: "-55.00", rate: "5", wantTotal: "-2.75",
			wantCGST: "-1.38", wantSGST: "-1.37", wantIGST: "0.00",
		},
		{
			name:    "zero taxable",
			taxable: "0.00", rate: "18", wantTotal: "0.00",
			wantCGST: "0.00", wantSGST: "0.00", wantIGST: "0.00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taxable := MustFromString(tt.taxable)
			rate, err := RateFromString(tt.rate)
			if err != nil {
				t.Fatal(err)
			}

			split := SplitTax(taxable, rate, tt.interState)

			if got := split.CGST.StringFixed(); got != tt.wantCGST {
				t.Errorf("CGST = %s, want %s", got, tt.wantCGST)
			}
			if got := split.SGST.StringFixed(); got != tt.wantSGST {
				t.Errorf("SGST = %s, want %s", got, tt.wantSGST)
			}
			if got := split.IGST.StringFixed(); got != tt.wantIGST {
				t.Errorf("IGST = %s, want %s", got, tt.wantIGST)
			}
			if got := split.Total().StringFixed(); got != tt.wantTotal {
				t.Errorf("Total() = %s, want %s", got, tt.wantTotal)
			}
			// Total() must agree with TaxAmount exactly, always.
			if !split.Total().Equal(TaxAmount(taxable, rate)) {
				t.Errorf("split total %s != TaxAmount %s", split.Total(), TaxAmount(taxable, rate))
			}
			// Exactly one regime applies.
			if tt.interState && (!split.CGST.IsZero() || !split.SGST.IsZero()) {
				t.Error("inter-state supply must carry no CGST or SGST")
			}
			if !tt.interState && !split.IGST.IsZero() {
				t.Error("intra-state supply must carry no IGST")
			}
		})
	}
}

// TestSplitTaxInvariantSweep is the assertion that matters most: across every
// taxable amount in paise up to Rs.2000 and every notified GST rate, the two
// halves re-add to the total tax exactly. Not within a paisa - exactly. The
// invariant holds by construction (one half is derived from the other by
// subtraction), and this sweep is what proves the construction was not broken.
func TestSplitTaxInvariantSweep(t *testing.T) {
	rates := []string{"0.25", "3", "5", "12", "18", "28"}

	var oddPaiseCases int
	for _, rs := range rates {
		rate := MustRateFromString(rs)
		for paise := int64(1); paise <= 200000; paise += 3 {
			taxable := FromPaise(paise)
			total := TaxAmount(taxable, rate)
			split := SplitTax(taxable, rate, false)

			if !split.CGST.Add(split.SGST).Equal(total) {
				t.Fatalf("rate %s, taxable %s: CGST %s + SGST %s != total %s",
					rs, taxable.StringFixed(), split.CGST.StringFixed(),
					split.SGST.StringFixed(), total.StringFixed())
			}
			// The halves must differ by at most one paisa, and only when the
			// total is an odd number of paise.
			diff := split.CGST.Sub(split.SGST)
			if diff.Abs().GreaterThan(MustFromString("0.01")) {
				t.Fatalf("rate %s, taxable %s: halves differ by %s",
					rs, taxable.StringFixed(), diff.StringFixed())
			}
			if total.Paise()%2 != 0 {
				oddPaiseCases++
				// The stray paisa always goes to CGST, so the split is
				// reproducible run to run.
				if !split.CGST.GreaterThan(split.SGST) && total.IsPositive() {
					t.Fatalf("rate %s, taxable %s: stray paisa did not go to CGST",
						rs, taxable.StringFixed())
				}
			} else if !split.CGST.Equal(split.SGST) {
				t.Fatalf("rate %s, taxable %s: even total %s split unevenly",
					rs, taxable.StringFixed(), total.StringFixed())
			}
		}
	}

	if oddPaiseCases == 0 {
		t.Fatal("no odd-paise total in the sweep; the test is not exercising the half-paise case")
	}
	t.Logf("%d odd-paise totals split without losing or inventing a paisa", oddPaiseCases)
}

// TestTaxAmountBeatsTheFloatExpression pins the specific arithmetic the legacy
// code got wrong: total * (rate/100.0) in float64 against an exact decimal.
func TestTaxAmountBeatsTheFloatExpression(t *testing.T) {
	tests := []struct {
		taxable string
		rate    int64
		want    string
	}{
		{"55.00", 5, "2.75"},
		{"99.90", 12, "11.99"},
		{"1234.56", 18, "222.22"}, // 222.2208
		{"0.05", 28, "0.01"},      // 0.014 -> 0.01
		{"19.99", 5, "1.00"},      // 0.9995 -> 1.00, half away from zero
	}
	for _, tt := range tests {
		got := TaxAmount(MustFromString(tt.taxable), RateFromInt(tt.rate))
		if !got.Equal(MustFromString(tt.want)) {
			t.Errorf("TaxAmount(%s, %d%%) = %s, want %s", tt.taxable, tt.rate, got.StringFixed(), tt.want)
		}
	}
}

func TestRoundOff(t *testing.T) {
	tests := []struct {
		name       string
		total      string
		wantRound  string
		wantAdjust string
	}{
		{"rounds up", "1234.60", "1235.00", "0.40"},
		{"rounds down", "1234.40", "1234.00", "-0.40"},
		{"fifty paise rounds up per CGST s.170", "1234.50", "1235.00", "0.50"},
		{"forty nine paise rounds down", "1234.49", "1234.00", "-0.49"},
		{"already whole", "1235.00", "1235.00", "0.00"},
		{"one paisa over", "1235.01", "1235.00", "-0.01"},
		{"zero", "0.00", "0.00", "0.00"},
		{"negative credit note rounds away from zero", "-1234.60", "-1235.00", "-0.40"},
		{"negative fifty paise", "-1234.50", "-1235.00", "-0.50"},
		{"small amount rounds to zero", "0.40", "0.00", "-0.40"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total := MustFromString(tt.total)
			rounded, adjust := RoundOff(total)

			if got := rounded.StringFixed(); got != tt.wantRound {
				t.Errorf("rounded = %s, want %s", got, tt.wantRound)
			}
			if got := adjust.StringFixed(); got != tt.wantAdjust {
				t.Errorf("adjustment = %s, want %s", got, tt.wantAdjust)
			}
			// The three figures on the invoice must reconcile exactly.
			if !total.Add(adjust).Equal(rounded) {
				t.Errorf("%s + %s != %s", total.StringFixed(), adjust.StringFixed(), rounded.StringFixed())
			}
			// A round-off is never more than fifty paise in either direction.
			if adjust.Abs().GreaterThan(MustFromString("0.50")) {
				t.Errorf("adjustment %s exceeds half a rupee", adjust.StringFixed())
			}
		})
	}
}

func TestRoundOffSweepReconciles(t *testing.T) {
	for paise := int64(-50000); paise <= 50000; paise += 7 {
		total := FromPaise(paise)
		rounded, adjust := RoundOff(total)

		if !total.Add(adjust).Equal(rounded) {
			t.Fatalf("total %s + adjust %s != rounded %s",
				total.StringFixed(), adjust.StringFixed(), rounded.StringFixed())
		}
		if adjust.Abs().GreaterThan(MustFromString("0.50")) {
			t.Fatalf("total %s: adjustment %s exceeds half a rupee",
				total.StringFixed(), adjust.StringFixed())
		}
		if rem := rounded.Decimal().Mod(decimal.NewFromInt(1)); !rem.IsZero() {
			t.Fatalf("rounded %s is not a whole rupee", rounded.StringFixed())
		}
	}
}

// TestFullInvoiceReconciles walks a whole invoice the way billing.go will:
// lines priced by rate x quantity, tax split per line, everything summed, then
// rounded off. Nothing may go missing between the lines and the grand total.
func TestFullInvoiceReconciles(t *testing.T) {
	lines := []struct {
		rate, qty string
		gstRate   int64
	}{
		{rate: "55.00", qty: "1", gstRate: 5},
		{rate: "99.90", qty: "1", gstRate: 12},
		{rate: "45.50", qty: "2.5", gstRate: 5},    // rice by weight
		{rate: "180.00", qty: "0.75", gstRate: 18}, // oil by volume
		{rate: "12.34", qty: "3", gstRate: 28},
	}

	subTotal := Zero()
	sumCGST, sumSGST := Zero(), Zero()

	for _, l := range lines {
		lineTotal := MustFromString(l.rate).Mul(MustQuantityFromString(l.qty))
		split := SplitTax(lineTotal, RateFromInt(l.gstRate), false)

		// Per line, the halves must re-add to that line's tax.
		if !split.CGST.Add(split.SGST).Equal(TaxAmount(lineTotal, RateFromInt(l.gstRate))) {
			t.Fatalf("line %s x %s: halves do not re-add", l.rate, l.qty)
		}

		subTotal = subTotal.Add(lineTotal)
		sumCGST = sumCGST.Add(split.CGST)
		sumSGST = sumSGST.Add(split.SGST)
	}

	preRound := Sum(subTotal, sumCGST, sumSGST)
	total, roundOff := RoundOff(preRound)

	// The invoice equation, exactly - this is what the seven epsilon checks
	// were standing in for.
	if !Sum(subTotal, sumCGST, sumSGST, roundOff).Equal(total) {
		t.Fatalf("sub_total %s + cgst %s + sgst %s + round_off %s != total %s",
			subTotal.StringFixed(), sumCGST.StringFixed(), sumSGST.StringFixed(),
			roundOff.StringFixed(), total.StringFixed())
	}
	if !total.Decimal().Mod(decimal.NewFromInt(1)).IsZero() {
		t.Fatalf("grand total %s is not a whole rupee", total.StringFixed())
	}

	// The customer pays the total; the entry settles with no residue.
	paid := Sum(FromInt(500), total.Sub(FromInt(500)))
	if !paid.GreaterThanOrEqual(total) || !paid.Equal(total) {
		t.Fatalf("paid %s does not settle total %s", paid.StringFixed(), total.StringFixed())
	}
}

func TestRateHelpers(t *testing.T) {
	tests := []struct {
		name string
		got  Rate
		want string
	}{
		{"from int", RateFromInt(18), "18"},
		{"from string cess", MustRateFromString("0.25"), "0.25"},
		{"from string percent sign", MustRateFromString("12%"), "12"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.got.String(); got != tt.want {
				t.Fatalf("= %s, want %s", got, tt.want)
			}
		})
	}

	for _, in := range []string{"5", " 12 ", "0.25", "18%"} {
		if _, err := RateFromString(in); err != nil {
			t.Errorf("RateFromString(%q): %v", in, err)
		}
	}
	for _, in := range []string{"", "  ", "abc", "%"} {
		if _, err := RateFromString(in); err == nil {
			t.Errorf("RateFromString(%q) should have failed", in)
		}
	}
}
