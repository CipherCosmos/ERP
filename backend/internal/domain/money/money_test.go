package money

import (
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"
)

func TestFromStringAndConstructors(t *testing.T) {
	tests := []struct {
		name string
		got  Money
		want string
	}{
		{"from string", MustFromString("1234.50"), "1234.50"},
		{"from string negative", MustFromString("-8.05"), "-8.05"},
		{"from string sub-paise is display-rounded", MustFromString("1.2345"), "1.23"},
		{"from int", FromInt(1234), "1234.00"},
		{"from int negative", FromInt(-7), "-7.00"},
		{"from paise", FromPaise(12345), "123.45"},
		{"from paise single", FromPaise(1), "0.01"},
		{"from paise negative", FromPaise(-250), "-2.50"},
		{"zero", Zero(), "0.00"},
		{"zero value struct", Money{}, "0.00"},
		{"from decimal", FromDecimal(decimal.RequireFromString("99.9")), "99.90"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.got.StringFixed(); got != tt.want {
				t.Fatalf("StringFixed() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFromStringRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "   ", "abc", "1.2.3", "₹12"} {
		if _, err := FromString(in); err == nil {
			t.Fatalf("FromString(%q) should have failed", in)
		}
	}
}

// TestAdditionIsExact is the assertion float64 cannot make. Adding one paisa a
// hundred times must land on exactly one rupee, with nothing left over.
func TestAdditionIsExact(t *testing.T) {
	total := Zero()
	paisa := MustFromString("0.01")
	for i := 0; i < 100; i++ {
		total = total.Add(paisa)
	}
	if !total.Equal(FromInt(1)) {
		t.Fatalf("100 x 0.01 = %s, want exactly 1", total)
	}
	if total.String() != "1" {
		t.Fatalf("unnormalised residue: %q", total.String())
	}

	// The same loop in float64, for the record.
	var f float64
	for i := 0; i < 100; i++ {
		f += 0.01
	}
	if f == 1.0 {
		t.Skip("float64 addition unexpectedly exact on this platform; the decimal assertion above still holds")
	}
}

func TestArithmetic(t *testing.T) {
	a := MustFromString("100.55")
	b := MustFromString("33.30")

	if got := a.Add(b).StringFixed(); got != "133.85" {
		t.Errorf("Add = %s, want 133.85", got)
	}
	if got := a.Sub(b).StringFixed(); got != "67.25" {
		t.Errorf("Sub = %s, want 67.25", got)
	}
	if got := b.Sub(a).StringFixed(); got != "-67.25" {
		t.Errorf("Sub negative = %s, want -67.25", got)
	}
	if got := a.Neg().StringFixed(); got != "-100.55" {
		t.Errorf("Neg = %s, want -100.55", got)
	}
	if got := a.Neg().Abs().StringFixed(); got != "100.55" {
		t.Errorf("Abs = %s, want 100.55", got)
	}
	if got := Sum(a, b, FromInt(1)).StringFixed(); got != "134.85" {
		t.Errorf("Sum = %s, want 134.85", got)
	}
	if got := Sum().StringFixed(); got != "0.00" {
		t.Errorf("Sum() = %s, want 0.00", got)
	}
}

// TestRateTimesQuantity covers the invoice-line operation across fractional
// quantities: a rate is Money, a weight is Quantity, and the product is an
// amount at exactly two places.
func TestRateTimesQuantity(t *testing.T) {
	tests := []struct {
		name string
		rate string
		qty  string
		want string
	}{
		{"whole units", "45.00", "3", "135.00"},
		{"half kg", "12.50", "2.5", "31.25"},
		{"three quarter litre", "99.99", "0.75", "74.99"}, // 74.9925 -> 74.99
		{"gram precision", "7.35", "1.234", "9.07"},       // 9.0699 -> 9.07
		{"rounds half away from zero", "1.00", "2.505", "2.51"},
		{"tiny quantity vanishes", "0.99", "0.001", "0.00"}, // 0.00099 -> 0.00
		{"rice 2.5kg at 55", "55.00", "2.5", "137.50"},
		{"negative rate for a return", "-20.00", "1.5", "-30.00"},
		{"zero quantity", "45.00", "0", "0.00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MustFromString(tt.rate).Mul(MustQuantityFromString(tt.qty))
			if got.StringFixed() != tt.want {
				t.Fatalf("%s x %s = %s, want %s", tt.rate, tt.qty, got.StringFixed(), tt.want)
			}
			// Exactness: the result must equal the parsed expectation under
			// the exact comparator, not merely print the same.
			if !got.Equal(MustFromString(tt.want)) {
				t.Fatalf("Equal(%s) false for %s", tt.want, got)
			}
		})
	}
}

func TestMulDecimalAndDiv(t *testing.T) {
	amount := MustFromString("1000.00")

	if got := amount.MulDecimal(decimal.RequireFromString("0.075")).StringFixed(); got != "75.00" {
		t.Errorf("7.5%% of 1000 = %s, want 75.00", got)
	}
	if got := amount.Div(decimal.NewFromInt(3)).StringFixed(); got != "333.33" {
		t.Errorf("1000/3 = %s, want 333.33", got)
	}
	if got := amount.Div(decimal.NewFromInt(8)).StringFixed(); got != "125.00" {
		t.Errorf("1000/8 = %s, want 125.00", got)
	}
	// 1000/16 = 62.5 exactly; 1000/32 = 31.25; 1000/64 = 15.625 -> 15.63
	if got := amount.Div(decimal.NewFromInt(64)).StringFixed(); got != "15.63" {
		t.Errorf("1000/64 = %s, want 15.63 (half away from zero)", got)
	}
}

func TestDivByZeroPanics(t *testing.T) {
	t.Run("decimal divisor", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("Div by zero should panic")
			}
		}()
		_ = FromInt(1).Div(decimal.Zero)
	})
	t.Run("quantity divisor", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("DivQuantity by zero should panic")
			}
		}()
		_ = FromInt(1).DivQuantity(ZeroQuantity())
	})
}

