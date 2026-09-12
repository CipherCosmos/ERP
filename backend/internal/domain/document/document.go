// Package document holds the numbering rules shared by every statutory
// document the system issues.
//
// Invoices and purchase orders both draw consecutive numbers from a per-store,
// per-terminal, per-financial-year counter. The rules live here rather than in
// each aggregate because they must not be able to diverge: two definitions of
// where the financial year starts would eventually produce two different
// series, and a series is only meaningful if it is unbroken.
package document

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Location is the timezone the shop's calendar runs on.
//
// A statutory year, like a trading day, turns at midnight where the shop is,
// not where the server is. Nothing in this system pins the process timezone,
// and a Go binary in a container built without tzdata reports time.Local as
// UTC — so a bill rung up at 00:30 on 1 April reaches the numbering code as
// 19:00 on 31 March, and every sale between midnight and 05:30 that morning
// would extend the closed year's series before the numbering jumped. The fix
// has to live with the rule rather than in a deployment variable somebody can
// forget to set.
//
// It is a fixed offset rather than a tzdata lookup, deliberately. India has
// observed UTC+05:30 with no daylight saving since 1945, so the offset IS the
// rule and a fixed zone states it exactly; a tzdata lookup would add a
// dependency on a database the runtime image does not carry, and would fail at
// startup rather than being right. Do not copy this shortcut for a country that
// changes its clocks.
var Location = time.FixedZone("IST", int((5*time.Hour + 30*time.Minute).Seconds()))

// FinancialYear returns the Indian financial year containing t, in the
// '2026-27' form that the document_sequences.fin_year CHECK requires.
//
// The year runs April to March, so January, February and March belong to the
// year that began the previous April.
//
// t is read in Location, whatever zone it arrives carrying, so the same instant
// always yields the same year. See Location for why that is not the caller's
// business to get right.
func FinancialYear(t time.Time) string {
	local := t.In(Location)
	year := local.Year()
	if local.Month() < time.April {
		year--
	}
	return fmt.Sprintf("%04d-%02d", year, (year+1)%100)
}

// Number renders an allocated sequence value as a document number, for
// example INV/T1/2026-27/00001.
//
// The terminal code and financial year appear in the number itself, not only
// in the counter table, for two reasons. A printed document has to be
// self-describing to anyone auditing it; and the uniqueness constraint is per
// store, so two terminals drawing from separate counters would otherwise both
// reach 00001 and collide.
func Number(prefix, terminalCode, finYear string, seq int64) string {
	return fmt.Sprintf("%s/%s/%s/%05d", prefix, terminalCode, finYear, seq)
}

// Prefixes for the series this system issues. CGST Rule 46(b) permits more
// than one series so long as each is consecutive and unique, which is what
// lets every terminal own its own.
const (
	PrefixInvoice       = "INV"
	PrefixPurchaseOrder = "PO"
)

// ParseNumber reads a document number back into its parts.
//
// It exists because a till that bills while the connection is down allocates its
// own numbers, and the server has to check that what arrives belongs to the
// terminal and year it claims to. Without that check a till could state any
// number at all: another terminal's series, a closed year's, or one far ahead of
// where the counter stands, and each of those quietly breaks the consecutive
// series CGST Rule 46(b) requires.
//
// A number is only well formed if it renders back to exactly what came in, so
// the check is the round trip rather than a pattern: that way the two functions
// cannot drift apart.
func ParseNumber(s string) (prefix, terminalCode, finYear string, seq int64, err error) {
	parts := strings.Split(s, "/")
	if len(parts) != 4 {
		return "", "", "", 0, fmt.Errorf("document number %q is not in the form PREFIX/TERMINAL/YEAR/SEQUENCE", s)
	}
	prefix, terminalCode, finYear = parts[0], parts[1], parts[2]
	// Every part has to be a bare token. The round trip below cannot catch a
	// stray space, because a prefix of " INV" renders back to exactly what came
	// in — the caller compares the prefix and terminal against what it expects,
	// and a space would fail that, but a parser that hands back a value it knows
	// is malformed is one comparison away from a mistake.
	for _, part := range parts[:3] {
		if part == "" || strings.TrimSpace(part) != part {
			return "", "", "", 0, fmt.Errorf("document number %q has a malformed part %q", s, part)
		}
	}
	seq, err = strconv.ParseInt(parts[3], 10, 64)
	if err != nil || seq < 1 {
		return "", "", "", 0, fmt.Errorf("document number %q does not end in a sequence number", s)
	}
	if Number(prefix, terminalCode, finYear, seq) != s {
		// Catches a sequence that is not zero-padded to five places, a stray
		// space, a leading zero count that differs — anything this system would
		// not itself have produced.
		return "", "", "", 0, fmt.Errorf("document number %q is not in the form this system issues", s)
	}
	return prefix, terminalCode, finYear, seq, nil
}
