package analytics_test

import (
	"testing"
	"time"

	"gbos/internal/domain/analytics"
	"gbos/internal/domain/document"
)

// TestADayStartsAtIndianMidnight.
//
// A trading day is the shop's day. Truncating in the instant's own location
// meant truncating in whatever zone the process carried — UTC in an image built
// without tzdata — so a day began at 05:30 local and the first five and a half
// hours of takings were counted against the previous day. The daily settlement
// a shop reconciles against its cash drawer then never matched the drawer.
func TestADayStartsAtIndianMidnight(t *testing.T) {
	// 00:30 IST on 12 September 2026 is 19:00 UTC on the 11th.
	earlyMorning := time.Date(2026, 9, 11, 19, 0, 0, 0, time.UTC)

	got := analytics.Day(earlyMorning)
	if want := time.Date(2026, 9, 12, 0, 0, 0, 0, document.Location); !got.Equal(want) {
		t.Errorf("00:30 IST on the 12th truncated to %s, want %s", got, want)
	}

	// And the last minute of the shop's day still belongs to that day.
	lateNight := time.Date(2026, 9, 12, 18, 29, 0, 0, time.UTC) // 23:59 IST on the 12th
	if got := analytics.Day(lateNight); got.Day() != 12 {
		t.Errorf("23:59 IST on the 12th fell into day %d", got.Day())
	}

	// The same instant read from any zone must land on the same trading day:
	// the boundary is the shop's, never the caller's.
	for _, loc := range []*time.Location{time.UTC, time.FixedZone("NY", -5*3600), document.Location} {
		if d := analytics.Day(earlyMorning.In(loc)); d.Day() != 12 {
			t.Errorf("read from %s the day became %d, want 12", loc, d.Day())
		}
	}
}
