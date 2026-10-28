// Package analytics holds the read models the business reports on, and the
// arithmetic that turns raw aggregates into the figures a shopkeeper acts on.
//
// # Why the arithmetic lives here and not in a handler
//
// Every figure in this package used to be computed inline in an HTTP handler,
// immediately after the query that produced its inputs. Two of those
// computations were wrong for years and nothing could have caught them:
//
//   - Gross profit subtracted CGST and SGST from revenue but never IGST, so
//     every inter-state sale overstated profit by the whole of its tax. On an
//     Rs.11,800 invoice that is Rs.1,800 of profit that does not exist.
//   - The average bill value divided revenue by an invoice count that included
//     voided invoices, so voiding a sale moved the average in the wrong
//     direction.
//
// Both are pure functions of a handful of amounts. Having them here means they
// can be exercised without a database, and means there is exactly one place
// where "taxable revenue" is defined.
//
// # What a report may count
//
// A voided invoice is not revenue. It is not a sale, it is not tax collected,
// it is not a line in the top-products table, and it is not part of the average
// bill. The queries that feed these types are responsible for the status
// filter; the types here are responsible for not inventing a second definition
// of the same figure.
package analytics

import (
	"encoding/json"
	"errors"
	"fmt"
	"gbos/internal/domain/document"
	"time"

	"gbos/internal/domain/money"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ErrValidation is the sentinel for a rejected value.
var ErrValidation = errors.New("validation failed")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, args...)...)
}

// DateLayout is the form every business date takes on the wire and in a query
// parameter.
const DateLayout = "2006-01-02"

// ---------------------------------------------------------------------------
// Date ranges
// ---------------------------------------------------------------------------

// Range is an inclusive span of business dates. Both ends are days, never
// instants: invoices.invoice_date is a DATE column and a report that asks for
// "this month" means whole days at both ends.
type Range struct {
	From time.Time
	To   time.Time
}

// NewRange returns the inclusive range from..to, normalised to whole days.
func NewRange(from, to time.Time) (Range, error) {
	if from.IsZero() || to.IsZero() {
		return Range{}, invalid("a report needs both a start and an end date")
	}
	r := Range{From: Day(from), To: Day(to)}
	if r.To.Before(r.From) {
		return Range{}, invalid("end date %s is before start date %s",
			r.To.Format(DateLayout), r.From.Format(DateLayout))
	}
	return r, nil
}

// SingleDay returns the range covering one day.
func SingleDay(day time.Time) Range {
	d := Day(day)
	return Range{From: d, To: d}
}

// Day truncates an instant to the start of its calendar day in the shop's zone.
//
// Not in the instant's own location, which is whatever zone the caller happened
// to carry — usually the process default, and a Go binary in an image built
// without tzdata reports that as UTC. A trading day would then start at 05:30
// in India, so the first five and a half hours of every day's takings landed in
// the day before, and the daily settlement a shop reconciles against its cash
// drawer never matched the drawer.
func Day(t time.Time) time.Time {
	local := t.In(document.Location)
	y, m, d := local.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, document.Location)
}

// Days is how many days the range covers, counting both ends.
func (r Range) Days() int { return int(r.To.Sub(r.From)/(24*time.Hour)) + 1 }

// StartString and EndString render the ends as plain dates.
func (r Range) StartString() string { return r.From.Format(DateLayout) }
func (r Range) EndString() string   { return r.To.Format(DateLayout) }

func (r Range) String() string { return r.StartString() + ".." + r.EndString() }

// MarshalJSON renders the range as {"start":"2026-04-01","end":"2026-04-30"}.
// Plain dates, not RFC 3339 instants: the range has no time-of-day and
// pretending otherwise invites a client to apply a timezone to it.
func (r Range) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}{Start: r.StartString(), End: r.EndString()})
}

// Period is a relative range the dashboard offers.
type Period string

const (
	PeriodToday Period = "today"
	PeriodWeek  Period = "week"
	PeriodMonth Period = "month"
	PeriodYear  Period = "year"
)

