// Package handler holds the HTTP layer: bind a request, call a service,
// render the result. No SQL, no business rules, no transactions.
package handler

import (
	"errors"
	"net/http"

	domain "gbos/internal/domain/catalogue"
	"gbos/internal/domain/money"
	mw "gbos/internal/http/middleware"
	scope "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	"gbos/internal/rbac"
	service "gbos/internal/service/catalogue"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Catalogue serves the product catalogue endpoints.
type Catalogue struct {
	svc *service.Service
}

// NewCatalogue returns a catalogue handler.
func NewCatalogue(svc *service.Service) *Catalogue {
	return &Catalogue{svc: svc}
}

// Register mounts the catalogue routes. Every route that reads or writes
// store-scoped data sits behind the store middleware, so no handler below can
// see a store id that has not been authorised.
func (h *Catalogue) Register(r gin.IRoutes) {
	r.GET("/categories", mw.RequirePermission(rbac.PermCatalogRead), h.ListCategories)
	r.POST("/categories", mw.RequirePermission(rbac.PermCategoryWrite), h.CreateCategory)
	r.POST("/categories/:id/deactivate", mw.RequirePermission(rbac.PermCategoryWrite), h.DeactivateCategory)

	// Unit families and units of measure are catalogue reference data. They are
	// rows rather than an enum precisely so a trade the shop takes up next year
	// needs an INSERT and not a deploy.
	r.GET("/unit-families", mw.RequirePermission(rbac.PermCatalogRead), h.ListFamilies)
	r.POST("/unit-families", mw.RequirePermission(rbac.PermUOMWrite), h.CreateFamily)
	r.PUT("/unit-families/:id", mw.RequirePermission(rbac.PermUOMWrite), h.UpdateFamily)
	r.POST("/unit-families/:id/base-unit", mw.RequirePermission(rbac.PermUOMWrite), h.SetFamilyBaseUnit)
	r.POST("/unit-families/:id/deactivate", mw.RequirePermission(rbac.PermUOMWrite), h.DeactivateFamily)
	r.POST("/unit-families/:id/activate", mw.RequirePermission(rbac.PermUOMWrite), h.ActivateFamily)

	r.GET("/uoms", mw.RequirePermission(rbac.PermCatalogRead), h.ListUnits)
	r.POST("/uoms", mw.RequirePermission(rbac.PermUOMWrite), h.CreateUnit)
	r.PUT("/uoms/:id", mw.RequirePermission(rbac.PermUOMWrite), h.UpdateUnit)
	r.POST("/uoms/:id/deactivate", mw.RequirePermission(rbac.PermUOMWrite), h.DeactivateUnit)
	r.POST("/uoms/:id/activate", mw.RequirePermission(rbac.PermUOMWrite), h.ActivateUnit)

	r.GET("/products", mw.RequirePermission(rbac.PermCatalogRead), h.ListProducts)
	r.GET("/products/:id", mw.RequirePermission(rbac.PermCatalogRead), h.GetProduct)
	r.POST("/products", mw.RequirePermission(rbac.PermProductCatalogWrite), h.CreateProduct)
	r.PUT("/products/:id", mw.RequirePermission(rbac.PermProductUpdate), h.UpdateProduct)
	r.POST("/products/:id/deactivate", mw.RequirePermission(rbac.PermProductUpdate), h.DeactivateProduct)
	r.POST("/products/:id/activate", mw.RequirePermission(rbac.PermProductUpdate), h.ActivateProduct)

	r.POST("/products/price", mw.RequirePermission(rbac.PermProductPrice), h.SetPrice)
	r.GET("/products/:id/prices", mw.RequirePermission(rbac.PermCatalogRead), h.ListPrices)

	// Was /products/:id/packing-units. A product unit is no longer "how it is
	// boxed" but "what it may be transacted as", which is a different question
	// and deserves the plainer path.
	r.GET("/products/:id/units", mw.RequirePermission(rbac.PermCatalogRead), h.ListProductUnits)
	r.POST("/products/:id/units", mw.RequirePermission(rbac.PermProductCatalogWrite), h.CreateProductUnit)
	r.PUT("/products/:id/units/:unit_id", mw.RequirePermission(rbac.PermProductCatalogWrite), h.UpdateProductUnit)
	r.POST("/products/:id/units/:unit_id/default", mw.RequirePermission(rbac.PermProductCatalogWrite), h.SetDefaultUnit)
	r.POST("/products/:id/units/:unit_id/deactivate", mw.RequirePermission(rbac.PermProductCatalogWrite), h.DeactivateProductUnit)
	r.POST("/products/:id/units/:unit_id/activate", mw.RequirePermission(rbac.PermProductCatalogWrite), h.ActivateProductUnit)

	r.GET("/barcodes/lookup/:barcode", mw.RequirePermission(rbac.PermCatalogRead), h.LookupBarcode)
}

// ---------------------------------------------------------------------------
// Unit families
// ---------------------------------------------------------------------------

func (h *Catalogue) ListFamilies(c *gin.Context) {
	families, err := h.svc.ListFamilies(c.Request.Context(), c.Query("include_inactive") == "true")
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"families": families})
}

