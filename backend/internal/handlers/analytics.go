package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gbos/internal/db"
)

type AnalyticsHandler struct{}

func NewAnalyticsHandler() *AnalyticsHandler {
	return &AnalyticsHandler{}
}

func (h *AnalyticsHandler) Dashboard(c *gin.Context) {
	storeID := c.Query("store_id")
	dateRange := c.DefaultQuery("range", "today")

	var startDate, endDate string
	now := time.Now()
	switch dateRange {
	case "today":
		startDate = now.Format("2006-01-02")
		endDate = startDate
	case "week":
		startDate = now.AddDate(0, 0, -7).Format("2006-01-02")
		endDate = now.Format("2006-01-02")
	case "month":
		startDate = now.AddDate(0, -1, 0).Format("2006-01-02")
		endDate = now.Format("2006-01-02")
	case "year":
		startDate = now.AddDate(-1, 0, 0).Format("2006-01-02")
		endDate = now.Format("2006-01-02")
	default:
		startDate = now.Format("2006-01-02")
		endDate = startDate
	}

	var revenue, gstCollected, discountGiven float64
	var invoiceCount, transactionCount int

	err := db.DB.QueryRow(`SELECT COALESCE(SUM(total), 0), COALESCE(SUM(cgst + sgst + igst), 0), 
		COALESCE(SUM(discount), 0), COUNT(DISTINCT id), COUNT(id)
		FROM invoices WHERE store_id = ? AND invoice_date >= ? AND invoice_date <= ?`,
		storeID, startDate, endDate+" 23:59:59").Scan(&revenue, &gstCollected, &discountGiven, &invoiceCount, &transactionCount)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var lowStockCount int
	db.DB.QueryRow(`SELECT COUNT(*) FROM (
		SELECT p.id FROM products p
		LEFT JOIN stock s ON p.id = s.product_id AND s.store_id = ?
		GROUP BY p.id HAVING COALESCE(SUM(s.quantity), 0) <= p.min_stock
	)`, storeID).Scan(&lowStockCount)

	var creditOutStanding float64
	db.DB.QueryRow(`SELECT COALESCE(SUM(amount - paid_amount), 0) FROM credit_entries WHERE status != 'paid'`).Scan(&creditOutStanding)

	c.JSON(http.StatusOK, map[string]interface{}{
		"revenue":         revenue,
		"gst_collected":   gstCollected,
		"discount_given":  discountGiven,
		"invoice_count":  invoiceCount,
		"transaction_count": transactionCount,
		"avg_bill_value":  func() float64 { if invoiceCount > 0 { return revenue / float64(invoiceCount) }; return 0 }(),
		"low_stock_count": lowStockCount,
		"credit_outstanding": creditOutStanding,
		"date_range":      map[string]string{"start": startDate, "end": endDate},
	})
}

func (h *AnalyticsHandler) SalesByCategory(c *gin.Context) {
	storeID := c.Query("store_id")
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")

	query := `
		SELECT c.name, SUM(ii.total) as sales, COUNT(*) as item_count
		FROM invoice_items ii
		JOIN invoices i ON ii.invoice_id = i.id
		JOIN products p ON ii.product_id = p.id
		JOIN categories c ON p.category_id = c.id
		WHERE i.store_id = ?`
	args := []interface{}{storeID}

	if startDate != "" {
		query += " AND i.invoice_date >= ?"
		args = append(args, startDate)
	}
	if endDate != "" {
		query += " AND i.invoice_date <= ?"
		args = append(args, endDate+" 23:59:59")
	}
	query += " GROUP BY c.id, c.name ORDER BY sales DESC"

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var categorySales []map[string]interface{}
	for rows.Next() {
		var name string
		var sales float64
		var itemCount int
		rows.Scan(&name, &sales, &itemCount)
		categorySales = append(categorySales, map[string]interface{}{
			"category": name, "sales": sales, "item_count": itemCount,
		})
	}

	c.JSON(http.StatusOK, categorySales)
}

