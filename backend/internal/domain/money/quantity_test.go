package money

import (
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"
)

func TestQuantityConstructors(t *testing.T) {
	tests := []struct {
		name string
		got  Quantity
		want string
	}{
		{"rice by the half kilo", MustQuantityFromString("2.5"), "2.500"},
		{"oil by the three quarter litre", MustQuantityFromString("0.75"), "0.750"},
		{"to the gram", MustQuantityFromString("1.234"), "1.234"},
		{"whole pieces", QuantityFromInt(12), "12.000"},
		{"zero", ZeroQuantity(), "0.000"},
		{"zero value struct", Quantity{}, "0.000"},
		{"negative for a stock reversal", MustQuantityFromString("-3"), "-3.000"},
		{"from decimal", QuantityFromDecimal(decimal.RequireFromString("0.5")), "0.500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.got.StringFixed(); got != tt.want {
				t.Fatalf("StringFixed() = %q, want %q", got, tt.want)
			}
		})
	}

	for _, in := range []string{"", "  ", "kg", "1.2.3"} {
		if _, err := QuantityFromString(in); err == nil {
			t.Errorf("QuantityFromString(%q) should have failed", in)
		}
	}
}

// TestQuantityStaysFractional guards the single most damaging thing that could
// happen to this type: someone deciding a quantity is really an integer. The
// shop weighs rice and pours oil.
func TestQuantityStaysFractional(t *testing.T) {
	q := MustQuantityFromString("2.5")
	if q.Decimal().IsInteger() {
		t.Fatal("2.5 kg must not be an integer")
	}
	if got := q.String(); got != "2.5" {
		t.Fatalf("String() = %q, want 2.5", got)
	}
	// A third of a kilo, to the gram, must survive.
	if got := MustQuantityFromString("0.333").StringFixed(); got != "0.333" {
		t.Fatalf("gram precision lost: %s", got)
	}
}

func TestQuantityArithmeticIsExact(t *testing.T) {
	// The canonical float failure: 0.1 + 0.2 != 0.3.
	a := MustQuantityFromString("0.1")
	b := MustQuantityFromString("0.2")
	if !a.Add(b).Equal(MustQuantityFromString("0.3")) {
		t.Fatalf("0.1 + 0.2 = %s, want exactly 0.3", a.Add(b).String())
	}

	stock := MustQuantityFromString("2.5")
	sold := MustQuantityFromString("0.75")
	if got := stock.Sub(sold).StringFixed(); got != "1.750" {
		t.Errorf("2.5 - 0.75 = %s, want 1.750", got)
	}
	if got := stock.Neg().StringFixed(); got != "-2.500" {
		t.Errorf("Neg = %s, want -2.500", got)
	}
	if got := stock.Neg().Abs().StringFixed(); got != "2.500" {
		t.Errorf("Abs = %s, want 2.500", got)
	}

	// A case pack of 12 x 0.5 L.
	if got := MustQuantityFromString("0.5").Mul(decimal.NewFromInt(12)).StringFixed(); got != "6.000" {
		t.Errorf("0.5 x 12 = %s, want 6.000", got)
	}
	// Splitting a 2.5 kg bag three ways rounds to the gram.
	if got := stock.Div(decimal.NewFromInt(3)).StringFixed(); got != "0.833" {
		t.Errorf("2.5 / 3 = %s, want 0.833", got)
	}

	// Ten sales of a tenth of a kilo empty a one-kilo bag exactly.
	remaining := QuantityFromInt(1)
	tenth := MustQuantityFromString("0.1")
	for i := 0; i < 10; i++ {
		remaining = remaining.Sub(tenth)
	}
	if !remaining.IsZero() {
		t.Fatalf("residue of %s left after ten equal deductions", remaining.String())
	}

	if got := SumQuantity(a, b, stock).StringFixed(); got != "2.800" {
		t.Errorf("SumQuantity = %s, want 2.800", got)
	}
	if !SumQuantity().IsZero() {
		t.Error("SumQuantity() should be zero")
	}
}

func TestQuantityDivByZeroPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Div by zero should panic")
		}
	}()
	_ = QuantityFromInt(1).Div(decimal.Zero)
}