type familyRequest struct {
	Code string `json:"code" binding:"required"`
	Name string `json:"name" binding:"required"`
}

func (h *Catalogue) CreateFamily(c *gin.Context) {
	var req familyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	created, err := h.svc.CreateFamily(c.Request.Context(), req.Code, req.Name)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

// updateFamilyRequest carries only the name. A family's code is what seed data
// and any import file join on, so it is immutable.
type updateFamilyRequest struct {
	Name string `json:"name" binding:"required"`
}

func (h *Catalogue) UpdateFamily(c *gin.Context) {
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	var req updateFamilyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	updated, err := h.svc.UpdateFamily(c.Request.Context(), id, req.Name)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

type familyBaseUnitRequest struct {
	UOMID string `json:"uom_id" binding:"required"`
}

func (h *Catalogue) SetFamilyBaseUnit(c *gin.Context) {
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	var req familyBaseUnitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	uomID, err := uuid.Parse(req.UOMID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("uom_id is not a valid identifier"))
		return
	}
	if err := h.svc.SetFamilyBaseUnit(c.Request.Context(), id, uomID); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "base_uom_id": uomID})
}

func (h *Catalogue) DeactivateFamily(c *gin.Context) { h.setFamilyActive(c, false) }
func (h *Catalogue) ActivateFamily(c *gin.Context)   { h.setFamilyActive(c, true) }

func (h *Catalogue) setFamilyActive(c *gin.Context, active bool) {
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.SetFamilyActive(c.Request.Context(), id, active); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "is_active": active})
}

func (h *Catalogue) ListCategories(c *gin.Context) {
	categories, err := h.svc.ListCategories(c.Request.Context())
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"categories": categories})
}

type createCategoryRequest struct {
	Name     string  `json:"name" binding:"required"`
	NameHi   string  `json:"name_hi"`
	NameKn   string  `json:"name_kn"`
	HSNCode  string  `json:"hsn_code"`
	ParentID *string `json:"parent_id"`
}

func (h *Catalogue) CreateCategory(c *gin.Context) {
	var req createCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	parentID, err := optionalUUID(req.ParentID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("parent_id is not a valid identifier"))
		return
	}

	created, err := h.svc.CreateCategory(c.Request.Context(), req.Name, req.NameHi, req.NameKn, req.HSNCode, parentID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (h *Catalogue) DeactivateCategory(c *gin.Context) {
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.SetCategoryActive(c.Request.Context(), id, false); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "is_active": false})
}

func (h *Catalogue) ListUnits(c *gin.Context) {
	familyID, err := optionalUUID(queryPtr(c, "family_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("family_id is not a valid identifier"))
		return
	}
	units, err := h.svc.ListUnits(c.Request.Context(), domain.UnitFilter{
		FamilyID:        familyID,
		IncludeInactive: c.Query("include_inactive") == "true",
	})
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"units": units})
}

// createUnitRequest is the wire shape for a unit of measure.
//
// factor_to_base is a money.Rate rather than a bare decimal, so it crosses the
// wire as a JSON number: a milligram is 0.000001 of a kilo and a square foot
// 0.09290304 of a square metre, and both have to survive the round trip whole.
type createUnitRequest struct {
	Code          string     `json:"code" binding:"required"`
	Name          string     `json:"name" binding:"required"`
	DecimalPlaces int        `json:"decimal_places"`
	FamilyID      string     `json:"family_id" binding:"required"`
	FactorToBase  money.Rate `json:"factor_to_base"`
}

