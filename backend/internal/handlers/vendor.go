package handlers

import (
	"database/sql"
	"net/http"
	"time"

	"gbos/internal/db"
	"gbos/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type VendorHandler struct{}

func NewVendorHandler() *VendorHandler {
	return &VendorHandler{}
}

func (h *VendorHandler) List(c *gin.Context) {
	search := c.Query("search")

	query := `SELECT id, name, phone, address, gstin, state, COALESCE(credit_days,0), COALESCE(credit_limit,0), is_active FROM vendors WHERE is_active = 1`
	if search != "" {
		query += " AND (name LIKE ? OR phone LIKE ?)"
	}
	query += " ORDER BY name LIMIT 50"

	var rows *sql.Rows
	var err error
	if search != "" {
		rows, err = db.DB.Query(query, "%"+search+"%", "%"+search+"%")
	} else {
		rows, err = db.DB.Query(query)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var vendors []map[string]interface{}
	for rows.Next() {
		var id, name, phone, address, gstin, state string
		var creditDays int
		var creditLimit float64
		var isActive bool
		rows.Scan(&id, &name, &phone, &address, &gstin, &state, &creditDays, &creditLimit, &isActive)
		vendors = append(vendors, map[string]interface{}{
			"id": id, "name": name, "phone": phone, "address": address,
			"gstin": gstin, "state": state, "credit_days": creditDays, "credit_limit": creditLimit,
			"is_active": isActive,
		})
	}

	c.JSON(http.StatusOK, vendors)
}

func (h *VendorHandler) Get(c *gin.Context) {
	id := c.Param("id")

	var vendor models.Vendor
	err := db.DB.QueryRow(`SELECT id, name, phone, address, gstin, state, COALESCE(credit_days,0), COALESCE(credit_limit,0), is_active
		FROM vendors WHERE id = ?`, id).Scan(
		&vendor.ID, &vendor.Name, &vendor.Phone, &vendor.Address,
		&vendor.GSTIN, &vendor.State, &vendor.CreditDays, &vendor.CreditLimit, &vendor.IsActive)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "vendor not found"})
		return
	}

	var totalPurchase float64
	db.DB.QueryRow(`SELECT COALESCE(SUM(total), 0) FROM purchase_orders WHERE vendor_id = ?`, id).Scan(&totalPurchase)

	c.JSON(http.StatusOK, gin.H{
		"vendor":         vendor,
		"total_purchase": totalPurchase,
	})
}

