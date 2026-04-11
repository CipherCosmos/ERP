package handlers

import (
	"database/sql"
	"net/http"
	"time"

	"gbos/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type StockHandler struct{}

func NewStockHandler() *StockHandler {
	return &StockHandler{}
}

func (h *StockHandler) List(c *gin.Context) {
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}

	rows, err := db.DB.Query(`
		SELECT s.id, s.product_id, p.name, s.store_id, s.batch_no, s.quantity, s.reserved_qty, 
		       s.expiry_date, s.cost_price, s.created_at
		FROM stock s
		JOIN products p ON s.product_id = p.id
		WHERE s.store_id = ? AND s.quantity > 0
		ORDER BY p.name`, storeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var stocks []map[string]interface{}
	for rows.Next() {
		var id, productID, storeID, batchNo, productName string
		var quantity, reservedQty, costPrice float64
		var expiryDate sql.NullString
		var createdAt time.Time

		err := rows.Scan(&id, &productID, &productName, &storeID, &batchNo, &quantity, &reservedQty, &expiryDate, &costPrice, &createdAt)
		if err != nil {
			continue
		}
		stocks = append(stocks, map[string]interface{}{
			"id": id, "product_id": productID, "product_name": productName, "batch_no": batchNo,
			"quantity": quantity, "reserved_qty": reservedQty,
			"expiry_date": expiryDate.String, "cost_price": costPrice,
		})
	}

	c.JSON(http.StatusOK, stocks)
}

func (h *StockHandler) GetProductStock(c *gin.Context) {
	productID := c.Param("id")
	storeID := c.Query("store_id")

	rows, err := db.DB.Query(`
		SELECT id, batch_no, quantity, reserved_qty, expiry_date, cost_price
		FROM stock WHERE product_id = ? AND store_id = ? AND quantity > 0
		ORDER BY expiry_date ASC`, productID, storeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var batches []map[string]interface{}
	for rows.Next() {
		var id, batchNo string
		var quantity, reservedQty, costPrice float64
		var expiryDate sql.NullString

		rows.Scan(&id, &batchNo, &quantity, &reservedQty, &expiryDate, &costPrice)
		batches = append(batches, map[string]interface{}{
			"id": id, "batch_no": batchNo, "quantity": quantity,
			"reserved_qty": reservedQty, "expiry_date": expiryDate.String,
			"cost_price": costPrice,
		})
	}

	c.JSON(http.StatusOK, batches)
}

func (h *StockHandler) Adjust(c *gin.Context) {
	var req struct {
		ProductID string  `json:"product_id" binding:"required"`
		StoreID   string  `json:"store_id" binding:"required"`
		BatchNo   string  `json:"batch_no"`
		Quantity  float64 `json:"quantity" binding:"required"`
		Type      string  `json:"type" binding:"required"` // add, remove, reserve, release
		Reason    string  `json:"reason"`                  // damage, expiry, error, count_variance, received, returned
		UserID    string  `json:"user_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Set default reason if not provided
	if req.Reason == "" {
		if req.Type == "add" {
			req.Reason = "received"
		} else if req.Type == "remove" {
			req.Reason = "count_variance"
		}
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	batchNo := req.BatchNo
	if batchNo == "" {
		batchNo = "ADJ-" + time.Now().Format("20060102150405")
	}

	var currentQty float64
	err = tx.QueryRow(`SELECT COALESCE(quantity, 0) FROM stock 
		WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
		req.ProductID, req.StoreID, batchNo).Scan(&currentQty)

	var newQty float64
	switch req.Type {
	case "add":
		newQty = currentQty + req.Quantity
	case "remove":
		newQty = currentQty - req.Quantity
	case "reserve":
		newQty = currentQty
		_, err = tx.Exec(`UPDATE stock SET reserved_qty = reserved_qty + ? 
			WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
			req.Quantity, req.ProductID, req.StoreID, batchNo)
	case "release":
		newQty = currentQty
		_, err = tx.Exec(`UPDATE stock SET reserved_qty = reserved_qty - ? 
			WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
			req.Quantity, req.ProductID, req.StoreID, batchNo)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid type"})
		return
	}

	if err == nil && (req.Type == "add" || req.Type == "remove") {
		if newQty < 0 {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient stock"})
			return
		}

		if currentQty == 0 {
			_, err = tx.Exec(`INSERT INTO stock (id, product_id, store_id, batch_no, quantity, cost_price)
				VALUES (?, ?, ?, ?, ?, 0)`, uuid.New().String(), req.ProductID, req.StoreID, batchNo, newQty)
		} else {
			_, err = tx.Exec(`UPDATE stock SET quantity = ? WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
				newQty, req.ProductID, req.StoreID, batchNo)
		}

		// Log the adjustment for audit
		if err == nil {
			_, err = tx.Exec(`INSERT INTO stock_adjustments (id, product_id, store_id, batch_no, quantity, adjustment_type, reason, user_id)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				uuid.New().String(), req.ProductID, req.StoreID, batchNo, req.Quantity, req.Type, req.Reason, req.UserID)
		}
	}

	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	tx.Commit()
	auditAction(c, "stock_adjust", "stock", req.ProductID, req.Type, req.Reason)
	c.JSON(http.StatusOK, gin.H{"success": true, "new_quantity": newQty})
}

func (h *StockHandler) Transfer(c *gin.Context) {
	var req struct {
		ProductID   string  `json:"product_id" binding:"required"`
		FromStoreID string  `json:"from_store_id" binding:"required"`
		ToStoreID   string  `json:"to_store_id" binding:"required"`
		Quantity    float64 `json:"quantity" binding:"required"`
		BatchNo     string  `json:"batch_no"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var fromQty float64
	err = tx.QueryRow(`SELECT quantity FROM stock 
		WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
		req.ProductID, req.FromStoreID, req.BatchNo).Scan(&fromQty)
	if err != nil || fromQty < req.Quantity {
		tx.Rollback()
		c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient stock"})
		return
	}

	tx.Exec(`UPDATE stock SET quantity = quantity - ? 
		WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
		req.Quantity, req.ProductID, req.FromStoreID, req.BatchNo)

	var toQty float64
	err = tx.QueryRow(`SELECT quantity FROM stock 
		WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
		req.ProductID, req.ToStoreID, req.BatchNo).Scan(&toQty)
	if err == sql.ErrNoRows {
		tx.Exec(`INSERT INTO stock (id, product_id, store_id, batch_no, quantity) 
			VALUES (?, ?, ?, ?, ?)`,
			uuid.New().String(), req.ProductID, req.ToStoreID, req.BatchNo, req.Quantity)
	} else {
		tx.Exec(`UPDATE stock SET quantity = quantity + ? 
			WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
			req.Quantity, req.ProductID, req.ToStoreID, req.BatchNo)
	}

	tx.Commit()
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *StockHandler) LowStock(c *gin.Context) {
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}

	rows, err := db.DB.Query(`
		SELECT p.id, p.name, p.min_stock, COALESCE(SUM(s.quantity), 0) as current_stock
		FROM products p
		LEFT JOIN stock s ON p.id = s.product_id AND s.store_id = ?
		WHERE p.is_active = 1
		GROUP BY p.id
		HAVING current_stock <= p.min_stock
		ORDER BY current_stock ASC`, storeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var lowStock []map[string]interface{}
	for rows.Next() {
		var id, name string
		var minStock, currentStock float64
		rows.Scan(&id, &name, &minStock, &currentStock)
		lowStock = append(lowStock, map[string]interface{}{
			"product_id": id, "name": name,
			"min_stock": minStock, "current_stock": currentStock,
		})
	}

	c.JSON(http.StatusOK, lowStock)
}

type SyncHandler struct{}

func NewSyncHandler() *SyncHandler {
	return &SyncHandler{}
}

func (h *SyncHandler) GetPending(c *gin.Context) {
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}

	rows, err := db.DB.Query(`
		SELECT id, table_name, record_id, operation, status, created_at
		FROM sync_records 
		WHERE store_id = ? AND status = 'pending'
		ORDER BY created_at ASC
		LIMIT 100`, storeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var pending []map[string]interface{}
	for rows.Next() {
		var id, tableName, recordID, operation, status string
		var createdAt time.Time
		rows.Scan(&id, &tableName, &recordID, &operation, &status, &createdAt)
		pending = append(pending, map[string]interface{}{
			"id": id, "table_name": tableName, "record_id": recordID,
			"operation": operation, "status": status, "created_at": createdAt,
		})
	}

	c.JSON(http.StatusOK, pending)
}

func (h *SyncHandler) MarkSynced(c *gin.Context) {
	var req struct {
		RecordIDs []string `json:"record_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now().Format("2006-01-02 15:04:05")
	for _, id := range req.RecordIDs {
		db.DB.Exec(`UPDATE sync_records SET status = 'synced', synced_at = ? WHERE id = ?`, now, id)
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *SyncHandler) Push(c *gin.Context) {
	var req struct {
		TableName string      `json:"table_name" binding:"required"`
		RecordID  string      `json:"record_id" binding:"required"`
		Operation string      `json:"operation" binding:"required"`
		StoreID   string      `json:"store_id" binding:"required"`
		Data      interface{} `json:"data"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	syncID := uuid.New().String()
	_, err := db.DB.Exec(`INSERT INTO sync_records (id, table_name, record_id, operation, store_id, status)
		VALUES (?, ?, ?, ?, ?, 'pending')`,
		syncID, req.TableName, req.RecordID, req.Operation, req.StoreID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": syncID, "status": "pending"})
}

func (h *SyncHandler) ConflictResolution(c *gin.Context) {
	var req struct {
		TableName  string `json:"table_name" binding:"required"`
		RecordID   string `json:"record_id" binding:"required"`
		Resolution string `json:"resolution" binding:"required"` // local_wins, remote_wins
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Resolution == "local_wins" {
		db.DB.Exec(`UPDATE sync_records SET status = 'pending' 
			WHERE table_name = ? AND record_id = ?`, req.TableName, req.RecordID)
	} else {
		db.DB.Exec(`DELETE FROM sync_records 
			WHERE table_name = ? AND record_id = ?`, req.TableName, req.RecordID)
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *StockHandler) GetAdjustments(c *gin.Context) {
	storeID := c.Query("store_id")
	productID := c.Query("product_id")
	limit := c.DefaultQuery("limit", "100")

	query := `SELECT sa.id, sa.product_id, p.name, sa.store_id, sa.batch_no, sa.quantity, 
		sa.adjustment_type, sa.reason, sa.user_id, sa.created_at
		FROM stock_adjustments sa
		JOIN products p ON sa.product_id = p.id
		WHERE 1=1`
	args := []interface{}{}

	if storeID != "" {
		query += " AND sa.store_id = ?"
		args = append(args, storeID)
	}
	if productID != "" {
		query += " AND sa.product_id = ?"
		args = append(args, productID)
	}
	query += " ORDER BY sa.created_at DESC LIMIT " + limit

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var adjustments []map[string]interface{}
	for rows.Next() {
		var id, pid, productName, sid, batchNo, adjustmentType, reason, userID string
		var quantity float64
		var createdAt time.Time

		if err := rows.Scan(&id, &pid, &productName, &sid, &batchNo, &quantity,
			&adjustmentType, &reason, &userID, &createdAt); err != nil {
			continue
		}

		adjustments = append(adjustments, map[string]interface{}{
			"id": id, "product_id": pid, "product_name": productName, "store_id": sid,
			"batch_no": batchNo, "quantity": quantity,
			"adjustment_type": adjustmentType, "reason": reason,
			"user_id": userID, "created_at": createdAt,
		})
	}

	c.JSON(http.StatusOK, adjustments)
}