func TestQuantityComparisonsAreExact(t *testing.T) {
	onHand := MustQuantityFromString("2.5")

	if !onHand.Equal(MustQuantityFromString("2.500")) {
		t.Error("2.5 should equal 2.500")
	}
	if !onHand.GreaterThanOrEqual(MustQuantityFromString("2.5")) {
		t.Error("a request for exactly what is on hand must be fulfillable")
	}
	if onHand.GreaterThanOrEqual(MustQuantityFromString("2.501")) {
		t.Error("one gram of oversell must be caught")
	}
	if !onHand.LessThan(MustQuantityFromString("2.501")) || !onHand.LessThanOrEqual(onHand) {
		t.Error("comparators disagree at one-gram resolution")
	}
	if !MustQuantityFromString("2.501").GreaterThan(onHand) {
		t.Error("GreaterThan wrong at one-gram resolution")
	}
	if onHand.Cmp(MustQuantityFromString("2.501")) != -1 || onHand.Cmp(onHand) != 0 {
		t.Error("Cmp disagrees with the comparators")
	}

	if !ZeroQuantity().IsZero() || ZeroQuantity().IsNegative() || ZeroQuantity().IsPositive() {
		t.Error("zero predicates wrong")
	}
	oversold := MustQuantityFromString("-0.001")
	if !oversold.IsNegative() || oversold.IsPositive() || oversold.IsZero() {
		t.Error("negative predicates wrong - an oversold ledger must be detectable")
	}
	if !onHand.IsPositive() {
		t.Error("2.5 should be positive")
	}
}

// TestQuantityRound exercises Round on values built through QuantityFromDecimal.
// The string constructor refuses anything finer than QuantityScale, so these
// figures can only arise the way the billing domain makes them: as the exact
// product of a unit quantity and a pack size, held unrounded so it can be asked
// whether it would survive storage.
func TestQuantityRound(t *testing.T) {
	tests := []struct{ in, want string }{
		{"1.2345", "1.235"}, // half away from zero
		{"1.2344", "1.234"},
		{"1.0005", "1.001"},
		{"-1.0005", "-1.001"},
		{"2.5", "2.500"},
	}
	for _, tt := range tests {
		exact := QuantityFromDecimal(decimal.RequireFromString(tt.in))
		if got := exact.Round().StringFixed(); got != tt.want {
			t.Errorf("Round(%s) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestQuantityScan(t *testing.T) {
	tests := []struct {
		name    string
		src     any
		want    string
		wantErr bool
	}{
		{"bytes", []byte("2.500"), "2.500", false},
		{"bytes fractional", []byte("0.75"), "0.750", false},
		{"bytes negative", []byte("-1.250"), "-1.250", false},
		{"string", "12", "12.000", false},
		{"string with spaces", "  2.5  ", "2.500", false},
		{"empty bytes", []byte(""), "0.000", false},
		{"int64", int64(50), "50.000", false},
		{"int", 3, "3.000", false},
		{"nil is zero", nil, "0.000", false},
		{"float64", 2.5, "", true},
		{"float64 residue", 0.1 + 0.2, "", true},
		{"float32", float32(0.75), "", true},
		{"decimal", decimal.RequireFromString("1.234"), "1.234", false},
		{"garbage", []byte("two point five"), "", true},
		{"unsupported type", map[string]int{}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var q Quantity
			err := q.Scan(tt.src)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Scan(%v) should have failed", tt.src)
				}
				return
			}
			if err != nil {
				t.Fatalf("Scan(%v): %v", tt.src, err)
			}
			if got := q.StringFixed(); got != tt.want {
				t.Fatalf("Scan(%v) = %s, want %s", tt.src, got, tt.want)
			}
		})
	}
}

func TestQuantityValueRoundTrip(t *testing.T) {
	for _, in := range []string{"0", "2.5", "0.75", "-1.25", "12", "0.001"} {
		original := MustQuantityFromString(in)

		v, err := original.Value()
		if err != nil {
			t.Fatalf("Value(%s): %v", in, err)
		}
		s, ok := v.(string)
		if !ok {
			t.Fatalf("Value(%s) returned %T, want string", in, v)
		}

		var fromString, fromBytes Quantity
		if err := fromString.Scan(s); err != nil {
			t.Fatalf("Scan(string): %v", err)
		}
		if err := fromBytes.Scan([]byte(s)); err != nil {
			t.Fatalf("Scan([]byte): %v", err)
		}
		if !fromString.Equal(original) || !fromBytes.Equal(original) {
			t.Fatalf("round trip of %s gave %s / %s", in, fromString, fromBytes)
		}
	}
}