func (h *AnalyticsHandler) TopProducts(c *gin.Context) {
	storeID := c.Query("store_id")
	limit := c.DefaultQuery("limit", "20")
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")

	query := `
		SELECT p.name, SUM(ii.quantity) as qty, SUM(ii.total) as sales
		FROM invoice_items ii
		JOIN invoices i ON ii.invoice_id = i.id
		JOIN products p ON ii.product_id = p.id
		WHERE i.store_id = ?`
	args := []interface{}{storeID}

	if startDate != "" {
		query += " AND i.invoice_date >= ?"
		args = append(args, startDate)
	}
	if endDate != "" {
		query += " AND i.invoice_date <= ?"
		args = append(args, endDate+" 23:59:59")
	}
	query += fmt.Sprintf(" GROUP BY p.id ORDER BY sales DESC LIMIT %s", limit)

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var topProducts []map[string]interface{}
	for rows.Next() {
		var name string
		var qty, sales float64
		rows.Scan(&name, &qty, &sales)
		topProducts = append(topProducts, map[string]interface{}{
			"product": name, "quantity": qty, "sales": sales,
		})
	}

	c.JSON(http.StatusOK, topProducts)
}

func (h *AnalyticsHandler) HourlySales(c *gin.Context) {
	storeID := c.Query("store_id")
	date := c.DefaultQuery("date", time.Now().Format("2006-01-02"))

	rows, err := db.DB.Query(`
		SELECT strftime('%H', invoice_date) as hour, COUNT(*) as count, SUM(total) as sales
		FROM invoices
		WHERE store_id = ? AND date(invoice_date) = ?
		GROUP BY hour
		ORDER BY hour`, storeID, date)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var hourlySales []map[string]interface{}
	for rows.Next() {
		var hour string
		var count int
		var sales float64
		rows.Scan(&hour, &count, &sales)
		hourlySales = append(hourlySales, map[string]interface{}{
			"hour": hour + ":00", "count": count, "sales": sales,
		})
	}

	c.JSON(http.StatusOK, hourlySales)
}

func (h *AnalyticsHandler) PaymentModeBreakup(c *gin.Context) {
	storeID := c.Query("store_id")
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")

	query := `SELECT payment_mode, COUNT(*) as count, SUM(total) as total FROM invoices WHERE store_id = ?`
	args := []interface{}{storeID}

	if startDate != "" {
		query += " AND invoice_date >= ?"
		args = append(args, startDate)
	}
	if endDate != "" {
		query += " AND invoice_date <= ?"
		args = append(args, endDate+" 23:59:59")
	}
	query += " GROUP BY payment_mode"

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var paymentBreakup []map[string]interface{}
	for rows.Next() {
		var mode string
		var count int
		var total float64
		rows.Scan(&mode, &count, &total)
		paymentBreakup = append(paymentBreakup, map[string]interface{}{
			"mode": mode, "count": count, "total": total,
		})
	}

	c.JSON(http.StatusOK, paymentBreakup)
}

type AuditHandler struct{}

func NewAuditHandler() *AuditHandler {
	return &AuditHandler{}
}

func (h *AuditHandler) List(c *gin.Context) {
	storeID := c.Query("store_id")
	userID := c.Query("user_id")
	tableName := c.Query("table")
	limit := c.DefaultQuery("limit", "100")

	query := `SELECT al.id, al.user_id, u.username, al.store_id, al.action, 
		al.table_name, al.record_id, al.old_value, al.new_value, al.created_at
		FROM audit_logs al
		JOIN users u ON al.user_id = u.id
		WHERE 1=1`
	args := []interface{}{}

	if storeID != "" {
		query += " AND al.store_id = ?"
		args = append(args, storeID)
	}
	if userID != "" {
		query += " AND al.user_id = ?"
		args = append(args, userID)
	}
	if tableName != "" {
		query += " AND al.table_name = ?"
		args = append(args, tableName)
	}
	query += " ORDER BY al.created_at DESC LIMIT " + limit

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var logs []map[string]interface{}
	for rows.Next() {
		var id, userID, username, storeID, action, tableName, recordID string
		var oldValue, newValue sql.NullString
		var createdAt time.Time

		rows.Scan(&id, &userID, &username, &storeID, &action, &tableName, &recordID, &oldValue, &newValue, &createdAt)
		logs = append(logs, map[string]interface{}{
			"id": id, "user_id": userID, "username": username,
			"store_id": storeID, "action": action, "table_name": tableName,
			"record_id": recordID, "old_value": oldValue.String,
			"new_value": newValue.String, "created_at": createdAt,
		})
	}

	c.JSON(http.StatusOK, logs)
}

