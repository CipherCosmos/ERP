package handlers

import (
	"database/sql"
	"net/http"

	"gbos/internal/db"
	"gbos/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type StoreHandler struct{}

func NewStoreHandler() *StoreHandler {
	return &StoreHandler{}
}

func (h *StoreHandler) List(c *gin.Context) {
	rows, err := db.DB.Query(`SELECT id, name, address, phone, gstin, COALESCE(location_type, 'store'), is_active, created_at, updated_at FROM stores WHERE is_active = 1`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var stores []map[string]interface{}
	for rows.Next() {
		var id, name, address, phone, gstin, locationType string
		var isActive bool
		var createdAt, updatedAt string
		rows.Scan(&id, &name, &address, &phone, &gstin, &locationType, &isActive, &createdAt, &updatedAt)
		stores = append(stores, map[string]interface{}{
			"id": id, "name": name, "address": address, "phone": phone,
			"gstin": gstin, "location_type": locationType, "is_active": isActive, "created_at": createdAt, "updated_at": updatedAt,
		})
	}

	c.JSON(http.StatusOK, stores)
}

func (h *StoreHandler) Get(c *gin.Context) {
	id := c.Param("id")

	var store models.Store
	err := db.DB.QueryRow(`SELECT id, name, address, phone, gstin, COALESCE(location_type, 'store'), is_active, created_at, updated_at 
		FROM stores WHERE id = ?`, id).Scan(
		&store.ID, &store.Name, &store.Address, &store.Phone,
		&store.GSTIN, &store.LocationType, &store.IsActive, &store.CreatedAt, &store.UpdatedAt)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "store not found"})
		return
	}

	c.JSON(http.StatusOK, store)
}