// TestCOGSPerUnit is the cost-of-goods path: a batch total over a fractional
// received quantity gives a per-unit cost that multiplies back to the total.
func TestCOGSPerUnit(t *testing.T) {
	tests := []struct {
		name      string
		total     string
		qty       string
		wantUnit  string
		roundTrip bool // unit x qty returns to total exactly
	}{
		{"450 over 2.5 kg", "450.00", "2.5", "180.00", true},
		{"1000 over 0.75 L", "1000.00", "0.75", "1333.33", false},
		{"99 over 4 pieces", "99.00", "4", "24.75", true},
		{"250 over 1.25 kg", "250.00", "1.25", "200.00", true},
		{"100 over 3 pieces", "100.00", "3", "33.33", false},
		{"12.34 over 2 pieces", "12.34", "2", "6.17", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total := MustFromString(tt.total)
			qty := MustQuantityFromString(tt.qty)
			unit := total.DivQuantity(qty)

			if !unit.Equal(MustFromString(tt.wantUnit)) {
				t.Fatalf("unit cost = %s, want %s", unit.StringFixed(), tt.wantUnit)
			}
			back := unit.Mul(qty)
			if tt.roundTrip {
				if !back.Equal(total) {
					t.Fatalf("unit x qty = %s, want exactly %s", back.StringFixed(), tt.total)
				}
				return
			}
			// Where the unit cost is not exactly representable in paise, the
			// round trip must still land within half a paisa per unit - that
			// is the whole error a single correct rounding can introduce.
			drift := back.Sub(total).Abs()
			maxDrift := MustFromString("0.005").Mul(qty).Add(MustFromString("0.01"))
			if drift.GreaterThan(maxDrift) {
				t.Fatalf("round-trip drift %s exceeds %s", drift.StringFixed(), maxDrift.StringFixed())
			}
		})
	}
}

// TestSettlementCompletes is defect (2). A debt paid off in instalments that
// sum to it must compare as settled, exactly, with nothing left stranded.
func TestSettlementCompletes(t *testing.T) {
	amount := MustFromString("1000.00")
	payments := []Money{
		MustFromString("333.33"),
		MustFromString("333.33"),
		MustFromString("0.01"),
		MustFromString("333.33"),
	}

	paid := Zero()
	for i, p := range payments {
		paid = paid.Add(p)
		if i < len(payments)-1 && paid.GreaterThanOrEqual(amount) {
			t.Fatalf("settled early after payment %d (paid %s of %s)", i+1, paid.StringFixed(), amount.StringFixed())
		}
	}

	if !paid.Equal(amount) {
		t.Fatalf("paid = %s, want exactly %s", paid.String(), amount.String())
	}
	if !paid.GreaterThanOrEqual(amount) {
		t.Fatal("GreaterThanOrEqual reports the debt unsettled after payment in full")
	}
	if outstanding := amount.Sub(paid); !outstanding.IsZero() {
		t.Fatalf("residue of %s left on a fully paid entry", outstanding.String())
	}
	if paid.LessThan(amount) {
		t.Fatal("LessThan reports a shortfall that does not exist")
	}
}

