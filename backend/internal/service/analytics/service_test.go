package analytics_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gbos/internal/app"
	domain "gbos/internal/domain/analytics"
	billingdomain "gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	purchasingdomain "gbos/internal/domain/purchasing"
	"gbos/internal/platform/cache"
	platform "gbos/internal/platform/postgres"
	service "gbos/internal/service/analytics"
	billingsvc "gbos/internal/service/billing"
	purchasingsvc "gbos/internal/service/purchasing"
	"gbos/internal/testsupport"

	"github.com/google/uuid"
)

// shop is a working retail store: a counter, a cashier, products on the shelf,
// and the services that sell them.
//
// The reports are asserted against sales raised through the billing service,
// never against invoice rows written by hand. A report that agrees with a
// fixture but not with a real sale is worth nothing, and the defect this suite
// exists to catch — voided invoices counted as revenue — can only be seen by
// voiding a real sale.
type shop struct {
	t    *testing.T
	db   *platform.DB
	svcs app.Services
	svc  *service.Service

	storeID    uuid.UUID
	terminalID uuid.UUID
	userID     uuid.UUID
	uomID      uuid.UUID
	categoryID uuid.UUID
}

func newShop(t *testing.T) *shop {
	t.Helper()
	db := testsupport.OpenDB(t)

	svcs, err := app.Build(db, app.Config{JWTSecret: "analytics-tests-signing-secret-x"})
	if err != nil {
		t.Fatalf("build services: %v", err)
	}

	x := &shop{t: t, db: db, svcs: svcs, svc: svcs.Analytics}
	mustScan(t, db, `INSERT INTO stores (name, gstin) VALUES ('Report Shop', '29ABCDE1234F1Z5') RETURNING id`, &x.storeID)
	mustScan(t, db, `INSERT INTO terminals (store_id, code, name) VALUES ($1, 'T1', 'Counter 1') RETURNING id`,
		&x.terminalID, x.storeID)
	mustScan(t, db, `INSERT INTO users (username, password_hash, role, store_id)
		VALUES ('report-cashier', 'x', 'cashier', $1) RETURNING id`, &x.userID, x.storeID)
	// The units migration seeds the ordinary Indian retail vocabulary, so the
	// kilogram is already on file with its family and conversion factor. A test
	// that inserted its own would collide on the unique code.
	mustScan(t, db, `SELECT id FROM units_of_measure WHERE code = 'KG'`, &x.uomID)
	mustScan(t, db, `INSERT INTO categories (name) VALUES ('Groceries') RETURNING id`, &x.categoryID)
	return x
}

func mustScan(t *testing.T, db *platform.DB, query string, dest any, args ...any) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(dest); err != nil {
		t.Fatalf("seed (%s): %v", query, err)
	}
}

func (x *shop) exec(query string, args ...any) {
	x.t.Helper()
	if _, err := x.db.ExecContext(context.Background(), query, args...); err != nil {
		x.t.Fatalf("exec (%s): %v", query, err)
	}
}

// day is the business date the database will stamp on a sale raised now.
//
// It is read from the database rather than from Go's clock on purpose:
// invoices.invoice_date is a DATE and the conversion from the instant billing
// supplies happens in the database session's timezone. Taking the date from
// anywhere else makes every range in this file flaky for the hours when the two
// clocks disagree about which day it is.
func (x *shop) day() time.Time {
	x.t.Helper()
	var d time.Time
	mustScan(x.t, x.db, `SELECT CURRENT_DATE`, &d)
	return d
}

func (x *shop) today() domain.Range { return domain.SingleDay(x.day()) }

func (x *shop) category(name string) uuid.UUID {
	x.t.Helper()
	var id uuid.UUID
	mustScan(x.t, x.db, `INSERT INTO categories (name) VALUES ($1) RETURNING id`, &id, name)
	return id
}

// stockProduct creates a product in the default category, priced at sell, with
// onHand units in stock at cost.
func (x *shop) stockProduct(name, sell, cost, onHand string, gstPercent int64, allowDecimal bool) uuid.UUID {
	return x.stockProductIn(x.categoryID, name, sell, cost, onHand, gstPercent, allowDecimal)
}

