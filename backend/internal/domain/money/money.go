// Package money provides exact decimal arithmetic for monetary amounts and
// quantities in GBOS.
//
// # Why this package exists
//
// GBOS historically represented every rupee value as a Go float64 and a SQLite
// REAL column. Binary floating point cannot represent 0.1, 0.05 or 1.375
// exactly, which produced three classes of verified defect:
//
//  1. Half-paise stranding. GST was computed as total*(rate/100) and then split
//     with cgst = tax/2, sgst = tax/2 and no rounding. On a Rs.55 line at 5%
//     each half is 1.375, which prints as 1.38 + 1.38 = 2.76 but stores as
//     2.75, drifting one paisa per line. See gst.go for the fix.
//  2. Settlement never completing. credit.go decided a debt was cleared with
//     "newPaid >= amount". Float residue leaves newPaid at 999.9999999999999
//     against an amount of 1000.0, so the entry stays "partial" forever, keeps
//     consuming the customer's credit limit and never leaves the overdue
//     report.
//  3. Epsilon comparisons. Seven "math.Abs(x-y) > 0.05" checks existed only
//     because two float computations of the same figure cannot be expected to
//     agree.
//
// Every comparison in this package is exact. There is no epsilon anywhere, and
// there must never be one: an epsilon is the symptom, not the cure.
//
// # Type safety
//
// Money and Quantity are distinct types with unexported fields, so the
// dimensionally meaningless operations are simply not expressible. There is no
// Money.Mul(Money): multiplying rupees by rupees yields rupees-squared, which
// is not a thing an invoice can hold. The meaningful crossings are:
//
//	Money.Mul(Quantity) Money         // a rate times a quantity is an amount
//	Money.DivQuantity(Quantity) Money // a total over a quantity is a unit cost
//
// A caller determined to multiply two amounts must go through Decimal()
// explicitly, which makes the intent visible in review.
//
// # Rounding policy
//
// All rounding in this package is half away from zero ("half-up" for positive
// values), never banker's rounding. This is a legal requirement, not a taste:
// section 170 of the CGST Act directs that tax and related amounts be rounded
// to the nearest rupee with "fifty paise or more" rounded upward. Banker's
// rounding would send 1234.50 down to 1234 half the time and produce invoices
// that do not agree with the statutory rule. Applying one rounding mode
// everywhere also keeps a line total, its tax and the invoice round_off
// mutually consistent.
package money

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// Scale is the number of decimal places at which money is stored and settled.
// A rupee has 100 paise, and a paisa is the atom of settlement: no Indian
// payment instrument can move a fraction of one. Persisted columns are
// NUMERIC(_, 2).
const Scale = 2

// Money is an exact rupee amount. The zero value is a valid zero amount, so a
// Money field on a struct needs no initialisation.
//
// Add, Sub and Neg are exact and never round: adding two 2dp values yields a
// 2dp value. Mul, Div and DivQuantity round their result to Scale, because
// those are the operations that can manufacture sub-paise digits and the
// result of each is an amount that will be printed on an invoice and settled
// in cash. Rounding once, at the point the sub-paise digit appears, is what
// keeps a stored figure equal to the figure the customer was shown.
type Money struct {
	d decimal.Decimal
}

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// Zero returns a zero amount.
func Zero() Money { return Money{} }

// FromString parses a plain decimal string such as "1234.50" or "-8.05".
// Exponent notation ("1.2e3") is accepted. The value is not rounded; use
// Round to force it to Scale.
func FromString(s string) (Money, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Money{}, fmt.Errorf("money: cannot parse empty string as an amount")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return Money{}, fmt.Errorf("money: cannot parse %q as an amount: %w", s, err)
	}
	return Money{d: d}, nil
}

// MustFromString is FromString for values known good at compile time, such as
// constants and test fixtures. It panics on a malformed input.
func MustFromString(s string) Money {
	m, err := FromString(s)
	if err != nil {
		panic(err)
	}
	return m
}

// FromInt returns an amount of whole rupees.
func FromInt(rupees int64) Money { return Money{d: decimal.NewFromInt(rupees)} }

// FromPaise returns an amount from a count of paise, so 12345 becomes 123.45.
func FromPaise(paise int64) Money { return Money{d: decimal.New(paise, -Scale)} }

// FromDecimal wraps a decimal.Decimal without rounding it.
func FromDecimal(d decimal.Decimal) Money { return Money{d: d} }

// Sum adds any number of amounts exactly. Summing an empty list yields zero.
func Sum(amounts ...Money) Money {
	total := Money{}
	for _, a := range amounts {
		total = total.Add(a)
	}
	return total
}

// ---------------------------------------------------------------------------
// Arithmetic
// ---------------------------------------------------------------------------

// Add returns m + other, exactly.
func (m Money) Add(other Money) Money { return Money{d: m.d.Add(other.d)} }

// Sub returns m - other, exactly.
func (m Money) Sub(other Money) Money { return Money{d: m.d.Sub(other.d)} }

// Neg returns -m.
func (m Money) Neg() Money { return Money{d: m.d.Neg()} }

