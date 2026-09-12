package money

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// Rate is an exact percentage: a GST rate, a gross margin, a discount
// percentage. It is stored as the percentage itself, so five percent is 5 and
// not 0.05, which is the form every column in this schema and every figure on
// an invoice uses.
//
// # Why a type rather than a bare decimal.Decimal
//
// A rate carried as a bare decimal.Decimal serialises to JSON as a quoted
// string, because that is what shopspring's MarshalJSON does unless the
// package-level decimal.MarshalJSONWithoutQuotes flag is set. Money and
// Quantity escape that by declaring their own MarshalJSON (see
// Money.MarshalJSON for the measured argument), which is why an amount reaches
// the client as 1393.25 while a rate reached it as "5". The client treats both
// as numbers - Products.tsx renders `{p.gst_rate}%` and the profit and loss
// page calls `.toFixed(1)` on gross_margin_percent - so the quoted form was a
// TypeError at render on one screen and a stray pair of quotes on the other.
//
// The fix is this type and not the global flag. Setting
// decimal.MarshalJSONWithoutQuotes would change the wire form of every
// decimal.Decimal in the process, including ones added later by someone who
// never reads this file, whereas a named type changes exactly the fields that
// opt into it and says at the declaration what shape they take.
//
// # Why there is no fixed scale
//
// Money settles at Scale and Quantity at QuantityScale, because a paisa and a
// gram are the atoms those types are measured in. A rate has no such atom:
// products.gst_rate is NUMERIC(5,2), the statutory rates include 0.25, and the
// profit and loss margin is computed to four places. So Rate never rounds on
// construction and never pads on output - it carries exactly the value it was
// given and renders it in its shortest exact decimal form. Callers that need a
// particular scale round explicitly, as ComputeProfitAndLoss does.
type Rate struct {
	d decimal.Decimal
}

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// ZeroRate returns a rate of zero percent.
func ZeroRate() Rate { return Rate{} }

// RateFromString parses a GST rate percentage such as "5", "12.5" or "0.25".
// A trailing percent sign is accepted and ignored, so "18%" and "18" are the
// same rate.
func RateFromString(s string) (Rate, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	if s == "" {
		return Rate{}, fmt.Errorf("money: cannot parse empty string as a GST rate")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return Rate{}, fmt.Errorf("money: cannot parse %q as a GST rate: %w", s, err)
	}
	return Rate{d: d}, nil
}

// MustRateFromString is RateFromString for values known good at compile time,
// such as constants and test fixtures. It panics on a malformed input.
func MustRateFromString(s string) Rate {
	r, err := RateFromString(s)
	if err != nil {
		panic(err)
	}
	return r
}

// RateFromInt returns a whole-percent rate: 5 means five percent.
func RateFromInt(percent int64) Rate { return Rate{d: decimal.NewFromInt(percent)} }

// RateFromDecimal wraps a decimal.Decimal without rounding it. It is the door
// a rate computed elsewhere - a margin, a ratio - comes through.
func RateFromDecimal(d decimal.Decimal) Rate { return Rate{d: d} }

// ---------------------------------------------------------------------------
// Comparison - exact, no epsilon
// ---------------------------------------------------------------------------

// Equal reports whether the two rates are numerically equal. Trailing zeros do
// not matter: 5 equals 5.00.
func (r Rate) Equal(other Rate) bool { return r.d.Equal(other.d) }

// GreaterThan reports whether r > other.
func (r Rate) GreaterThan(other Rate) bool { return r.d.GreaterThan(other.d) }

// LessThan reports whether r < other.
func (r Rate) LessThan(other Rate) bool { return r.d.LessThan(other.d) }

// IsZero reports whether the rate is exactly zero - a nil-rated or exempt
// supply, and the case TaxAmount short-circuits on.
func (r Rate) IsZero() bool { return r.d.IsZero() }

// IsNegative reports whether r < 0.
func (r Rate) IsNegative() bool { return r.d.IsNegative() }

// IsPositive reports whether r > 0.
func (r Rate) IsPositive() bool { return r.d.IsPositive() }

// ---------------------------------------------------------------------------
// Conversion and display
// ---------------------------------------------------------------------------

// Decimal returns the underlying decimal, for the arithmetic this type
// deliberately does not expose.
func (r Rate) Decimal() decimal.Decimal { return r.d }

// String returns the percentage with no sign and no padding, for example "5",
// "12.5" or "25.1234".
func (r Rate) String() string { return r.d.String() }

// Round returns the rate rounded to places decimal places, half away from
// zero, for the reports that fix a presentation scale.
func (r Rate) Round(places int32) Rate { return Rate{d: r.d.Round(places)} }

// ---------------------------------------------------------------------------
// database/sql
// ---------------------------------------------------------------------------

// Scan implements sql.Scanner so a Rate can be read from a NUMERIC column.
// The accepted forms mirror Money.Scan, and a float64 is refused the same way.
// A NULL scans to zero, which is the faithful reading of a NOT NULL DEFAULT 0
// column and of "no rate stated".
func (r *Rate) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*r = Rate{}
		return nil
	case []byte:
		return r.scanString(string(v))
	case string:
		return r.scanString(v)
	case int64:
		*r = RateFromInt(v)
		return nil
	case int32:
		*r = RateFromInt(int64(v))
		return nil
	case int:
		*r = RateFromInt(int64(v))
		return nil
	case float64, float32:
		return fmt.Errorf("money: refusing to scan %T into Rate: a tax rate must not pass through binary floating point", src)
	case decimal.Decimal:
		*r = Rate{d: v}
		return nil
	default:
		return fmt.Errorf("money: cannot scan %T into Rate", src)
	}
}

func (r *Rate) scanString(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		*r = Rate{}
		return nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return fmt.Errorf("money: cannot scan %q into Rate: %w", s, err)
	}
	*r = Rate{d: d}
	return nil
}

// Value implements driver.Valuer, emitting the rate as a decimal string so the
// driver sends a NUMERIC literal rather than a float64.
func (r Rate) Value() (driver.Value, error) { return r.d.String(), nil }

// ---------------------------------------------------------------------------
// encoding/json
// ---------------------------------------------------------------------------

// MarshalJSON emits the rate as a JSON *number*, for example 5, 12.5 or
// 25.1234 - never the quoted "5" a bare decimal.Decimal would produce. See the
// type documentation for why, and Money.MarshalJSON for the full argument
// about numbers versus strings on this wire.
//
// The bytes come from decimal.String, which is exact decimal text and never
// exponent notation, so the output is always a valid JSON number.
func (r Rate) MarshalJSON() ([]byte, error) {
	return []byte(r.d.String()), nil
}

// UnmarshalJSON accepts a JSON number (5), a JSON string ("5" or "5%"), or
// null. Both forms are accepted unconditionally so that a client still sending
// the old quoted shape keeps working, and a number is parsed from its literal
// bytes rather than through a float64. An empty or null value reads as zero:
// the client sends "" for a cleared numeric input.
func (r *Rate) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		*r = Rate{}
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return fmt.Errorf("money: invalid JSON string for a rate: %w", err)
		}
		if strings.TrimSpace(str) == "" {
			*r = Rate{}
			return nil
		}
		parsed, err := RateFromString(str)
		if err != nil {
			return err
		}
		*r = parsed
		return nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return fmt.Errorf("money: cannot parse %s as a rate: %w", s, err)
	}
	*r = Rate{d: d}
	return nil
}