// TestSettlementSweep runs the settlement predicate over a wide range of
// realistic (amount, instalment) pairs. Every one must settle exactly. The
// float64 arm is there so the test cannot pass vacuously: it demonstrates that
// the pairs being swept really do strand under the old representation.
func TestSettlementSweep(t *testing.T) {
	fortyPct := decimal.RequireFromString("0.40")
	thirtyFivePct := decimal.RequireFromString("0.35")

	var floatStranded int
	for paise := int64(1); paise <= 200000; paise += 7 {
		amount := FromPaise(paise)

		// A customer pays 40%, then 35%, then clears the balance.
		p1 := amount.MulDecimal(fortyPct)
		p2 := amount.MulDecimal(thirtyFivePct)
		p3 := amount.Sub(p1).Sub(p2)

		paid := Sum(p1, p2, p3)
		if !paid.Equal(amount) {
			t.Fatalf("amount %s: paid %s, want exact equality", amount.StringFixed(), paid.StringFixed())
		}
		if !paid.GreaterThanOrEqual(amount) {
			t.Fatalf("amount %s: stranded as partial after full payment", amount.StringFixed())
		}

		// Same arithmetic in float64, as credit.go does it today.
		fa := float64(paise) / 100.0
		f1 := fa * 0.40
		f2 := fa * 0.35
		f3 := fa - f1 - f2
		if f1+f2+f3 < fa {
			floatStranded++
		}
	}

	if floatStranded == 0 {
		t.Error("no float64 pair stranded; the sweep is not exercising the defect it claims to")
	} else {
		t.Logf("float64 stranded %d of the swept pairs; decimal stranded 0", floatStranded)
	}
}

func TestComparisonsAreExact(t *testing.T) {
	a := MustFromString("10.00")
	b := MustFromString("10.000")
	c := MustFromString("10.01")

	if !a.Equal(b) {
		t.Error("10.00 should equal 10.000 - trailing zeros are not significant")
	}
	if a.Equal(c) {
		t.Error("10.00 should not equal 10.01")
	}
	// One paisa apart is a real difference, not noise to be absorbed by an
	// epsilon. This is the assertion the seven math.Abs(x-y) > 0.05 checks
	// could not make.
	if !a.LessThan(c) || !c.GreaterThan(a) {
		t.Error("a one-paisa difference must be visible to LessThan/GreaterThan")
	}
	if !a.LessThanOrEqual(b) || !a.GreaterThanOrEqual(b) {
		t.Error("equal values must satisfy both inclusive comparators")
	}
	if a.Cmp(c) != -1 || c.Cmp(a) != 1 || a.Cmp(b) != 0 {
		t.Error("Cmp disagrees with the comparators")
	}

	if !Zero().IsZero() || Zero().IsNegative() || Zero().IsPositive() {
		t.Error("zero predicates wrong")
	}
	neg := MustFromString("-0.01")
	if !neg.IsNegative() || neg.IsPositive() || neg.IsZero() {
		t.Error("negative predicates wrong")
	}
	if !c.IsPositive() {
		t.Error("10.01 should be positive")
	}
}

