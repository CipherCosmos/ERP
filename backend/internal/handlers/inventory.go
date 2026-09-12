package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"gbos/internal/db"
	"gbos/internal/middleware"
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
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}

	rows, err := db.DB.Query(`
		SELECT s.id, s.product_id, p.name, s.store_id, s.batch_no, s.quantity, s.reserved_qty,
		       s.expiry_date, s.cost_price
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

		err := rows.Scan(&id, &productID, &productName, &storeID, &batchNo, &quantity, &reservedQty, &expiryDate, &costPrice)
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
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}

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

		if err := rows.Scan(&id, &batchNo, &quantity, &reservedQty, &expiryDate, &costPrice); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to scan stock data"})
			return
		}
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
		ProductID  string  `json:"product_id" binding:"required"`
		StoreID    string  `json:"store_id" binding:"required"`
		BatchNo    string  `json:"batch_no"`
		Quantity   float64 `json:"quantity" binding:"required"`
		Type       string  `json:"type" binding:"required"` // add, remove, reserve, release
		Reason     string  `json:"reason"`                  // damage, expiry, error, count_variance, received, returned
		CostPrice  float64 `json:"cost_price"`
		ExpiryDate string  `json:"expiry_date"`
		MfgDate    string  `json:"mfg_date"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Quantity < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "quantity must be positive"})
		return
	}
	uid := middleware.GetUserID(c)
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user context required"})
		return
	}
	if !sessionMayAccessStore(c, req.StoreID) {
		abortStoreMismatch(c)
		return
	}

	// Set default reason if not provided
	if req.Reason == "" {
		if req.Type == "add" {
			req.Reason = "received"
		} else if req.Type == "remove" {
			req.Reason = "count_variance"
		} else {
			req.Reason = req.Type
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
	if err == sql.ErrNoRows {
		currentQty = 0
		err = nil
	}

	var newQty float64
	switch req.Type {
	case "add":
		newQty = currentQty + req.Quantity
	case "remove":
		newQty = currentQty - req.Quantity
	case "reserve":
		newQty = currentQty
		// Check underflow before reserve
		var currentReserved float64
		tx.QueryRow(`SELECT COALESCE(reserved_qty, 0) FROM stock WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
			req.ProductID, req.StoreID, batchNo).Scan(&currentReserved)
		if currentReserved+req.Quantity > currentQty {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot reserve more than available quantity"})
			return
		}
		_, err = tx.Exec(`UPDATE stock SET reserved_qty = reserved_qty + ?
			WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
			req.Quantity, req.ProductID, req.StoreID, batchNo)
	case "release":
		newQty = currentQty
		// Check underflow before release
		var currentReserved float64
		tx.QueryRow(`SELECT COALESCE(reserved_qty, 0) FROM stock WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
			req.ProductID, req.StoreID, batchNo).Scan(&currentReserved)
		if req.Quantity > currentReserved {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot release more than reserved quantity"})
			return
		}
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

		// Determine cost_price: use provided, or fall back to product_prices
		costPrice := req.CostPrice
		if costPrice == 0 && req.Type == "add" {
			_ = tx.QueryRow(`SELECT COALESCE(cost_price, 0) FROM product_prices WHERE product_id = ? AND store_id = ?`,
				req.ProductID, req.StoreID).Scan(&costPrice)
		}

		if currentQty == 0 {
			_, err = tx.Exec(`INSERT INTO stock (id, product_id, store_id, batch_no, quantity, cost_price, expiry_date, mfg_date)
				VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''))`,
				uuid.New().String(), req.ProductID, req.StoreID, batchNo, newQty, costPrice, req.ExpiryDate, req.MfgDate)
		} else {
			_, err = tx.Exec(`UPDATE stock SET quantity = ?, cost_price = CASE WHEN ? > 0 THEN ? ELSE cost_price END,
				expiry_date = CASE WHEN ? != '' THEN ? ELSE expiry_date END,
				mfg_date = CASE WHEN ? != '' THEN ? ELSE mfg_date END
				WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
				newQty, costPrice, costPrice, req.ExpiryDate, req.ExpiryDate, req.MfgDate, req.MfgDate,
				req.ProductID, req.StoreID, batchNo)
		}
	}

	// Log ALL adjustment types for audit
	if err == nil {
		_, err = tx.Exec(`INSERT INTO stock_adjustments (id, product_id, store_id, batch_no, quantity, adjustment_type, reason, user_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.New().String(), req.ProductID, req.StoreID, batchNo, req.Quantity, req.Type, req.Reason, uid)
	}

	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
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
	if !transferStoresAllowed(c, req.FromStoreID, req.ToStoreID) {
		abortStoreMismatch(c)
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var fromQty, costPrice float64
	var expiryDate, mfgDate sql.NullString
	err = tx.QueryRow(`SELECT quantity, cost_price, expiry_date, mfg_date FROM stock
		WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
		req.ProductID, req.FromStoreID, req.BatchNo).Scan(&fromQty, &costPrice, &expiryDate, &mfgDate)
	if err != nil || fromQty < req.Quantity {
		tx.Rollback()
		c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient stock"})
		return
	}

	// Check reserved_qty
	var reservedQty float64
	if err := tx.QueryRow(`SELECT COALESCE(reserved_qty, 0) FROM stock
		WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
		req.ProductID, req.FromStoreID, req.BatchNo).Scan(&reservedQty); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check reserved stock"})
		return
	}
	if fromQty-reservedQty < req.Quantity {
		tx.Rollback()
		c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient unreserved stock for transfer"})
		return
	}

	if _, err := tx.Exec(`UPDATE stock SET quantity = quantity - ?
		WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
		req.Quantity, req.ProductID, req.FromStoreID, req.BatchNo); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var toQty float64
	err = tx.QueryRow(`SELECT quantity FROM stock
		WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
		req.ProductID, req.ToStoreID, req.BatchNo).Scan(&toQty)
	if err == sql.ErrNoRows {
		if _, err := tx.Exec(`INSERT INTO stock (id, product_id, store_id, batch_no, quantity, cost_price, expiry_date, mfg_date)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.New().String(), req.ProductID, req.ToStoreID, req.BatchNo, req.Quantity,
			costPrice, expiryDate, mfgDate); err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	} else {
		if _, err := tx.Exec(`UPDATE stock SET quantity = quantity + ?
			WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
			req.Quantity, req.ProductID, req.ToStoreID, req.BatchNo); err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	auditAction(c, "stock_transfer", "stock", req.ProductID, req.FromStoreID, req.ToStoreID)
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *StockHandler) LowStock(c *gin.Context) {
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}

	rows, err := db.DB.Query(`
		SELECT p.id, p.name, p.min_stock, COALESCE(SUM(s.quantity), 0) as current_stock
		FROM products p
		LEFT JOIN stock s ON p.id = s.product_id AND s.store_id = ?
		WHERE p.is_active = 1 AND p.min_stock > 0
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
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
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
		var createdAt string
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
		var sid string
		err := db.DB.QueryRow(`SELECT store_id FROM sync_records WHERE id = ?`, id).Scan(&sid)
		if err == sql.ErrNoRows {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unknown sync record: " + id})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !sessionMayAccessStore(c, sid) {
			abortStoreMismatch(c)
			return
		}
		if _, err := db.DB.Exec(`UPDATE sync_records SET status = 'synced', synced_at = ? WHERE id = ?`, now, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
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
	if !sessionMayAccessStore(c, req.StoreID) {
		abortStoreMismatch(c)
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

	var sid string
	err := db.DB.QueryRow(`
		SELECT store_id FROM sync_records
		WHERE table_name = ? AND record_id = ?
		ORDER BY created_at DESC LIMIT 1`, req.TableName, req.RecordID).Scan(&sid)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "sync record not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !sessionMayAccessStore(c, sid) {
		abortStoreMismatch(c)
		return
	}

	if req.Resolution == "local_wins" {
		_, _ = db.DB.Exec(`UPDATE sync_records SET status = 'pending' 
			WHERE table_name = ? AND record_id = ?`, req.TableName, req.RecordID)
	} else {
		_, _ = db.DB.Exec(`DELETE FROM sync_records 
			WHERE table_name = ? AND record_id = ?`, req.TableName, req.RecordID)
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *StockHandler) Convert(c *gin.Context) {
	var req struct {
		ProductID   string  `json:"product_id" binding:"required"`
		StoreID     string  `json:"store_id" binding:"required"`
		FromBatchNo string  `json:"from_batch_no" binding:"required"`
		ToUnitID    string  `json:"to_unit_id" binding:"required"`
		Quantity    float64 `json:"quantity" binding:"required"`
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

	// Look up the conversion factor from product_units
	var qtyInParent float64
	var label string
	err := db.DB.QueryRow(`SELECT qty_in_parent, label FROM product_units WHERE id = ? AND product_id = ?`,
		req.ToUnitID, req.ProductID).Scan(&qtyInParent, &label)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusBadRequest, gin.H{"error": "packing unit not found for this product"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if qtyInParent <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversion factor"})
		return
	}

	convertedQty := req.Quantity * qtyInParent

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Deduct source quantity
	var sourceQty float64
	err = tx.QueryRow(`SELECT quantity FROM stock WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
		req.ProductID, req.StoreID, req.FromBatchNo).Scan(&sourceQty)
	if err != nil || sourceQty < req.Quantity {
		tx.Rollback()
		c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient stock in source batch"})
		return
	}

	_, err = tx.Exec(`UPDATE stock SET quantity = quantity - ? WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
		req.Quantity, req.ProductID, req.StoreID, req.FromBatchNo)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Add converted quantity to a new batch
	newBatch := fmt.Sprintf("CONV-%s-%s", label, time.Now().Format("20060102150405"))
	var costPrice float64
	_ = tx.QueryRow(`SELECT cost_price FROM stock WHERE product_id = ? AND store_id = ? AND batch_no = ?`,
		req.ProductID, req.StoreID, req.FromBatchNo).Scan(&costPrice)

	_, err = tx.Exec(`INSERT INTO stock (id, product_id, store_id, batch_no, quantity, cost_price)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(product_id, store_id, batch_no) DO UPDATE SET quantity = quantity + excluded.quantity`,
		uuid.New().String(), req.ProductID, req.StoreID, newBatch, convertedQty, costPrice/qtyInParent)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Log as stock_adjustment
	_, err = tx.Exec(`INSERT INTO stock_adjustments (id, product_id, store_id, batch_no, quantity, adjustment_type, reason, user_id)
		VALUES (?, ?, ?, ?, ?, 'conversion', ?, ?)`,
		uuid.New().String(), req.ProductID, req.StoreID, req.FromBatchNo, req.Quantity,
		fmt.Sprintf("converted %.2f to %.2f via unit %s", req.Quantity, convertedQty, label), uid)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	auditAction(c, "stock_convert", "stock", req.ProductID, req.FromBatchNo, newBatch)
	c.JSON(http.StatusOK, gin.H{
		"success":       true,
		"from_qty":      req.Quantity,
		"converted_qty": convertedQty,
		"new_batch":     newBatch,
	})
}

func (h *StockHandler) GetAdjustments(c *gin.Context) {
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}
	productID := c.Query("product_id")
	limit := clampLimit(c.DefaultQuery("limit", "100"), 100, 500)

	query := `SELECT sa.id, sa.product_id, p.name, sa.store_id, sa.batch_no, sa.quantity, 
		sa.adjustment_type, sa.reason, sa.user_id, sa.created_at
		FROM stock_adjustments sa
		JOIN products p ON sa.product_id = p.id
		WHERE sa.store_id = ?`
	args := []interface{}{storeID}
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
		var createdAt string

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