// Abs returns the magnitude of m.
func (m Money) Abs() Money { return Money{d: m.d.Abs()} }

// Mul multiplies a per-unit rate by a quantity and returns the resulting
// amount, rounded to Scale. This is the invoice-line operation: rate x qty.
func (m Money) Mul(q Quantity) Money { return Money{d: m.d.Mul(q.d).Round(Scale)} }

// MulDecimal scales an amount by a dimensionless factor - a discount fraction,
// a markup multiplier, a tax rate - and rounds the result to Scale.
func (m Money) MulDecimal(factor decimal.Decimal) Money {
	return Money{d: m.d.Mul(factor).Round(Scale)}
}

// Div divides an amount by a dimensionless divisor, rounding the exact
// quotient to Scale in a single step. It panics if divisor is zero.
func (m Money) Div(divisor decimal.Decimal) Money {
	if divisor.IsZero() {
		panic("money: division of an amount by zero")
	}
	return Money{d: m.d.DivRound(divisor, Scale)}
}

// DivQuantity divides a total amount by a quantity and returns the per-unit
// amount, rounding the exact quotient to Scale in a single step. This is the
// COGS path: total batch cost over units received gives cost per unit.
//
// It panics if q is zero. A per-unit cost over zero units is undefined, and
// there is no value this function could return that would not silently corrupt
// a ledger. Callers hold Quantity.IsZero for exactly this guard.
func (m Money) DivQuantity(q Quantity) Money {
	if q.d.IsZero() {
		panic("money: division of an amount by a zero quantity")
	}
	return Money{d: m.d.DivRound(q.d, Scale)}
}

// Share returns the part of m that corresponds to part out of whole, rounding
// the exact quotient to Scale in a single step.
//
// This is the proportional-value operation: what a partial return takes out of
// what the whole line cost, or what a partial consumption takes out of a batch.
// Going via a unit cost instead — m over whole, then times part — rounds twice
// and is what loses paise on pack sizes that do not divide evenly.
//
// A part equal to whole returns m exactly, with no division at all, so the last
// piece of something always carries the rest of its value.
//
// It panics if whole is zero. A share of nothing is not a number a ledger can
// hold, and callers validate the quantity first.
func (m Money) Share(part, whole Quantity) Money {
	if whole.d.IsZero() {
		panic("money: share of a zero whole")
	}
	if part.d.Equal(whole.d) {
		return m
	}
	return Money{d: m.d.Mul(part.d).DivRound(whole.d, Scale)}
}

// Round returns m rounded to Scale, half away from zero. See the package
// documentation for why that mode and not banker's rounding.
func (m Money) Round() Money { return Money{d: m.d.Round(Scale)} }

// ---------------------------------------------------------------------------
// Comparison - exact, no epsilon
// ---------------------------------------------------------------------------

// Equal reports whether the two amounts are numerically equal. Trailing zeros
// do not matter: 1.5 equals 1.50.
func (m Money) Equal(other Money) bool { return m.d.Equal(other.d) }

// Cmp returns -1, 0 or +1 as m is less than, equal to, or greater than other.
func (m Money) Cmp(other Money) int { return m.d.Cmp(other.d) }

// LessThan reports whether m < other.
func (m Money) LessThan(other Money) bool { return m.d.LessThan(other.d) }

// LessThanOrEqual reports whether m <= other.
func (m Money) LessThanOrEqual(other Money) bool { return m.d.LessThanOrEqual(other.d) }

// GreaterThan reports whether m > other.
func (m Money) GreaterThan(other Money) bool { return m.d.GreaterThan(other.d) }

// GreaterThanOrEqual reports whether m >= other. This is the settlement
// predicate: paid.GreaterThanOrEqual(amount) is true the moment the debt is
// actually cleared, with no float residue to strand it one ten-trillionth of a
// rupee short.
func (m Money) GreaterThanOrEqual(other Money) bool { return m.d.GreaterThanOrEqual(other.d) }

// IsZero reports whether m is exactly zero.
func (m Money) IsZero() bool { return m.d.IsZero() }

// IsNegative reports whether m < 0.
func (m Money) IsNegative() bool { return m.d.IsNegative() }

// IsPositive reports whether m > 0.
func (m Money) IsPositive() bool { return m.d.IsPositive() }

// ---------------------------------------------------------------------------
// Conversion and display
// ---------------------------------------------------------------------------

// Decimal returns the underlying decimal. Use it to reach arithmetic this type
// deliberately does not expose; the explicitness is the point.
func (m Money) Decimal() decimal.Decimal { return m.d }

// String returns a plain decimal string with no currency symbol and no padding,
// for example "1234.5". Use StringFixed when a stable 2dp form is wanted.
func (m Money) String() string { return m.d.String() }

// StringFixed returns the amount at exactly Scale decimal places, for example
// "1234.50". This is the storage and wire form.
func (m Money) StringFixed() string { return m.d.StringFixed(Scale) }

// Paise returns the amount as a whole number of paise, rounding to Scale first.
func (m Money) Paise() int64 { return m.d.Round(Scale).Shift(Scale).IntPart() }

