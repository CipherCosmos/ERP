package handlers

import (
	"database/sql"
	"fmt"
	"net/http"

	"gbos/internal/db"
	"gbos/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type PurchaseReturnHandler struct{}

func NewPurchaseReturnHandler() *PurchaseReturnHandler {
	return &PurchaseReturnHandler{}
}

type PurchaseReturnItemReq struct {
	ProductID string  `json:"product_id" binding:"required"`
	Quantity  float64 `json:"quantity" binding:"required"`
	Rate      float64 `json:"rate"`
}

func (h *PurchaseReturnHandler) Create(c *gin.Context) {
	var req struct {
		POID       *string                 `json:"po_id"`
		VendorID   string                  `json:"vendor_id" binding:"required"`
		StoreID    string                  `json:"store_id" binding:"required"`
		ReturnDate string                  `json:"return_date" binding:"required"`
		Reason     string                  `json:"reason"`
		Items      []PurchaseReturnItemReq `json:"items" binding:"required"`
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

	// Validate vendor
	var vendorName string
	err := db.DB.QueryRow(`SELECT name FROM vendors WHERE id = ?`, req.VendorID).Scan(&vendorName)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusBadRequest, gin.H{"error": "vendor not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	returnID := uuid.New().String()
	var total float64
	for _, item := range req.Items {
		lineTotal := item.Rate * item.Quantity
		total += lineTotal
	}

	_, err = tx.Exec(`INSERT INTO purchase_returns (id, po_id, vendor_id, store_id, return_date, total, status, reason, user_id)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		returnID, req.POID, req.VendorID, req.StoreID, req.ReturnDate, total, req.Reason, uid)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	for _, item := range req.Items {
		itemID := uuid.New().String()
		lineTotal := item.Rate * item.Quantity
		_, err = tx.Exec(`INSERT INTO purchase_return_items (id, return_id, product_id, quantity, rate, total)
			VALUES (?, ?, ?, ?, ?, ?)`,
			itemID, returnID, item.ProductID, item.Quantity, item.Rate, lineTotal)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	auditAction(c, "purchase_return_create", "purchase_returns", returnID, "", fmt.Sprintf("vendor=%s total=%.2f", vendorName, total))
	c.JSON(http.StatusCreated, gin.H{"id": returnID, "total": total, "status": "pending"})
}

func (h *PurchaseReturnHandler) List(c *gin.Context) {
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
	limit := clampLimit(c.DefaultQuery("limit", "50"), 50, 500)

	query := `SELECT pr.id, pr.po_id, pr.vendor_id, v.name as vendor_name, pr.store_id,
		pr.return_date, pr.total, pr.status, pr.reason, pr.user_id, pr.created_at
		FROM purchase_returns pr
		JOIN vendors v ON pr.vendor_id = v.id
		WHERE pr.store_id = ?`
	args := []interface{}{storeID}
	if vendorID != "" {
		query += " AND pr.vendor_id = ?"
		args = append(args, vendorID)
	}
	query += " ORDER BY pr.created_at DESC LIMIT " + limit

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var returns []map[string]interface{}
	for rows.Next() {
		var id, vid, vendorName, sid, returnDate, status, reason, userID, createdAt string
		var poID sql.NullString
		var total float64

		if err := rows.Scan(&id, &poID, &vid, &vendorName, &sid, &returnDate, &total, &status, &reason, &userID, &createdAt); err != nil {
			continue
		}

		returns = append(returns, map[string]interface{}{
			"id": id, "po_id": poID.String, "vendor_id": vid, "vendor_name": vendorName,
			"store_id": sid, "return_date": returnDate, "total": total,
			"status": status, "reason": reason, "user_id": userID, "created_at": createdAt,
		})
	}

	c.JSON(http.StatusOK, returns)
}

func (h *PurchaseReturnHandler) Approve(c *gin.Context) {
	id := c.Param("id")
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var status string
	err = tx.QueryRow(`SELECT status FROM purchase_returns WHERE id = ? AND store_id = ?`, id, storeID).Scan(&status)
	if err == sql.ErrNoRows {
		tx.Rollback()
		c.JSON(http.StatusNotFound, gin.H{"error": "purchase return not found"})
		return
	}
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if status != "pending" {
		tx.Rollback()
		c.JSON(http.StatusBadRequest, gin.H{"error": "only pending returns can be approved"})
		return
	}

	// Update status
	_, err = tx.Exec(`UPDATE purchase_returns SET status = 'approved' WHERE id = ?`, id)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Deduct stock for each return item
	rows, err := tx.Query(`SELECT product_id, quantity FROM purchase_return_items WHERE return_id = ?`, id)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	for rows.Next() {
		var productID string
		var qty float64
		if err := rows.Scan(&productID, &qty); err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Deduct from any available batch
		res, err := tx.Exec(`UPDATE stock SET quantity = quantity - ?
			WHERE rowid = (
				SELECT rowid FROM stock
				WHERE product_id = ? AND store_id = ? AND quantity >= ?
				ORDER BY quantity DESC LIMIT 1
			)`, qty, productID, storeID, qty)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("insufficient stock for product %s", productID)})
			return
		}
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	auditAction(c, "purchase_return_approve", "purchase_returns", id, "pending", "approved")
	c.JSON(http.StatusOK, gin.H{"success": true, "status": "approved"})
}
