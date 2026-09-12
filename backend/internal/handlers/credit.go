package handlers

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"

	"gbos/internal/db"
	"gbos/internal/middleware"
	"gbos/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type CustomerHandler struct{}

func NewCustomerHandler() *CustomerHandler {
	return &CustomerHandler{}
}

func (h *CustomerHandler) List(c *gin.Context) {
	search := c.Query("search")
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}

	query := `SELECT id, name, name_hi, phone, address, gstin, credit_limit, is_wholesale, store_id, is_active FROM customers WHERE is_active = 1 AND store_id = ?`
	args := []interface{}{storeID}
	if search != "" {
		query += " AND (name LIKE ? OR phone LIKE ?)"
		args = append(args, "%"+search+"%", "%"+search+"%")
	}
	query += " ORDER BY name LIMIT 50"

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var customers []map[string]interface{}
	for rows.Next() {
		var id, name, nameHi, phone, address, gstin, custStoreID string
		var creditLimit float64
		var isWholesale int
		var isActive bool

		rows.Scan(&id, &name, &nameHi, &phone, &address, &gstin, &creditLimit, &isWholesale, &custStoreID, &isActive)
		customers = append(customers, map[string]interface{}{
			"id": id, "name": name, "name_hi": nameHi, "phone": phone,
			"address": address, "gstin": gstin, "credit_limit": creditLimit,
			"is_wholesale": isWholesale != 0, "store_id": custStoreID, "is_active": isActive,
		})
	}

	c.JSON(http.StatusOK, customers)
}

func (h *CustomerHandler) Get(c *gin.Context) {
	id := c.Param("id")

	var customer models.Customer
	var storeID sql.NullString
	var isWholesale int
	err := db.DB.QueryRow(`SELECT id, name, name_hi, phone, address, gstin, credit_limit, is_wholesale, store_id, is_active
		FROM customers WHERE id = ?`, id).Scan(
		&customer.ID, &customer.Name, &customer.NameHi, &customer.Phone,
		&customer.Address, &customer.GSTIN, &customer.CreditLimit, &isWholesale, &storeID, &customer.IsActive)
	customer.IsWholesale = isWholesale != 0
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "customer not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if storeID.Valid && storeID.String != "" {
		if sid, perr := uuid.Parse(storeID.String); perr == nil {
			customer.StoreID = sid
		}
	}
	if !storeID.Valid || storeID.String == "" || !sessionMayAccessStore(c, storeID.String) {
		c.JSON(http.StatusNotFound, gin.H{"error": "customer not found"})
		return
	}

	var outstanding float64
	db.DB.QueryRow(`SELECT COALESCE(SUM(amount - paid_amount), 0) FROM credit_entries 
		WHERE customer_id = ? AND status != 'paid'`, id).Scan(&outstanding)

	c.JSON(http.StatusOK, gin.H{
		"customer":    customer,
		"outstanding": outstanding,
	})
}

