package postgres

import (
	"context"
	"fmt"
	"time"

	"gbos/internal/domain/analytics"
	"gbos/internal/domain/money"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// Analytics reads the aggregates behind the reports.
//
// # The status filter
//
// Eight of the aggregates ported into this file had no status filter at all.
// Dashboard revenue, GST collected, discount given, the invoice count and the
// average bill; sales by category; top products; hourly sales; the payment-mode
// breakdown; and the owner's multi-store rollup all counted voided invoices as
// takings. Voiding a sale returned the goods to stock and cancelled the
// receivable, and then the shop's own dashboard went on reporting the money.
//
// Every query below that reads `invoices` therefore carries
// `i.status = 'completed'`, and every one of them is covered by a test that
// raises real sales, voids one, and checks the report moved by exactly the
// voided invoice.
//
// # SQLite date functions
//
// The queries this file replaces were written for SQLite and used strftime,
// julianday and date() — none of which exist in Postgres. They are rewritten
// here with to_char/EXTRACT, plain date subtraction, and typed bounds. The
// wrappers are also gone from the predicates: `date(invoice_date) >= ?` cannot
// use idx_invoices_store_date because the indexed expression is the bare
// column, and invoice_date is a real DATE now, so the wrapper bought nothing
// and cost the index.
type Analytics struct{}

// NewAnalytics returns an analytics repository.
func NewAnalytics() *Analytics { return &Analytics{} }

// analyticsMaxTopProducts bounds the top-products listing.
const analyticsMaxTopProducts = 100

// analyticsDefaultTopProducts is the listing size when the caller asks for none.
const analyticsDefaultTopProducts = 20

// analyticsDate renders a business date as an unambiguous 'YYYY-MM-DD' literal
// for a `$n::date` bound parameter.
//
// Binding a time.Time directly would send a timestamptz, and comparing a DATE
// column against a timestamptz promotes the column — which both drags the
// session timezone into a date-only comparison and stops the comparison using
// the index on the bare column. A plain date string cast to `date` has neither
// problem.
func analyticsDate(t time.Time) string { return t.Format(analytics.DateLayout) }

// analyticsClampLimit keeps a caller-supplied listing size sane. The limit is
// bound as a parameter and never interpolated: the handler this replaces built
// its LIMIT with fmt.Sprintf.
func analyticsClampLimit(limit int) int {
	if limit <= 0 {
		return analyticsDefaultTopProducts
	}
	if limit > analyticsMaxTopProducts {
		return analyticsMaxTopProducts
	}
	return limit
}

// ---------------------------------------------------------------------------
// Invoice header aggregates
// ---------------------------------------------------------------------------

// SalesTotals returns the invoice-header aggregates for one store over a range
// of days. It feeds both the dashboard and the profit and loss report, so the
// two can never disagree about what a period's revenue was.
func (r *Analytics) SalesTotals(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng analytics.Range) (analytics.SalesTotals, error) {
	var t analytics.SalesTotals
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(i.sub_total), 0),
		        COALESCE(SUM(i.cgst), 0),
		        COALESCE(SUM(i.sgst), 0),
		        COALESCE(SUM(i.igst), 0),
		        COALESCE(SUM(i.discount), 0),
		        COALESCE(SUM(i.round_off), 0),
		        COALESCE(SUM(i.total), 0),
		        COUNT(*)
		   FROM invoices i
		  WHERE i.store_id = $1
		    AND i.status = 'completed'
		    AND i.invoice_date >= $2::date
		    AND i.invoice_date <= $3::date`,
		storeID, analyticsDate(rng.From), analyticsDate(rng.To)).
		Scan(&t.SubTotal, &t.CGST, &t.SGST, &t.IGST, &t.Discount, &t.RoundOff,
			&t.Total, &t.InvoiceCount)
	if err != nil {
		return analytics.SalesTotals{}, fmt.Errorf("read sales totals: %w", platform.MapError(err))
	}
	return t, nil
}

// LowStockCount counts the products in a store at or below their reorder level.
//
// Two Postgres rules bite here and both were violated by the SQLite original.
// The derived table it counted over had no alias, which Postgres rejects
// outright, so it is a CTE now. And `p.is_active = 1` is a type error against a
// boolean column.
func (r *Analytics) LowStockCount(ctx context.Context, q platform.Querier, storeID uuid.UUID) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx,
		`WITH low AS (
		     SELECT p.id
		       FROM products p
		       LEFT JOIN stock s ON s.product_id = p.id AND s.store_id = $1
		      WHERE p.is_active AND p.min_stock > 0
		      GROUP BY p.id, p.min_stock
		     HAVING COALESCE(SUM(s.quantity), 0) <= p.min_stock
		 )
		 SELECT COUNT(*) FROM low`, storeID).Scan(&n)
	if err != nil {
		// The handler this replaces swallowed this error and reported zero
		// products low on stock, which reads as "nothing to reorder".
		return 0, fmt.Errorf("count low stock: %w", platform.MapError(err))
	}
	return n, nil
}

// DashboardExtensions reads inventory valuation, expiring batches, pending POs, and vendor payables.
func (r *Analytics) DashboardExtensions(ctx context.Context, q platform.Querier, storeID uuid.UUID) (valuation money.Money, expiring int64, pendingPO int64, payables money.Money, err error) {
	row := q.QueryRowContext(ctx, `
		SELECT
			COALESCE((SELECT SUM(total_cost) FROM stock WHERE store_id = $1 AND quantity > 0), 0),
			COALESCE((SELECT COUNT(*) FROM stock WHERE store_id = $1 AND quantity > 0 AND expiry_date IS NOT NULL AND expiry_date <= CURRENT_DATE + INTERVAL '30 days'), 0),
			COALESCE((SELECT COUNT(*) FROM purchase_orders WHERE store_id = $1 AND status = 'pending'), 0),
			GREATEST(0,
				COALESCE((SELECT SUM(total) FROM purchase_orders WHERE store_id = $1 AND status != 'cancelled'), 0) -
				COALESCE((SELECT SUM(amount) FROM vendor_payments WHERE store_id = $1), 0)
			)
	`, storeID)
	var val, pay money.Money
	if err := row.Scan(&val, &expiring, &pendingPO, &pay); err != nil {
		return money.Zero(), 0, 0, money.Zero(), fmt.Errorf("read dashboard extensions: %w", platform.MapError(err))
	}
	return val, expiring, pendingPO, pay, nil
}

// ---------------------------------------------------------------------------
// Line-level aggregates
// ---------------------------------------------------------------------------

// SalesByCategory returns takings grouped by product category.
//
// The join to categories is a LEFT JOIN. products.category_id is nullable, and
// an inner join silently dropped the sales of every uncategorised product — so
// the rows of a "sales by category" report did not add up to the sales they
// were derived from, with no indication that anything was missing.
func (r *Analytics) SalesByCategory(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng analytics.Range) ([]analytics.CategorySales, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT c.id,
		        COALESCE(c.name, $4),
		        COALESCE(SUM(ii.total), 0)    AS sales,
		        COALESCE(SUM(ii.quantity), 0) AS quantity,
		        COUNT(*)                      AS line_count
		   FROM invoice_items ii
		   JOIN invoices i  ON i.id = ii.invoice_id
		   JOIN products p  ON p.id = ii.product_id
		   LEFT JOIN categories c ON c.id = p.category_id
		  WHERE i.store_id = $1
		    AND i.status = 'completed'
		    AND i.invoice_date >= $2::date
		    AND i.invoice_date <= $3::date
		  GROUP BY c.id, c.name
		  ORDER BY sales DESC, COALESCE(c.name, $4)`,
		storeID, analyticsDate(rng.From), analyticsDate(rng.To), analytics.UncategorisedLabel)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	out := make([]analytics.CategorySales, 0)
	for rows.Next() {
		var c analytics.CategorySales
		if err := rows.Scan(&c.CategoryID, &c.Category, &c.Sales, &c.Quantity, &c.LineCount); err != nil {
			// Never skip a row that failed to scan. The handler this replaces
			// did `continue`, so a report could quietly omit a category and
			// still look complete.
			return nil, fmt.Errorf("scan category sales: %w", platform.MapError(err))
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return out, nil
}

// TopProducts returns the best selling products of a store over a range.
func (r *Analytics) TopProducts(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng analytics.Range, limit int) ([]analytics.ProductSales, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT p.id, p.name,
		        COALESCE(SUM(ii.quantity), 0) AS quantity,
		        COALESCE(SUM(ii.total), 0)    AS sales
		   FROM invoice_items ii
		   JOIN invoices i ON i.id = ii.invoice_id
		   JOIN products p ON p.id = ii.product_id
		  WHERE i.store_id = $1
		    AND i.status = 'completed'
		    AND i.invoice_date >= $2::date
		    AND i.invoice_date <= $3::date
		  GROUP BY p.id, p.name
		  ORDER BY sales DESC, p.name
		  LIMIT $4`,
		storeID, analyticsDate(rng.From), analyticsDate(rng.To), analyticsClampLimit(limit))
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	out := make([]analytics.ProductSales, 0)
	for rows.Next() {
		var p analytics.ProductSales
		if err := rows.Scan(&p.ProductID, &p.Product, &p.Quantity, &p.Sales); err != nil {
			return nil, fmt.Errorf("scan product sales: %w", platform.MapError(err))
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return out, nil
}

// HourlySales returns one trading day's takings bucketed by hour.
//
// The hour comes from created_at, not from invoice_date. The SQLite schema kept
// invoice_date as a datetime string and the report read the hour out of it with
// strftime('%H', invoice_date); here invoice_date is a real DATE and carries no
// time of day at all, so reading the hour from it would put every sale in the
// day at midnight. created_at is the timestamptz that records when the sale was
// actually rung up.
//
// tz is an IANA zone name. A timestamptz has no hour until one is chosen, and
// choosing the server's happens to work only while the server sits in the same
// country as the shop.
func (r *Analytics) HourlySales(ctx context.Context, q platform.Querier, storeID uuid.UUID, day time.Time, tz string) ([]analytics.HourBucket, error) {
	rows, err := q.QueryContext(ctx,
		`WITH sales AS (
		     SELECT EXTRACT(HOUR FROM i.created_at AT TIME ZONE $3::text)::int AS hour,
		            i.total
		       FROM invoices i
		      WHERE i.store_id = $1
		        AND i.status = 'completed'
		        AND i.invoice_date = $2::date
		 )
		 SELECT hour, COUNT(*), COALESCE(SUM(total), 0)
		   FROM sales
		  GROUP BY hour
		  ORDER BY hour`,
		storeID, analyticsDate(day), tz)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	out := make([]analytics.HourBucket, 0)
	for rows.Next() {
		var (
			hour  int
			count int64
			sales money.Money
		)
		if err := rows.Scan(&hour, &count, &sales); err != nil {
			return nil, fmt.Errorf("scan hourly sales: %w", platform.MapError(err))
		}
		out = append(out, analytics.NewHourBucket(hour, count, sales))
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return out, nil
}

// PaymentModes returns takings split by how they were settled.
func (r *Analytics) PaymentModes(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng analytics.Range) ([]analytics.PaymentModeTotal, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT i.payment_mode, COUNT(*), COALESCE(SUM(i.total), 0) AS total
		   FROM invoices i
		  WHERE i.store_id = $1
		    AND i.status = 'completed'
		    AND i.invoice_date >= $2::date
		    AND i.invoice_date <= $3::date
		  GROUP BY i.payment_mode
		  ORDER BY total DESC, i.payment_mode`,
		storeID, analyticsDate(rng.From), analyticsDate(rng.To))
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	out := make([]analytics.PaymentModeTotal, 0)
	for rows.Next() {
		var p analytics.PaymentModeTotal
		if err := rows.Scan(&p.Mode, &p.InvoiceCount, &p.Total); err != nil {
			return nil, fmt.Errorf("scan payment mode total: %w", platform.MapError(err))
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return out, nil
}

// StoreSales returns one row per named store, whether or not it sold anything.
//
// The date range and the status filter live in the LEFT JOIN's ON clause, not
// in the WHERE. Moved to the WHERE they would turn the outer join back into an
// inner one, and a shop that took nothing would vanish from the rollup instead
// of showing a zero — which is the one thing an owner comparing shops needs to
// see.
func (r *Analytics) StoreSales(ctx context.Context, q platform.Querier, storeIDs []uuid.UUID, rng analytics.Range) ([]analytics.StoreSales, error) {
	if len(storeIDs) == 0 {
		return []analytics.StoreSales{}, nil
	}
	rows, err := q.QueryContext(ctx,
		`SELECT s.id, s.name,
		        COALESCE(SUM(i.total), 0)                    AS revenue,
		        COALESCE(SUM(i.cgst + i.sgst + i.igst), 0)   AS gst,
		        COUNT(i.id)                                  AS invoice_count
		   FROM stores s
		   LEFT JOIN invoices i
		          ON i.store_id = s.id
		         AND i.status = 'completed'
		         AND i.invoice_date >= $2::date
		         AND i.invoice_date <= $3::date
		  WHERE s.is_active
		    AND s.id = ANY($1)
		  GROUP BY s.id, s.name
		  ORDER BY revenue DESC, s.name`,
		storeIDs, analyticsDate(rng.From), analyticsDate(rng.To))
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	out := make([]analytics.StoreSales, 0, len(storeIDs))
	for rows.Next() {
		var s analytics.StoreSales
		if err := rows.Scan(&s.StoreID, &s.StoreName, &s.Revenue, &s.GST, &s.InvoiceCount); err != nil {
			return nil, fmt.Errorf("scan store sales: %w", platform.MapError(err))
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Profit and loss inputs
// ---------------------------------------------------------------------------

// COGS returns the cost of the goods sold in a period, summed from the value
// snapshotted on each invoice line when the goods left. Using today's cost price
// instead would make last month's profit move whenever a supplier changes a
// price.
//
// It sums cost_of_goods rather than unit_cost_at_sale times quantity. The
// product of a rounded per-unit average and a quantity is not the money that
// left inventory: a 30 kg sack at Rs.940 is Rs.31.3333... a kilo, and thirty
// times Rs.31.33 is Rs.939.90. Summing the stored total instead makes the cost
// of goods sold for a delivery add up to what the delivery cost.
func (r *Analytics) COGS(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng analytics.Range) (money.Money, error) {
	var total money.Money
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(ii.cost_of_goods), 0)
		   FROM invoice_items ii
		   JOIN invoices i ON i.id = ii.invoice_id
		  WHERE i.store_id = $1
		    AND i.status = 'completed'
		    AND i.invoice_date >= $2::date
		    AND i.invoice_date <= $3::date`,
		storeID, analyticsDate(rng.From), analyticsDate(rng.To)).Scan(&total)
	if err != nil {
		return money.Zero(), fmt.Errorf("read cost of goods sold: %w", platform.MapError(err))
	}
	return total, nil
}

// ReturnsTotal returns what was refunded to customers in a period.
//
// returns.created_at is a timestamptz, so the day filter is a half-open
// interval rather than a `date(created_at)` wrapper: the wrapper defeats any
// index on the column and, unlike the interval, has to be evaluated for every
// row in the table.
func (r *Analytics) ReturnsTotal(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng analytics.Range) (money.Money, error) {
	var total money.Money
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(rt.amount), 0)
		   FROM returns rt
		  WHERE rt.store_id = $1
		    AND rt.status = 'completed'
		    AND rt.created_at >= $2::date
		    AND rt.created_at < ($3::date + 1)`,
		storeID, analyticsDate(rng.From), analyticsDate(rng.To)).Scan(&total)
	if err != nil {
		return money.Zero(), fmt.Errorf("read returns total: %w", platform.MapError(err))
	}
	return total, nil
}

// InventoryLossValue returns the cost value of stock written off in a period,
// valued at the average cost of the batches the store still holds.
func (r *Analytics) InventoryLossValue(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng analytics.Range) (money.Money, error) {
	var total money.Money
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(il.quantity * COALESCE((
		            SELECT AVG(s.cost_price)
		              FROM stock s
		             WHERE s.product_id = il.product_id
		               AND s.store_id = il.store_id
		               AND s.cost_price > 0), 0)), 0)
		   FROM inventory_losses il
		  WHERE il.store_id = $1
		    AND il.created_at >= $2::date
		    AND il.created_at < ($3::date + 1)`,
		storeID, analyticsDate(rng.From), analyticsDate(rng.To)).Scan(&total)
	if err != nil {
		return money.Zero(), fmt.Errorf("read inventory loss value: %w", platform.MapError(err))
	}
	return total, nil
}

// ExpensesTotal returns what the store spent running itself in a period.
func (r *Analytics) ExpensesTotal(ctx context.Context, q platform.Querier, storeID uuid.UUID, rng analytics.Range) (money.Money, error) {
	var total money.Money
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(e.amount), 0)
		   FROM expenses e
		  WHERE e.store_id = $1
		    AND e.expense_date >= $2::date
		    AND e.expense_date <= $3::date`,
		storeID, analyticsDate(rng.From), analyticsDate(rng.To)).Scan(&total)
	if err != nil {
		return money.Zero(), fmt.Errorf("read expenses total: %w", platform.MapError(err))
	}
	return total, nil
}

// ---------------------------------------------------------------------------
// Credit aging
// ---------------------------------------------------------------------------

// CreditAging returns each customer's outstanding balance split by age, as of a
// date the caller supplies.
//
// Three things changed from the query this replaces.
//
// The twelve julianday('now') calls are gone. Postgres subtracts one date from
// another and yields a whole number of days, which is what an aging ladder
// means; julianday returns a fractional day count and the comparisons against
// it were therefore off by up to a day depending on the time of the request.
//
// asOf is a parameter rather than now(), so the report is deterministic and the
// buckets can be tested against known ages.
//
// The debt predicate is creditOpenStatuses, shared with every other reader of
// this ledger. The original said `ce.status != 'paid'`, which counts voided
// entries — cancelled sales — as money owed, and disagreed with the
// `status IN ('pending','partial')` used by the dashboard two hundred lines
// earlier in the same file. The type filter is there for the same reason: a
// payment row is written self-settled, and only an 'issue' row is a debt.
func (r *Analytics) CreditAging(ctx context.Context, q platform.Querier, storeID uuid.UUID, asOf time.Time) ([]analytics.CustomerAging, error) {
	rows, err := q.QueryContext(ctx,
		`WITH due AS (
		     SELECT c.id   AS customer_id,
		            c.name AS customer_name,
		            ce.amount - ce.paid_amount AS balance,
		            ($2::date - ce.created_at::date) AS age_days
		       FROM credit_entries ce
		       JOIN customers c ON c.id = ce.customer_id
		      WHERE c.store_id = $1
		        AND ce.type = 'issue'
		        AND ce.status IN `+creditOpenStatuses+`
		 )
		 SELECT customer_id, customer_name,
		        COALESCE(SUM(balance), 0) AS total_due,
		        COALESCE(SUM(balance) FILTER (WHERE age_days <= $3), 0) AS current,
		        COALESCE(SUM(balance) FILTER (WHERE age_days > $3 AND age_days <= $4), 0) AS days_30,
		        COALESCE(SUM(balance) FILTER (WHERE age_days > $4 AND age_days <= $5), 0) AS days_60,
		        COALESCE(SUM(balance) FILTER (WHERE age_days > $5), 0) AS over_90
		   FROM due
		  GROUP BY customer_id, customer_name
		 HAVING SUM(balance) > 0
		  ORDER BY total_due DESC, customer_name`,
		storeID, analyticsDate(asOf),
		analytics.AgeCurrentDays, analytics.AgeDays30, analytics.AgeDays60)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	out := make([]analytics.CustomerAging, 0)
	for rows.Next() {
		var a analytics.CustomerAging
		if err := rows.Scan(&a.CustomerID, &a.CustomerName, &a.TotalDue,
			&a.Current, &a.Days30, &a.Days60, &a.Over90); err != nil {
			return nil, fmt.Errorf("scan credit aging row: %w", platform.MapError(err))
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// GSTR-3B
// ---------------------------------------------------------------------------

// OutwardSupplies returns the month's sales tax position. from..to is
// half-open.
//
// The taxable value is sub_total, not `sub_total - discount` as the query this
// replaces had it. Under the SQLite schema sub_total was a client-supplied
// gross figure; here it is computed by billing.Compute as the sum of the line
// taxable amounts, each already net of its line discount and its share of the
// bill discount. Subtracting the discount column again would take every
// discount off twice and under-declare the outward supply — and, since the tax
// columns are computed on the real taxable value, the return would no longer
// even be internally consistent.
func (r *Analytics) OutwardSupplies(ctx context.Context, q platform.Querier, storeID uuid.UUID, from, to time.Time) (analytics.TaxSummary, error) {
	var s analytics.TaxSummary
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(i.sub_total), 0),
		        COALESCE(SUM(i.cgst), 0),
		        COALESCE(SUM(i.sgst), 0),
		        COALESCE(SUM(i.igst), 0)
		   FROM invoices i
		  WHERE i.store_id = $1
		    AND i.status = 'completed'
		    AND i.invoice_date >= $2::date
		    AND i.invoice_date <  $3::date`,
		storeID, analyticsDate(from), analyticsDate(to)).
		Scan(&s.Taxable, &s.CGST, &s.SGST, &s.IGST)
	if err != nil {
		// The handler this replaces wrote `_ = db.DB.QueryRow(...).Scan(...)`.
		// A failed read left every figure at zero and the response reported
		// those zeros as the month's real tax position.
		return analytics.TaxSummary{}, fmt.Errorf("read outward supplies: %w", platform.MapError(err))
	}
	return s, nil
}

// InwardSupplies returns the month's purchase tax position — the input credit
// side of the return.
//
// The status filter is the fix. Without it a cancelled purchase order was
// claimed as input tax credit: the order was cancelled, no goods arrived, no
// money moved, and the shop still reduced its declared GST liability by the tax
// on it. Under-declaring output tax is a recoverable arithmetic slip; claiming
// credit for a purchase that never happened is the kind of thing an assessment
// notice is written about.
//
// The taxable value is purchase_orders.sub_total for the same reason as the
// outward side: purchasing accumulates it from each line's taxable amount,
// already net of that line's discount.
//
// Only 'cancelled' is excluded. Strictly, input credit under section 16(2) of
// the CGST Act arises on receipt of the goods, so a 'pending' order is early —
// but narrowing this to 'received' would change the figure the business
// currently reports for reasons beyond the defect at hand, and it belongs to
// whoever reconciles this against the purchase register.
func (r *Analytics) InwardSupplies(ctx context.Context, q platform.Querier, storeID uuid.UUID, from, to time.Time) (analytics.TaxSummary, error) {
	var s analytics.TaxSummary
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(po.sub_total), 0),
		        COALESCE(SUM(po.cgst), 0),
		        COALESCE(SUM(po.sgst), 0),
		        COALESCE(SUM(po.igst), 0)
		   FROM purchase_orders po
		  WHERE po.store_id = $1
		    AND po.status <> 'cancelled'
		    AND po.order_date >= $2::date
		    AND po.order_date <  $3::date`,
		storeID, analyticsDate(from), analyticsDate(to)).
		Scan(&s.Taxable, &s.CGST, &s.SGST, &s.IGST)
	if err != nil {
		return analytics.TaxSummary{}, fmt.Errorf("read inward supplies: %w", platform.MapError(err))
	}
	return s, nil
}