func TestQuantityJSON(t *testing.T) {
	// Marshal emits a JSON number at QuantityScale, matching the frontend's
	// item.quantity.toFixed(2) call sites.
	b, err := json.Marshal(MustQuantityFromString("2.5"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "2.500" {
		t.Fatalf("Marshal = %s, want 2.500", b)
	}
	if b[0] == '"' {
		t.Fatal("Marshal emitted a JSON string; the client calls .toFixed on this field")
	}

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"number", `2.5`, "2.500", false},
		{"string", `"2.5"`, "2.500", false},
		{"number at full scale", `1.234`, "1.234", false},
		{"string at full scale", `"1.234"`, "1.234", false},
		{"integer number", `12`, "12.000", false},
		{"integer string", `"12"`, "12.000", false},
		{"negative", `-1.25`, "-1.250", false},
		{"null is zero", `null`, "0.000", false},
		{"empty string is zero", `""`, "0.000", false},
		{"garbage string", `"two"`, "", true},
		{"garbage literal", `false`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var q Quantity
			err := q.UnmarshalJSON([]byte(tt.in))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("UnmarshalJSON(%s) should have failed", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalJSON(%s): %v", tt.in, err)
			}
			if got := q.StringFixed(); got != tt.want {
				t.Fatalf("UnmarshalJSON(%s) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}

	// An inbound JSON number is parsed from its bytes, not through a float64.
	var a, b2, c Quantity
	if err := a.UnmarshalJSON([]byte(`0.1`)); err != nil {
		t.Fatal(err)
	}
	if err := b2.UnmarshalJSON([]byte(`0.2`)); err != nil {
		t.Fatal(err)
	}
	if err := c.UnmarshalJSON([]byte(`0.3`)); err != nil {
		t.Fatal(err)
	}
	if !a.Add(b2).Equal(c) {
		t.Fatalf("0.1 + 0.2 = %s, want exactly 0.3", a.Add(b2).String())
	}
}

// TestStockLedgerReconciles walks a batch through receipt, fractional sales
// and a return, and checks the ledger closes on the nose.
func TestStockLedgerReconciles(t *testing.T) {
	received := MustQuantityFromString("50")
	sales := []string{"2.5", "0.75", "1.333", "0.1", "0.2", "12", "0.125"}
	returned := MustQuantityFromString("0.75")

	onHand := received
	sold := ZeroQuantity()
	for _, s := range sales {
		q := MustQuantityFromString(s)
		if !onHand.GreaterThanOrEqual(q) {
			t.Fatalf("oversell detected at %s with %s on hand", s, onHand.StringFixed())
		}
		onHand = onHand.Sub(q)
		sold = sold.Add(q)
	}
	onHand = onHand.Add(returned)
	sold = sold.Sub(returned)

	if !onHand.Add(sold).Equal(received) {
		t.Fatalf("on hand %s + sold %s != received %s",
			onHand.StringFixed(), sold.StringFixed(), received.StringFixed())
	}
	if onHand.IsNegative() {
		t.Fatalf("on hand went negative: %s", onHand.StringFixed())
	}

	// Valuing the remaining stock at a per-unit cost derived from the batch
	// total is the COGS round trip, end to end.
	batchCost := MustFromString("4500.00")
	unitCost := batchCost.DivQuantity(received) // 90.00
	if !unitCost.Equal(MustFromString("90.00")) {
		t.Fatalf("unit cost = %s, want 90.00", unitCost.StringFixed())
	}
	if !unitCost.Mul(onHand).Add(unitCost.Mul(sold)).Equal(batchCost) {
		t.Fatalf("stock value %s + COGS %s != batch cost %s",
			unitCost.Mul(onHand).StringFixed(), unitCost.Mul(sold).StringFixed(), batchCost.StringFixed())
	}
}
