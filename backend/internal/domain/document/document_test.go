package document_test

import (
	"testing"
	"time"

	"gbos/internal/domain/document"
)

// TestTheFinancialYearIsAnIndianOneWhereverTheServerRuns is the clock-edge case
// that decides which series a bill is numbered in.
//
// The financial year turns at midnight in India. A server has no opinion about
// that: a container built without tzdata — which is what an Alpine runtime image
// is unless somebody installs it — reports time.Local as UTC, so a sale rung up
// at one minute past midnight on 1 April arrives here as 18:31 on 31 March.
// Deriving the year from the calendar fields of whatever zone the caller's clock
// happens to carry numbers that sale into the year that has just ended.
//
// The consequence is not cosmetic. Every bill raised between midnight and 05:30
// on 1 April would extend the closed year's series, then the series would jump
// when the sun came up — so the old year's numbering would not be the unbroken
// consecutive run CGST Rule 46(b) requires, and the new year's would start
// somewhere other than 1.
func TestTheFinancialYearIsAnIndianOneWhereverTheServerRuns(t *testing.T) {
	t.Parallel()
	ist := time.FixedZone("IST", int((5*time.Hour + 30*time.Minute).Seconds()))

	for _, tc := range []struct {
		what string
		at   time.Time
		want string
	}{
		{"one minute before midnight on 31 March, in India", time.Date(2026, time.March, 31, 23, 59, 0, 0, ist), "2025-26"},
		{"one minute after midnight on 1 April, in India", time.Date(2026, time.April, 1, 0, 1, 0, 0, ist), "2026-27"},
		{"the shop opening at 07:00 on 1 April", time.Date(2026, time.April, 1, 7, 0, 0, 0, ist), "2026-27"},
		{"the middle of the year", time.Date(2026, time.November, 12, 12, 0, 0, 0, ist), "2026-27"},
		{"January, which belongs to the year that began last April", time.Date(2027, time.January, 15, 9, 0, 0, 0, ist), "2026-27"},
	} {
		if got := document.FinancialYear(tc.at); got != tc.want {
			t.Errorf("%s: FinancialYear = %q, want %q", tc.what, got, tc.want)
		}
		// The same instant expressed in UTC is the same instant, and must land
		// in the same year. This is the assertion that fails on a server whose
		// clock reads UTC.
		if got := document.FinancialYear(tc.at.UTC()); got != tc.want {
			t.Errorf("%s, read from a UTC clock: FinancialYear = %q, want %q", tc.what, got, tc.want)
		}
	}
}

// TestTheYearTurnsOnceAcrossTheBoundaryMinute walks the minutes either side of
// the turn and insists the answer changes exactly once, at exactly midnight
// India time. A boundary that moves with the server's zone would change at the
// wrong minute; one that flickers would produce two interleaved series.
func TestTheYearTurnsOnceAcrossTheBoundaryMinute(t *testing.T) {
	t.Parallel()
	ist := time.FixedZone("IST", int((5*time.Hour + 30*time.Minute).Seconds()))
	midnight := time.Date(2026, time.April, 1, 0, 0, 0, 0, ist)

	changes := 0
	previous := document.FinancialYear(midnight.Add(-6 * time.Hour))
	for offset := -6 * time.Hour; offset <= 6*time.Hour; offset += time.Minute {
		at := midnight.Add(offset)
		got := document.FinancialYear(at.UTC())
		if got != previous {
			changes++
			if !at.Equal(midnight) {
				t.Errorf("the year turned at %s, want midnight India time", at.In(ist).Format(time.RFC3339))
			}
			previous = got
		}
	}
	if changes != 1 {
		t.Errorf("the financial year changed %d times over twelve hours, want once", changes)
	}
}
