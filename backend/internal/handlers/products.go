package handlers

import (
	"database/sql"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gbos/internal/db"
	"gbos/internal/models"
)

type CategoryHandler struct{}

func NewCategoryHandler() *CategoryHandler {
	return &CategoryHandler{}
}

func (h *CategoryHandler) List(c *gin.Context) {
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}

	rows, err := db.DB.Query(`
		SELECT id, name, name_hi, name_kn, parent_id, hsn_code, is_active, created_at, updated_at
		FROM categories WHERE is_active = 1 ORDER BY name`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var categories []models.Category
	for rows.Next() {
		var cat models.Category
		err := rows.Scan(&cat.ID, &cat.Name, &cat.NameHi, &cat.NameKn, &cat.ParentID, &cat.HSNCode, &cat.IsActive, &cat.CreatedAt, &cat.UpdatedAt)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		categories = append(categories, cat)
	}

	c.JSON(http.StatusOK, categories)
}

func (h *CategoryHandler) Create(c *gin.Context) {
	var req struct {
		Name    string  `json:"name" binding:"required"`
		NameHi string  `json:"name_hi"`
		NameKn string  `json:"name_kn"`
		HSNCode string `json:"hsn_code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	catID := uuid.New().String()
	_, err := db.DB.Exec(`INSERT INTO categories (id, name, name_hi, name_kn, hsn_code) VALUES (?, ?, ?, ?, ?)`,
		catID, req.Name, req.NameHi, req.NameKn, req.HSNCode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": catID, "name": req.Name})
}

type ProductHandler struct{}

func NewProductHandler() *ProductHandler {
	return &ProductHandler{}
}

func (h *ProductHandler) List(c *gin.Context) {
	storeID := c.Query("store_id")
	categoryID := c.Query("category_id")
	barcode := c.Query("barcode")
	search := c.Query("search")

	query := `
		SELECT p.id, p.name, COALESCE(p.name_hi, ''), COALESCE(p.name_kn, ''), p.category_id, COALESCE(p.hsn_code, ''), p.barcode, 
		       p.uom_id, p.base_unit_qty, p.gst_rate, p.is_free, p.is_active, p.allow_decimal, p.min_stock,
		       COALESCE(pp.sell_price, 0) as sell_price,
		       COALESCE(pp.mrp, 0) as mrp,
		       COALESCE(pp.wholesale_sell_price, 0) as wholesale_sell_price
		FROM products p
		LEFT JOIN product_prices pp ON p.id = pp.product_id AND pp.store_id = ?
		WHERE p.is_active = 1`
	args := []interface{}{storeID}

	if categoryID != "" {
		query += " AND p.category_id = ?"
		args = append(args, categoryID)
	}
	if barcode != "" {
		query += " AND p.barcode = ?"
		args = append(args, barcode)
	}
	if search != "" {
		query += " AND (p.name LIKE ? OR p.barcode LIKE ?)"
		args = append(args, "%"+search+"%", "%"+search+"%")
	}
	query += " ORDER BY p.name LIMIT 100"

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	products := make([]map[string]interface{}, 0)
	for rows.Next() {
		var id, name, nameHi, nameKn, categoryID, hsnCode, barcode, uomID string
		var baseUnitQty, gstRate, minStock, sellPrice, mrp, wholesaleSell float64
		var isFree, isActive, allowDecimal bool

		err := rows.Scan(&id, &name, &nameHi, &nameKn, &categoryID, &hsnCode, &barcode,
			&uomID, &baseUnitQty, &gstRate, &isFree, &isActive, &allowDecimal, &minStock,
			&sellPrice, &mrp, &wholesaleSell)
		if err != nil {
			fmt.Println("Scan error:", err)
			continue
		}
		products = append(products, map[string]interface{}{
			"id": id, "name": name, "name_hi": nameHi, "name_kn": nameKn,
			"category_id": categoryID, "hsn_code": hsnCode, "barcode": barcode,
			"uom_id": uomID, "base_unit_qty": baseUnitQty, "gst_rate": gstRate,
			"is_free": isFree, "is_active": isActive, "allow_decimal": allowDecimal,
			"min_stock": minStock, "sell_price": sellPrice, "mrp": mrp,
			"wholesale_sell_price": wholesaleSell,
		})
	}

	c.JSON(http.StatusOK, products)
}

func (h *ProductHandler) Get(c *gin.Context) {
	id := c.Param("id")
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}

	var product models.Product
	var sellPrice, mrp, costPrice, wholesale float64
	err := db.DB.QueryRow(`
		SELECT p.id, p.name, p.name_hi, p.name_kn, p.category_id, p.hsn_code, p.barcode,
		       p.uom_id, p.base_unit_qty, p.gst_rate, p.is_free, p.is_active, p.allow_decimal, p.min_stock,
		       COALESCE(pp.sell_price, 0), COALESCE(pp.mrp, 0), COALESCE(pp.cost_price, 0),
		       COALESCE(pp.wholesale_sell_price, 0)
		FROM products p
		LEFT JOIN product_prices pp ON p.id = pp.product_id AND pp.store_id = ?
		WHERE p.id = ?`, storeID, id).Scan(
		&product.ID, &product.Name, &product.NameHi, &product.NameKn, &product.CategoryID,
		&product.HSNCode, &product.Barcode, &product.UOMID, &product.BaseUnitQty, &product.GSTRate,
		&product.IsFree, &product.Is_active, &product.AllowDecimal, &product.MinStock,
		&sellPrice, &mrp, &costPrice, &wholesale,
	)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id": product.ID, "name": product.Name, "name_hi": product.NameHi, "name_kn": product.NameKn,
		"category_id": product.CategoryID, "hsn_code": product.HSNCode, "barcode": product.Barcode,
		"uom_id": product.UOMID, "base_unit_qty": product.BaseUnitQty, "gst_rate": product.GSTRate,
		"is_free": product.IsFree, "is_active": product.Is_active, "allow_decimal": product.AllowDecimal,
		"min_stock": product.MinStock,
		"sell_price": sellPrice, "mrp": mrp, "cost_price": costPrice, "wholesale_sell_price": wholesale,
	})
}

func (h *ProductHandler) Create(c *gin.Context) {
	var req struct {
		Name         string  `json:"name" binding:"required"`
		NameHi      string  `json:"name_hi"`
		NameKn      string  `json:"name_kn"`
		CategoryID  string  `json:"category_id" binding:"required"`
		HSNCode     string  `json:"hsn_code"`
		Barcode     string  `json:"barcode"`
		UOMID       string  `json:"uom_id" binding:"required"`
		BaseUnitQty float64 `json:"base_unit_qty"`
		GSTRate     float64 `json:"gst_rate"`
		IsFree      bool    `json:"is_free"`
		AllowDecimal bool  `json:"allow_decimal"`
		MinStock    float64 `json:"min_stock"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.BaseUnitQty == 0 {
		req.BaseUnitQty = 1
	}

	barcode := req.Barcode
	if barcode == "" {
		catPrefix := req.CategoryID
		if len(catPrefix) > 4 {
			catPrefix = catPrefix[:4]
		}
		barcode = fmt.Sprintf("89%s%s", catPrefix, uuid.New().String()[:8])
	}

	productID := uuid.New().String()
	_, err := db.DB.Exec(`INSERT INTO products 
		(id, name, name_hi, name_kn, category_id, hsn_code, barcode, uom_id, base_unit_qty, gst_rate, is_free, allow_decimal, min_stock)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		productID, req.Name, req.NameHi, req.NameKn, req.CategoryID, req.HSNCode, barcode,
		req.UOMID, req.BaseUnitQty, req.GSTRate, req.IsFree, req.AllowDecimal, req.MinStock)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": productID, "barcode": barcode})
}

func (h *ProductHandler) SetPrice(c *gin.Context) {
	var req struct {
		ProductID           string  `json:"product_id" binding:"required"`
		StoreID             string  `json:"store_id" binding:"required"`
		MRP                 float64 `json:"mrp"`
		CostPrice           float64 `json:"cost_price"`
		SellPrice           float64 `json:"sell_price" binding:"required"`
		WholesaleSellPrice  float64 `json:"wholesale_sell_price"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var existingID string
	err := db.DB.QueryRow(`SELECT id FROM product_prices WHERE product_id = ? AND store_id = ?`,
		req.ProductID, req.StoreID).Scan(&existingID)
	var priceID string
	if err == sql.ErrNoRows {
		priceID = uuid.New().String()
		_, err = db.DB.Exec(`INSERT INTO product_prices (id, product_id, store_id, mrp, cost_price, sell_price, wholesale_sell_price)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			priceID, req.ProductID, req.StoreID, req.MRP, req.CostPrice, req.SellPrice, req.WholesaleSellPrice)
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	} else {
		priceID = existingID
		_, err = db.DB.Exec(`UPDATE product_prices SET mrp = ?, cost_price = ?, sell_price = ?, wholesale_sell_price = ?, updated_at = datetime('now') WHERE id = ?`,
			req.MRP, req.CostPrice, req.SellPrice, req.WholesaleSellPrice, existingID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	auditAction(c, "price_set", "product_prices", req.ProductID, "", req.StoreID)
	c.JSON(http.StatusOK, gin.H{"id": priceID})
}

func (h *ProductHandler) ListPackingUnits(c *gin.Context) {
	productID := c.Param("id")
	rows, err := db.DB.Query(`
		SELECT id, product_id, parent_unit_id, label, qty_in_parent, barcode, sort_order, created_at
		FROM product_units WHERE product_id = ? ORDER BY sort_order, label`, productID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var out []map[string]interface{}
	for rows.Next() {
		var id, pid, label, createdAt string
		var parentID, barcode sql.NullString
		var qty float64
		var sortOrder int
		if err := rows.Scan(&id, &pid, &parentID, &label, &qty, &barcode, &sortOrder, &createdAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		m := map[string]interface{}{
			"id": id, "product_id": pid, "label": label, "qty_in_parent": qty,
			"sort_order": sortOrder, "created_at": createdAt,
		}
		if parentID.Valid {
			m["parent_unit_id"] = parentID.String
		}
		if barcode.Valid {
			m["barcode"] = barcode.String
		}
		out = append(out, m)
	}
	c.JSON(http.StatusOK, out)
}

func (h *ProductHandler) CreatePackingUnit(c *gin.Context) {
	productID := c.Param("id")
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(1) FROM products WHERE id = ?`, productID).Scan(&n); err != nil || n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}

	var req struct {
		Label          string   `json:"label" binding:"required"`
		QtyInParent    float64  `json:"qty_in_parent"`
		Barcode        string   `json:"barcode"`
		ParentUnitID   *string  `json:"parent_unit_id"`
		SortOrder      int      `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.QtyInParent <= 0 {
		req.QtyInParent = 1
	}

	var parent interface{}
	if req.ParentUnitID != nil && *req.ParentUnitID != "" {
		parent = *req.ParentUnitID
	}

	unitID := uuid.New().String()
	_, err := db.DB.Exec(`INSERT INTO product_units (id, product_id, parent_unit_id, label, qty_in_parent, barcode, sort_order)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?)`,
		unitID, productID, parent, req.Label, req.QtyInParent, req.Barcode, req.SortOrder)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": unitID})
}