func TestRoundHalfAwayFromZero(t *testing.T) {
	tests := []struct{ in, want string }{
		{"1.005", "1.01"}, // half up, not banker's (which would give 1.00)
		{"1.015", "1.02"},
		{"1.025", "1.03"}, // banker's would give 1.02
		{"2.675", "2.68"},
		{"-1.005", "-1.01"}, // away from zero in both directions
		{"-1.025", "-1.03"},
		{"1.004", "1.00"},
		{"1.0", "1.00"},
	}
	for _, tt := range tests {
		if got := MustFromString(tt.in).Round().StringFixed(); got != tt.want {
			t.Errorf("Round(%s) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestPaiseAndFormat(t *testing.T) {
	if got := MustFromString("1234.56").Paise(); got != 123456 {
		t.Errorf("Paise = %d, want 123456", got)
	}
	if got := MustFromString("-2.50").Paise(); got != -250 {
		t.Errorf("Paise negative = %d, want -250", got)
	}
	if got := FromPaise(99).Format(); got != "₹0.99" {
		t.Errorf("Format = %q, want ₹0.99", got)
	}
	if got := MustFromString("-12.00").Format(); got != "-₹12.00" {
		t.Errorf("Format negative = %q, want -₹12.00", got)
	}
	if got := MustFromString("1234.5").String(); got != "1234.5" {
		t.Errorf("String = %q, want 1234.5 (plain, unpadded)", got)
	}
}

// TestFromFloatRepairsLegacyResidue: the migration door rounds away the binary
// residue that stranded these entries in the first place.
func TestFromFloatRepairsLegacyResidue(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{999.9999999999999, "1000.00"},
		{0.1 + 0.2, "0.30"},
		{1234.5600000000001, "1234.56"},
		{55.0, "55.00"},
	}
	for _, tt := range tests {
		if got := FromFloat(tt.in).StringFixed(); got != tt.want {
			t.Errorf("FromFloat(%v) = %s, want %s", tt.in, got, tt.want)
		}
	}

	// The repaired value settles; the raw float does not.
	amount := FromInt(1000)
	if !FromFloat(999.9999999999999).GreaterThanOrEqual(amount) {
		t.Fatal("a legacy paid_amount of 999.9999999999999 must settle a 1000.00 debt after repair")
	}
	if 999.9999999999999 >= 1000.0 {
		t.Fatal("float64 comparison unexpectedly true; the defect premise is wrong")
	}
}

// ---------------------------------------------------------------------------
// database/sql
// ---------------------------------------------------------------------------

func TestMoneyScan(t *testing.T) {
	tests := []struct {
		name    string
		src     any
		want    string
		wantErr bool
	}{
		// The forms a Postgres NUMERIC actually arrives in.
		{"bytes", []byte("1234.50"), "1234.50", false},
		{"bytes negative", []byte("-0.05"), "-0.05", false},
		{"bytes high precision", []byte("1234.500000"), "1234.50", false},
		{"string", "999.99", "999.99", false},
		{"string with spaces", "  42.00  ", "42.00", false},
		{"empty bytes", []byte(""), "0.00", false},
		{"int64", int64(1500), "1500.00", false},
		{"int", 7, "7.00", false},
		{"nil is zero", nil, "0.00", false},
		// Legacy SQLite REAL, accepted and repaired on the way in.
		{"float64", 99.5, "99.50", false},
		{"float64 residue", 999.9999999999999, "1000.00", false},
		{"float32", float32(2.5), "2.50", false},
		{"decimal", decimal.RequireFromString("3.14"), "3.14", false},
		{"garbage bytes", []byte("not-a-number"), "", true},
		{"unsupported type", struct{}{}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Money
			err := m.Scan(tt.src)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Scan(%v) should have failed", tt.src)
				}
				return
			}
			if err != nil {
				t.Fatalf("Scan(%v): %v", tt.src, err)
			}
			if got := m.StringFixed(); got != tt.want {
				t.Fatalf("Scan(%v) = %s, want %s", tt.src, got, tt.want)
			}
		})
	}
}

func TestMoneyValueRoundTrip(t *testing.T) {
	for _, in := range []string{"0", "1234.50", "-8.05", "0.01", "99999999.99", "1234.5"} {
		original := MustFromString(in)

		v, err := original.Value()
		if err != nil {
			t.Fatalf("Value(%s): %v", in, err)
		}
		s, ok := v.(string)
		if !ok {
			t.Fatalf("Value(%s) returned %T, want string - a float64 here would defeat the whole exercise", in, v)
		}

		// Value emits at Scale so the column always receives exactly 2dp.
		if s != original.StringFixed() {
			t.Fatalf("Value(%s) = %q, want %q", in, s, original.StringFixed())
		}

		// Round-trip through both forms the driver may hand back.
		var fromString, fromBytes Money
		if err := fromString.Scan(s); err != nil {
			t.Fatalf("Scan(string): %v", err)
		}
		if err := fromBytes.Scan([]byte(s)); err != nil {
			t.Fatalf("Scan([]byte): %v", err)
		}
		if !fromString.Equal(original.Round()) || !fromBytes.Equal(original.Round()) {
			t.Fatalf("round trip of %s gave %s / %s", in, fromString, fromBytes)
		}
	}
}

// ---------------------------------------------------------------------------
// encoding/json
// ---------------------------------------------------------------------------