// ParsePeriod resolves the `range` query parameter. An empty value means today.
//
// An unrecognised value is an error, not a silent fall back to today. The
// handler this replaces defaulted anything it did not recognise to today, so a
// client asking for "quarter" was shown one day's takings labelled as a
// quarter's and had no way to tell.
func ParsePeriod(s string) (Period, error) {
	switch Period(s) {
	case "":
		return PeriodToday, nil
	case PeriodToday, PeriodWeek, PeriodMonth, PeriodYear:
		return Period(s), nil
	default:
		return "", invalid("%q is not a reporting period; use today, week, month or year", s)
	}
}

// Range resolves the period against a clock. The end is always today and the
// start is measured back from it, which is what the dashboard has always meant
// by "week" and "month".
func (p Period) Range(now time.Time) Range {
	today := Day(now)
	switch p {
	case PeriodWeek:
		return Range{From: today.AddDate(0, 0, -7), To: today}
	case PeriodMonth:
		return Range{From: today.AddDate(0, -1, 0), To: today}
	case PeriodYear:
		return Range{From: today.AddDate(-1, 0, 0), To: today}
	default:
		return Range{From: today, To: today}
	}
}

// ParseMonth turns "2026-04" into the half-open instant range covering it.
//
// Half-open, not inclusive: the end is the first day of the next month and the
// query uses `< to`. That is the only form that cannot drop a transaction on
// the last day of the month or double count one on the first of the next.
func ParseMonth(month string) (from, to time.Time, err error) {
	from, err = time.Parse("2006-01", month)
	if err != nil {
		return time.Time{}, time.Time{}, invalid("month must look like 2026-04, got %q", month)
	}
	return from, from.AddDate(0, 1, 0), nil
}

// ---------------------------------------------------------------------------
// Sales aggregates
// ---------------------------------------------------------------------------

// SalesTotals are the invoice-header aggregates over a range of days. Every
// figure here counts completed invoices only.
type SalesTotals struct {
	SubTotal money.Money `json:"sub_total"`
	CGST     money.Money `json:"cgst"`
	SGST     money.Money `json:"sgst"`
	IGST     money.Money `json:"igst"`
	Discount money.Money `json:"discount"`
	RoundOff money.Money `json:"round_off"`
	// Total is what the customers actually paid, tax included.
	Total money.Money `json:"total"`
	// InvoiceCount is how many invoices produced these figures.
	//
	// There is one count here where the handler this replaces reported two:
	// COUNT(DISTINCT id) as "invoice_count" and COUNT(id) as
	// "transaction_count". id is the primary key, so those two numbers are
	// equal by definition and always were.
	InvoiceCount int64 `json:"invoice_count"`
}

// TaxCollected is the whole GST on these sales.
func (t SalesTotals) TaxCollected() money.Money {
	return t.CGST.Add(t.SGST).Add(t.IGST)
}

// TaxableRevenue is the revenue net of the tax collected on it — the figure
// profit is measured against, because the tax was never the shop's money.
//
// This is the IGST fix. The profit and loss report computed
//
//	taxableRevenue := totalRevenue - totalCGST - totalSGST
//
// with no IGST term at all. An inter-state supply carries IGST and no
// CGST/SGST, so for every such sale the whole tax stayed in "revenue" and was
// booked as gross profit: Rs.1,800 of invented profit on an Rs.11,800 invoice.
// Subtracting all three components is the only definition that holds for both
// intra-state and inter-state supplies.
func (t SalesTotals) TaxableRevenue() money.Money {
	return t.Total.Sub(t.CGST).Sub(t.SGST).Sub(t.IGST)
}

// AverageBill is revenue over invoice count, exactly, and zero when there were
// no invoices.
func (t SalesTotals) AverageBill() money.Money {
	if t.InvoiceCount <= 0 {
		return money.Zero()
	}
	return t.Total.Div(decimal.NewFromInt(t.InvoiceCount))
}

