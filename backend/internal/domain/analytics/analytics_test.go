package analytics_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gbos/internal/domain/analytics"
	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

func rupees(s string) money.Money { return money.MustFromString(s) }

// --- the IGST fix ------------------------------------------------------------

func TestTaxableRevenueSubtractsIGSTAsWellAsCGSTAndSGST(t *testing.T) {
	t.Parallel()

	// One inter-state supply: Rs.10,000 taxable at 18% IGST, Rs.11,800 paid.
	interState := analytics.SalesTotals{
		SubTotal:     rupees("10000.00"),
		IGST:         rupees("1800.00"),
		Total:        rupees("11800.00"),
		InvoiceCount: 1,
	}
	if got := interState.TaxableRevenue(); !got.Equal(rupees("10000.00")) {
		// The report this replaces computed total - cgst - sgst and never
		// touched IGST, so this figure came out at the full 11,800 and the
		// whole Rs.1,800 of tax was booked as gross profit.
		t.Errorf("inter-state taxable revenue = %s, want 10000.00", got)
	}

	// The same supply within one state carries CGST + SGST instead.
	intraState := analytics.SalesTotals{
		SubTotal:     rupees("10000.00"),
		CGST:         rupees("900.00"),
		SGST:         rupees("900.00"),
		Total:        rupees("11800.00"),
		InvoiceCount: 1,
	}
	if got := intraState.TaxableRevenue(); !got.Equal(rupees("10000.00")) {
		t.Errorf("intra-state taxable revenue = %s, want 10000.00", got)
	}
	if !interState.TaxableRevenue().Equal(intraState.TaxableRevenue()) {
		t.Error("the same supply must yield the same taxable revenue either side of a state line")
	}
}

func TestProfitOnAnInterStateSaleExcludesTheTax(t *testing.T) {
	t.Parallel()

	pl := analytics.ComputeProfitAndLoss(analytics.ProfitInput{
		Range: analytics.SingleDay(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)),
		Sales: analytics.SalesTotals{
			SubTotal:     rupees("10000.00"),
			IGST:         rupees("1800.00"),
			Total:        rupees("11800.00"),
			InvoiceCount: 1,
		},
		COGS:     rupees("7500.00"),
		Returns:  money.Zero(),
		Losses:   money.Zero(),
		Expenses: rupees("500.00"),
	})

	if !pl.TaxableRevenue.Equal(rupees("10000.00")) {
		t.Errorf("taxable revenue = %s, want 10000.00", pl.TaxableRevenue)
	}
	if !pl.GrossProfit.Equal(rupees("2500.00")) {
		t.Errorf("gross profit = %s, want 2500.00 — the IGST is not profit", pl.GrossProfit)
	}
	if !pl.GrossMargin.Equal(money.RateFromInt(25)) {
		t.Errorf("gross margin = %s%%, want 25%%", pl.GrossMargin)
	}
	if !pl.NetProfit.Equal(rupees("2000.00")) {
		t.Errorf("net profit = %s, want 2000.00", pl.NetProfit)
	}
}

