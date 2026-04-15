package handlers

import (
	"net/http"

	"gbos/internal/db"
	"gbos/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ExpenseHandler struct{}

func NewExpenseHandler() *ExpenseHandler {
	return &ExpenseHandler{}
}

type CreateExpenseRequest struct {
	StoreID     string  `json:"store_id" binding:"required"`
	CategoryID  string  `json:"category_id" binding:"required"`
	Amount      float64 `json:"amount" binding:"required"`
	Description string  `json:"description"`
	ExpenseDate string  `json:"expense_date" binding:"required"`
}

func (h *ExpenseHandler) Create(c *gin.Context) {
	var req CreateExpenseRequest
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

	expenseID := uuid.New().String()
	_, err := db.DB.Exec(`INSERT INTO expenses (id, store_id, category_id, amount, description, expense_date, user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		expenseID, req.StoreID, req.CategoryID, req.Amount, req.Description, req.ExpenseDate, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create expense"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": expenseID})
}

func (h *ExpenseHandler) List(c *gin.Context) {
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
	categoryID := c.Query("category_id")
	limit := c.DefaultQuery("limit", "50")

	query := `SELECT e.id, e.store_id, e.category_id, ec.name as category_name, e.amount, e.description, e.expense_date, e.user_id, e.created_at
		FROM expenses e
		JOIN expense_categories ec ON e.category_id = ec.id
		WHERE e.store_id = ?`
	args := []interface{}{storeID}
	if categoryID != "" {
		query += " AND e.category_id = ?"
		args = append(args, categoryID)
	}
	if startDate != "" {
		query += " AND e.expense_date >= ?"
		args = append(args, startDate)
	}
	if endDate != "" {
		query += " AND e.expense_date <= ?"
		args = append(args, endDate)
	}
	query += " ORDER BY e.expense_date DESC LIMIT " + limit

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var expenses []map[string]interface{}
	for rows.Next() {
		var id, storeID, categoryID, categoryName, description, userID string
		var amount float64
		var expenseDate, createdAt string

		rows.Scan(&id, &storeID, &categoryID, &categoryName, &amount, &description, &expenseDate, &userID, &createdAt)

		expenses = append(expenses, map[string]interface{}{
			"id": id, "store_id": storeID, "category_id": categoryID,
			"category_name": categoryName, "amount": amount,
			"description": description, "expense_date": expenseDate,
			"user_id": userID, "created_at": createdAt,
		})
	}

	c.JSON(http.StatusOK, expenses)
}

func (h *ExpenseHandler) GetCategories(c *gin.Context) {
	rows, err := db.DB.Query(`SELECT id, name, is_active FROM expense_categories WHERE is_active = 1 ORDER BY name`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var categories []map[string]interface{}
	for rows.Next() {
		var id, name string
		var isActive int
		rows.Scan(&id, &name, &isActive)
		categories = append(categories, map[string]interface{}{
			"id": id, "name": name, "is_active": isActive == 1,
		})
	}

	c.JSON(http.StatusOK, categories)
}

func (h *ExpenseHandler) GetSummary(c *gin.Context) {
	storeID := c.Query("store_id")
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")

	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}

	query := `SELECT ec.name, SUM(e.amount) as total
		FROM expenses e
		JOIN expense_categories ec ON e.category_id = ec.id
		WHERE e.store_id = ?`
	args := []interface{}{storeID}

	if startDate != "" {
		query += " AND e.expense_date >= ?"
		args = append(args, startDate)
	}
	if endDate != "" {
		query += " AND e.expense_date <= ?"
		args = append(args, endDate)
	}
	query += " GROUP BY ec.id, ec.name ORDER BY total DESC"

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var summary []map[string]interface{}
	var grandTotal float64
	for rows.Next() {
		var name string
		var total float64
		rows.Scan(&name, &total)
		summary = append(summary, map[string]interface{}{"category": name, "total": total})
		grandTotal += total
	}

	c.JSON(http.StatusOK, map[string]interface{}{"summary": summary, "grand_total": grandTotal})
}