// Dashboard is the shop's headline figures for a period.
type Dashboard struct {
	Range              Range       `json:"date_range"`
	Revenue            money.Money `json:"revenue"`
	GSTCollected       money.Money `json:"gst_collected"`
	DiscountGiven      money.Money `json:"discount_given"`
	InvoiceCount       int64       `json:"invoice_count"`
	AverageBill        money.Money `json:"avg_bill_value"`
	LowStockCount      int64       `json:"low_stock_count"`
	CreditOutstanding  money.Money `json:"credit_outstanding"`
	InventoryValuation money.Money `json:"inventory_valuation"`
	ExpiringCount      int64       `json:"expiring_count"`
	PendingPOCount     int64       `json:"pending_po_count"`
	VendorPayables     money.Money `json:"vendor_payables"`
}

// NewDashboard assembles the dashboard from its parts.
func NewDashboard(
	r Range,
	totals SalesTotals,
	lowStock int64,
	creditOutstanding money.Money,
	valuation money.Money,
	expiring int64,
	pendingPO int64,
	payables money.Money,
) Dashboard {
	return Dashboard{
		Range:              r,
		Revenue:            totals.Total,
		GSTCollected:       totals.TaxCollected(),
		DiscountGiven:      totals.Discount,
		InvoiceCount:       totals.InvoiceCount,
		AverageBill:        totals.AverageBill(),
		LowStockCount:      lowStock,
		CreditOutstanding:  creditOutstanding,
		InventoryValuation: valuation,
		ExpiringCount:      expiring,
		PendingPOCount:     pendingPO,
		VendorPayables:     payables,
	}
}

// CategorySales is one row of the sales-by-category report.
type CategorySales struct {
	// CategoryID is nil for products that belong to no category. Their sales
	// are still reported, under Category "Uncategorised", so that the rows add
	// up to the takings they came from.
	CategoryID *uuid.UUID     `json:"category_id"`
	Category   string         `json:"category"`
	Sales      money.Money    `json:"sales"`
	Quantity   money.Quantity `json:"quantity"`
	LineCount  int64          `json:"item_count"`
}

// UncategorisedLabel names the bucket for products with no category.
const UncategorisedLabel = "Uncategorised"

// ProductSales is one row of the top-products report.
type ProductSales struct {
	ProductID uuid.UUID      `json:"product_id"`
	Product   string         `json:"product"`
	Quantity  money.Quantity `json:"quantity"`
	Sales     money.Money    `json:"sales"`
}

// HourBucket is one hour of one trading day.
type HourBucket struct {
	// Hour is 0-23 in the store's own timezone.
	Hour  int    `json:"hour"`
	Label string `json:"label"`
	// InvoiceCount is how many invoices were raised in this hour.
	InvoiceCount int64       `json:"count"`
	Sales        money.Money `json:"sales"`
}

// NewHourBucket labels an hour as the report displays it.
func NewHourBucket(hour int, count int64, sales money.Money) HourBucket {
	return HourBucket{
		Hour:         hour,
		Label:        fmt.Sprintf("%02d:00", hour),
		InvoiceCount: count,
		Sales:        sales,
	}
}

// PaymentModeTotal is one row of the payment-mode breakdown.
type PaymentModeTotal struct {
	Mode         string      `json:"mode"`
	InvoiceCount int64       `json:"count"`
	Total        money.Money `json:"total"`
}

// ---------------------------------------------------------------------------
// Multi-store rollup
// ---------------------------------------------------------------------------

// StoreSales is one store's line in the owner's rollup.
type StoreSales struct {
	StoreID      uuid.UUID   `json:"store_id"`
	StoreName    string      `json:"store_name"`
	Revenue      money.Money `json:"revenue"`
	GST          money.Money `json:"gst"`
	InvoiceCount int64       `json:"invoice_count"`
}

// GroupTotals is the sum across the stores in a rollup.
type GroupTotals struct {
	Revenue      money.Money `json:"revenue"`
	GST          money.Money `json:"gst"`
	InvoiceCount int64       `json:"invoice_count"`
}

// MultiStore is the owner's view across several shops.
type MultiStore struct {
	Range  Range        `json:"date_range"`
	Totals GroupTotals  `json:"totals"`
	Stores []StoreSales `json:"stores"`
}