func (h *StoreHandler) Create(c *gin.Context) {
	var req struct {
		Name         string `json:"name" binding:"required"`
		Address      string `json:"address"`
		Phone        string `json:"phone"`
		GSTIN        string `json:"gstin"`
		LocationType string `json:"location_type"` // store | warehouse
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	loc := req.LocationType
	if loc != "warehouse" {
		loc = "store"
	}

	storeID := uuid.New().String()
	_, err := db.DB.Exec(`INSERT INTO stores (id, name, address, phone, gstin, location_type) VALUES (?, ?, ?, ?, ?, ?)`,
		storeID, req.Name, req.Address, req.Phone, req.GSTIN, loc)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": storeID, "name": req.Name})
}

func (h *StoreHandler) Update(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		Name         string `json:"name"`
		Address      string `json:"address"`
		Phone        string `json:"phone"`
		GSTIN        string `json:"gstin"`
		LocationType string `json:"location_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	_, err := db.DB.Exec(`UPDATE stores SET 
		name = COALESCE(NULLIF(?, ''), name),
		address = COALESCE(NULLIF(?, ''), address),
		phone = COALESCE(NULLIF(?, ''), phone),
		gstin = COALESCE(NULLIF(?, ''), gstin),
		location_type = CASE WHEN ? IN ('store','warehouse') THEN ? ELSE location_type END,
		updated_at = datetime('now')
		WHERE id = ?`,
		req.Name, req.Address, req.Phone, req.GSTIN,
		req.LocationType, req.LocationType, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

type UOMHandler struct{}

func NewUOMHandler() *UOMHandler {
	return &UOMHandler{}
}

func (h *UOMHandler) List(c *gin.Context) {
	rows, err := db.DB.Query(`SELECT id, code, name, decimal_places, is_active FROM units_of_measure WHERE is_active = 1`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var uoms []map[string]interface{}
	for rows.Next() {
		var id, code, name string
		var decimalPlaces int
		var isActive bool
		rows.Scan(&id, &code, &name, &decimalPlaces, &isActive)
		uoms = append(uoms, map[string]interface{}{
			"id": id, "code": code, "name": name, "decimal_places": decimalPlaces, "is_active": isActive,
		})
	}

	c.JSON(http.StatusOK, uoms)
}

func (h *UOMHandler) Create(c *gin.Context) {
	var req struct {
		Code          string `json:"code" binding:"required"`
		Name          string `json:"name" binding:"required"`
		DecimalPlaces int    `json:"decimal_places"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	uomID := uuid.New().String()
	_, err := db.DB.Exec(`INSERT INTO units_of_measure (id, code, name, decimal_places) VALUES (?, ?, ?, ?)`,
		uomID, req.Code, req.Name, req.DecimalPlaces)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": uomID, "code": req.Code, "name": req.Name})
}

type BarcodeHandler struct{}

func NewBarcodeHandler() *BarcodeHandler {
	return &BarcodeHandler{}
}

func (h *BarcodeHandler) Generate(c *gin.Context) {
	var req struct {
		CategoryID string `json:"category_id"`
		ProductID  string `json:"product_id"`
		Prefix     string `json:"prefix"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var barcode string
	if req.Prefix != "" {
		barcode = req.Prefix + uuid.New().String()[:8]
	} else if req.CategoryID != "" {
		cat := req.CategoryID
		if len(cat) > 4 {
			cat = cat[:4]
		}
		barcode = "89" + cat + uuid.New().String()[:8]
	} else {
		barcode = "89" + uuid.New().String()[:10]
	}

	c.JSON(http.StatusOK, gin.H{"barcode": barcode, "format": "EAN13"})
}

func (h *BarcodeHandler) Lookup(c *gin.Context) {
	barcode := c.Param("barcode")
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}

	stockSub := `(SELECT COALESCE(SUM(quantity), 0) FROM stock s WHERE s.product_id = p.id AND s.store_id = ?)`
	sel := `
		SELECT p.id, p.name, COALESCE(p.name_hi, ''), COALESCE(p.name_kn, ''), p.barcode, COALESCE(p.hsn_code, ''), p.gst_rate,
		       p.uom_id, p.category_id, p.base_unit_qty, p.allow_decimal, p.min_stock, p.is_free, p.is_active,
		       COALESCE(pp.sell_price, 0), COALESCE(pp.mrp, 0), COALESCE(pp.wholesale_sell_price, 0),
		       ` + stockSub + `
		FROM products p
		LEFT JOIN product_prices pp ON p.id = pp.product_id AND pp.store_id = ?
	`

	var (
		id, name, nameHi, nameKn, pbarcode, hsn, uomID, categoryID   string
		baseQty, gstRate, minStock, sellPrice, mrp, wholesale, stock float64
		allowDec, isFree, isActive                                   int
	)

	err := db.DB.QueryRow(sel+` WHERE p.barcode = ? AND p.is_active = 1`, storeID, storeID, barcode).Scan(
		&id, &name, &nameHi, &nameKn, &pbarcode, &hsn, &gstRate,
		&uomID, &categoryID, &baseQty, &allowDec, &minStock, &isFree, &isActive,
		&sellPrice, &mrp, &wholesale, &stock)
	if err == sql.ErrNoRows {
		err = db.DB.QueryRow(sel+`
			INNER JOIN product_units u ON u.product_id = p.id AND u.barcode = ? AND u.barcode != ''
			WHERE p.is_active = 1`, storeID, storeID, barcode).Scan(
			&id, &name, &nameHi, &nameKn, &pbarcode, &hsn, &gstRate,
			&uomID, &categoryID, &baseQty, &allowDec, &minStock, &isFree, &isActive,
			&sellPrice, &mrp, &wholesale, &stock)
	}
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id": id, "name": name, "name_hi": nameHi, "name_kn": nameKn,
		"barcode": pbarcode, "hsn_code": hsn, "gst_rate": gstRate,
		"uom_id": uomID, "category_id": categoryID, "base_unit_qty": baseQty,
		"allow_decimal": allowDec != 0, "min_stock": minStock,
		"is_free": isFree != 0, "is_active": isActive != 0,
		"sell_price": sellPrice, "mrp": mrp, "wholesale_sell_price": wholesale, "stock": stock,
	})
}

type ExpiryHandler struct{}

func NewExpiryHandler() *ExpiryHandler {
	return &ExpiryHandler{}
}

func (h *ExpiryHandler) GetExpiring(c *gin.Context) {
	storeID := c.Query("store_id")
	days := c.DefaultQuery("days", "30")

	rows, err := db.DB.Query(`
		SELECT s.id, p.name, s.batch_no, s.quantity, s.expiry_date,
		       CAST(julianday(s.expiry_date) - julianday('now') AS INTEGER) as days_until_expiry
		FROM stock s
		JOIN products p ON s.product_id = p.id
		WHERE s.store_id = ? AND s.quantity > 0 AND s.expiry_date IS NOT NULL
		  AND s.expiry_date <= date('now', '+' || ? || ' days')
		ORDER BY s.expiry_date ASC`, storeID, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var expiring []map[string]interface{}
	for rows.Next() {
		var id, productName, batchNo string
		var quantity float64
		var expiryDate string
		var daysUntilExpiry int

		rows.Scan(&id, &productName, &batchNo, &quantity, &expiryDate, &daysUntilExpiry)
		expiring = append(expiring, map[string]interface{}{
			"id": id, "product_name": productName, "batch_no": batchNo,
			"quantity": quantity, "expiry_date": expiryDate, "days_until_expiry": daysUntilExpiry,
		})
	}

	c.JSON(http.StatusOK, expiring)
}

func (h *ExpiryHandler) GetExpired(c *gin.Context) {
	storeID := c.Query("store_id")

	rows, err := db.DB.Query(`
		SELECT s.id, p.name, s.batch_no, s.quantity, s.expiry_date
		FROM stock s
		JOIN products p ON s.product_id = p.id
		WHERE s.store_id = ? AND s.quantity > 0 AND s.expiry_date IS NOT NULL
		  AND s.expiry_date < date('now')
		ORDER BY s.expiry_date DESC`, storeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var expired []map[string]interface{}
	for rows.Next() {
		var id, productName, batchNo string
		var quantity float64
		var expiryDate string

		rows.Scan(&id, &productName, &batchNo, &quantity, &expiryDate)
		expired = append(expired, map[string]interface{}{
			"id": id, "product_name": productName, "batch_no": batchNo,
			"quantity": quantity, "expiry_date": expiryDate,
		})
	}

	c.JSON(http.StatusOK, expired)
}
