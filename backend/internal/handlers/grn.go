package handlers

import (
	"database/sql"
	"net/http"

	"gbos/internal/db"
	"gbos/internal/middleware"
	"gbos/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type GRNHandler struct{}

func NewGRNHandler() *GRNHandler {
	return &GRNHandler{}
}

type CreateGRNRequest struct {
	POID         string           `json:"po_id" binding:"required"`
	ReceivedDate string           `json:"received_date" binding:"required"`
	Notes        string           `json:"notes"`
	Items        []GRNItemRequest `json:"items" binding:"required"`
}

type GRNItemRequest struct {
	ProductID   string  `json:"product_id" binding:"required"`
	OrderedQty  float64 `json:"ordered_qty"`
	ReceivedQty float64 `json:"received_qty" binding:"required"`
	AcceptedQty float64 `json:"accepted_qty"`
	RejectedQty float64 `json:"rejected_qty"`
	Reason      string  `json:"reason"`
}

func (h *GRNHandler) Create(c *gin.Context) {
	var req CreateGRNRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var poStore string
	err := db.DB.QueryRow(`SELECT store_id FROM purchase_orders WHERE id = ?`, req.POID).Scan(&poStore)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "purchase order not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !sessionMayAccessStore(c, poStore) {
		abortStoreMismatch(c)
		return
	}
	uid := middleware.GetUserID(c)
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user context required"})
		return
	}
	receivedBy := uid
	var uname string
	if err := db.DB.QueryRow(`SELECT username FROM users WHERE id = ?`, uid).Scan(&uname); err == nil && uname != "" {
		receivedBy = uname
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	grnID := uuid.New().String()
	_, err = tx.Exec(`INSERT INTO goods_receipts (id, po_id, received_date, received_by, notes)
		VALUES (?, ?, ?, ?, ?)`,
		grnID, req.POID, req.ReceivedDate, receivedBy, req.Notes)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create GRN"})
		return
	}

	for _, item := range req.Items {
		itemID := uuid.New().String()
		var accepted, rejected float64
		if item.AcceptedQty > 0 {
			accepted = item.AcceptedQty
		} else {
			accepted = item.ReceivedQty
		}
		if item.RejectedQty > 0 {
			rejected = item.RejectedQty
		}

		_, err = tx.Exec(`INSERT INTO goods_receipt_items (id, gr_id, product_id, ordered_qty, received_qty, accepted_qty, rejected_qty, reason)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			itemID, grnID, item.ProductID, item.OrderedQty, item.ReceivedQty, accepted, rejected, item.Reason)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add GRN item"})
			return
		}

		var costPrice float64
		_ = tx.QueryRow(`SELECT COALESCE(cost_price, 0) FROM product_prices WHERE product_id = ? AND store_id = ?`,
			item.ProductID, poStore).Scan(&costPrice)

		_, err = tx.Exec(`INSERT INTO stock (id, product_id, store_id, batch_no, quantity, expiry_date, cost_price)
			VALUES (?, ?, (SELECT store_id FROM purchase_orders WHERE id = ?), ?, ?, NULL, ?)`,
			uuid.New().String(), item.ProductID, req.POID, "GRN-"+grnID[:8], accepted, costPrice)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add stock"})
			return
		}

		_, err = tx.Exec(`UPDATE purchase_items SET received_qty = received_qty + ? 
			WHERE po_id = ? AND product_id = ?`,
			item.ReceivedQty, req.POID, item.ProductID)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update PO"})
			return
		}
	}

	var pendingCount int
	tx.QueryRow(`SELECT COUNT(*) FROM purchase_items WHERE po_id = ? AND received_qty < quantity`, req.POID).Scan(&pendingCount)
	if pendingCount == 0 {
		_, _ = tx.Exec(`UPDATE purchase_orders SET status = 'received' WHERE id = ?`, req.POID)
	} else {
		_, _ = tx.Exec(`UPDATE purchase_orders SET status = 'partial' WHERE id = ?`, req.POID)
	}

	tx.Commit()

	c.JSON(http.StatusCreated, gin.H{"id": grnID})
}

func (h *GRNHandler) Get(c *gin.Context) {
	id := c.Param("id")

	var grn models.GoodsReceipt
	err := db.DB.QueryRow(`SELECT gr.id, gr.po_id, gr.received_date, gr.received_by, gr.notes, gr.created_at 
		FROM goods_receipts gr
		JOIN purchase_orders po ON gr.po_id = po.id
		WHERE gr.id = ? AND po.store_id = ?`, id, sessionStoreID(c)).Scan(
		&grn.ID, &grn.POID, &grn.ReceivedDate, &grn.ReceivedBy,
		&grn.Notes, &grn.CreatedAt,
	)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "GRN not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	rows, _ := db.DB.Query(`SELECT id, gr_id, product_id, ordered_qty, received_qty, accepted_qty, rejected_qty, reason
		FROM goods_receipt_items WHERE gr_id = ?`, id)
	defer rows.Close()

	var items []models.GoodsReceiptItem
	for rows.Next() {
		var item models.GoodsReceiptItem
		rows.Scan(&item.ID, &item.GRID, &item.ProductID, &item.OrderedQty, &item.ReceivedQty,
			&item.AcceptedQty, &item.RejectedQty, &item.Reason)
		items = append(items, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"grn":   grn,
		"items": items,
	})
}

func (h *GRNHandler) List(c *gin.Context) {
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}
	poID := c.Query("po_id")
	limit := clampLimit(c.DefaultQuery("limit", "50"), 50, 500)

	query := `SELECT gr.id, gr.po_id, gr.received_date, gr.received_by, gr.notes, gr.created_at 
		FROM goods_receipts gr
		JOIN purchase_orders po ON gr.po_id = po.id
		WHERE po.store_id = ?`
	args := []interface{}{storeID}

	if poID != "" {
		query += " AND gr.po_id = ?"
		args = append(args, poID)
	}
	query += " ORDER BY gr.created_at DESC LIMIT " + limit

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var grns []map[string]interface{}
	for rows.Next() {
		var id, rowPOID, receivedBy, notes string
		var receivedDate, createdAt string

		rows.Scan(&id, &rowPOID, &receivedDate, &receivedBy, &notes, &createdAt)

		grns = append(grns, map[string]interface{}{
			"id": id, "po_id": rowPOID, "received_date": receivedDate,
			"received_by": receivedBy, "notes": notes, "created_at": createdAt,
		})
	}

	c.JSON(http.StatusOK, grns)
}