// SummariseStores totals a rollup. A store that sold nothing still appears,
// with zeros, because "which of my shops took nothing today" is the question
// the report exists to answer.
func SummariseStores(r Range, stores []StoreSales) MultiStore {
	out := MultiStore{
		Range:  r,
		Totals: GroupTotals{Revenue: money.Zero(), GST: money.Zero()},
		Stores: stores,
	}
	for _, s := range stores {
		out.Totals.Revenue = out.Totals.Revenue.Add(s.Revenue)
		out.Totals.GST = out.Totals.GST.Add(s.GST)
		out.Totals.InvoiceCount += s.InvoiceCount
	}
	return out
}

// ---------------------------------------------------------------------------
// Profit and loss
// ---------------------------------------------------------------------------

// ProfitInput is everything the profit and loss report is computed from.
type ProfitInput struct {
	Range Range
	Sales SalesTotals
	// COGS is the cost of the goods that actually left, taken from the unit
	// cost snapshotted on each invoice line rather than from today's cost.
	COGS money.Money
	// Returns is what was refunded to customers in the period.
	Returns money.Money
	// Losses is the cost value of stock written off.
	Losses money.Money
	// Expenses is what the shop spent running itself.
	Expenses money.Money
}

// ProfitAndLoss is the period's trading result.
type ProfitAndLoss struct {
	Range Range `json:"date_range"`

	// TotalRevenue is gross takings, tax included, because that is the number
	// on the till roll.
	TotalRevenue money.Money `json:"total_revenue"`
	// TaxCollected is the GST inside TotalRevenue. It is shown because it is
	// the difference between the two revenue figures, and because leaving it
	// implicit is how the IGST defect survived.
	TaxCollected money.Money `json:"tax_collected"`
	// TaxableRevenue is TotalRevenue less all three tax components.
	TaxableRevenue money.Money `json:"taxable_revenue"`

	TotalCOGS   money.Money `json:"total_cogs"`
	GrossProfit money.Money `json:"gross_profit"`
	// GrossMargin is a percentage, so it is a money.Rate and reaches the client
	// as a JSON number. It was a bare decimal.Decimal, which serialises quoted,
	// and the profit and loss page calls .toFixed(1) on it - a method a string
	// does not have.
	GrossMargin money.Rate `json:"gross_margin_percent"`

	TotalReturns  money.Money `json:"total_returns"`
	NetRevenue    money.Money `json:"net_revenue"`
	TotalLosses   money.Money `json:"total_losses"`
	TotalExpenses money.Money `json:"total_expenses"`
	NetProfit     money.Money `json:"net_profit"`
}

// marginScale is how many decimal places the margin percentage carries. Four
// is enough to distinguish 12.3456% from 12.3457% and few enough that the
// figure reads as a percentage rather than as noise.
const marginScale = 4

// ComputeProfitAndLoss turns the period's aggregates into a trading result.
//
// COGS arrives as a sum of (unit cost x quantity) over NUMERIC(14,4) rates and
// NUMERIC(14,3) quantities, so it can carry up to seven decimal places. It is
// rounded once here, at the point it becomes a figure on a report, rather than
// left to drift into every derived number.
func ComputeProfitAndLoss(in ProfitInput) ProfitAndLoss {
	taxable := in.Sales.TaxableRevenue()
	cogs := in.COGS.Round()
	gross := taxable.Sub(cogs)

	margin := money.ZeroRate()
	if taxable.IsPositive() {
		margin = money.RateFromDecimal(gross.Decimal().
			DivRound(taxable.Decimal(), marginScale+2).
			Mul(decimal.NewFromInt(100))).
			Round(marginScale)
	}

	return ProfitAndLoss{
		Range:          in.Range,
		TotalRevenue:   in.Sales.Total,
		TaxCollected:   in.Sales.TaxCollected(),
		TaxableRevenue: taxable,
		TotalCOGS:      cogs,
		GrossProfit:    gross,
		GrossMargin:    margin,
		TotalReturns:   in.Returns,
		NetRevenue:     in.Sales.Total.Sub(in.Returns),
		TotalLosses:    in.Losses.Round(),
		TotalExpenses:  in.Expenses,
		NetProfit:      gross.Sub(in.Returns).Sub(in.Losses.Round()).Sub(in.Expenses),
	}
}

