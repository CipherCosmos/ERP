package handlers

import (
	"database/sql"
	"math"
	"net/http"
	"strings"
	"time"

	"gbos/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type PaymentHandler struct{}

func NewPaymentHandler() *PaymentHandler {
	return &PaymentHandler{}
}

type SplitPaymentRequest struct {
	InvoiceID string            `json:"invoice_id" binding:"required"`
	Payments  []PaymentEntryReq `json:"payments" binding:"required"`
}

type PaymentEntryReq struct {
	PaymentMode string  `json:"payment_mode" binding:"required"`
	Amount      float64 `json:"amount" binding:"required"`
	Reference   string  `json:"reference"`
}

func (h *PaymentHandler) AddSplitPayments(c *gin.Context) {
	var req SplitPaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var invTotal float64
	var invStore string
	err = tx.QueryRow(`SELECT total, store_id FROM invoices WHERE id = ?`, req.InvoiceID).Scan(&invTotal, &invStore)
	if err == sql.ErrNoRows {
		tx.Rollback()
		c.JSON(http.StatusNotFound, gin.H{"error": "invoice not found"})
		return
	}
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !sessionMayAccessStore(c, invStore) {
		tx.Rollback()
		abortStoreMismatch(c)
		return
	}

	var totalPaid float64
	for _, p := range req.Payments {
		paymentID := uuid.New().String()
		_, err = tx.Exec(`INSERT INTO invoice_payments (id, invoice_id, payment_mode, amount, reference)
			VALUES (?, ?, ?, ?, ?)`,
			paymentID, req.InvoiceID, p.PaymentMode, p.Amount, p.Reference)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add payment"})
			return
		}
		totalPaid += p.Amount
	}

	if math.Abs(totalPaid-invTotal) > 0.05 {
		tx.Rollback()
		c.JSON(http.StatusBadRequest, gin.H{"error": "split payment total must match invoice total"})
		return
	}

	tx.Commit()

	c.JSON(http.StatusCreated, gin.H{
		"invoice_id": req.InvoiceID,
		"total_paid": totalPaid,
	})
}

func (h *PaymentHandler) GetInvoicePayments(c *gin.Context) {
	invoiceID := c.Param("id")

	var invStore string
	err := db.DB.QueryRow(`SELECT store_id FROM invoices WHERE id = ?`, invoiceID).Scan(&invStore)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "invoice not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !sessionMayAccessStore(c, invStore) {
		c.JSON(http.StatusNotFound, gin.H{"error": "invoice not found"})
		return
	}

	rows, err := db.DB.Query(`SELECT id, payment_mode, amount, reference, created_at 
		FROM invoice_payments WHERE invoice_id = ?`, invoiceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var payments []map[string]interface{}
	for rows.Next() {
		var id, paymentMode, reference string
		var amount float64
		var createdAt time.Time

		rows.Scan(&id, &paymentMode, &amount, &reference, &createdAt)

		payments = append(payments, map[string]interface{}{
			"id": id, "payment_mode": paymentMode, "amount": amount,
			"reference": reference, "created_at": createdAt,
		})
	}

	c.JSON(http.StatusOK, payments)
}

type SettlementReportRequest struct {
	StoreID string `form:"store_id" binding:"required"`
	Date    string `form:"date"`
}

type SettlementReport struct {
	Date               string  `json:"date"`
	TotalSales         float64 `json:"total_sales"`
	TotalInvoices      int     `json:"total_invoices"`
	CashTotal          float64 `json:"cash_total"`
	UPITotal           float64 `json:"upi_total"`
	CardTotal          float64 `json:"card_total"`
	CreditTotal        float64 `json:"credit_total"`
	BankTotal          float64 `json:"bank_total"`
	CashCount          int     `json:"cash_count"`
	UPICount           int     `json:"upi_count"`
	CardCount          int     `json:"card_count"`
	CreditCount        int     `json:"credit_count"`
	BankCount          int     `json:"bank_count"`
	ReturnsTotal       float64 `json:"returns_total"`
	NetCollections     float64 `json:"net_collections"`
	SplitBreakdownUsed bool    `json:"split_breakdown_used"`
}

func normalizePayMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	switch m {
	case "cash":
		return "cash"
	case "upi", "phonepe", "gpay", "googlepay", "paytm":
		return "upi"
	case "card", "debit", "credit_card":
		return "card"
	case "credit", "udhar":
		return "credit"
	case "bank", "bank_transfer", "neft", "rtgs", "imps", "netbanking":
		return "bank"
	default:
		return m
	}
}