// TestProfitAndLossMarginIsAJSONNumber pins the wire shape of
// gross_margin_percent.
//
// It was a bare decimal.Decimal, which shopspring marshals as a quoted string,
// so the report went out as "gross_margin_percent":"25". The profit and loss
// page calls .toFixed(1) on that field and a string has no such method, so the
// page threw at render rather than displaying a slightly wrong figure. Only
// the margin is affected: every money field on the report was already a number,
// and this test checks both halves so a future change cannot fix one by
// breaking the other.
func TestProfitAndLossMarginIsAJSONNumber(t *testing.T) {
	t.Parallel()

	pl := analytics.ComputeProfitAndLoss(analytics.ProfitInput{
		Range: analytics.SingleDay(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)),
		Sales: analytics.SalesTotals{
			SubTotal:     rupees("10000.00"),
			IGST:         rupees("1800.00"),
			Total:        rupees("11800.00"),
			InvoiceCount: 1,
		},
		COGS: rupees("7500.00"),
	})

	raw, err := json.Marshal(pl)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"gross_margin_percent":25`) {
		t.Errorf("gross_margin_percent is not an unquoted number: %s", raw)
	}
	if strings.Contains(string(raw), `"gross_margin_percent":"`) {
		t.Errorf("gross_margin_percent went out as a quoted string; the client calls .toFixed(1) on it: %s", raw)
	}
	// The money fields on the same report must be unaffected.
	for _, want := range []string{`"total_revenue":11800.00`, `"gross_profit":2500.00`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("expected %s in the report: %s", want, raw)
		}
	}

	// And the whole document has to be JSON a client can read back, with the
	// margin arriving as a number rather than a string.
	var decoded struct {
		GrossMargin json.Number `json:"gross_margin_percent"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.GrossMargin.String() != "25" {
		t.Errorf("gross_margin_percent decoded as %s, want 25", decoded.GrossMargin)
	}
}

func TestCOGSIsRoundedOnceWhenItBecomesAFigure(t *testing.T) {
	t.Parallel()

	// unit_cost_at_sale is NUMERIC(14,4) and quantity NUMERIC(14,3), so their
	// product can carry seven decimal places out of the database.
	pl := analytics.ComputeProfitAndLoss(analytics.ProfitInput{
		Sales: analytics.SalesTotals{Total: rupees("1000.00"), InvoiceCount: 1},
		COGS:  money.MustFromString("333.3335"),
	})
	if !pl.TotalCOGS.Equal(rupees("333.33")) {
		t.Errorf("cost of goods sold = %s, want 333.33", pl.TotalCOGS)
	}
	if !pl.GrossProfit.Equal(rupees("666.67")) {
		t.Errorf("gross profit = %s, want 666.67", pl.GrossProfit)
	}
}

// --- average bill ------------------------------------------------------------

func TestAverageBillIsRevenueOverInvoiceCount(t *testing.T) {
	t.Parallel()

	totals := analytics.SalesTotals{Total: rupees("1299.00"), InvoiceCount: 2}
	if got := totals.AverageBill(); !got.Equal(rupees("649.50")) {
		t.Errorf("average bill = %s, want 649.50", got)
	}

	empty := analytics.SalesTotals{Total: money.Zero(), InvoiceCount: 0}
	if got := empty.AverageBill(); !got.IsZero() {
		t.Errorf("average bill over no invoices = %s, want 0", got)
	}
}

// --- the credit aging ladder -------------------------------------------------

func TestAgingBucketsSumToTheTotalDue(t *testing.T) {
	t.Parallel()

	row := analytics.CustomerAging{
		TotalDue: rupees("3307.50"),
		Current:  rupees("945.00"),
		Days30:   rupees("945.00"),
		Days60:   rupees("945.00"),
		Over90:   rupees("472.50"),
	}
	if got := row.BucketTotal(); !got.Equal(row.TotalDue) {
		t.Errorf("buckets sum to %s but the total due is %s", got, row.TotalDue)
	}
}

func TestTheAgingReportHasNoAlwaysZeroBucket(t *testing.T) {
	t.Parallel()

	// days_90 was declared on the struct, never produced by the SQL and never
	// read by the Scan, so it reported zero to every caller for the life of the
	// report. It is gone rather than filled in: a fifth bucket would have to be
	// carved out of over_90, leaving a field named "over 90 days" that no
	// longer meant it. A field a reader believes and that is always zero is
	// worse than no field.
	raw, err := json.Marshal(analytics.CustomerAging{
		TotalDue: rupees("100.00"),
		Current:  rupees("100.00"),
		Days30:   money.Zero(),
		Days60:   money.Zero(),
		Over90:   money.Zero(),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "days_90") {
		t.Errorf("the aging row still carries a days_90 field: %s", raw)
	}
	for _, want := range []string{"current", "days_30", "days_60", "over_90", "total_due"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the aging row is missing %q: %s", want, raw)
		}
	}
}

// --- ranges ------------------------------------------------------------------

func TestPeriodRangesAreMeasuredBackFromToday(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	cases := map[analytics.Period][2]string{
		analytics.PeriodToday: {"2026-06-15", "2026-06-15"},
		analytics.PeriodWeek:  {"2026-06-08", "2026-06-15"},
		analytics.PeriodMonth: {"2026-05-15", "2026-06-15"},
		analytics.PeriodYear:  {"2025-06-15", "2026-06-15"},
	}
	for period, want := range cases {
		got := period.Range(now)
		if got.StartString() != want[0] || got.EndString() != want[1] {
			t.Errorf("%s = %s..%s, want %s..%s", period,
				got.StartString(), got.EndString(), want[0], want[1])
		}
	}
}

func TestAnUnknownPeriodIsRefusedRatherThanTreatedAsToday(t *testing.T) {
	t.Parallel()

	// The handler this replaces defaulted anything it did not recognise to
	// today, so a client asking for a quarter was shown one day's takings under
	// a heading that said quarter.
	if _, err := analytics.ParsePeriod("quarter"); !errors.Is(err, analytics.ErrValidation) {
		t.Errorf("ParsePeriod(\"quarter\") = %v, want a validation error", err)
	}
	got, err := analytics.ParsePeriod("")
	if err != nil || got != analytics.PeriodToday {
		t.Errorf("ParsePeriod(\"\") = %q, %v; want today with no error", got, err)
	}
}

func TestRangeRefusesAnEndBeforeItsStart(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if _, err := analytics.NewRange(from, from.AddDate(0, 0, -1)); !errors.Is(err, analytics.ErrValidation) {
		t.Errorf("a backwards range should be refused, got %v", err)
	}
	r, err := analytics.NewRange(from, from)
	if err != nil {
		t.Fatalf("single day range: %v", err)
	}
	if r.Days() != 1 {
		t.Errorf("a one day range covers %d days, want 1", r.Days())
	}
}

func TestRangeMarshalsAsPlainDates(t *testing.T) {
	t.Parallel()

	r := analytics.Range{
		From: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"start":"2026-04-01","end":"2026-04-30"}`; string(raw) != want {
		t.Errorf("range marshalled as %s, want %s", raw, want)
	}
}