func (h *VendorHandler) Create(c *gin.Context) {
	var req struct {
		Name        string  `json:"name" binding:"required"`
		Phone       string  `json:"phone"`
		Address     string  `json:"address"`
		GSTIN       string  `json:"gstin"`
		State       string  `json:"state"`
		CreditDays  int     `json:"credit_days"`
		CreditLimit float64 `json:"credit_limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	vendorID := uuid.New().String()
	_, err := db.DB.Exec(`INSERT INTO vendors (id, name, phone, address, gstin, state, credit_days, credit_limit)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		vendorID, req.Name, req.Phone, req.Address, req.GSTIN, req.State, req.CreditDays, req.CreditLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": vendorID, "name": req.Name})
}

type PurchaseHandler struct{}

func NewPurchaseHandler() *PurchaseHandler {
	return &PurchaseHandler{}
}

func (h *PurchaseHandler) List(c *gin.Context) {
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
	status := c.Query("status")

	query := `SELECT po.id, po.po_no, po.store_id, po.vendor_id, v.name as vendor_name,
		po.order_date, po.expected_date, po.total, po.status, po.created_at
		FROM purchase_orders po
		JOIN vendors v ON po.vendor_id = v.id
		WHERE po.store_id = ?`
	args := []interface{}{storeID}
	if vendorID != "" {
		query += " AND po.vendor_id = ?"
		args = append(args, vendorID)
	}
	if status != "" {
		query += " AND po.status = ?"
		args = append(args, status)
	}
	query += " ORDER BY po.order_date DESC LIMIT 100"

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	orders := make([]map[string]interface{}, 0)
	for rows.Next() {
		var id, poNo, storeID, vendorID, vendorName, status string
		var orderDate, expectedDate, createdAt sql.NullString
		var total float64

		rows.Scan(&id, &poNo, &storeID, &vendorID, &vendorName, &orderDate, &expectedDate, &total, &status, &createdAt)
		orders = append(orders, map[string]interface{}{
			"id": id, "po_no": poNo, "store_id": storeID, "vendor_id": vendorID,
			"vendor_name": vendorName, "order_date": orderDate.String,
			"expected_date": expectedDate.String, "total": total, "status": status,
		})
	}

	c.JSON(http.StatusOK, orders)
}

func (h *PurchaseHandler) Get(c *gin.Context) {
	id := c.Param("id")

	var po models.PurchaseOrder
	var vendorName string
	err := db.DB.QueryRow(`
		SELECT po.id, po.po_no, po.store_id, po.vendor_id, v.name as vendor_name,
		       po.order_date, po.expected_date, po.sub_total, po.cgst, po.sgst, po.igst,
		       po.discount, po.total, po.status
		FROM purchase_orders po
		JOIN vendors v ON po.vendor_id = v.id
		WHERE po.id = ? AND po.store_id = ?`, id, sessionStoreID(c)).Scan(
		&po.ID, &po.PO_NO, &po.StoreID, &po.VendorID, &vendorName,
		&po.OrderDate, &po.ExpectedDate, &po.SubTotal, &po.CGST, &po.SGST,
		&po.IGST, &po.Discount, &po.Total, &po.Status)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "purchase order not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	rows, _ := db.DB.Query(`
		SELECT pi.id, pi.product_id, p.name, pi.quantity, pi.uom_id, 
		       pi.received_qty, pi.rate, pi.discount, pi.total
		FROM purchase_items pi
		JOIN products p ON pi.product_id = p.id
		WHERE pi.po_id = ?`, id)
	defer rows.Close()

	type POItem struct {
		ID          string  `json:"id"`
		ProductID   string  `json:"product_id"`
		Name        string  `json:"name"`
		Quantity    float64 `json:"quantity"`
		UOMID       string  `json:"uom_id"`
		ReceivedQty float64 `json:"received_qty"`
		Rate        float64 `json:"rate"`
		Discount    float64 `json:"discount"`
		Total       float64 `json:"total"`
	}
	var items []POItem
	for rows.Next() {
		var item POItem
		rows.Scan(&item.ID, &item.ProductID, &item.Name, &item.Quantity,
			&item.UOMID, &item.ReceivedQty, &item.Rate, &item.Discount, &item.Total)
		items = append(items, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"po":          po,
		"vendor_name": vendorName,
		"items":       items,
	})
}

type PurchaseRequest struct {
	StoreID      string            `json:"store_id" binding:"required"`
	VendorID     string            `json:"vendor_id" binding:"required"`
	OrderDate    string            `json:"order_date" binding:"required"`
	ExpectedDate string            `json:"expected_date"`
	Items        []PurchaseItemReq `json:"items" binding:"required"`
}

type PurchaseItemReq struct {
	ProductID string  `json:"product_id" binding:"required"`
	Quantity  float64 `json:"quantity" binding:"required"`
	UOMID     string  `json:"uom_id"`
	Rate      float64 `json:"rate" binding:"required"`
	Discount  float64 `json:"discount"`
	Total     float64 `json:"total" binding:"required"`
}

func (h *PurchaseHandler) Create(c *gin.Context) {
	var req PurchaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !sessionMayAccessStore(c, req.StoreID) {
		abortStoreMismatch(c)
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	poID := uuid.New().String()
	poNo := "PO-" + time.Now().Format("20060102150405")

	var subtotal, totalGST, total float64
	for _, item := range req.Items {
		subtotal += item.Total - item.Discount
		totalGST += item.Total * 0.05
		total += item.Total - item.Discount
	}
	cgst := totalGST / 2
	sgst := totalGST / 2

	_, err = tx.Exec(`INSERT INTO purchase_orders 
		(id, po_no, store_id, vendor_id, order_date, expected_date, sub_total, cgst, sgst, igst, total, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 'pending')`,
		poID, poNo, req.StoreID, req.VendorID, req.OrderDate, req.ExpectedDate,
		subtotal, cgst, sgst, total)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	for _, item := range req.Items {
		itemID := uuid.New().String()
		_, err = tx.Exec(`INSERT INTO purchase_items 
			(id, po_id, product_id, quantity, uom_id, rate, discount, cgst, sgst, igst, total)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			itemID, poID, item.ProductID, item.Quantity, item.UOMID,
			item.Rate, item.Discount, cgst/float64(len(req.Items)), sgst/float64(len(req.Items)), item.Total-item.Discount)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	tx.Commit()

	c.JSON(http.StatusCreated, gin.H{"id": poID, "po_no": poNo, "total": total})
}