func (h *Catalogue) CreateUnit(c *gin.Context) {
	var req createUnitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	familyID, err := uuid.Parse(req.FamilyID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("family_id is not a valid identifier"))
		return
	}
	// An omitted factor means "this IS the family's canonical unit", which is
	// how a kilo relates to a kilo. Any other reading of an absent factor is a
	// division by zero waiting in the conversion path.
	factor := req.FactorToBase
	if factor.IsZero() {
		factor = money.RateFromInt(1)
	}

	created, err := h.svc.CreateUnit(c.Request.Context(), service.CreateUnitInput{
		Code:          req.Code,
		Name:          req.Name,
		DecimalPlaces: req.DecimalPlaces,
		FamilyID:      familyID,
		FactorToBase:  factor,
	})
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

// updateUnitRequest omits the code and the family: see the repository for why a
// unit cannot change families after anything has been measured with it.
type updateUnitRequest struct {
	Name          string     `json:"name" binding:"required"`
	DecimalPlaces int        `json:"decimal_places"`
	FactorToBase  money.Rate `json:"factor_to_base"`
}

func (h *Catalogue) UpdateUnit(c *gin.Context) {
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	var req updateUnitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	factor := req.FactorToBase
	if factor.IsZero() {
		factor = money.RateFromInt(1)
	}

	updated, err := h.svc.UpdateUnit(c.Request.Context(), id, service.UpdateUnitInput{
		Name:          req.Name,
		DecimalPlaces: req.DecimalPlaces,
		FactorToBase:  factor,
	})
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (h *Catalogue) DeactivateUnit(c *gin.Context) { h.setUnitActive(c, false) }
func (h *Catalogue) ActivateUnit(c *gin.Context)   { h.setUnitActive(c, true) }

func (h *Catalogue) setUnitActive(c *gin.Context, active bool) {
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.SetUnitActive(c.Request.Context(), id, active); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "is_active": active})
}

func (h *Catalogue) ListProducts(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	categoryID, err := optionalUUID(queryPtr(c, "category_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("category_id is not a valid identifier"))
		return
	}

	products, err := h.svc.ListProducts(c.Request.Context(), domain.ProductFilter{
		StoreID:         storeID,
		CategoryID:      categoryID,
		Barcode:         c.Query("barcode"),
		Search:          c.Query("search"),
		Limit:           intQuery(c, "limit", 0),
		IncludeInactive: c.Query("include_inactive") == "true",
		// ?unclassified=true lists the products carrying neither an HSN nor a
		// SAC code. New products cannot be saved without one; these are the
		// ones that predate the rule, and every invoice selling one goes out
		// without the classification a tax invoice has to print.
		Unclassified: c.Query("unclassified") == "true",
	})
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"products": products})
}

func (h *Catalogue) GetProduct(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	product, err := h.svc.GetProduct(c.Request.Context(), id, storeID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, product)
}

func (h *Catalogue) LookupBarcode(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	product, err := h.svc.FindByBarcode(c.Request.Context(), c.Param("barcode"), storeID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, product)
}

// productRequest is the wire shape for creating or updating a product.
// Monetary, quantity and rate fields accept a JSON number or a string; the
// money package parses both without going through a float.
type productRequest struct {
	Name         string         `json:"name" binding:"required"`
	NameHi       string         `json:"name_hi"`
	NameKn       string         `json:"name_kn"`
	CategoryID   string         `json:"category_id" binding:"required"`
	HSNCode      string         `json:"hsn_code"`
	SACCode      string         `json:"sac_code"`
	Barcode      string         `json:"barcode"`
	UOMID        string         `json:"uom_id" binding:"required"`
	BaseUnitQty  money.Quantity `json:"base_unit_qty"`
	GSTRate      money.Rate     `json:"gst_rate"`
	IsFree       bool           `json:"is_free"`
	AllowDecimal bool           `json:"allow_decimal"`
	MinStock     money.Quantity `json:"min_stock"`
	ImageURL     string         `json:"image_url"`
	Brand        string         `json:"brand"`
	Description  string         `json:"description"`

	// TracksExpiry says the goods have a shelf life, so receiving asks for a
	// date and the expiry report covers them. ShelfLifeDays lets a receipt work
	// the date out instead of a clerk reading it off the packet.
	TracksExpiry      bool    `json:"tracks_expiry"`
	ShelfLifeDays     *int    `json:"shelf_life_days"`
	PreferredVendorID *string `json:"preferred_vendor_id"`

	// Optional opening price, written in the same transaction as the product.
	MRP                *money.Money `json:"mrp"`
	CostPrice          *money.Money `json:"cost_price"`
	SellPrice          *money.Money `json:"sell_price"`
	WholesaleSellPrice *money.Money `json:"wholesale_sell_price"`
}

