package money

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// QuantityScale is the number of decimal places at which quantities are stored.
//
// Three places, not zero. The shop sells 2.5 kg of rice and 0.75 L of oil, and
// a loose-goods counter routinely weighs to the gram, so a quantity is
// genuinely fractional and must never be collapsed to an integer. Three places
// covers grams within a kilogram and millilitres within a litre, which is the
// finest division any of the stocked units of measure is sold in. Persisted
// columns are NUMERIC(_, 3).
const QuantityScale = 3

// Quantity is an exact count of stock units - pieces, kilograms, litres,
// metres. The zero value is a valid zero quantity.
//
// Quantity carries no unit of measure. Whether a value means kilograms or
// pieces is a property of the product it belongs to, and mixing units is a
// data problem this type cannot see. What it does guarantee is that arithmetic
// on quantities is exact and that comparisons need no tolerance.
type Quantity struct {
	d decimal.Decimal
}

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// ZeroQuantity returns a zero quantity.
func ZeroQuantity() Quantity { return Quantity{} }

// QuantityFromString parses a plain decimal string such as "2.5" or "0.750".
//
// A value finer than QuantityScale is refused, not rounded. Every quantity column
// is NUMERIC(_, 3) and Value renders three places, so a fourth decimal does not
// survive the round trip: a 0.5 g pack size entered as 0.0005 was stored as
// 0.001, doubling every sale of it with no error anywhere. Refusing is the only
// answer that tells somebody, and the remedy is a finer base unit of measure —
// grams instead of kilograms — which is an INSERT, because units are rows.
func QuantityFromString(s string) (Quantity, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Quantity{}, fmt.Errorf("money: cannot parse empty string as a quantity")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return Quantity{}, fmt.Errorf("money: cannot parse %q as a quantity: %w", s, err)
	}
	return quantityWithinScale(d, s)
}

// quantityWithinScale wraps a decimal, refusing one that carries more decimal
// places than a quantity can hold. literal is what the caller wrote, so the error
// can quote it back.
func quantityWithinScale(d decimal.Decimal, literal string) (Quantity, error) {
	if d.Exponent() < -QuantityScale && !d.Equal(d.Round(QuantityScale)) {
		return Quantity{}, scaleError{literal: literal, places: int(-d.Exponent())}
	}
	return Quantity{d: d}, nil
}

// scaleError is returned when a value arrives finer than it can be stored.
//
// It carries a client message of its own because the alternative is what the HTTP
// layer does with an unrecognised binding error: flatten it to "the request could
// not be processed", which tells a shopkeeper entering a 0.5 g sachet nothing at
// all. The ClientMessage method is the whole point; httperr looks for it.
type scaleError struct {
	literal string
	places  int
}

func (e scaleError) Error() string {
	return fmt.Sprintf("money: %s has %d decimal places and a quantity holds %d",
		e.literal, e.places, QuantityScale)
}

// ClientMessage renders the problem for whoever typed the value.
func (e scaleError) ClientMessage() string {
	return fmt.Sprintf(
		"%s is finer than a quantity can be recorded to: %d decimal places, and stock is counted to %d. "+
			"Storing it would round it silently. Use a smaller unit of measure — grams rather than kilograms — "+
			"so the figure is a whole number of them.",
		e.literal, e.places, QuantityScale)
}

// MustQuantityFromString is QuantityFromString for values known good at compile
// time, such as constants and test fixtures. It panics on a malformed input.
func MustQuantityFromString(s string) Quantity {
	q, err := QuantityFromString(s)
	if err != nil {
		panic(err)
	}
	return q
}

// QuantityFromInt returns a whole-unit quantity.
func QuantityFromInt(units int64) Quantity { return Quantity{d: decimal.NewFromInt(units)} }

// QuantityFromDecimal wraps a decimal.Decimal without rounding it, and without
// the scale check the string and JSON constructors apply.
//
// It is the deliberate door for a figure computed rather than received: the exact
// product of a unit quantity and a pack size, held unrounded precisely so the
// caller can ask whether it would survive being stored. See validateLine in the
// billing domain, which is what it exists for. Do not use it for anything
// arriving from outside.
func QuantityFromDecimal(d decimal.Decimal) Quantity { return Quantity{d: d} }

// SumQuantity adds any number of quantities exactly.
func SumQuantity(quantities ...Quantity) Quantity {
	total := Quantity{}
	for _, q := range quantities {
		total = total.Add(q)
	}
	return total
}

// ---------------------------------------------------------------------------
// Arithmetic
// ---------------------------------------------------------------------------

// Add returns q + other, exactly.
func (q Quantity) Add(other Quantity) Quantity { return Quantity{d: q.d.Add(other.d)} }

// Sub returns q - other, exactly. Stock deductions run through here, so the
// result may legitimately be negative when an oversell is being detected.
func (q Quantity) Sub(other Quantity) Quantity { return Quantity{d: q.d.Sub(other.d)} }

// Neg returns -q.
func (q Quantity) Neg() Quantity { return Quantity{d: q.d.Neg()} }

// Abs returns the magnitude of q.
func (q Quantity) Abs() Quantity { return Quantity{d: q.d.Abs()} }

// Mul scales a quantity by a dimensionless factor - a pack size, a conversion
// factor between units of measure - and rounds to QuantityScale.
func (q Quantity) Mul(factor decimal.Decimal) Quantity {
	return Quantity{d: q.d.Mul(factor).Round(QuantityScale)}
}