func (h *PurchaseHandler) Receive(c *gin.Context) {
	var req struct {
		POID    string `json:"po_id" binding:"required"`
		StoreID string `json:"store_id" binding:"required"`
		Items   []struct {
			ProductID string  `json:"product_id" binding:"required"`
			Quantity  float64 `json:"quantity" binding:"required"`
			BatchNo   string  `json:"batch_no"`
			CostPrice float64 `json:"cost_price"`
		} `json:"items" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var poStore, poStatus string
	err := db.DB.QueryRow(`SELECT store_id, status FROM purchase_orders WHERE id = ?`, req.POID).Scan(&poStore, &poStatus)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "purchase order not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if poStatus == "cancelled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "purchase order is cancelled"})
		return
	}
	if req.StoreID != poStore {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id does not match purchase order"})
		return
	}
	if !sessionMayAccessStore(c, req.StoreID) {
		abortStoreMismatch(c)
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	for _, item := range req.Items {
		batchNo := item.BatchNo
		if batchNo == "" {
			batchNo = "RCV-" + uuid.New().String()[:12]
		}

		_, err = tx.Exec(`INSERT INTO stock (id, product_id, store_id, batch_no, quantity, cost_price)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(product_id, store_id, batch_no) DO UPDATE SET quantity = quantity + excluded.quantity`,
			uuid.New().String(), item.ProductID, req.StoreID, batchNo,
			item.Quantity, item.CostPrice)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		tx.Exec(`UPDATE purchase_items SET received_qty = received_qty + ?
			WHERE po_id = ? AND product_id = ?`,
			item.Quantity, req.POID, item.ProductID)
	}

	var totalReceived float64
	tx.QueryRow(`SELECT COALESCE(SUM(received_qty), 0) FROM purchase_items WHERE po_id = ?`, req.POID).Scan(&totalReceived)
	var totalOrdered float64
	tx.QueryRow(`SELECT COALESCE(SUM(quantity), 0) FROM purchase_items WHERE po_id = ?`, req.POID).Scan(&totalOrdered)

	status := "partial"
	if totalReceived >= totalOrdered {
		status = "received"
	}
	tx.Exec(`UPDATE purchase_orders SET status = ? WHERE id = ?`, status, req.POID)

	tx.Commit()

	c.JSON(http.StatusOK, gin.H{"status": status})
}

func (h *PurchaseHandler) Cancel(c *gin.Context) {
	id := c.Param("id")

	var poStore string
	err := db.DB.QueryRow(`SELECT store_id FROM purchase_orders WHERE id = ?`, id).Scan(&poStore)
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

	result, err := db.DB.Exec(`UPDATE purchase_orders SET status = 'cancelled' WHERE id = ? AND status = 'pending' AND store_id = ?`, id, poStore)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot cancel: order not found or not pending"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}