// Format renders the amount for display with a rupee sign, for example
// "₹1234.50". Negative amounts render as "-₹12.00".
func (m Money) Format() string {
	if m.d.IsNegative() {
		return "-₹" + m.d.Neg().StringFixed(Scale)
	}
	return "₹" + m.d.StringFixed(Scale)
}

// ---------------------------------------------------------------------------
// database/sql
// ---------------------------------------------------------------------------

// Scan implements sql.Scanner so a Money can be read from a NUMERIC column.
//
// database/sql hands a Postgres NUMERIC to Go as []byte or string depending on
// the driver, so both are the normal path. int64 turns up for integral values
// on some drivers.
//
// A float64 is refused, not rounded. Every money column in this schema is
// NUMERIC and pgx hands those over as text, so a float64 arriving here means
// something upstream routed a rupee value through binary floating point. That
// is the class of defect this type exists to prevent, and it is worth an error
// the moment it appears rather than a quietly repaired total.
//
// A NULL column scans to zero. Every money column in this schema is
// NOT NULL DEFAULT 0 or semantically "no amount", so zero is the faithful
// reading; a caller needing to distinguish NULL from 0.00 should scan into a
// *string and construct explicitly.
func (m *Money) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*m = Money{}
		return nil
	case []byte:
		return m.scanString(string(v))
	case string:
		return m.scanString(v)
	case int64:
		*m = FromInt(v)
		return nil
	case int32:
		*m = FromInt(int64(v))
		return nil
	case int:
		*m = FromInt(int64(v))
		return nil
	case float64, float32:
		return fmt.Errorf("money: refusing to scan %T into Money: rupee values must not pass through binary floating point", src)
	case decimal.Decimal:
		*m = Money{d: v}
		return nil
	default:
		return fmt.Errorf("money: cannot scan %T into Money", src)
	}
}

func (m *Money) scanString(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		*m = Money{}
		return nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return fmt.Errorf("money: cannot scan %q into Money: %w", s, err)
	}
	*m = Money{d: d}
	return nil
}

// Value implements driver.Valuer. It emits the amount as a decimal string at
// exactly Scale places, never as a float64, so the driver sends the database a
// NUMERIC literal and no binary rounding can occur in transit. Emitting at
// Scale also guarantees the stored value equals what Round would produce, so a
// write followed by a read is the identity.
func (m Money) Value() (driver.Value, error) { return m.StringFixed(), nil }

// ---------------------------------------------------------------------------
// encoding/json
// ---------------------------------------------------------------------------

// MarshalJSON emits the amount as a JSON *number* with exactly Scale decimal
// places, for example 1234.50.
//
// This is a deliberate choice against the more theoretically correct JSON
// string, and it was made by measuring the client rather than by taste. The
// React frontend calls .toFixed(2) on money fields in 119 places, and only 28
// of those wrap the value in Number() first; the other 91 call the method
// straight off the field, as in `r.amount?.toFixed(2)` and
// `item.cost_price.toFixed(2)`. Strings have no toFixed method, so shipping
// `"1234.50"` would not degrade those 91 sites, it would throw a TypeError at
// render, and the TypeScript declarations would stop compiling besides.
//
// The cost of a JSON number is that the value passes through a float64 in the
// browser. That cost is bounded and is not where the defects live:
//
//   - Go never touches a float here. The bytes come from StringFixed, which is
//     exact decimal text; there is no float64 anywhere in this method.
//   - Decimal text at 2dp survives the round trip through a double and back
//     out through toFixed(2) exactly, for every magnitude an Indian retail
//     invoice can reach. The nearest double to 1234.50 is off by ~1e-13, and
//     rounding to 2dp recovers the original.
//   - All three defects this package exists to kill are server-side arithmetic
//     defects. None of them is caused by the wire representation.
//
// UnmarshalJSON accepts a JSON string as readily as a number, so migrating the
// client to strings later needs no coordinated flag day: fix the client call
// sites, then change this one method.
func (m Money) MarshalJSON() ([]byte, error) {
	return []byte(m.StringFixed()), nil
}

// UnmarshalJSON accepts a JSON number (1234.5), a JSON string ("1234.50") or
// null. Accepting both forms unconditionally is what lets the wire format
// above be revisited without breaking inbound requests from the existing
// client, and it costs nothing.
//
// A JSON number is parsed from its literal bytes, never via float64, so an
// inbound 0.1 is the exact decimal 0.1 and not 0.1000000000000000055511151231.
// An empty or null value is read as zero: the client sends "" for a cleared
// numeric input, and a cleared amount field means no amount.
func (m *Money) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		*m = Money{}
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return fmt.Errorf("money: invalid JSON string for an amount: %w", err)
		}
		str = strings.TrimSpace(str)
		if str == "" {
			*m = Money{}
			return nil
		}
		d, err := decimal.NewFromString(str)
		if err != nil {
			return fmt.Errorf("money: cannot parse %q as an amount: %w", str, err)
		}
		*m = Money{d: d}
		return nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return fmt.Errorf("money: cannot parse %s as an amount: %w", s, err)
	}
	*m = Money{d: d}
	return nil
}