// Div divides a quantity by a dimensionless divisor, rounding the exact
// quotient to QuantityScale in a single step. It panics if divisor is zero.
func (q Quantity) Div(divisor decimal.Decimal) Quantity {
	if divisor.IsZero() {
		panic("money: division of a quantity by zero")
	}
	return Quantity{d: q.d.DivRound(divisor, QuantityScale)}
}

// Round returns q rounded to QuantityScale, half away from zero.
func (q Quantity) Round() Quantity { return Quantity{d: q.d.Round(QuantityScale)} }

// ---------------------------------------------------------------------------
// Comparison - exact, no epsilon
// ---------------------------------------------------------------------------

// Equal reports whether the two quantities are numerically equal. Trailing
// zeros do not matter: 2.5 equals 2.500.
func (q Quantity) Equal(other Quantity) bool { return q.d.Equal(other.d) }

// Cmp returns -1, 0 or +1 as q is less than, equal to, or greater than other.
func (q Quantity) Cmp(other Quantity) int { return q.d.Cmp(other.d) }

// LessThan reports whether q < other.
func (q Quantity) LessThan(other Quantity) bool { return q.d.LessThan(other.d) }

// LessThanOrEqual reports whether q <= other.
func (q Quantity) LessThanOrEqual(other Quantity) bool { return q.d.LessThanOrEqual(other.d) }

// GreaterThan reports whether q > other.
func (q Quantity) GreaterThan(other Quantity) bool { return q.d.GreaterThan(other.d) }

// GreaterThanOrEqual reports whether q >= other. This is the stock-availability
// predicate: onHand.GreaterThanOrEqual(requested).
func (q Quantity) GreaterThanOrEqual(other Quantity) bool { return q.d.GreaterThanOrEqual(other.d) }

// IsZero reports whether q is exactly zero. Guard Money.DivQuantity with this.
func (q Quantity) IsZero() bool { return q.d.IsZero() }

// IsNegative reports whether q < 0, which for on-hand stock means the ledger
// has been oversold and needs attention.
func (q Quantity) IsNegative() bool { return q.d.IsNegative() }

// IsPositive reports whether q > 0.
func (q Quantity) IsPositive() bool { return q.d.IsPositive() }

// ---------------------------------------------------------------------------
// Conversion and display
// ---------------------------------------------------------------------------

// Decimal returns the underlying decimal.
func (q Quantity) Decimal() decimal.Decimal { return q.d }

// String returns a plain decimal string with no padding, for example "2.5".
func (q Quantity) String() string { return q.d.String() }

// StringFixed returns the quantity at exactly QuantityScale decimal places,
// for example "2.500". This is the storage and wire form.
func (q Quantity) StringFixed() string { return q.d.StringFixed(QuantityScale) }

// ---------------------------------------------------------------------------
// database/sql
// ---------------------------------------------------------------------------

// Scan implements sql.Scanner. It handles the []byte and string forms a
// Postgres NUMERIC arrives as, the int64 some drivers use for integral values,
// and NULL as zero. A float64 is refused for the reason given on Money.Scan:
// 0.1 kg three times must weigh exactly 0.3 kg.
func (q *Quantity) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*q = Quantity{}
		return nil
	case []byte:
		return q.scanString(string(v))
	case string:
		return q.scanString(v)
	case int64:
		*q = QuantityFromInt(v)
		return nil
	case int32:
		*q = QuantityFromInt(int64(v))
		return nil
	case int:
		*q = QuantityFromInt(int64(v))
		return nil
	case float64, float32:
		return fmt.Errorf("money: refusing to scan %T into Quantity: weights must not pass through binary floating point", src)
	case decimal.Decimal:
		*q = Quantity{d: v}
		return nil
	default:
		return fmt.Errorf("money: cannot scan %T into Quantity", src)
	}
}

func (q *Quantity) scanString(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		*q = Quantity{}
		return nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return fmt.Errorf("money: cannot scan %q into Quantity: %w", s, err)
	}
	*q = Quantity{d: d}
	return nil
}

// Value implements driver.Valuer, emitting the quantity as a decimal string at
// exactly QuantityScale places so the driver sends a NUMERIC literal.
func (q Quantity) Value() (driver.Value, error) { return q.StringFixed(), nil }

// ---------------------------------------------------------------------------
// encoding/json
// ---------------------------------------------------------------------------

// MarshalJSON emits the quantity as a JSON number at QuantityScale places, for
// the same measured reason Money does: the frontend calls .toFixed on these
// fields directly, as in `item.quantity.toFixed(2)`, and a string has no such
// method. See Money.MarshalJSON for the full argument.
func (q Quantity) MarshalJSON() ([]byte, error) {
	return []byte(q.StringFixed()), nil
}

// UnmarshalJSON accepts a JSON number (2.5), a JSON string ("2.500") or null,
// parsing the number from its literal bytes rather than through a float64.
func (q *Quantity) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		*q = Quantity{}
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return fmt.Errorf("money: invalid JSON string for a quantity: %w", err)
		}
		str = strings.TrimSpace(str)
		if str == "" {
			*q = Quantity{}
			return nil
		}
		d, err := decimal.NewFromString(str)
		if err != nil {
			return fmt.Errorf("money: cannot parse %q as a quantity: %w", str, err)
		}
		parsed, err := quantityWithinScale(d, str)
		if err != nil {
			return err
		}
		*q = parsed
		return nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return fmt.Errorf("money: cannot parse %s as a quantity: %w", s, err)
	}
	parsed, err := quantityWithinScale(d, s)
	if err != nil {
		return err
	}
	*q = parsed
	return nil
}