func TestMonthIsAHalfOpenRange(t *testing.T) {
	t.Parallel()

	from, to, err := analytics.ParseMonth("2026-02")
	if err != nil {
		t.Fatalf("ParseMonth: %v", err)
	}
	if from.Format(analytics.DateLayout) != "2026-02-01" {
		t.Errorf("month starts %s, want 2026-02-01", from.Format(analytics.DateLayout))
	}
	// The end is the first of the next month and the query uses `< to`, which
	// is the only form that neither drops the last day nor double counts the
	// first of the next.
	if to.Format(analytics.DateLayout) != "2026-03-01" {
		t.Errorf("month ends before %s, want 2026-03-01", to.Format(analytics.DateLayout))
	}
	if _, _, err := analytics.ParseMonth("Feb 2026"); !errors.Is(err, analytics.ErrValidation) {
		t.Errorf("a malformed month should be refused, got %v", err)
	}
}

// --- rollup ------------------------------------------------------------------

func TestSummariseStoresTotalsEveryShopIncludingTheOnesThatSoldNothing(t *testing.T) {
	t.Parallel()

	rollup := analytics.SummariseStores(
		analytics.SingleDay(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)),
		[]analytics.StoreSales{
			{StoreName: "Main", Revenue: rupees("1000.00"), GST: rupees("50.00"), InvoiceCount: 3},
			{StoreName: "Quiet", Revenue: money.Zero(), GST: money.Zero(), InvoiceCount: 0},
		})

	if !rollup.Totals.Revenue.Equal(rupees("1000.00")) {
		t.Errorf("group revenue = %s, want 1000.00", rollup.Totals.Revenue)
	}
	if rollup.Totals.InvoiceCount != 3 {
		t.Errorf("group invoice count = %d, want 3", rollup.Totals.InvoiceCount)
	}
	if len(rollup.Stores) != 2 {
		t.Errorf("the shop that sold nothing was dropped from the rollup")
	}
}

func TestGSTR3BNetsTheTwoSides(t *testing.T) {
	t.Parallel()

	out := analytics.TaxSummary{Taxable: rupees("10000.00"), CGST: rupees("250.00"), SGST: rupees("250.00")}
	in := analytics.TaxSummary{Taxable: rupees("6000.00"), CGST: rupees("150.00"), SGST: rupees("150.00")}

	got := analytics.NewGSTR3B("2026-04", uuid.New(), out, in)
	if !got.NetTaxPayable.Equal(rupees("200.00")) {
		t.Errorf("net tax payable = %s, want 200.00", got.NetTaxPayable)
	}
	if got.Note == "" {
		t.Error("the summary must carry its own caveat")
	}
}
