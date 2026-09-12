package money

import "testing"

// TestShareConservesTheWhole is the property Share exists for: splitting an
// amount across parts that add up to the whole must give back the whole, with
// the remainder carrying whatever the rounding left.
//
// The figures are the pack sizes that broke the old per-unit cost model. A 30 kg
// sack at Rs.940 has no exact per-kilo price, so 10 kg and then 20 kg of it must
// still come to Rs.940.
func TestShareConservesTheWhole(t *testing.T) {
	cases := []struct {
		name  string
		total string
		whole string
		parts []string
	}{
		{"a 30 kg sack sold in two lots", "940.00", "30", []string{"10", "20"}},
		{"a 30 kg sack sold kilo by kilo", "940.00", "30", repeat("1", 30)},
		{"a 14 kg tin in 100 g pours", "1850.00", "14", repeat("0.1", 140)},
		{"a 7 kg bag in thirds", "505.00", "7", []string{"2.333", "2.333", "2.334"}},
		{"one lot takes everything", "940.00", "30", []string{"30"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remaining := MustFromString(tc.total)
			left := MustQuantityFromString(tc.whole)
			charged := Zero()
			for _, p := range tc.parts {
				part := MustQuantityFromString(p)
				// Each share comes out of what is left, as a consumption does.
				c := remaining.Share(part, left)
				charged = charged.Add(c)
				remaining = remaining.Sub(c)
				left = left.Sub(part)
			}
			if !charged.Equal(MustFromString(tc.total)) {
				t.Errorf("charged %s over the whole, want %s (remainder left: %s)",
					charged, tc.total, remaining)
			}
			if !remaining.IsZero() {
				t.Errorf("remainder = %s, want zero once the whole is consumed", remaining)
			}
		})
	}
}

// TestSharingTheLastPieceNeedsNoDivision pins that an exact part returns the
// amount untouched, which is what makes the final consumption of a batch close
// its value to zero rather than to a rounding residue.
func TestSharingTheLastPieceNeedsNoDivision(t *testing.T) {
	total := MustFromString("31.33")
	if got := total.Share(MustQuantityFromString("0.003"), MustQuantityFromString("0.003")); !got.Equal(total) {
		t.Errorf("Share(x, x) = %s, want %s", got, total)
	}
}

func TestShareOfAZeroWholePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a share of nothing must panic rather than return a number")
		}
	}()
	_ = MustFromString("1.00").Share(MustQuantityFromString("1"), ZeroQuantity())
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}