func (r productRequest) toInput() (domain.NewProductInput, error) {
	categoryID, err := uuid.Parse(r.CategoryID)
	if err != nil {
		return domain.NewProductInput{}, errors.New("category_id is not a valid identifier")
	}
	uomID, err := uuid.Parse(r.UOMID)
	if err != nil {
		return domain.NewProductInput{}, errors.New("uom_id is not a valid identifier")
	}
	baseQty := r.BaseUnitQty
	if baseQty.IsZero() {
		baseQty = money.QuantityFromInt(1)
	}
	vendorID, err := optionalUUID(r.PreferredVendorID)
	if err != nil {
		return domain.NewProductInput{}, errors.New("preferred_vendor_id is not a valid identifier")
	}
	return domain.NewProductInput{
		Name:         r.Name,
		NameHi:       r.NameHi,
		NameKn:       r.NameKn,
		CategoryID:   categoryID,
		HSNCode:      r.HSNCode,
		SACCode:      r.SACCode,
		Barcode:      r.Barcode,
		UOMID:        uomID,
		BaseUnitQty:  baseQty,
		GSTRate:      r.GSTRate,
		IsFree:       r.IsFree,
		AllowDecimal: r.AllowDecimal,
		MinStock:     r.MinStock,
		ImageURL:     r.ImageURL,
		Brand:        r.Brand,
		Description:  r.Description,

		TracksExpiry:      r.TracksExpiry,
		ShelfLifeDays:     r.ShelfLifeDays,
		PreferredVendorID: vendorID,
	}, nil
}

// price returns the opening price when any price field was supplied. It names
// no unit: the service attaches it to the default unit it creates alongside the
// product, in the same transaction.
func (r productRequest) price() *domain.UnitPrice {
	if r.MRP == nil && r.CostPrice == nil && r.SellPrice == nil && r.WholesaleSellPrice == nil {
		return nil
	}
	return &domain.UnitPrice{
		MRP:                deref(r.MRP),
		CostPrice:          deref(r.CostPrice),
		SellPrice:          deref(r.SellPrice),
		WholesaleSellPrice: deref(r.WholesaleSellPrice),
	}
}

func (h *Catalogue) CreateProduct(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	var req productRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	in, err := req.toInput()
	if err != nil {
		httperr.Respond(c, httperr.BadRequest(err.Error()))
		return
	}

	created, err := h.svc.CreateProduct(c.Request.Context(), service.CreateProductInput{
		Product: in,
		StoreID: storeID,
		Price:   req.price(),
	})
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (h *Catalogue) UpdateProduct(c *gin.Context) {
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	var req productRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	in, err := req.toInput()
	if err != nil {
		httperr.Respond(c, httperr.BadRequest(err.Error()))
		return
	}

	updated, err := h.svc.UpdateProduct(c.Request.Context(), id, in)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (h *Catalogue) DeactivateProduct(c *gin.Context) { h.setProductActive(c, false) }
func (h *Catalogue) ActivateProduct(c *gin.Context)   { h.setProductActive(c, true) }

func (h *Catalogue) setProductActive(c *gin.Context, active bool) {
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.SetProductActive(c.Request.Context(), id, active); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "is_active": active})
}

// ---------------------------------------------------------------------------
// Product units
// ---------------------------------------------------------------------------

func (h *Catalogue) ListProductUnits(c *gin.Context) {
	productID, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	units, err := h.svc.ListProductUnits(c.Request.Context(), productID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"units": units})
}

// productUnitRequest is the wire shape for one way of transacting a product.
// The product comes from the path, never from the body.
//
// is_sellable and is_purchasable are pointers so that an omitted field means
// "yes" — the common case is a unit that can be both bought and sold, and a
// plain bool would read an absent field as a refusal.
//
// qty_in_base is deliberately NOT defaulted. It is the number that turns a sale
// into a stock movement, and a form that forgot it should be rejected rather
// than quietly given a size of one.
type productUnitRequest struct {
	Label         string         `json:"label" binding:"required"`
	QtyInBase     money.Quantity `json:"qty_in_base"`
	UOMID         *string        `json:"uom_id"`
	Barcode       string         `json:"barcode"`
	IsDefault     bool           `json:"is_default"`
	IsSellable    *bool          `json:"is_sellable"`
	IsPurchasable *bool          `json:"is_purchasable"`
	SortOrder     int            `json:"sort_order"`
}