// ---------------------------------------------------------------------------
// Credit aging
// ---------------------------------------------------------------------------

// The aging ladder, in days since the credit was extended. A balance falls in
// exactly one bucket, so the four always sum to the customer's total due.
const (
	AgeCurrentDays = 30
	AgeDays30      = 60
	AgeDays60      = 90
)

// CustomerAging is one customer's outstanding balance, split by age.
//
// There are four buckets, not five. The struct this replaces declared a
// `days_90` field that the SQL never produced and the Scan never read, so it
// reported zero to every caller for the life of the report. It is removed
// rather than filled in: the four buckets below are the standard receivables
// ladder, and any fifth bucket would have to be carved out of Over90, leaving
// a field named `over_90` that no longer means "over ninety days". A field
// that always reads zero is worse than no field, because a reader believes it.
type CustomerAging struct {
	CustomerID   uuid.UUID   `json:"customer_id"`
	CustomerName string      `json:"customer_name"`
	TotalDue     money.Money `json:"total_due"`
	// Current is 30 days old or less.
	Current money.Money `json:"current"`
	// Days30 is 31 to 60 days old.
	Days30 money.Money `json:"days_30"`
	// Days60 is 61 to 90 days old.
	Days60 money.Money `json:"days_60"`
	// Over90 is more than 90 days old.
	Over90 money.Money `json:"over_90"`
}

// BucketTotal sums the four age buckets. It equals TotalDue for any row this
// package produces; a difference means the ladder has a hole in it.
func (a CustomerAging) BucketTotal() money.Money {
	return a.Current.Add(a.Days30).Add(a.Days60).Add(a.Over90)
}

// AgingReport is the store's receivables, oldest balances first.
type AgingReport struct {
	AsOf             time.Time       `json:"as_of"`
	Customers        []CustomerAging `json:"report"`
	TotalOutstanding money.Money     `json:"total_outstanding"`
}

// SummariseAging totals a set of customer balances.
func SummariseAging(asOf time.Time, customers []CustomerAging) AgingReport {
	total := money.Zero()
	for _, c := range customers {
		total = total.Add(c.TotalDue)
	}
	return AgingReport{AsOf: Day(asOf), Customers: customers, TotalOutstanding: total}
}

// ---------------------------------------------------------------------------
// GSTR-3B
// ---------------------------------------------------------------------------

// TaxSummary is a taxable value and its tax, on either side of the return.
type TaxSummary struct {
	Taxable money.Money `json:"taxable_value"`
	CGST    money.Money `json:"cgst"`
	SGST    money.Money `json:"sgst"`
	IGST    money.Money `json:"igst"`
}

// TotalTax is the whole tax on the supplies.
func (t TaxSummary) TotalTax() money.Money { return t.CGST.Add(t.SGST).Add(t.IGST) }

// GSTR3BNote is attached to every summary. This is a preparation aid, not a
// filing.
const GSTR3BNote = "Internal summary only; validate against books and the GST portal before filing GSTR-3B."

// GSTR3B is a month's outward and inward tax position.
type GSTR3B struct {
	Month   string    `json:"month"`
	StoreID uuid.UUID `json:"store_id"`

	Outward TaxSummary `json:"outward_supplies"`
	Inward  TaxSummary `json:"inward_supplies"`

	// NetTaxPayable is outward tax less the input credit claimed on inward
	// supplies. It is a hint: the real liability depends on eligibility rules
	// this system does not model.
	NetTaxPayable money.Money `json:"net_tax_payable_hint"`
	Note          string      `json:"note"`
}

// NewGSTR3B assembles the return summary.
func NewGSTR3B(month string, storeID uuid.UUID, outward, inward TaxSummary) GSTR3B {
	return GSTR3B{
		Month:         month,
		StoreID:       storeID,
		Outward:       outward,
		Inward:        inward,
		NetTaxPayable: outward.TotalTax().Sub(inward.TotalTax()),
		Note:          GSTR3BNote,
	}
}