func (h *PaymentHandler) GetSettlementReport(c *gin.Context) {
	storeID := c.Query("store_id")
	date := c.DefaultQuery("date", time.Now().Format("2006-01-02"))

	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}

	startDate := date + " 00:00:00"
	endDate := date + " 23:59:59"

	var totalSales float64
	var totalInvoices int
	err := db.DB.QueryRow(`
		SELECT COALESCE(SUM(total), 0), COUNT(*)
		FROM invoices 
		WHERE store_id = ? AND invoice_date >= ? AND invoice_date <= ? AND status = 'completed'`,
		storeID, startDate, endDate).Scan(&totalSales, &totalInvoices)
	if err != nil && err != sql.ErrNoRows {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var cashTotal, upiTotal, cardTotal, creditTotal, bankTotal float64
	var cashCount, upiCount, cardCount, creditCount, bankCount int
	splitUsed := false

	rows, err := db.DB.Query(`
		SELECT LOWER(TRIM(ip.payment_mode)) as pm, SUM(ip.amount) as amt, COUNT(*) as cnt
		FROM invoice_payments ip
		INNER JOIN invoices i ON i.id = ip.invoice_id
		WHERE i.store_id = ? AND i.invoice_date >= ? AND i.invoice_date <= ? AND i.status = 'completed'
		GROUP BY LOWER(TRIM(ip.payment_mode))`,
		storeID, startDate, endDate)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var pm string
			var amt float64
			var cnt int
			if err := rows.Scan(&pm, &amt, &cnt); err != nil {
				continue
			}
			splitUsed = true
			switch normalizePayMode(pm) {
			case "cash":
				cashTotal += amt
				cashCount += cnt
			case "upi":
				upiTotal += amt
				upiCount += cnt
			case "card":
				cardTotal += amt
				cardCount += cnt
			case "credit":
				creditTotal += amt
				creditCount += cnt
			case "bank":
				bankTotal += amt
				bankCount += cnt
			}
		}
	}

	rows2, err := db.DB.Query(`
		SELECT LOWER(TRIM(i.payment_mode)) as pm, SUM(i.total) as amt, COUNT(*) as cnt
		FROM invoices i
		WHERE i.store_id = ? AND i.invoice_date >= ? AND i.invoice_date <= ? AND i.status = 'completed'
		AND NOT EXISTS (SELECT 1 FROM invoice_payments p WHERE p.invoice_id = i.id)
		GROUP BY LOWER(TRIM(i.payment_mode))`,
		storeID, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows2.Close()

	for rows2.Next() {
		var pm string
		var amt float64
		var cnt int
		if err := rows2.Scan(&pm, &amt, &cnt); err != nil {
			continue
		}
		switch normalizePayMode(pm) {
		case "cash":
			cashTotal += amt
			cashCount += cnt
		case "upi":
			upiTotal += amt
			upiCount += cnt
		case "card":
			cardTotal += amt
			cardCount += cnt
		case "credit":
			creditTotal += amt
			creditCount += cnt
		case "bank":
			bankTotal += amt
			bankCount += cnt
		case "split":
			// invoice marked split but no rows yet — ignore here
		default:
			// unknown header mode: treat as cash for settlement visibility
			cashTotal += amt
			cashCount += cnt
		}
	}

	var returnsTotal float64
	db.DB.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM returns 
		WHERE store_id = ? AND date(created_at) = ? AND status = 'completed'`,
		storeID, date).Scan(&returnsTotal)

	net := cashTotal + upiTotal + cardTotal + bankTotal - returnsTotal

	report := SettlementReport{
		Date:               date,
		TotalSales:         totalSales,
		TotalInvoices:      totalInvoices,
		CashTotal:          cashTotal,
		UPITotal:           upiTotal,
		CardTotal:          cardTotal,
		CreditTotal:        creditTotal,
		BankTotal:          bankTotal,
		CashCount:          cashCount,
		UPICount:           upiCount,
		CardCount:          cardCount,
		CreditCount:        creditCount,
		BankCount:          bankCount,
		ReturnsTotal:       returnsTotal,
		NetCollections:     net,
		SplitBreakdownUsed: splitUsed,
	}

	c.JSON(http.StatusOK, report)
}