func (x *shop) stockProductIn(categoryID uuid.UUID, name, sell, cost, onHand string, gstPercent int64, allowDecimal bool) uuid.UUID {
	x.t.Helper()

	var productID uuid.UUID
	mustScan(x.t, x.db, `INSERT INTO products (name, category_id, uom_id, gst_rate, allow_decimal)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, &productID,
		name, categoryID, x.uomID, gstPercent, allowDecimal)

	// One selling unit, worth one kilo, priced for this shop. Prices are per
	// unit now — a sack is not 25 times the loose kilo — and nothing in these
	// reporting cases turns on bulk.
	var unitID uuid.UUID
	mustScan(x.t, x.db, `INSERT INTO product_units (product_id, uom_id, label, qty_in_base, is_default)
		VALUES ($1, $2, 'Loose', 1, true) RETURNING id`, &unitID, productID, x.uomID)

	x.exec(`INSERT INTO product_prices (product_id, store_id, product_unit_id, mrp, cost_price, sell_price)
		VALUES ($1, $2, $3, $4, $5, $4)`, productID, x.storeID, unitID,
		money.MustFromString(sell), money.MustFromString(cost))
	x.exec(`INSERT INTO stock (product_id, store_id, batch_no, quantity, total_cost)
		VALUES ($1, $2, 'OPENING', $3, round($3::numeric * $4::numeric, 2))`,
		productID, x.storeID,
		money.MustQuantityFromString(onHand), money.MustFromString(cost))
	return productID
}

func (x *shop) customer(name, creditLimit, gstin string) uuid.UUID {
	x.t.Helper()
	var id uuid.UUID
	mustScan(x.t, x.db, `INSERT INTO customers (name, store_id, credit_limit, gstin)
		VALUES ($1, $2, $3, NULLIF($4, '')) RETURNING id`,
		&id, name, x.storeID, money.MustFromString(creditLimit), gstin)
	return id
}

func (x *shop) sale(in billingsvc.CreateInput) billingsvc.Result {
	x.t.Helper()
	if in.StoreID == uuid.Nil {
		in.StoreID = x.storeID
	}
	if in.UserID == uuid.Nil {
		in.UserID = x.userID
	}
	if in.TerminalID == uuid.Nil {
		in.TerminalID = x.terminalID
	}
	if in.PaymentMode == "" {
		in.PaymentMode = billingdomain.PaymentCash
	}
	got, err := x.svcs.Billing.Create(context.Background(), in)
	if err != nil {
		x.t.Fatalf("sale: %v", err)
	}
	return got
}

func (x *shop) void(invoiceID uuid.UUID) {
	x.t.Helper()
	if err := x.svcs.Billing.Void(context.Background(), x.storeID, invoiceID); err != nil {
		x.t.Fatalf("void: %v", err)
	}
}

func line(productID uuid.UUID, qty string) billingdomain.LineInput {
	return billingdomain.LineInput{ProductID: productID, UnitQty: money.MustQuantityFromString(qty)}
}

func rupees(s string) money.Money { return money.MustFromString(s) }

func wantMoney(t *testing.T, what string, got money.Money, want string) {
	t.Helper()
	if !got.Equal(rupees(want)) {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

// ---------------------------------------------------------------------------
// The headline fix: a voided sale is not revenue, anywhere
// ---------------------------------------------------------------------------

// TestAVoidedSaleIsExcludedFromEveryRevenueReport is the most important test in
// this package.
//
// Eight aggregates in the report handlers had no status filter: dashboard
// revenue, GST, discount, invoice count and average bill; sales by category;
// top products; hourly sales; the payment-mode breakdown; and the multi-store
// rollup. Voiding a sale put the goods back on the shelf and cancelled the
// customer's debt, and every one of those figures went on counting the money.
//
// The shop below takes two sales and voids a third. Every report must show the
// two, and none may show the third.
func TestAVoidedSaleIsExcludedFromEveryRevenueReport(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	household := x.category("Household")
	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, true)
	soap := x.stockProductIn(household, "Soap", "60.00", "45.00", "100", 18, false)

	// Kept: rice x2 with Rs.100 off the bill -> 800 taxable + 40 tax = 840.
	kept1 := x.sale(billingsvc.CreateInput{
		Lines:        []billingdomain.LineInput{line(rice, "2")},
		BillDiscount: rupees("100.00"),
		PaymentMode:  billingdomain.PaymentCash,
	})
	// Kept: soap x5 -> 300 taxable + 54 tax = 354.
	kept2 := x.sale(billingsvc.CreateInput{
		Lines:       []billingdomain.LineInput{line(soap, "5")},
		PaymentMode: billingdomain.PaymentUPI,
	})
	// Voided: rice x4 -> 1800 taxable + 90 tax = 1890, paid in cash.
	voided := x.sale(billingsvc.CreateInput{
		Lines:       []billingdomain.LineInput{line(rice, "4")},
		PaymentMode: billingdomain.PaymentCash,
	})

	wantMoney(t, "kept sale 1 total", kept1.Invoice.Total, "840.00")
	wantMoney(t, "kept sale 2 total", kept2.Invoice.Total, "354.00")
	wantMoney(t, "the sale about to be voided", voided.Invoice.Total, "1890.00")

	x.void(voided.Invoice.ID)

	today := x.today()

	// --- dashboard ---------------------------------------------------------
	dash, err := x.svc.DashboardForRange(ctx, x.storeID, today)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	wantMoney(t, "dashboard revenue", dash.Revenue, "1194.00")
	wantMoney(t, "dashboard GST collected", dash.GSTCollected, "94.00")
	wantMoney(t, "dashboard discount given", dash.DiscountGiven, "100.00")
	if dash.InvoiceCount != 2 {
		t.Errorf("dashboard invoice count = %d, want 2 — the voided sale was counted", dash.InvoiceCount)
	}
	wantMoney(t, "dashboard average bill", dash.AverageBill, "597.00")

	// --- sales by category -------------------------------------------------
	categories, err := x.svc.SalesByCategory(ctx, x.storeID, today)
	if err != nil {
		t.Fatalf("sales by category: %v", err)
	}
	byName := map[string]money.Money{}
	categorySum := money.Zero()
	for _, c := range categories {
		byName[c.Category] = c.Sales
		categorySum = categorySum.Add(c.Sales)
	}
	wantMoney(t, "Groceries sales", byName["Groceries"], "840.00")
	wantMoney(t, "Household sales", byName["Household"], "354.00")
	if !categorySum.Equal(dash.Revenue) {
		t.Errorf("the categories sum to %s but the day's revenue was %s", categorySum, dash.Revenue)
	}

	// --- top products ------------------------------------------------------
	products, err := x.svc.TopProducts(ctx, x.storeID, today, 10)
	if err != nil {
		t.Fatalf("top products: %v", err)
	}
	if len(products) != 2 {
		t.Fatalf("top products returned %d rows, want 2", len(products))
	}
	if products[0].Product != "Rice" {
		t.Errorf("best seller is %q, want Rice", products[0].Product)
	}
	wantMoney(t, "Rice sales", products[0].Sales, "840.00")
	if got := products[0].Quantity; !got.Equal(money.MustQuantityFromString("2")) {
		t.Errorf("Rice quantity sold = %s, want 2 — the voided four kilos were counted", got)
	}
	wantMoney(t, "Soap sales", products[1].Sales, "354.00")

	// --- hourly ------------------------------------------------------------
	hours, err := x.svc.HourlySales(ctx, x.storeID, x.day())
	if err != nil {
		t.Fatalf("hourly sales: %v", err)
	}
	hourlyTotal := money.Zero()
	hourlyCount := int64(0)
	for _, h := range hours {
		hourlyTotal = hourlyTotal.Add(h.Sales)
		hourlyCount += h.InvoiceCount
	}
	wantMoney(t, "hourly sales total", hourlyTotal, "1194.00")
	if hourlyCount != 2 {
		t.Errorf("hourly buckets hold %d invoices, want 2", hourlyCount)
	}

	// --- payment modes -----------------------------------------------------
	modes, err := x.svc.PaymentModes(ctx, x.storeID, today)
	if err != nil {
		t.Fatalf("payment modes: %v", err)
	}
	byMode := map[string]domain.PaymentModeTotal{}
	for _, m := range modes {
		byMode[m.Mode] = m
	}
	// Cash would read 2730.00 across two invoices with the defect present.
	wantMoney(t, "cash takings", byMode["cash"].Total, "840.00")
	if byMode["cash"].InvoiceCount != 1 {
		t.Errorf("cash invoice count = %d, want 1", byMode["cash"].InvoiceCount)
	}
	wantMoney(t, "upi takings", byMode["upi"].Total, "354.00")

	// --- multi-store rollup ------------------------------------------------
	rollup, err := x.svc.MultiStoreForRange(ctx, []uuid.UUID{x.storeID}, today)
	if err != nil {
		t.Fatalf("multi-store: %v", err)
	}
	wantMoney(t, "rollup revenue", rollup.Totals.Revenue, "1194.00")
	wantMoney(t, "rollup GST", rollup.Totals.GST, "94.00")
	if rollup.Totals.InvoiceCount != 2 {
		t.Errorf("rollup invoice count = %d, want 2", rollup.Totals.InvoiceCount)
	}

	// --- profit and loss ---------------------------------------------------
	pl, err := x.svc.ProfitAndLoss(ctx, x.storeID, today)
	if err != nil {
		t.Fatalf("profit and loss: %v", err)
	}
	wantMoney(t, "P&L revenue", pl.TotalRevenue, "1194.00")
	// Cost of the goods that actually left: 2 x 337.50 + 5 x 45.00.
	wantMoney(t, "P&L cost of goods sold", pl.TotalCOGS, "900.00")
	wantMoney(t, "P&L taxable revenue", pl.TaxableRevenue, "1100.00")
	wantMoney(t, "P&L gross profit", pl.GrossProfit, "200.00")

	// --- GSTR-3B -----------------------------------------------------------
	month := x.day().Format("2006-01")
	gst, err := x.svc.GSTR3B(ctx, x.storeID, month)
	if err != nil {
		t.Fatalf("gstr3b: %v", err)
	}
	// The taxable value is sub_total, which is already net of the Rs.100
	// discount. The query this replaces subtracted the discount column again.
	wantMoney(t, "GSTR-3B outward taxable value", gst.Outward.Taxable, "1100.00")
	wantMoney(t, "GSTR-3B outward CGST", gst.Outward.CGST, "47.00")
	wantMoney(t, "GSTR-3B outward SGST", gst.Outward.SGST, "47.00")
	// Taxable value plus tax must come back to the money taken.
	if got := gst.Outward.Taxable.Add(gst.Outward.TotalTax()); !got.Equal(dash.Revenue) {
		t.Errorf("GSTR-3B outward supplies come to %s but the day's revenue was %s", got, dash.Revenue)
	}
}

// ---------------------------------------------------------------------------
// Profit and loss
// ---------------------------------------------------------------------------

// TestInterStateProfitSubtractsIGST is the second half of the void test's
// arithmetic: the report handler computed taxable revenue as
// total - cgst - sgst and never subtracted IGST, so an inter-state supply
// carried its whole tax into gross profit.
func TestInterStateProfitSubtractsIGST(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, true)
	// State code 27 against the shop's 29: an inter-state supply, so IGST and
	// no CGST or SGST.
	outOfState := x.customer("Maharashtra Buyer", "0.00", "27ABCDE1234F1Z5")

	sold := x.sale(billingsvc.CreateInput{
		Lines:      []billingdomain.LineInput{line(rice, "2.5")},
		CustomerID: &outOfState,
	})
	wantMoney(t, "invoice IGST", sold.Invoice.IGST, "56.25")
	if !sold.Invoice.CGST.IsZero() || !sold.Invoice.SGST.IsZero() {
		t.Fatalf("an inter-state supply carried CGST %s and SGST %s", sold.Invoice.CGST, sold.Invoice.SGST)
	}
	wantMoney(t, "invoice total", sold.Invoice.Total, "1181.25")

	pl, err := x.svc.ProfitAndLoss(ctx, x.storeID, x.today())
	if err != nil {
		t.Fatalf("profit and loss: %v", err)
	}

	wantMoney(t, "taxable revenue", pl.TaxableRevenue, "1125.00")
	wantMoney(t, "cost of goods sold", pl.TotalCOGS, "843.75")
	// 1125.00 - 843.75. The defect produced 337.50 here — the true profit plus
	// the entire Rs.56.25 of IGST, which was never the shop's money.
	wantMoney(t, "gross profit", pl.GrossProfit, "281.25")

	if pl.GrossProfit.Equal(pl.TotalRevenue.Sub(pl.TotalCOGS)) {
		t.Error("gross profit still equals gross revenue less cost — the IGST was not subtracted")
	}
	if got := pl.TotalRevenue.Sub(pl.TaxCollected); !got.Equal(pl.TaxableRevenue) {
		t.Errorf("revenue %s less tax %s is %s, but taxable revenue reads %s",
			pl.TotalRevenue, pl.TaxCollected, got, pl.TaxableRevenue)
	}
}

// TestProfitAndLossCountsReturnsLossesAndExpenses exercises the three date
// filters that were `date(created_at)` and `expense_date` wrappers in SQLite.
func TestProfitAndLossCountsReturnsLossesAndExpenses(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, true)
	sold := x.sale(billingsvc.CreateInput{Lines: []billingdomain.LineInput{line(rice, "4")}})
	wantMoney(t, "invoice total", sold.Invoice.Total, "1890.00")

	// A refund, a write-off of two kilos, and a bill to pay — all dated today.
	x.exec(`INSERT INTO returns (invoice_id, product_id, store_id, quantity, amount, reason, status)
		VALUES ($1, $2, $3, 1, $4, 'damaged', 'completed')`,
		sold.Invoice.ID, rice, x.storeID, rupees("200.00"))
	x.exec(`INSERT INTO inventory_losses (product_id, store_id, batch_no, quantity, loss_type, user_id)
		VALUES ($1, $2, 'OPENING', 2, 'expiry', $3)`, rice, x.storeID, x.userID)

	var expenseCategory uuid.UUID
	mustScan(t, x.db, `INSERT INTO expense_categories (name) VALUES ('Rent') RETURNING id`, &expenseCategory)
	x.exec(`INSERT INTO expenses (store_id, category_id, amount, expense_date, user_id)
		VALUES ($1, $2, $3, CURRENT_DATE, $4)`, x.storeID, expenseCategory, rupees("300.00"), x.userID)

	pl, err := x.svc.ProfitAndLoss(ctx, x.storeID, x.today())
	if err != nil {
		t.Fatalf("profit and loss: %v", err)
	}

	wantMoney(t, "taxable revenue", pl.TaxableRevenue, "1800.00")
	wantMoney(t, "cost of goods sold", pl.TotalCOGS, "1350.00")
	wantMoney(t, "gross profit", pl.GrossProfit, "450.00")
	wantMoney(t, "returns", pl.TotalReturns, "200.00")
	wantMoney(t, "net revenue", pl.NetRevenue, "1690.00")
	// Two kilos valued at the batch's own cost price.
	wantMoney(t, "inventory losses", pl.TotalLosses, "675.00")
	wantMoney(t, "expenses", pl.TotalExpenses, "300.00")
	wantMoney(t, "net profit", pl.NetProfit, "-725.00")
}

// ---------------------------------------------------------------------------
// Credit aging
// ---------------------------------------------------------------------------

// TestCreditAgingBucketsSumToTheTotalOutstanding walks the whole ladder,
// including both sides of each boundary, and checks that nothing falls between
// two buckets or lands in both.
//
// The query this replaces called julianday('now') twelve times to age its
// entries, which does not exist in Postgres, and produced four buckets while
// the struct declared five — so the fifth, days_90, read zero forever.
func TestCreditAgingBucketsSumToTheTotalOutstanding(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, false)
	buyer := x.customer("Slow Payer", "100000.00", "")

	// One credit sale per age, each 472.50 (450 + 5% GST).
	ages := []int{0, 30, 31, 60, 61, 90, 91}
	for _, age := range ages {
		sold := x.sale(billingsvc.CreateInput{
			Lines:       []billingdomain.LineInput{line(rice, "1")},
			CustomerID:  &buyer,
			PaymentMode: billingdomain.PaymentCredit,
		})
		wantMoney(t, "credit sale total", sold.Invoice.Total, "472.50")
		// A real sale, moved back in time. The ledger row is the one the aging
		// report reads.
		x.exec(`UPDATE credit_entries SET created_at = now() - make_interval(days => $2)
			WHERE invoice_id = $1`, sold.Invoice.ID, age)
	}

	report, err := x.svc.CreditAging(ctx, x.storeID, x.day())
	if err != nil {
		t.Fatalf("credit aging: %v", err)
	}
	if len(report.Customers) != 1 {
		t.Fatalf("aging report has %d customers, want 1", len(report.Customers))
	}

	row := report.Customers[0]
	if row.CustomerName != "Slow Payer" {
		t.Errorf("aging row is for %q, want Slow Payer", row.CustomerName)
	}
	wantMoney(t, "total due", row.TotalDue, "3307.50")
	// 0 and 30 days old.
	wantMoney(t, "current (0-30 days)", row.Current, "945.00")
	// 31 and 60.
	wantMoney(t, "31-60 days", row.Days30, "945.00")
	// 61 and 90.
	wantMoney(t, "61-90 days", row.Days60, "945.00")
	// 91.
	wantMoney(t, "over 90 days", row.Over90, "472.50")

	// The ladder must have no holes and no overlaps.
	if got := row.BucketTotal(); !got.Equal(row.TotalDue) {
		t.Errorf("the buckets sum to %s but the customer owes %s", got, row.TotalDue)
	}
	if !report.TotalOutstanding.Equal(row.TotalDue) {
		t.Errorf("report total %s does not match the one customer's %s",
			report.TotalOutstanding, row.TotalDue)
	}

	// And it must agree with the credit aggregate's own view of the debt.
	outstanding, err := x.svcs.Credit.StoreOutstanding(ctx, x.storeID)
	if err != nil {
		t.Fatalf("store outstanding: %v", err)
	}
	if !outstanding.Equal(report.TotalOutstanding) {
		t.Errorf("the aging report says %s is outstanding, the credit ledger says %s",
			report.TotalOutstanding, outstanding)
	}
}

// TestVoidedCreditIsNotOutstanding pins the other half of the aging fix. The
// original predicate was `status != 'paid'`, which counts a voided entry — a
// cancelled sale — as money owed.
func TestVoidedCreditIsNotOutstanding(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, false)
	buyer := x.customer("Cancelled Order", "100000.00", "")

	kept := x.sale(billingsvc.CreateInput{
		Lines: []billingdomain.LineInput{line(rice, "1")}, CustomerID: &buyer,
		PaymentMode: billingdomain.PaymentCredit,
	})
	cancelled := x.sale(billingsvc.CreateInput{
		Lines: []billingdomain.LineInput{line(rice, "2")}, CustomerID: &buyer,
		PaymentMode: billingdomain.PaymentCredit,
	})
	x.void(cancelled.Invoice.ID)

	report, err := x.svc.CreditAging(ctx, x.storeID, x.day())
	if err != nil {
		t.Fatalf("credit aging: %v", err)
	}
	if !report.TotalOutstanding.Equal(kept.Invoice.Total) {
		t.Errorf("outstanding = %s, want only the surviving sale's %s",
			report.TotalOutstanding, kept.Invoice.Total)
	}

	dash, err := x.svc.DashboardForRange(ctx, x.storeID, x.today())
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	// The dashboard and the aging report must agree. They used two different
	// SQL predicates for "still owed" and differed by every voided sale.
	if !dash.CreditOutstanding.Equal(report.TotalOutstanding) {
		t.Errorf("the dashboard says %s is outstanding, the aging report says %s",
			dash.CreditOutstanding, report.TotalOutstanding)
	}
}

// ---------------------------------------------------------------------------
// GSTR-3B
// ---------------------------------------------------------------------------

// TestGSTR3BInwardExcludesCancelledPurchaseOrders covers the input-credit
// defect: the inward query had no status filter, so a cancelled purchase order
// reduced the tax the business declared it owed. No goods arrived and no money
// moved, and the shop still claimed the credit.
func TestGSTR3BInwardExcludesCancelledPurchaseOrders(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, true)

	vendor, err := x.svcs.Purchasing.CreateVendor(ctx, purchasingdomain.VendorInput{
		Name:  "Wholesaler",
		GSTIN: "29AAACW1234F1Z5",
		State: "Karnataka",
	})
	if err != nil {
		t.Fatalf("create vendor: %v", err)
	}

	order := func(qty string) uuid.UUID {
		t.Helper()
		po, _, err := x.svcs.Purchasing.CreatePurchaseOrder(ctx, purchasingsvc.CreatePurchaseOrderInput{
			StoreID:    x.storeID,
			VendorID:   vendor.ID,
			TerminalID: x.terminalID,
			OrderDate:  x.day(),
			Lines: []purchasingdomain.LineInput{{
				ProductID: rice,
				UnitQty:   money.MustQuantityFromString(qty),
				Rate:      rupees("300.00"),
			}},
		})
		if err != nil {
			t.Fatalf("create purchase order: %v", err)
		}
		return po.ID
	}

	// A real order: 10 x 300 = 3000 taxable, 5% GST split as 75 + 75.
	order("10")
	// And one twice the size that is cancelled before anything arrives.
	scrapped := order("20")
	if err := x.svcs.Purchasing.CancelPurchaseOrder(ctx, scrapped, x.storeID); err != nil {
		t.Fatalf("cancel purchase order: %v", err)
	}

	got, err := x.svc.GSTR3B(ctx, x.storeID, x.day().Format("2006-01"))
	if err != nil {
		t.Fatalf("gstr3b: %v", err)
	}

	// With the defect present these read 9000.00, 225.00 and 225.00.
	wantMoney(t, "inward taxable value", got.Inward.Taxable, "3000.00")
	wantMoney(t, "inward CGST", got.Inward.CGST, "75.00")
	wantMoney(t, "inward SGST", got.Inward.SGST, "75.00")
	wantMoney(t, "inward total tax", got.Inward.TotalTax(), "150.00")

	// No sales, so the whole input credit is what the net hint reflects.
	wantMoney(t, "net tax payable hint", got.NetTaxPayable, "-150.00")
	if got.Note == "" {
		t.Error("the summary must carry its caveat about validating before filing")
	}
}

// TestGSTR3BReportsRealFiguresRatherThanZeros guards the discarded Scan errors.
// Both halves of the return were read with `_ = ...Scan(...)`, so any failure
// left every figure at zero and the response presented those zeros as the
// month's tax position.
func TestGSTR3BReportsRealFiguresRatherThanZeros(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, true)
	sold := x.sale(billingsvc.CreateInput{Lines: []billingdomain.LineInput{line(rice, "2")}})

	got, err := x.svc.GSTR3B(ctx, x.storeID, x.day().Format("2006-01"))
	if err != nil {
		t.Fatalf("gstr3b: %v", err)
	}
	if got.Outward.Taxable.IsZero() {
		t.Fatal("the outward taxable value came back zero against a real sale")
	}
	if !got.Outward.Taxable.Equal(sold.Invoice.SubTotal) {
		t.Errorf("outward taxable = %s, want the invoice's %s", got.Outward.Taxable, sold.Invoice.SubTotal)
	}
	if !got.Outward.TotalTax().Equal(sold.Invoice.CGST.Add(sold.Invoice.SGST).Add(sold.Invoice.IGST)) {
		t.Errorf("outward tax = %s, want the invoice's tax", got.Outward.TotalTax())
	}

	// A month outside the trading period is genuinely empty, which is a
	// different thing from a read that failed.
	empty, err := x.svc.GSTR3B(ctx, x.storeID, x.day().AddDate(0, -6, 0).Format("2006-01"))
	if err != nil {
		t.Fatalf("gstr3b for a quiet month: %v", err)
	}
	if !empty.Outward.Taxable.IsZero() {
		t.Errorf("a month with no trading reported %s of outward supplies", empty.Outward.Taxable)
	}

	if _, err := x.svc.GSTR3B(ctx, x.storeID, "April 2026"); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("a malformed month should be refused, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Hourly sales
// ---------------------------------------------------------------------------

// TestHourlySalesBucketByHour checks the replacement for strftime('%H', ...).
//
// The hour has to come from created_at now: invoice_date is a DATE in this
// schema and carries no time of day, so reading the hour from it would put
// every sale of the day at midnight.
func TestHourlySalesBucketByHour(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	rice := x.stockProduct("Rice", "100.00", "75.00", "100", 5, false)

	// Two sales in the morning rush and one late in the afternoon, then one
	// more that is voided — a voided sale must not appear in any hour.
	morningA := x.sale(billingsvc.CreateInput{Lines: []billingdomain.LineInput{line(rice, "1")}})
	morningB := x.sale(billingsvc.CreateInput{Lines: []billingdomain.LineInput{line(rice, "2")}})
	afternoon := x.sale(billingsvc.CreateInput{Lines: []billingdomain.LineInput{line(rice, "3")}})
	cancelled := x.sale(billingsvc.CreateInput{Lines: []billingdomain.LineInput{line(rice, "4")}})

	x.setHour(morningA.Invoice.ID, 9)
	x.setHour(morningB.Invoice.ID, 9)
	x.setHour(afternoon.Invoice.ID, 17)
	x.setHour(cancelled.Invoice.ID, 17)
	x.void(cancelled.Invoice.ID)

	// The hours were written as UTC, so the report is asked for UTC. A
	// timestamptz has no hour until a zone is chosen.
	hours, err := x.svc.WithTimeZone("UTC").HourlySales(ctx, x.storeID, x.day())
	if err != nil {
		t.Fatalf("hourly sales: %v", err)
	}
	if len(hours) != 2 {
		t.Fatalf("hourly sales returned %d buckets, want 2 (09:00 and 17:00): %+v", len(hours), hours)
	}

	if hours[0].Hour != 9 || hours[0].Label != "09:00" {
		t.Errorf("first bucket is hour %d labelled %q, want 9 / 09:00", hours[0].Hour, hours[0].Label)
	}
	if hours[0].InvoiceCount != 2 {
		t.Errorf("09:00 holds %d invoices, want 2", hours[0].InvoiceCount)
	}
	// 105.00 + 210.00.
	wantMoney(t, "09:00 takings", hours[0].Sales, "315.00")

	if hours[1].Hour != 17 {
		t.Errorf("second bucket is hour %d, want 17", hours[1].Hour)
	}
	if hours[1].InvoiceCount != 1 {
		t.Errorf("17:00 holds %d invoices, want 1 — the voided sale was counted", hours[1].InvoiceCount)
	}
	wantMoney(t, "17:00 takings", hours[1].Sales, "315.00")
}

// setHour moves a real sale to a given hour of its own business day, expressed
// in UTC so the bucketing is deterministic whatever the database session's
// timezone happens to be.
func (x *shop) setHour(invoiceID uuid.UUID, hour int) {
	x.t.Helper()
	x.exec(`UPDATE invoices
	           SET created_at = (invoice_date::timestamp + make_interval(hours => $2)) AT TIME ZONE 'UTC'
	         WHERE id = $1`, invoiceID, hour)
}

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

func TestDashboardAverageBillIsRevenueOverInvoiceCount(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, true)

	// Three sales whose totals do not divide evenly, so the division is a real
	// one: 472.50 + 945.00 + 1417.50 = 2835.00 over three bills.
	for _, qty := range []string{"1", "2", "3"} {
		x.sale(billingsvc.CreateInput{Lines: []billingdomain.LineInput{line(rice, qty)}})
	}

	dash, err := x.svc.DashboardForRange(ctx, x.storeID, x.today())
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	wantMoney(t, "revenue", dash.Revenue, "2835.00")
	if dash.InvoiceCount != 3 {
		t.Fatalf("invoice count = %d, want 3", dash.InvoiceCount)
	}
	wantMoney(t, "average bill", dash.AverageBill, "945.00")

	// And exactly, against the two figures it is derived from.
	totals := domain.SalesTotals{Total: dash.Revenue, InvoiceCount: dash.InvoiceCount}
	if !dash.AverageBill.Equal(totals.AverageBill()) {
		t.Errorf("average bill %s is not revenue %s over %d invoices",
			dash.AverageBill, dash.Revenue, dash.InvoiceCount)
	}
}

func TestDashboardCountsProductsBelowTheirReorderLevel(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	// Two products with a reorder level, one of which is already short. The
	// count this replaces ran over a derived table with no alias, which
	// Postgres rejects outright, and swallowed the resulting error as zero.
	low := x.stockProduct("Rice", "450.00", "337.50", "2", 5, true)
	x.exec(`UPDATE products SET min_stock = 10 WHERE id = $1`, low)
	plenty := x.stockProduct("Soap", "60.00", "45.00", "100", 18, false)
	x.exec(`UPDATE products SET min_stock = 10 WHERE id = $1`, plenty)

	dash, err := x.svc.DashboardForRange(ctx, x.storeID, x.today())
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if dash.LowStockCount != 1 {
		t.Errorf("low stock count = %d, want 1", dash.LowStockCount)
	}
}

func TestDashboardPeriodsAreResolvedAgainstTheClock(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, false)
	x.sale(billingsvc.CreateInput{Lines: []billingdomain.LineInput{line(rice, "1")}})

	// The clock is pinned to the business day the database stamped on the
	// sale, so "today" means the same thing on both sides.
	svc := x.svc.WithClock(func() time.Time { return x.day() })

	dash, err := svc.Dashboard(ctx, x.storeID, domain.PeriodToday)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	wantMoney(t, "today's revenue", dash.Revenue, "472.50")
	if dash.Range.StartString() != dash.Range.EndString() {
		t.Errorf("today spans %s", dash.Range)
	}

	// A month back from today includes it too.
	monthly, err := svc.Dashboard(ctx, x.storeID, domain.PeriodMonth)
	if err != nil {
		t.Fatalf("monthly dashboard: %v", err)
	}
	wantMoney(t, "this month's revenue", monthly.Revenue, "472.50")
	if monthly.Range.Days() < 28 {
		t.Errorf("a month covers %d days", monthly.Range.Days())
	}
}

func TestReportsRefuseToRunWithoutAStore(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	if _, err := x.svc.DashboardForRange(ctx, uuid.Nil, x.today()); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("dashboard without a store: %v", err)
	}
	if _, err := x.svc.CreditAging(ctx, uuid.Nil, x.day()); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("credit aging without a store: %v", err)
	}
	// The rollup crosses the store boundary every other report is scoped by, so
	// it takes its stores explicitly. The handler this replaces read an empty
	// list as "every store in the system".
	if _, err := x.svc.MultiStoreForRange(ctx, nil, x.today()); !errors.Is(err, service.ErrNoStores) {
		t.Errorf("multi-store with no stores: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Multi-store
// ---------------------------------------------------------------------------

func TestMultiStoreKeepsAShopThatSoldNothing(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	var quiet uuid.UUID
	mustScan(t, x.db, `INSERT INTO stores (name, gstin) VALUES ('Quiet Branch', '29ZZZZZ1234F1Z5') RETURNING id`, &quiet)

	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, false)
	x.sale(billingsvc.CreateInput{Lines: []billingdomain.LineInput{line(rice, "2")}})

	rollup, err := x.svc.MultiStoreForRange(ctx, []uuid.UUID{x.storeID, quiet}, x.today())
	if err != nil {
		t.Fatalf("multi-store: %v", err)
	}
	if len(rollup.Stores) != 2 {
		t.Fatalf("rollup has %d stores, want 2 — the shop that took nothing was dropped", len(rollup.Stores))
	}
	if rollup.Stores[0].StoreID != x.storeID {
		t.Errorf("the busiest shop is not first: %+v", rollup.Stores)
	}
	wantMoney(t, "busy shop revenue", rollup.Stores[0].Revenue, "945.00")
	if !rollup.Stores[1].Revenue.IsZero() || rollup.Stores[1].InvoiceCount != 0 {
		t.Errorf("the quiet shop shows %s over %d invoices",
			rollup.Stores[1].Revenue, rollup.Stores[1].InvoiceCount)
	}
	wantMoney(t, "group revenue", rollup.Totals.Revenue, "945.00")
}

// ---------------------------------------------------------------------------
// Caching
// ---------------------------------------------------------------------------

// TestCachingServesHistoryButNeverACreditBalance is the test that pins the
// caching rule.
//
// A cached figure that only a human reads is fine a minute stale. A credit
// balance is not: someone reads it and then decides whether to let a customer
// take goods away without paying.
func TestCachingServesHistoryButNeverACreditBalance(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	// CleanupInterval is negative so no janitor goroutine outlives the test.
	c := cache.NewMemory(cache.MemoryOptions{CleanupInterval: -1})
	defer func() { _ = c.Close() }()
	svc := x.svc.WithCache(c, time.Minute)

	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, false)
	buyer := x.customer("Regular", "100000.00", "")
	today := x.today()

	x.sale(billingsvc.CreateInput{Lines: []billingdomain.LineInput{line(rice, "1")}})

	first, err := svc.SalesByCategory(ctx, x.storeID, today)
	if err != nil {
		t.Fatalf("sales by category: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("sales by category returned %d rows, want 1", len(first))
	}
	wantMoney(t, "cached category sales", first[0].Sales, "472.50")

	// A second sale, and a credit sale that changes what the customer owes.
	x.sale(billingsvc.CreateInput{Lines: []billingdomain.LineInput{line(rice, "1")}})
	onCredit := x.sale(billingsvc.CreateInput{
		Lines:       []billingdomain.LineInput{line(rice, "2")},
		CustomerID:  &buyer,
		PaymentMode: billingdomain.PaymentCredit,
	})

	// The history report is still the cached one. That is the bargain.
	again, err := svc.SalesByCategory(ctx, x.storeID, today)
	if err != nil {
		t.Fatalf("sales by category: %v", err)
	}
	wantMoney(t, "category sales within the TTL", again[0].Sales, "472.50")

	// The credit balance is not cached and never was. Nor is the aging report.
	dash, err := svc.DashboardForRange(ctx, x.storeID, today)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if !dash.CreditOutstanding.Equal(onCredit.Invoice.Total) {
		t.Errorf("dashboard credit outstanding = %s, want the live %s",
			dash.CreditOutstanding, onCredit.Invoice.Total)
	}
	aging, err := svc.CreditAging(ctx, x.storeID, x.day())
	if err != nil {
		t.Fatalf("credit aging: %v", err)
	}
	if !aging.TotalOutstanding.Equal(onCredit.Invoice.Total) {
		t.Errorf("credit aging total = %s, want the live %s",
			aging.TotalOutstanding, onCredit.Invoice.Total)
	}

	// Dropping the namespace makes the stale report catch up.
	if err := svc.InvalidateCache(ctx); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	fresh, err := svc.SalesByCategory(ctx, x.storeID, today)
	if err != nil {
		t.Fatalf("sales by category after invalidation: %v", err)
	}
	// 472.50 x 2 + 945.00.
	wantMoney(t, "category sales after invalidation", fresh[0].Sales, "1890.00")
}

func TestReportsRunLiveWhenNoCacheIsConfigured(t *testing.T) {
	t.Parallel()
	x := newShop(t)
	ctx := context.Background()

	rice := x.stockProduct("Rice", "450.00", "337.50", "100", 5, false)
	today := x.today()

	x.sale(billingsvc.CreateInput{Lines: []billingdomain.LineInput{line(rice, "1")}})
	before, err := x.svc.TopProducts(ctx, x.storeID, today, 10)
	if err != nil {
		t.Fatalf("top products: %v", err)
	}
	wantMoney(t, "sales before the second bill", before[0].Sales, "472.50")

	x.sale(billingsvc.CreateInput{Lines: []billingdomain.LineInput{line(rice, "1")}})
	after, err := x.svc.TopProducts(ctx, x.storeID, today, 10)
	if err != nil {
		t.Fatalf("top products: %v", err)
	}
	// Caching is off unless a composition root turns it on, so this is live.
	wantMoney(t, "sales after the second bill", after[0].Sales, "945.00")
}