func (h *AuditHandler) Log(c *gin.Context) {
	var req struct {
		UserID    string `json:"user_id" binding:"required"`
		StoreID   string `json:"store_id" binding:"required"`
		Action    string `json:"action" binding:"required"`
		TableName string `json:"table_name"`
		RecordID  string `json:"record_id"`
		OldValue  string `json:"old_value"`
		NewValue  string `json:"new_value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	_, err := db.DB.Exec(`INSERT INTO audit_logs (id, user_id, store_id, action, table_name, record_id, old_value, new_value)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		fmt.Sprintf("AUD-%d", time.Now().UnixNano()), req.UserID, req.StoreID,
		req.Action, req.TableName, req.RecordID, req.OldValue, req.NewValue)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

type ReportHandler struct{}

func NewReportHandler() *ReportHandler {
	return &ReportHandler{}
}

type ProfitLossReport struct {
	StartDate      string  `json:"start_date"`
	EndDate        string  `json:"end_date"`
	TotalRevenue   float64 `json:"total_revenue"`
	TotalCOGS      float64 `json:"total_cogs"`
	GrossProfit    float64 `json:"gross_profit"`
	GrossMargin    float64 `json:"gross_margin_percent"`
	TotalReturns   float64 `json:"total_returns"`
	NetRevenue     float64 `json:"net_revenue"`
	TotalLosses    float64 `json:"total_losses"`
	TotalExpenses  float64 `json:"total_expenses"`
	NetProfit      float64 `json:"net_profit"`
}

func (h *ReportHandler) GetProfitLoss(c *gin.Context) {
	storeID := c.Query("store_id")
	startDate := c.DefaultQuery("start_date", time.Now().AddDate(0, -1, 0).Format("2006-01-02"))
	endDate := c.DefaultQuery("end_date", time.Now().Format("2006-01-02"))

	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}

	var totalRevenue, totalCGST, totalSGST float64
	err := db.DB.QueryRow(`SELECT COALESCE(SUM(total), 0), COALESCE(SUM(cgst), 0), COALESCE(SUM(sgst), 0)
		FROM invoices WHERE store_id = ? AND invoice_date >= ? AND invoice_date <= ? AND status = 'completed'`,
		storeID, startDate, endDate+" 23:59:59").Scan(&totalRevenue, &totalCGST, &totalSGST)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var totalCOGS float64
	db.DB.QueryRow(`SELECT COALESCE(SUM(s.cost_price * ii.quantity), 0)
		FROM invoice_items ii
		JOIN invoices i ON ii.invoice_id = i.id
		JOIN stock s ON ii.product_id = s.product_id AND i.store_id = s.store_id
		WHERE i.store_id = ? AND i.invoice_date >= ? AND i.invoice_date <= ?`,
		storeID, startDate, endDate+" 23:59:59").Scan(&totalCOGS)

	var totalReturns float64
	db.DB.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM returns 
		WHERE store_id = ? AND created_at >= ? AND created_at <= ? AND status = 'completed'`,
		storeID, startDate, endDate+" 23:59:59").Scan(&totalReturns)

	var totalLosses float64
	db.DB.QueryRow(`SELECT COALESCE(SUM(quantity * (SELECT cost_price FROM stock WHERE product_id = inventory_losses.product_id LIMIT 1)), 0)
		FROM inventory_losses WHERE store_id = ? AND created_at >= ? AND created_at <= ?`,
		storeID, startDate, endDate+" 23:59:59").Scan(&totalLosses)

	var totalExpenses float64
	db.DB.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM expenses 
		WHERE store_id = ? AND expense_date >= ? AND expense_date <= ?`,
		storeID, startDate, endDate).Scan(&totalExpenses)

	taxableRevenue := totalRevenue - totalCGST - totalSGST
	grossProfit := taxableRevenue - totalCOGS
	grossMargin := 0.0
	if taxableRevenue > 0 {
		grossMargin = (grossProfit / taxableRevenue) * 100
	}
	netProfit := grossProfit - totalReturns - totalLosses - totalExpenses

	report := ProfitLossReport{
		StartDate:      startDate,
		EndDate:        endDate,
		TotalRevenue:   totalRevenue,
		TotalCOGS:      totalCOGS,
		GrossProfit:    grossProfit,
		GrossMargin:    grossMargin,
		TotalReturns:   totalReturns,
		NetRevenue:     totalRevenue - totalReturns,
		TotalLosses:    totalLosses,
		TotalExpenses:  totalExpenses,
		NetProfit:      netProfit,
	}

	c.JSON(http.StatusOK, report)
}

type CreditAgingReport struct {
	CustomerID   string  `json:"customer_id"`
	CustomerName string  `json:"customer_name"`
	TotalDue     float64 `json:"total_due"`
	Current      float64 `json:"current"`
	Days30       float64 `json:"days_30"`
	Days60       float64 `json:"days_60"`
	Days90       float64 `json:"days_90"`
	Over90       float64 `json:"over_90"`
}