func (h *CustomerHandler) Create(c *gin.Context) {
	var req struct {
		Name        string  `json:"name" binding:"required"`
		NameHi      string  `json:"name_hi"`
		Phone       string  `json:"phone"`
		Address     string  `json:"address"`
		GSTIN       string  `json:"gstin"`
		CreditLimit float64 `json:"credit_limit"`
		IsWholesale bool    `json:"is_wholesale"`
		StoreID     string  `json:"store_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.StoreID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, req.StoreID) {
		abortStoreMismatch(c)
		return
	}

	iw := 0
	if req.IsWholesale {
		iw = 1
	}
	customerID := uuid.New().String()
	_, err := db.DB.Exec(`INSERT INTO customers (id, name, name_hi, phone, address, gstin, credit_limit, is_wholesale, store_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		customerID, req.Name, req.NameHi, req.Phone, req.Address, req.GSTIN, req.CreditLimit, iw, req.StoreID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": customerID, "name": req.Name})
}

func (h *CustomerHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Name        string   `json:"name"`
		NameHi      string   `json:"name_hi"`
		Phone       string   `json:"phone"`
		Address     string   `json:"address"`
		GSTIN       string   `json:"gstin"`
		CreditLimit *float64 `json:"credit_limit"`
		IsWholesale *bool    `json:"is_wholesale"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var customerStore sql.NullString
	if err := db.DB.QueryRow(`SELECT store_id FROM customers WHERE id = ?`, id).Scan(&customerStore); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "customer not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load customer"})
		return
	}
	// 404 rather than 403: a caller outside this store should not learn the
	// customer exists.
	if !sessionMayAccessStore(c, customerStore.String) {
		c.JSON(http.StatusNotFound, gin.H{"error": "customer not found"})
		return
	}

	var cl interface{}
	if req.CreditLimit != nil {
		cl = *req.CreditLimit
	}
	var iw interface{}
	if req.IsWholesale != nil {
		if *req.IsWholesale {
			iw = 1
		} else {
			iw = 0
		}
	}

	res, err := db.DB.Exec(`UPDATE customers SET 
		name = COALESCE(NULLIF(?, ''), name),
		name_hi = COALESCE(NULLIF(?, ''), name_hi),
		phone = COALESCE(NULLIF(?, ''), phone),
		address = COALESCE(NULLIF(?, ''), address),
		gstin = COALESCE(NULLIF(?, ''), gstin),
		credit_limit = COALESCE(?, credit_limit),
		is_wholesale = COALESCE(?, is_wholesale),
		updated_at = datetime('now')
		WHERE id = ?`,
		req.Name, req.NameHi, req.Phone, req.Address, req.GSTIN, cl, iw, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "customer not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

type CreditHandler struct{}

func NewCreditHandler() *CreditHandler {
	return &CreditHandler{}
}

func (h *CreditHandler) List(c *gin.Context) {
	customerID := c.Query("customer_id")
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}

	query := `SELECT ce.id, ce.customer_id, c.name as customer_name, ce.invoice_id,
		ce.amount, ce.type, ce.due_date, ce.paid_amount, ce.status, ce.created_at
		FROM credit_entries ce
		JOIN customers c ON ce.customer_id = c.id
		WHERE 1=1`
	args := []interface{}{}

	if customerID != "" {
		query += " AND ce.customer_id = ?"
		args = append(args, customerID)
	}
	query += " AND c.store_id = ?"
	args = append(args, storeID)
	query += " ORDER BY ce.created_at DESC LIMIT 100"

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	entries := make([]map[string]interface{}, 0)
	for rows.Next() {
		var id, customerID, customerName, invoiceID, creditType, status string
		var amount, paidAmount float64
		var dueDate, createdAt sql.NullString

		rows.Scan(&id, &customerID, &customerName, &invoiceID, &amount, &creditType, &dueDate, &paidAmount, &status, &createdAt)
		entries = append(entries, map[string]interface{}{
			"id": id, "customer_id": customerID, "customer_name": customerName,
			"invoice_id": invoiceID, "amount": amount, "type": creditType,
			"due_date": dueDate.String, "paid_amount": paidAmount, "status": status,
			"created_at": createdAt.String,
		})
	}

	c.JSON(http.StatusOK, entries)
}

func (h *CreditHandler) Get(c *gin.Context) {
	id := c.Param("id")

	var entry models.CreditEntry
	var customerName string
	var customerStore sql.NullString
	err := db.DB.QueryRow(`
		SELECT ce.id, ce.customer_id, c.name, c.store_id, ce.invoice_id, ce.amount, ce.type,
		       ce.due_date, ce.paid_amount, ce.status, ce.created_at
		FROM credit_entries ce
		JOIN customers c ON ce.customer_id = c.id
		WHERE ce.id = ?`, id).Scan(
		&entry.ID, &entry.CustomerID, &customerName, &customerStore,
		&entry.InvoiceID, &entry.Amount, &entry.Type,
		&entry.DueDate, &entry.PaidAmount, &entry.Status, &entry.CreatedAt)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "credit entry not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load credit entry"})
		return
	}
	// 404 rather than 403: a caller outside this store should not learn the
	// entry exists.
	if !sessionMayAccessStore(c, customerStore.String) {
		c.JSON(http.StatusNotFound, gin.H{"error": "credit entry not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"entry":         entry,
		"customer_name": customerName,
	})
}

func (h *CreditHandler) Payment(c *gin.Context) {
	var req struct {
		CustomerID string  `json:"customer_id" binding:"required"`
		Amount     float64 `json:"amount" binding:"required"`
		Method     string  `json:"method" binding:"required"` // cash, upi, bank
		Notes      string  `json:"notes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	headerStore := middleware.GetStoreID(c)
	if headerStore == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store context required"})
		return
	}
	var custStore sql.NullString
	if err := db.DB.QueryRow(`SELECT store_id FROM customers WHERE id = ?`, req.CustomerID).Scan(&custStore); err == sql.ErrNoRows {
		c.JSON(http.StatusBadRequest, gin.H{"error": "customer not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !custStore.Valid || custStore.String != headerStore {
		c.JSON(http.StatusForbidden, gin.H{"error": "customer does not belong to current store"})
		return
	}

	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	paymentID := uuid.New().String()
	_, err = tx.Exec(`INSERT INTO credit_entries (id, customer_id, amount, type, status)
		VALUES (?, ?, ?, 'payment', 'completed')`,
		paymentID, req.CustomerID, req.Amount)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	rows, err := tx.Query(`SELECT id, amount, paid_amount FROM credit_entries 
		WHERE customer_id = ? AND type = 'issue' AND status IN ('pending','partial')
		ORDER BY datetime(created_at) ASC`, req.CustomerID)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	remaining := req.Amount
	for rows.Next() && remaining > 0 {
		var id string
		var amount, paid float64
		if err := rows.Scan(&id, &amount, &paid); err != nil {
			continue
		}
		due := amount - paid
		if due <= 0 {
			continue
		}
		pay := math.Min(remaining, due)
		newPaid := paid + pay
		st := "partial"
		if newPaid >= amount {
			st = "paid"
		}
		_, err = tx.Exec(`UPDATE credit_entries SET paid_amount = ?, status = ? WHERE id = ?`, newPaid, st, id)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		remaining -= pay
	}
	if err := rows.Err(); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Handle overpayment: store as advance credit
	var advanceNote string
	if remaining > 0.01 {
		advanceID := uuid.New().String()
		_, err = tx.Exec(`INSERT INTO credit_entries (id, customer_id, amount, paid_amount, type, status)
			VALUES (?, ?, ?, ?, 'advance', 'completed')`,
			advanceID, req.CustomerID, remaining, remaining)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		advanceNote = fmt.Sprintf("%.2f advance balance recorded", remaining)
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	auditAction(c, "credit_payment", "credit_entries", paymentID, "", fmt.Sprintf("%.2f", req.Amount))
	resp := gin.H{"id": paymentID, "status": "completed"}
	if advanceNote != "" {
		resp["advance_note"] = advanceNote
	}
	c.JSON(http.StatusOK, resp)
}

func (h *CreditHandler) Overdue(c *gin.Context) {
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}

	query := `
		SELECT c.id, c.name, c.phone, c.credit_limit,
		       COALESCE(SUM(ce.amount - ce.paid_amount), 0) as outstanding,
		       COUNT(ce.id) as due_count,
		       MIN(ce.due_date) as earliest_due
		FROM customers c
		LEFT JOIN credit_entries ce ON c.id = ce.customer_id 
			AND ce.status != 'paid' AND ce.due_date < date('now')
		WHERE c.store_id = ?`
	args := []interface{}{storeID}
	query += ` GROUP BY c.id, c.name, c.phone, c.credit_limit
		HAVING outstanding > 0
		ORDER BY outstanding DESC`

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	overdue := make([]map[string]interface{}, 0)
	for rows.Next() {
		var id, name, phone string
		var creditLimit, outstanding float64
		var dueCount int
		var earliestDue sql.NullString

		rows.Scan(&id, &name, &phone, &creditLimit, &outstanding, &dueCount, &earliestDue)
		overdue = append(overdue, map[string]interface{}{
			"customer_id": id, "name": name, "phone": phone,
			"credit_limit": creditLimit, "outstanding": outstanding,
			"due_count": dueCount, "earliest_due": earliestDue.String,
		})
	}

	c.JSON(http.StatusOK, overdue)
}
