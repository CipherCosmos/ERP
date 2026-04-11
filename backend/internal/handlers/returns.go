package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gbos/internal/db"
)

type ReturnHandler struct{}

func NewReturnHandler() *ReturnHandler {
	return &ReturnHandler{}
}

type CreateReturnRequest struct {
	InvoiceID  *string `json:"invoice_id"`
	ProductID  string   `json:"product_id" binding:"required"`
	Quantity   float64  `json:"quantity" binding:"required"`
	Amount     float64  `json:"amount" binding:"required"`
	Reason     string   `json:"reason" binding:"required"`
	RefundMode string   `json:"refund_mode"`
	StoreID    string   `json:"store_id" binding:"required"`
	UserID     string   `json:"user_id" binding:"required"`
}

func (h *ReturnHandler) Create(c *gin.Context) {
	var req CreateReturnRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	returnID := uuid.New().String()
	_, err = tx.Exec(`INSERT INTO returns (id, invoice_id, product_id, store_id, quantity, amount, reason, refund_mode, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending')`,
		returnID, req.InvoiceID, req.ProductID, req.StoreID, req.Quantity, req.Amount, req.Reason, req.RefundMode)
	if err != nil {
		_ = tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create return"})
		return
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	auditAction(c, "return_create", "returns", returnID, "", fmt.Sprintf("qty=%.3f amount=%.2f", req.Quantity, req.Amount))
	c.JSON(http.StatusCreated, gin.H{"id": returnID, "status": "pending"})
}

func (h *ReturnHandler) List(c *gin.Context) {
	storeID := c.Query("store_id")
	limit := c.DefaultQuery("limit", "50")

	query := `SELECT id, invoice_id, product_id, store_id, quantity, amount, reason, refund_mode, status, created_at 
		FROM returns`
	args := []interface{}{}

	if storeID != "" {
		query += " WHERE store_id = ?"
		args = append(args, storeID)
	}
	query += " ORDER BY created_at DESC LIMIT " + limit

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var returns []map[string]interface{}
	for rows.Next() {
		var id, invoiceID, productID, storeID, reason, refundMode, status string
		var quantity, amount float64
		var createdAt time.Time

		rows.Scan(&id, &invoiceID, &productID, &storeID, &quantity, &amount, &reason, &refundMode, &status, &createdAt)

		returns = append(returns, map[string]interface{}{
			"id": id, "invoice_id": invoiceID, "product_id": productID, "store_id": storeID,
			"quantity": quantity, "amount": amount, "reason": reason,
			"refund_mode": refundMode, "status": status, "created_at": createdAt,
		})
	}

	c.JSON(http.StatusOK, returns)
}

func (h *ReturnHandler) Approve(c *gin.Context) {
	id := c.Param("id")
	storeID := c.Query("store_id")

	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var productID string
	var quantity float64
	err = tx.QueryRow(`SELECT product_id, quantity FROM returns WHERE id = ?`, id).Scan(&productID, &quantity)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusNotFound, gin.H{"error": "return not found"})
		return
	}

	_, err = tx.Exec(`UPDATE returns SET status = 'completed' WHERE id = ?`, id)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to approve return"})
		return
	}

	var batchNo string
	err = tx.QueryRow(`SELECT batch_no FROM stock WHERE product_id = ? AND store_id = ? AND quantity > 0 LIMIT 1`,
		productID, storeID).Scan(&batchNo)
	if err == nil && batchNo != "" {
		_, err = tx.Exec(`UPDATE stock SET quantity = quantity + ? 
			WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
			quantity, productID, storeID, batchNo)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to restore stock"})
			return
		}
	} else {
		_, err = tx.Exec(`INSERT INTO stock (id, product_id, store_id, batch_no, quantity, cost_price)
			VALUES (?, ?, ?, 'RETURN-STOCK', ?, 0)`,
			uuid.New().String(), productID, storeID, quantity)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create stock entry"})
			return
		}
	}

	tx.Commit()

	c.JSON(http.StatusOK, gin.H{"status": "completed"})
}

type LossHandler struct{}

func NewLossHandler() *LossHandler {
	return &LossHandler{}
}

type CreateLossRequest struct {
	ProductID string  `json:"product_id" binding:"required"`
	StoreID   string  `json:"store_id" binding:"required"`
	BatchNo   string  `json:"batch_no"`
	Quantity  float64 `json:"quantity" binding:"required"`
	LossType  string  `json:"loss_type" binding:"required"`
	Reason    string  `json:"reason"`
	UserID    string  `json:"user_id" binding:"required"`
}

func (h *LossHandler) Create(c *gin.Context) {
	var req CreateLossRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	batchNo := req.BatchNo
	if batchNo == "" {
		batchNo = "LOSS-" + time.Now().Format("20060102150405")
	}

	lossID := uuid.New().String()
	_, err = tx.Exec(`INSERT INTO inventory_losses (id, product_id, store_id, batch_no, quantity, loss_type, reason, user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		lossID, req.ProductID, req.StoreID, batchNo, req.Quantity, req.LossType, req.Reason, req.UserID)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record loss"})
		return
	}

	// Deduct stock from any available batch
	_, err = tx.Exec(`UPDATE stock SET quantity = quantity - ? 
		WHERE product_id = ? AND store_id = ? AND quantity >= ?
		ORDER BY quantity DESC LIMIT 1`,
		req.Quantity, req.ProductID, req.StoreID, req.Quantity)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to adjust stock: " + err.Error()})
		return
	}

	tx.Commit()

	c.JSON(http.StatusCreated, gin.H{"id": lossID})
}

func (h *LossHandler) List(c *gin.Context) {
	storeID := c.Query("store_id")
	lossType := c.Query("loss_type")
	limit := c.DefaultQuery("limit", "50")

	query := `SELECT l.id, l.product_id, l.store_id, l.batch_no, l.quantity, l.loss_type, l.reason, l.user_id, l.created_at
		FROM inventory_losses l WHERE 1=1`
	args := []interface{}{}

	if storeID != "" {
		query += " AND l.store_id = ?"
		args = append(args, storeID)
	}
	if lossType != "" {
		query += " AND l.loss_type = ?"
		args = append(args, lossType)
	}
	query += " ORDER BY l.created_at DESC LIMIT " + limit

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var losses []map[string]interface{}
	for rows.Next() {
		var id, productID, storeID, batchNo, lossType, reason, userID string
		var quantity float64
		var createdAt time.Time

		rows.Scan(&id, &productID, &storeID, &batchNo, &quantity, &lossType, &reason, &userID, &createdAt)

		losses = append(losses, map[string]interface{}{
			"id": id, "product_id": productID, "store_id": storeID,
			"batch_no": batchNo, "quantity": quantity, "loss_type": lossType,
			"reason": reason, "user_id": userID, "created_at": createdAt,
		})
	}

	c.JSON(http.StatusOK, losses)
}