func (h *ReportHandler) GetCreditAging(c *gin.Context) {
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}

	rows, err := db.DB.Query(`
		SELECT c.id, c.name,
			COALESCE(SUM(ce.amount - ce.paid_amount), 0) as total_due,
			COALESCE(SUM(CASE WHEN julianday('now') - julianday(ce.created_at) <= 30 THEN ce.amount - ce.paid_amount ELSE 0 END), 0) as current,
			COALESCE(SUM(CASE WHEN julianday('now') - julianday(ce.created_at) > 30 AND julianday('now') - julianday(ce.created_at) <= 60 THEN ce.amount - ce.paid_amount ELSE 0 END), 0) as days30,
			COALESCE(SUM(CASE WHEN julianday('now') - julianday(ce.created_at) > 60 AND julianday('now') - julianday(ce.created_at) <= 90 THEN ce.amount - ce.paid_amount ELSE 0 END), 0) as days60,
			COALESCE(SUM(CASE WHEN julianday('now') - julianday(ce.created_at) > 90 THEN ce.amount - ce.paid_amount ELSE 0 END), 0) as over90
		FROM credit_entries ce
		JOIN customers c ON ce.customer_id = c.id
		WHERE ce.status != 'paid' AND ce.amount - ce.paid_amount > 0
		  AND c.store_id = ?
		GROUP BY c.id, c.name
		ORDER BY total_due DESC`, storeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var aging []CreditAgingReport
	var totalDue float64
	for rows.Next() {
		var r CreditAgingReport
		rows.Scan(&r.CustomerID, &r.CustomerName, &r.TotalDue, &r.Current, &r.Days30, &r.Days60, &r.Over90)
		aging = append(aging, r)
		totalDue += r.TotalDue
	}

	c.JSON(http.StatusOK, map[string]interface{}{
		"report": aging,
		"total_outstanding": totalDue,
	})
}

// GetGSTR3B returns a month-level summary of outward (sales) vs inward (purchases) tax for internal GSTR-3B prep.
func (h *ReportHandler) GetGSTR3B(c *gin.Context) {
	storeID := c.Query("store_id")
	month := c.Query("month")
	if storeID == "" || month == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id and month required"})
		return
	}

	startDate := month + "-01"
	year, monthNum := 0, 0
	fmt.Sscanf(month, "%d-%d", &year, &monthNum)
	var endDate string
	if monthNum == 12 {
		endDate = fmt.Sprintf("%d-01-01", year+1)
	} else {
		endDate = fmt.Sprintf("%d-%02d-01", year, monthNum+1)
	}

	var outTaxable, outCGST, outSGST, outIGST float64
	_ = db.DB.QueryRow(`SELECT 
		COALESCE(SUM(sub_total - discount), 0),
		COALESCE(SUM(cgst), 0),
		COALESCE(SUM(sgst), 0),
		COALESCE(SUM(igst), 0)
		FROM invoices WHERE store_id = ? AND status = 'completed' 
		AND invoice_date >= ? AND invoice_date < ?`,
		storeID, startDate, endDate).Scan(&outTaxable, &outCGST, &outSGST, &outIGST)

	var inTaxable, inCGST, inSGST, inIGST float64
	_ = db.DB.QueryRow(`SELECT 
		COALESCE(SUM(sub_total - discount), 0),
		COALESCE(SUM(cgst), 0),
		COALESCE(SUM(sgst), 0),
		COALESCE(SUM(igst), 0)
		FROM purchase_orders WHERE store_id = ? 
		AND order_date >= ? AND order_date < ?`,
		storeID, startDate, endDate).Scan(&inTaxable, &inCGST, &inSGST, &inIGST)

	outTotalTax := outCGST + outSGST + outIGST
	inTotalTax := inCGST + inSGST + inIGST

	c.JSON(http.StatusOK, gin.H{
		"month":    month,
		"store_id": storeID,
		"outward_supplies": gin.H{
			"taxable_value": outTaxable,
			"cgst":          outCGST, "sgst": outSGST, "igst": outIGST,
			"total_tax": outTotalTax,
		},
		"inward_supplies": gin.H{
			"taxable_value": inTaxable,
			"cgst":          inCGST, "sgst": inSGST, "igst": inIGST,
			"total_tax": inTotalTax,
		},
		"net_tax_payable_hint": outTotalTax - inTotalTax,
		"note":                 "Internal summary only; validate against books and GST portal before filing GSTR-3B.",
	})
}