func TestMoneyMarshalJSONEmitsANumber(t *testing.T) {
	tests := []struct{ in, want string }{
		{"1234.50", "1234.50"},
		{"1234.5", "1234.50"},
		{"0", "0.00"},
		{"-8.05", "-8.05"},
		{"0.01", "0.01"},
	}
	for _, tt := range tests {
		b, err := json.Marshal(MustFromString(tt.in))
		if err != nil {
			t.Fatalf("Marshal(%s): %v", tt.in, err)
		}
		if string(b) != tt.want {
			t.Fatalf("Marshal(%s) = %s, want %s", tt.in, b, tt.want)
		}
		// It must be a bare JSON number, not a quoted string: 91 of the
		// frontend's 119 call sites invoke .toFixed straight off the value.
		if b[0] == '"' {
			t.Fatalf("Marshal(%s) emitted a JSON string; the client calls .toFixed on this field", tt.in)
		}
		// And it must still parse as JSON.
		var f float64
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatalf("Marshal(%s) produced invalid JSON: %v", tt.in, err)
		}
	}
}

func TestMoneyUnmarshalJSONAcceptsBothForms(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"number", `12.34`, "12.34", false},
		{"string", `"12.34"`, "12.34", false},
		{"integer number", `1000`, "1000.00", false},
		{"integer string", `"1000"`, "1000.00", false},
		{"negative number", `-8.05`, "-8.05", false},
		{"negative string", `"-8.05"`, "-8.05", false},
		{"exponent number", `1.2e3`, "1200.00", false},
		{"padded string", `" 42.5 "`, "42.50", false},
		{"null is zero", `null`, "0.00", false},
		{"empty string is zero", `""`, "0.00", false},
		{"whitespace tolerated", ` 12.34 `, "12.34", false},
		{"garbage string", `"abc"`, "", true},
		{"garbage literal", `true`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Money
			err := m.UnmarshalJSON([]byte(tt.in))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("UnmarshalJSON(%s) should have failed", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalJSON(%s): %v", tt.in, err)
			}
			if got := m.StringFixed(); got != tt.want {
				t.Fatalf("UnmarshalJSON(%s) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// TestMoneyUnmarshalJSONAvoidsFloat: an inbound JSON number is parsed from its
// literal bytes, so 0.1 is the exact decimal 0.1 and never the double that is
// really 0.1000000000000000055511151231257827.
func TestMoneyUnmarshalJSONAvoidsFloat(t *testing.T) {
	var a, b, c Money
	if err := a.UnmarshalJSON([]byte(`0.1`)); err != nil {
		t.Fatal(err)
	}
	if err := b.UnmarshalJSON([]byte(`0.2`)); err != nil {
		t.Fatal(err)
	}
	if err := c.UnmarshalJSON([]byte(`0.3`)); err != nil {
		t.Fatal(err)
	}
	if !a.Add(b).Equal(c) {
		t.Fatalf("0.1 + 0.2 = %s, want exactly 0.3", a.Add(b).String())
	}
	if a.String() != "0.1" {
		t.Fatalf("inbound 0.1 became %q; it went through a float64", a.String())
	}
}

func TestMoneyJSONRoundTripInAStruct(t *testing.T) {
	type invoice struct {
		SubTotal Money    `json:"sub_total"`
		Discount Money    `json:"discount"`
		Total    Money    `json:"total"`
		Qty      Quantity `json:"qty"`
	}

	in := invoice{
		SubTotal: MustFromString("1234.50"),
		Discount: MustFromString("-50.00"),
		Total:    MustFromString("1184.50"),
		Qty:      MustQuantityFromString("2.5"),
	}

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"sub_total":1234.50,"discount":-50.00,"total":1184.50,"qty":2.500}`
	if string(b) != want {
		t.Fatalf("marshalled to %s, want %s", b, want)
	}

	var out invoice
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !out.SubTotal.Equal(in.SubTotal) || !out.Discount.Equal(in.Discount) ||
		!out.Total.Equal(in.Total) || !out.Qty.Equal(in.Qty) {
		t.Fatalf("round trip changed the values: %+v", out)
	}

	// The client sends strings for some fields today and numbers for others;
	// both must land in the same place.
	var mixed invoice
	if err := json.Unmarshal([]byte(`{"sub_total":"1234.50","discount":-50,"total":"1184.5","qty":"2.5"}`), &mixed); err != nil {
		t.Fatal(err)
	}
	if !mixed.SubTotal.Equal(in.SubTotal) || !mixed.Total.Equal(in.Total) || !mixed.Qty.Equal(in.Qty) {
		t.Fatalf("mixed-form request did not parse equivalently: %+v", mixed)
	}
}
