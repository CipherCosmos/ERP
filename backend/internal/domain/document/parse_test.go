package document

import "testing"

// TestParseNumberRoundTrips: a till that bills offline allocates its own
// numbers, so the server has to be able to read one back and check it belongs to
// the terminal and year it claims.
func TestParseNumberRoundTrips(t *testing.T) {
	for _, seq := range []int64{1, 42, 99999, 100000, 1234567} {
		s := Number(PrefixInvoice, "T1", "2026-27", seq)
		prefix, terminal, year, got, err := ParseNumber(s)
		if err != nil {
			t.Fatalf("ParseNumber(%q): %v", s, err)
		}
		if prefix != PrefixInvoice || terminal != "T1" || year != "2026-27" || got != seq {
			t.Errorf("ParseNumber(%q) = %s/%s/%s/%d", s, prefix, terminal, year, got)
		}
	}
}

// TestParseNumberRefusesAnythingThisSystemWouldNotIssue is the half that
// matters. An offline till states its own number; anything the server would not
// itself have produced has to be refused, or the series stops being the series.
func TestParseNumberRefusesAnythingThisSystemWouldNotIssue(t *testing.T) {
	bad := []string{
		"",
		"INV",
		"INV/T1/2026-27",
		"INV/T1/2026-27/00001/extra",
		"INV/T1/2026-27/1",      // not padded to five places
		"INV/T1/2026-27/000001", // padded to six, but not by Number
		"INV/T1/2026-27/abc",
		"INV/T1/2026-27/00000", // a series starts at one
		"INV/T1/2026-27/-00001",
		"INV/T1/2026-27/ 00001",
		" INV/T1/2026-27/00001",
	}
	for _, s := range bad {
		if _, _, _, _, err := ParseNumber(s); err == nil {
			t.Errorf("ParseNumber(%q) was accepted", s)
		}
	}
}
