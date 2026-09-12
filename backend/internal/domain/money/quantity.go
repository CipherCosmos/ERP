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
func QuantityFromString(s string) (Quantity, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Quantity{}, fmt.Errorf("money: cannot parse empty string as a quantity")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return Quantity{}, fmt.Errorf("money: cannot parse %q as a quantity: %w", s, err)
	}
	return Quantity{d: d}, nil
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

// QuantityFromDecimal wraps a decimal.Decimal without rounding it.
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
		*q = Quantity{d: d}
		return nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return fmt.Errorf("money: cannot parse %s as a quantity: %w", s, err)
	}
	*q = Quantity{d: d}
	return nil
}