func (r productUnitRequest) toInput(productID uuid.UUID) (domain.NewProductUnitInput, error) {
	uomID, err := optionalUUID(r.UOMID)
	if err != nil {
		return domain.NewProductUnitInput{}, errors.New("uom_id is not a valid identifier")
	}
	return domain.NewProductUnitInput{
		ProductID:     productID,
		UOMID:         uomID,
		Label:         r.Label,
		QtyInBase:     r.QtyInBase,
		Barcode:       r.Barcode,
		IsDefault:     r.IsDefault,
		IsSellable:    boolOr(r.IsSellable, true),
		IsPurchasable: boolOr(r.IsPurchasable, true),
		SortOrder:     r.SortOrder,
	}, nil
}

func (h *Catalogue) CreateProductUnit(c *gin.Context) {
	productID, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	var req productUnitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	in, err := req.toInput(productID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest(err.Error()))
		return
	}

	created, err := h.svc.AddProductUnit(c.Request.Context(), in)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (h *Catalogue) UpdateProductUnit(c *gin.Context) {
	productID, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	unitID, ok := pathUUID(c, "unit_id")
	if !ok {
		return
	}
	var req productUnitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	in, err := req.toInput(productID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest(err.Error()))
		return
	}

	updated, err := h.svc.UpdateProductUnit(c.Request.Context(), unitID, in)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// SetDefaultUnit makes one unit the one a bare quantity means. The previous
// default is cleared in the same transaction; see the service for why that
// cannot be two requests.
func (h *Catalogue) SetDefaultUnit(c *gin.Context) {
	productID, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	unitID, ok := pathUUID(c, "unit_id")
	if !ok {
		return
	}
	unit, err := h.svc.SetDefaultUnit(c.Request.Context(), unitID, productID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, unit)
}

func (h *Catalogue) DeactivateProductUnit(c *gin.Context) { h.setProductUnitActive(c, false) }
func (h *Catalogue) ActivateProductUnit(c *gin.Context)   { h.setProductUnitActive(c, true) }

func (h *Catalogue) setProductUnitActive(c *gin.Context, active bool) {
	productID, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	unitID, ok := pathUUID(c, "unit_id")
	if !ok {
		return
	}
	if err := h.svc.SetProductUnitActive(c.Request.Context(), unitID, productID, active); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": unitID, "product_id": productID, "is_active": active})
}

// ---------------------------------------------------------------------------
// Prices, per unit
// ---------------------------------------------------------------------------

// ListPrices returns every sellable unit of a product with its price in this
// store, default first and then smallest to largest — the order a counter
// reaches for them in.
func (h *Catalogue) ListPrices(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	productID, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	prices, err := h.svc.ListPrices(c.Request.Context(), productID, storeID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"product_id": productID, "store_id": storeID, "prices": prices})
}

// setPriceRequest names the unit being priced.
//
// product_unit_id is required. A price with no unit is the ambiguity this whole
// change removes: it cannot say whether Rs.1,050 is for a kilo or for a sack.
type setPriceRequest struct {
	ProductID          string      `json:"product_id" binding:"required"`
	ProductUnitID      string      `json:"product_unit_id" binding:"required"`
	MRP                money.Money `json:"mrp"`
	CostPrice          money.Money `json:"cost_price"`
	SellPrice          money.Money `json:"sell_price"`
	WholesaleSellPrice money.Money `json:"wholesale_sell_price"`
}

func (h *Catalogue) SetPrice(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	var req setPriceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	productID, err := uuid.Parse(req.ProductID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("product_id is not a valid identifier"))
		return
	}
	productUnitID, err := uuid.Parse(req.ProductUnitID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("product_unit_id is not a valid identifier"))
		return
	}

	price, err := h.svc.SetPrice(c.Request.Context(), domain.UnitPrice{
		ProductID:          productID,
		ProductUnitID:      productUnitID,
		StoreID:            storeID,
		MRP:                req.MRP,
		CostPrice:          req.CostPrice,
		SellPrice:          req.SellPrice,
		WholesaleSellPrice: req.WholesaleSellPrice,
	})
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, price)
}
