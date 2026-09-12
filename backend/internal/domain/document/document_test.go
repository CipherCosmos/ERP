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

// A range described as "the last 30 days" begins at the start of a day. A
// rolling cut drops this morning's entries out of the range by lunchtime, so
// the same screen refreshed an hour later shows fewer rows with nothing having
// been deleted.
func TestStartOfDayIsMidnightInIndia(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   time.Time
		want string
	}{
		{
			// Late evening in India is already the next day in UTC. Truncating
			// the UTC instant would put this at the wrong midnight.
			name: "evening in India, next day in UTC",
			in:   time.Date(2026, 9, 12, 19, 45, 0, 0, document.Location),
			want: "2026-09-12T00:00:00+05:30",
		},
		{
			// The same instant arriving as UTC has to give the same answer.
			name: "the same instant expressed in UTC",
			in:   time.Date(2026, 9, 12, 14, 15, 0, 0, time.UTC),
			want: "2026-09-12T00:00:00+05:30",
		},
		{
			// India is 5.5 hours ahead, so a UTC evening is already tomorrow
			// there. A server truncating the UTC instant would file this under
			// the 12th, a day the shop has already finished.
			name: "UTC evening is already tomorrow in India",
			in:   time.Date(2026, 9, 12, 19, 30, 0, 0, time.UTC),
			want: "2026-09-13T00:00:00+05:30",
		},
		{
			name: "midnight is already the start of its own day",
			in:   time.Date(2026, 9, 12, 0, 0, 0, 0, document.Location),
			want: "2026-09-12T00:00:00+05:30",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := document.StartOfDay(tc.in).Format(time.RFC3339)
			if got != tc.want {
				t.Errorf("StartOfDay(%s) = %s, want %s", tc.in.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}
