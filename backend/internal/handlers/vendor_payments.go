package handlers

import (
	"database/sql"
	"net/http"

	"gbos/internal/db"
	"gbos/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type VendorPaymentHandler struct{}

func NewVendorPaymentHandler() *VendorPaymentHandler {
	return &VendorPaymentHandler{}
}

func (h *VendorPaymentHandler) Create(c *gin.Context) {
	var req struct {
		VendorID    string  `json:"vendor_id" binding:"required"`
		POID        *string `json:"po_id"`
		Amount      float64 `json:"amount" binding:"required"`
		PaymentMode string  `json:"payment_mode" binding:"required"`
		Reference   string  `json:"reference"`
		PaymentDate string  `json:"payment_date" binding:"required"`
		Notes       string  `json:"notes"`
		StoreID     string  `json:"store_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !sessionMayAccessStore(c, req.StoreID) {
		abortStoreMismatch(c)
		return
	}
	uid := middleware.GetUserID(c)
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user context required"})
		return
	}

	// Validate vendor exists
	var vendorName string
	err := db.DB.QueryRow(`SELECT name FROM vendors WHERE id = ? AND is_active = 1`, req.VendorID).Scan(&vendorName)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusBadRequest, gin.H{"error": "vendor not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	paymentID := uuid.New().String()
	_, err = db.DB.Exec(`INSERT INTO vendor_payments (id, vendor_id, po_id, amount, payment_mode, reference, payment_date, notes, user_id, store_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		paymentID, req.VendorID, req.POID, req.Amount, req.PaymentMode, req.Reference, req.PaymentDate, req.Notes, uid, req.StoreID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	auditAction(c, "vendor_payment_create", "vendor_payments", paymentID, "", vendorName)
	c.JSON(http.StatusCreated, gin.H{"id": paymentID})
}

func (h *VendorPaymentHandler) List(c *gin.Context) {
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}
	vendorID := c.Query("vendor_id")
	limit := c.DefaultQuery("limit", "50")

	query := `SELECT vp.id, vp.vendor_id, v.name as vendor_name, vp.po_id, vp.amount, vp.payment_mode,
		vp.reference, vp.payment_date, vp.notes, vp.user_id, vp.created_at
		FROM vendor_payments vp
		JOIN vendors v ON vp.vendor_id = v.id
		WHERE vp.store_id = ?`
	args := []interface{}{storeID}
	if vendorID != "" {
		query += " AND vp.vendor_id = ?"
		args = append(args, vendorID)
	}
	query += " ORDER BY vp.payment_date DESC LIMIT " + limit

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var payments []map[string]interface{}
	for rows.Next() {
		var id, vid, vendorName, paymentMode, reference, paymentDate, notes, userID, createdAt string
		var poID sql.NullString
		var amount float64

		if err := rows.Scan(&id, &vid, &vendorName, &poID, &amount, &paymentMode, &reference, &paymentDate, &notes, &userID, &createdAt); err != nil {
			continue
		}

		payments = append(payments, map[string]interface{}{
			"id": id, "vendor_id": vid, "vendor_name": vendorName,
			"po_id": poID.String, "amount": amount, "payment_mode": paymentMode,
			"reference": reference, "payment_date": paymentDate,
			"notes": notes, "user_id": userID, "created_at": createdAt,
		})
	}

	c.JSON(http.StatusOK, payments)
}

func (h *VendorPaymentHandler) Summary(c *gin.Context) {
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")

	query := `SELECT v.id, v.name,
		COALESCE(SUM(vp.amount), 0) as total_paid,
		COUNT(vp.id) as payment_count
		FROM vendors v
		LEFT JOIN vendor_payments vp ON v.id = vp.vendor_id AND vp.store_id = ?`
	args := []interface{}{storeID}

	if startDate != "" {
		query += " AND vp.payment_date >= ?"
		args = append(args, startDate)
	}
	if endDate != "" {
		query += " AND vp.payment_date <= ?"
		args = append(args, endDate)
	}
	query += ` WHERE v.is_active = 1
		GROUP BY v.id, v.name
		HAVING total_paid > 0
		ORDER BY total_paid DESC`

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var summary []map[string]interface{}
	var grandTotal float64
	for rows.Next() {
		var vid, vname string
		var totalPaid float64
		var paymentCount int
		if err := rows.Scan(&vid, &vname, &totalPaid, &paymentCount); err != nil {
			continue
		}
		summary = append(summary, map[string]interface{}{
			"vendor_id": vid, "vendor_name": vname,
			"total_paid": totalPaid, "payment_count": paymentCount,
		})
		grandTotal += totalPaid
	}

	c.JSON(http.StatusOK, gin.H{"summary": summary, "grand_total": grandTotal})
}
