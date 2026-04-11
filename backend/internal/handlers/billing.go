package handlers

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"gbos/internal/db"
	"gbos/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type InvoiceHandler struct{}

func NewInvoiceHandler() *InvoiceHandler {
	return &InvoiceHandler{}
}

// deductStockFromSale removes qty from stock (FIFO when batch hint missing or invalid) and returns
// the batch and unit cost_price actually consumed for COGS snapshotting on invoice_lines.
func deductStockFromSale(tx *sql.Tx, productID, storeID, batchHint string, qty float64) (actualBatch string, unitCost float64, err error) {
	if qty <= 0 {
		return "DEFAULT", 0, nil
	}
	if batchHint != "" && batchHint != "DEFAULT" {
		var cp float64
		var b sql.NullString
		qerr := tx.QueryRow(`SELECT cost_price, batch_no FROM stock
			WHERE product_id = ? AND store_id = ? AND batch_no = ? AND quantity >= ?`,
			productID, storeID, batchHint, qty).Scan(&cp, &b)
		if qerr == nil {
			res, e2 := tx.Exec(`UPDATE stock SET quantity = quantity - ? WHERE product_id = ? AND store_id = ? AND batch_no = ? AND quantity >= ?`,
				qty, productID, storeID, batchHint, qty)
			if e2 != nil {
				return "", 0, e2
			}
			n, _ := res.RowsAffected()
			if n == 1 {
				ab := batchHint
				if b.Valid && b.String != "" {
					ab = b.String
				}
				return ab, cp, nil
			}
		} else if qerr != sql.ErrNoRows {
			return "", 0, qerr
		}
	}
	var rowID string
	var cp float64
	var b sql.NullString
	err = tx.QueryRow(`
		SELECT id, cost_price, batch_no FROM stock
		WHERE product_id = ? AND store_id = ? AND quantity >= ?
		ORDER BY
			CASE WHEN expiry_date IS NULL OR TRIM(expiry_date) = '' THEN 1 ELSE 0 END ASC,
			expiry_date ASC,
			datetime(created_at) ASC
		LIMIT 1`, productID, storeID, qty).Scan(&rowID, &cp, &b)
	if err == sql.ErrNoRows {
		return "", 0, fmt.Errorf("insufficient stock for product %s", productID)
	}
	if err != nil {
		return "", 0, err
	}
	res, err := tx.Exec(`UPDATE stock SET quantity = quantity - ? WHERE id = ? AND quantity >= ?`, qty, rowID, qty)
	if err != nil {
		return "", 0, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return "", 0, fmt.Errorf("insufficient stock for product %s", productID)
	}
	actualBatch = "DEFAULT"
	if b.Valid && b.String != "" {
		actualBatch = b.String
	}
	return actualBatch, cp, nil
}

func (h *InvoiceHandler) List(c *gin.Context) {
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}

	startDate := c.Query("start_date")
	endDate := c.Query("end_date")
	limit := c.DefaultQuery("limit", "100")

	query := `
		SELECT id, invoice_no, store_id, user_id, customer_id, invoice_date, 
		       sub_total, cgst, sgst, igst, discount, round_off, total, status, payment_mode
		FROM invoices WHERE store_id = ?`
	args := []interface{}{storeID}

	if startDate != "" {
		query += " AND invoice_date >= ?"
		args = append(args, startDate)
	}
	if endDate != "" {
		query += " AND invoice_date <= ?"
		args = append(args, endDate)
	}
	query += " ORDER BY invoice_date DESC LIMIT " + limit

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var invoices []map[string]interface{}
	for rows.Next() {
		var id, invoiceNo, storeID, userID string
		var customerID sql.NullString
		var invoiceDate time.Time
		var subTotal, cgst, sgst, igst, discount, roundOff, total float64
		var status, paymentMode string

		rows.Scan(&id, &invoiceNo, &storeID, &userID, &customerID, &invoiceDate,
			&subTotal, &cgst, &sgst, &igst, &discount, &roundOff, &total, &status, &paymentMode)

		invoices = append(invoices, map[string]interface{}{
			"id": id, "invoice_no": invoiceNo, "store_id": storeID,
			"customer_id": customerID.String, "invoice_date": invoiceDate,
			"sub_total": subTotal, "cgst": cgst, "sgst": sgst, "igst": igst,
			"discount": discount, "round_off": roundOff, "total": total,
			"status": status, "payment_mode": paymentMode,
		})
	}

	c.JSON(http.StatusOK, invoices)
}

