package money

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// encoding/json
// ---------------------------------------------------------------------------

// TestRateMarshalJSONEmitsANumber is the whole reason Rate exists.
//
// A percentage carried as a bare decimal.Decimal reached the client quoted -
// "gst_rate":"5", "gross_margin_percent":"25" - because shopspring's
// MarshalJSON quotes unless a package-level flag says otherwise. The client
// treats both as numbers: the products screen renders {p.gst_rate}% and the
// profit and loss page calls .toFixed(1) on the margin, which a string does not
// have. This test fails against a bare decimal.Decimal.
func TestRateMarshalJSONEmitsANumber(t *testing.T) {
	tests := []struct {
		name string
		in   Rate
		want string
	}{
		{"whole percent", RateFromInt(5), "5"},
		{"zero", ZeroRate(), "0"},
		{"standard slab", RateFromInt(18), "18"},
		{"quarter percent", MustRateFromString("0.25"), "0.25"},
		{"half percent", MustRateFromString("12.5"), "12.5"},
		{"margin at four places", MustRateFromString("25.1234"), "25.1234"},
		{"negative margin", MustRateFromString("-3.75"), "-3.75"},
		// Trailing zeros carry no information in a percentage and are not
		// emitted, so 5.00 and 5 are the same number on the wire.
		{"trailing zeros trimmed", MustRateFromString("5.00"), "5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatalf("Marshal(%s): %v", tt.in, err)
			}
			if b[0] == '"' {
				t.Fatalf("Marshal(%s) emitted a JSON string %s; the client uses this field as a number",
					tt.in, b)
			}
			if string(b) != tt.want {
				t.Fatalf("Marshal(%s) = %s, want %s", tt.in, b, tt.want)
			}
			// Whatever the shape, it has to be valid JSON - an exponent form
			// such as 5e-1 would be, but a stray decimal point would not.
			var f float64
			if err := json.Unmarshal(b, &f); err != nil {
				t.Fatalf("Marshal(%s) produced invalid JSON %s: %v", tt.in, b, err)
			}
		})
	}
}

// TestRateMarshalJSONInAStruct pins the field as it appears on the wire, which
// is what a client is actually reading.
func TestRateMarshalJSONInAStruct(t *testing.T) {
	type product struct {
		Name    string `json:"name"`
		GSTRate Rate   `json:"gst_rate"`
	}
	b, err := json.Marshal(product{Name: "Rice", GSTRate: RateFromInt(5)})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"name":"Rice","gst_rate":5}`
	if string(b) != want {
		t.Fatalf("= %s, want %s", b, want)
	}
}

// TestRateUnmarshalJSONAcceptsBothForms: inbound requests are not migrated in
// lockstep with the response shape, so a client still sending "5" must keep
// working. A number is parsed from its literal bytes and never through a
// float64.
func TestRateUnmarshalJSONAcceptsBothForms(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"number", `5`, "5", false},
		{"string", `"5"`, "5", false},
		{"fractional number", `12.5`, "12.5", false},
		{"fractional string", `"12.5"`, "12.5", false},
		{"string with a percent sign", `"18%"`, "18", false},
		{"padded string", `" 0.25 "`, "0.25", false},
		{"null is zero", `null`, "0", false},
		{"empty string is zero", `""`, "0", false},
		{"garbage string", `"abc"`, "", true},
		{"garbage literal", `true`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r Rate
			err := r.UnmarshalJSON([]byte(tt.in))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("UnmarshalJSON(%s) should have failed", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalJSON(%s): %v", tt.in, err)
			}
			if got := r.String(); got != tt.want {
				t.Fatalf("UnmarshalJSON(%s) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// TestRateUnmarshalJSONAvoidsFloat: 0.25 is a notified GST rate, and it has no
// exact binary representation. Parsing from the literal bytes keeps it exact.
func TestRateUnmarshalJSONAvoidsFloat(t *testing.T) {
	var r Rate
	if err := r.UnmarshalJSON([]byte(`0.25`)); err != nil {
		t.Fatal(err)
	}
	if got := r.String(); got != "0.25" {
		t.Fatalf("inbound 0.25 became %q; it went through a float64", got)
	}
	// The tax on Rs.400 at 0.25% is exactly one rupee, with no residue.
	if got := TaxAmount(FromInt(400), r); !got.Equal(FromInt(1)) {
		t.Fatalf("TaxAmount(400, 0.25%%) = %s, want 1.00", got.StringFixed())
	}
}

// ---------------------------------------------------------------------------
// database/sql
// ---------------------------------------------------------------------------

// TestRateScanFromDriverForms covers what a NUMERIC column actually hands back:
// []byte or string from pgx, and a float64 from the legacy SQLite REAL column.
func TestRateScanFromDriverForms(t *testing.T) {
	tests := []struct {
		name string
		src  any
		want string
	}{
		{"bytes", []byte("5.00"), "5"},
		{"string", "12.50", "12.5"},
		{"int64", int64(18), "18"},
		{"float64 from a legacy REAL column", 5.000000000000001, "5"},
		{"null is zero", nil, "0"},
		{"blank is zero", "", "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r Rate
			if err := r.Scan(tt.src); err != nil {
				t.Fatalf("Scan(%v): %v", tt.src, err)
			}
			if got := r.String(); got != tt.want {
				t.Fatalf("Scan(%v) = %s, want %s", tt.src, got, tt.want)
			}
		})
	}

	var r Rate
	if err := r.Scan("not a rate"); err == nil {
		t.Error("Scan of a non-numeric string should have failed")
	}
	if err := r.Scan(struct{}{}); err == nil {
		t.Error("Scan of an unsupported type should have failed")
	}
}

// TestRateValueIsADecimalString: the driver must send a NUMERIC literal, never
// a float64, so a write followed by a read is the identity.
func TestRateValueIsADecimalString(t *testing.T) {
	v, err := MustRateFromString("0.25").Value()
	if err != nil {
		t.Fatal(err)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("Value() returned %T, want a string so the driver sends NUMERIC text", v)
	}
	if s != "0.25" {
		t.Fatalf("Value() = %q, want \"0.25\"", s)
	}
}

// ---------------------------------------------------------------------------
// Comparison
// ---------------------------------------------------------------------------

// TestRateComparisonIsExact: the statutory range check is a comparison against
// 100, and there is no tolerance in it.
func TestRateComparisonIsExact(t *testing.T) {
	hundred := RateFromInt(100)
	if MustRateFromString("100.00").GreaterThan(hundred) {
		t.Error("100.00 must not read as greater than 100 — trailing zeros are not a difference")
	}
	if !MustRateFromString("100.0001").GreaterThan(hundred) {
		t.Error("100.0001 is outside the statutory range and must read as greater than 100")
	}
	if !RateFromInt(5).Equal(MustRateFromString("5.000")) {
		t.Error("5 and 5.000 are the same rate")
	}
	if !MustRateFromString("-0.01").IsNegative() {
		t.Error("-0.01 is negative")
	}
	if !ZeroRate().IsZero() {
		t.Error("the zero value of Rate is zero percent")
	}
}
