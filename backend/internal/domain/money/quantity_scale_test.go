package money

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func decimalFromInt(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

// TestAQuantityFinerThanGramsIsRefused pins the rule that replaced a silent
// rounding.
//
// qty_in_base was numeric(14,4) while every quantity column is numeric(14,3), so
// a shop defining a 0.5 g sachet had 0.0005 accepted and 0.001 stored. Doubled,
// with no error: every sale of that pack took twice the stock and attributed
// twice the cost. A value that cannot survive the round trip has to be refused at
// the edge, because nothing downstream can tell it happened.
func TestAQuantityFinerThanGramsIsRefused(t *testing.T) {
	refused := []string{"0.0005", "0.1234", "2.5001", "-0.0005", "1.000000001"}
	for _, in := range refused {
		_, err := QuantityFromString(in)
		if err == nil {
			t.Errorf("QuantityFromString(%q) was accepted; it cannot be stored as written", in)
			continue
		}
		if !strings.Contains(err.Error(), "decimal places") {
			t.Errorf("QuantityFromString(%q): %v — the error should say why", in, err)
		}
		// And it must be able to explain itself to whoever typed it, or the HTTP
		// layer flattens it to "the request could not be processed".
		var describable interface{ ClientMessage() string }
		if !errors.As(err, &describable) {
			t.Errorf("QuantityFromString(%q) returned %T, which cannot describe itself to a client", in, err)
			continue
		}
		if !strings.Contains(describable.ClientMessage(), "unit of measure") {
			t.Errorf("the client message for %q does not say what to do about it: %s",
				in, describable.ClientMessage())
		}
	}

	// Three places or fewer, and trailing zeros beyond them, are fine: they are
	// the same number.
	accepted := []string{"0.001", "2.5", "0.750", "1", "12.345", "0.0010", "2.50000", "-3.125"}
	for _, in := range accepted {
		if _, err := QuantityFromString(in); err != nil {
			t.Errorf("QuantityFromString(%q): %v", in, err)
		}
	}
}

// TestInboundJSONRefusesTooFineAQuantity covers the path a client actually uses.
// The web form posts a number, not a string, and both forms go through the same
// check.
func TestInboundJSONRefusesTooFineAQuantity(t *testing.T) {
	for _, body := range []string{`0.0005`, `"0.0005"`} {
		var q Quantity
		if err := json.Unmarshal([]byte(body), &q); err == nil {
			t.Errorf("JSON %s was accepted as %s; it cannot be stored as written", body, q)
		}
	}
	for _, body := range []string{`0.001`, `"2.5"`, `null`, `""`, `12`} {
		var q Quantity
		if err := json.Unmarshal([]byte(body), &q); err != nil {
			t.Errorf("JSON %s: %v", body, err)
		}
	}
}

// TestArithmeticStillRounds: the refusal is for values arriving from outside.
// Multiplying and dividing quantities rounds deliberately and must keep doing so,
// or a sale of a third of a kilo would have nothing to store.
func TestArithmeticStillRounds(t *testing.T) {
	third := MustQuantityFromString("1").Div(decimalFromInt(3))
	if got := third.StringFixed(); got != "0.333" {
		t.Errorf("a third of a kilo = %s, want 0.333", got)
	}
	pouch := MustQuantityFromString("0.25")
	if got := pouch.Mul(decimalFromInt(3)).StringFixed(); got != "0.750" {
		t.Errorf("three 250 g pouches = %s, want 0.750", got)
	}
}