func (h *InvoiceHandler) Get(c *gin.Context) {
	id := c.Param("id")

	var invoice models.Invoice
	err := db.DB.QueryRow(`
		SELECT id, invoice_no, store_id, user_id, customer_id, invoice_date,
		       sub_total, cgst, sgst, igst, discount, round_off, total, status, payment_mode
		FROM invoices WHERE id = ?`, id).Scan(
		&invoice.ID, &invoice.InvoiceNo, &invoice.StoreID,
		&invoice.UserID, &invoice.CustomerID, &invoice.InvoiceDate,
		&invoice.SubTotal, &invoice.CGST, &invoice.SGST, &invoice.IGST,
		&invoice.Discount, &invoice.RoundOff, &invoice.Total,
		&invoice.Status, &invoice.PaymentMode,
	)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "invoice not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	rows, _ := db.DB.Query(`
		SELECT id, invoice_id, product_id, batch_no, quantity, rate, discount, cgst, sgst, igst, total, unit_cost_at_sale
		FROM invoice_items WHERE invoice_id = ?`, id)
	defer rows.Close()

	var items []models.InvoiceItem
	for rows.Next() {
		var item models.InvoiceItem
		rows.Scan(&item.ID, &item.InvoiceID, &item.ProductID, &item.BatchNo, &item.Quantity, &item.Rate,
			&item.Discount, &item.CGST, &item.SGST, &item.IGST, &item.Total, &item.UnitCostAtSale)
		items = append(items, item)
	}
	c.JSON(http.StatusOK, gin.H{
		"invoice": invoice,
		"items":   items,
	})
}

type InvoiceRequest struct {
	StoreID     string           `json:"store_id" binding:"required"`
	UserID      string           `json:"user_id" binding:"required"`
	CustomerID  *string          `json:"customer_id"`
	InvoiceDate string           `json:"invoice_date" binding:"required"`
	SubTotal    float64          `json:"sub_total" binding:"required"`
	CGST        float64          `json:"cgst"`
	SGST        float64          `json:"sgst"`
	IGST        float64          `json:"igst"`
	Discount    float64          `json:"discount"`
	RoundOff    float64          `json:"round_off"`
	Total       float64          `json:"total" binding:"required"`
	Status      string           `json:"status" binding:"required"`
	PaymentMode string           `json:"payment_mode" binding:"required"`
	Items       []InvoiceItemReq `json:"items" binding:"required"`
}

type InvoiceItemReq struct {
	ProductID string  `json:"product_id" binding:"required"`
	Quantity  float64 `json:"quantity" binding:"required"`
	Rate      float64 `json:"rate" binding:"required"`
	Discount  float64 `json:"discount"`
	BatchNo   string  `json:"batch_no"`
	Total     float64 `json:"total" binding:"required"`
}

func (h *InvoiceHandler) Create(c *gin.Context) {
	var req InvoiceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.PaymentMode == "credit" {
		if req.CustomerID == nil || *req.CustomerID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "customer_id required for credit sales"})
			return
		}
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var lineSum float64
	for _, item := range req.Items {
		lineSum += item.Total
	}
	if math.Abs(lineSum-req.SubTotal) > 0.05 {
		tx.Rollback()
		c.JSON(http.StatusBadRequest, gin.H{"error": "line item totals do not match sub_total"})
		return
	}

	if req.CustomerID != nil && *req.CustomerID != "" {
		var custStore sql.NullString
		err = tx.QueryRow(`SELECT store_id FROM customers WHERE id = ?`, *req.CustomerID).Scan(&custStore)
		if err == sql.ErrNoRows {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": "customer not found"})
			return
		}
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !custStore.Valid || custStore.String == "" || custStore.String != req.StoreID {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": "customer does not belong to this store"})
			return
		}
	}

	if req.CustomerID != nil && req.PaymentMode == "credit" {
		var creditLimit float64
		err = tx.QueryRow(`SELECT credit_limit FROM customers WHERE id = ?`, *req.CustomerID).Scan(&creditLimit)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": "customer not found for credit sale"})
			return
		}
		var outstanding float64
		_ = tx.QueryRow(`SELECT COALESCE(SUM(amount - paid_amount), 0) FROM credit_entries 
			WHERE customer_id = ? AND status IN ('pending','partial')`, *req.CustomerID).Scan(&outstanding)
		if creditLimit > 0 && outstanding+req.Total > creditLimit {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": "credit limit exceeded"})
			return
		}
	}

	type taxedLine struct {
		item  InvoiceItemReq
		cgst  float64
		sgst  float64
		batch string
	}
	lines := make([]taxedLine, 0, len(req.Items))
	var sumCGST, sumSGST float64
	for _, item := range req.Items {
		var gstRate float64
		qerr := tx.QueryRow(`SELECT COALESCE(gst_rate, 0) FROM products WHERE id = ?`, item.ProductID).Scan(&gstRate)
		if qerr != nil {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product on line: " + item.ProductID})
			return
		}
		var cgst, sgst float64
		if gstRate > 0 {
			itemGST := item.Total * (gstRate / 100.0)
			cgst = itemGST / 2
			sgst = itemGST / 2
		}
		sumCGST += cgst
		sumSGST += sgst
		batchNo := item.BatchNo
		lines = append(lines, taxedLine{item, cgst, sgst, batchNo})
	}

	if math.Abs(sumCGST-req.CGST) > 0.05 || math.Abs(sumSGST-req.SGST) > 0.05 {
		tx.Rollback()
		c.JSON(http.StatusBadRequest, gin.H{"error": "invoice CGST/SGST must match line taxes from each product gst_rate"})
		return
	}

	invoiceID := uuid.New().String()
	invoiceNo := "INV-" + time.Now().Format("20060102150405")

	_, err = tx.Exec(`INSERT INTO invoices 
		(id, invoice_no, store_id, user_id, customer_id, invoice_date, sub_total, 
		 cgst, sgst, igst, discount, round_off, total, status, payment_mode)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		invoiceID, invoiceNo, req.StoreID, req.UserID, req.CustomerID,
		req.InvoiceDate, req.SubTotal, req.CGST, req.SGST, req.IGST,
		req.Discount, req.RoundOff, req.Total, req.Status, req.PaymentMode)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	for _, tl := range lines {
		actualBatch, unitCost, derr := deductStockFromSale(tx, tl.item.ProductID, req.StoreID, tl.batch, tl.item.Quantity)
		if derr != nil {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": derr.Error()})
			return
		}

		itemID := uuid.New().String()
		_, err = tx.Exec(`INSERT INTO invoice_items 
			(id, invoice_id, product_id, batch_no, quantity, rate, discount, cgst, sgst, igst, total, unit_cost_at_sale)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
			itemID, invoiceID, tl.item.ProductID, actualBatch, tl.item.Quantity,
			tl.item.Rate, tl.item.Discount, tl.cgst, tl.sgst, tl.item.Total, unitCost)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to insert item: " + err.Error()})
			return
		}
	}

	if req.CustomerID != nil && req.PaymentMode == "credit" {
		creditID := uuid.New().String()
		_, _ = tx.Exec(`INSERT INTO credit_entries (id, customer_id, invoice_id, amount, type, status)
			VALUES (?, ?, ?, ?, 'issue', 'pending')`,
			creditID, *req.CustomerID, invoiceID, req.Total)
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	auditAction(c, "invoice_create", "invoices", invoiceID, "", strconv.FormatFloat(req.Total, 'f', 2, 64))
	c.JSON(http.StatusCreated, gin.H{
		"id": invoiceID, "invoice_no": invoiceNo, "total": req.Total,
	})
}

func (h *InvoiceHandler) GSTR1Export(c *gin.Context) {
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

	rows, err := db.DB.Query(`
		SELECT i.invoice_no, i.invoice_date, i.total, i.cgst, i.sgst, i.igst,
		       COALESCE(c.gstin, '') as gstin, COALESCE(c.name, 'Cash Customer') as customer_name
		FROM invoices i
		LEFT JOIN customers c ON i.customer_id = c.id
		WHERE i.store_id = ? AND i.invoice_date >= ? AND i.invoice_date < ?
		  AND i.is_gstr1_filed = 0 AND i.total > 0
		ORDER BY i.invoice_date`, storeID, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var gstr1Data []map[string]interface{}
	var totalTax, totalInvoice float64

	for rows.Next() {
		var invoiceNo, customerName, gstin string
		var invoiceDate time.Time
		var total, cgst, sgst, igst float64

		rows.Scan(&invoiceNo, &invoiceDate, &total, &cgst, &sgst, &igst, &gstin, &customerName)

		gstr1Data = append(gstr1Data, map[string]interface{}{
			"invoice_no":    invoiceNo,
			"date":          invoiceDate.Format("02-01-2006"),
			"customer_name": customerName,
			"gstin":         gstin,
			"taxable_value": total - cgst - sgst - igst,
			"cgst":          cgst, "sgst": sgst, "igst": igst,
			"total_tax": cgst + sgst + igst,
			"total":     total,
		})
		totalTax += cgst + sgst + igst
		totalInvoice += total
	}

	c.JSON(http.StatusOK, map[string]interface{}{
		"month": month,
		"summary": map[string]interface{}{
			"total_invoices": len(gstr1Data),
			"total_tax":      totalTax,
			"total_value":    totalInvoice,
		},
		"invoices": gstr1Data,
	})
}
