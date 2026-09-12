package handler

import (
	"errors"
	"net/http"

	"gbos/internal/domain/money"
	domain "gbos/internal/domain/stock"
	mw "gbos/internal/http/middleware"
	scope "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	"gbos/internal/rbac"
	service "gbos/internal/service/stock"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Stock serves the on-hand inventory endpoints: what is in the shop, what is
// running out, what is about to expire, and every movement in or out.
type Stock struct {
	svc *service.Service
}

// NewStock returns a stock handler.
func NewStock(svc *service.Service) *Stock {
	return &Stock{svc: svc}
}

// Register mounts the stock routes.
//
// Every one is store-scoped, so they belong behind the store middleware. The
// paths mirror the ones the existing frontend already calls — /stock/*,
// /losses and /expiry/* — so the cutover does not need a client change.
func (h *Stock) Register(r gin.IRoutes) {
	r.GET("/stock", mw.RequirePermission(rbac.PermCatalogRead), h.List)
	r.GET("/stock/low", mw.RequirePermission(rbac.PermCatalogRead), h.LowStock)
	r.GET("/stock/adjustments", mw.RequirePermission(rbac.PermCatalogRead), h.ListAdjustments)
	r.GET("/stock/product/:id", mw.RequirePermission(rbac.PermCatalogRead), h.ProductStock)
	r.POST("/stock/adjust", mw.RequirePermission(rbac.PermStockMove), h.Adjust)
	r.POST("/stock/transfer", mw.RequirePermission(rbac.PermStockMove), h.Transfer)
	// There is no /stock/convert. Stock is one pool in the product's base unit,
	// so there is nothing to convert it into: a sale or a receipt states its own
	// unit and multiplies at that moment. See the stock service for the longer
	// version.
	r.POST("/stock/reserve", mw.RequirePermission(rbac.PermStockMove), h.Reserve)
	r.POST("/stock/release", mw.RequirePermission(rbac.PermStockMove), h.Release)

	r.GET("/losses", mw.RequirePermission(rbac.PermLossRead), h.ListLosses)
	r.POST("/losses", mw.RequirePermission(rbac.PermLossWrite), h.RecordLoss)

	r.GET("/expiry/expiring", mw.RequirePermission(rbac.PermExpiryRead), h.Expiring)
	r.GET("/expiry/expired", mw.RequirePermission(rbac.PermExpiryRead), h.Expired)
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

func (h *Stock) List(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	lines, err := h.svc.ListStock(c.Request.Context(), storeID)
	if err != nil {
		respondStock(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"stock": lines})
}

// ProductStock returns one product's batches in this store, earliest expiry
// first — the order they will be sold in.
func (h *Stock) ProductStock(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	productID, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	batches, err := h.svc.ProductStock(c.Request.Context(), productID, storeID)
	if err != nil {
		respondStock(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"product_id": productID, "batches": batches})
}

func (h *Stock) LowStock(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	items, err := h.svc.LowStock(c.Request.Context(), storeID)
	if err != nil {
		respondStock(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"low_stock": items})
}

func (h *Stock) ListAdjustments(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	productID, err := optionalUUID(queryPtr(c, "product_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("product_id is not a valid identifier"))
		return
	}
	adjustments, err := h.svc.ListAdjustments(c.Request.Context(), domain.AdjustmentFilter{
		StoreID:   storeID,
		ProductID: productID,
		Limit:     intQuery(c, "limit", 0),
	})
	if err != nil {
		respondStock(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"adjustments": adjustments})
}

func (h *Stock) ListLosses(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	losses, err := h.svc.ListLosses(c.Request.Context(), domain.LossFilter{
		StoreID:  storeID,
		LossType: c.Query("loss_type"),
		Limit:    intQuery(c, "limit", 0),
	})
	if err != nil {
		respondStock(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"losses": losses})
}

// Expiring reports the batches that expire within the requested window and
// have not expired yet — a batch already past its date belongs to /expiry/expired
// and appears there only. An absent or unparseable `days` is passed through as
// zero, which the service reads as its default horizon rather than as "no
// window at all".
func (h *Stock) Expiring(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	days := intQuery(c, "days", 0)
	batches, err := h.svc.Expiring(c.Request.Context(), storeID, days)
	if err != nil {
		respondStock(c, err)
		return
	}
	if days <= 0 {
		days = service.DefaultExpiryWindow
	}
	c.JSON(http.StatusOK, gin.H{"days": days, "batches": batches})
}

func (h *Stock) Expired(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	batches, err := h.svc.Expired(c.Request.Context(), storeID)
	if err != nil {
		respondStock(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"batches": batches})
}

// ---------------------------------------------------------------------------
// Writes
// ---------------------------------------------------------------------------

// stockAdjustRequest is the wire shape of a stock-take correction or a manual
// receipt. Quantity is a positive magnitude and Type carries the direction, so
// "add minus fifty" is not expressible.
//
// A movement may be stated either way round:
//
//   - quantity alone, in the product's base unit — "250 kilograms";
//   - product_unit_id with unit_qty — "ten sacks", which the service multiplies
//     into the same 250 kilograms of the same single pool.
//
// quantity therefore loses its binding:"required": a request that names a unit
// supplies no base quantity at all, and requiring one would force every client
// to do the arithmetic it just delegated.
type stockAdjustRequest struct {
	ProductID     string         `json:"product_id" binding:"required"`
	BatchNo       string         `json:"batch_no"`
	Quantity      money.Quantity `json:"quantity"`
	ProductUnitID *string        `json:"product_unit_id"`
	UnitQty       money.Quantity `json:"unit_qty"`
	Type          string         `json:"type" binding:"required"`
	Reason        string         `json:"reason"`
	CostPrice     money.Money    `json:"cost_price"`
	ExpiryDate    *string        `json:"expiry_date"`
	MfgDate       *string        `json:"mfg_date"`
}

func (h *Stock) Adjust(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	userID, ok := stockActor(c)
	if !ok {
		return
	}

	var req stockAdjustRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	productID, err := uuid.Parse(req.ProductID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("product_id is not a valid identifier"))
		return
	}
	expiryDate, err := optionalDate(req.ExpiryDate)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("expiry_date must look like 2026-04-30"))
		return
	}
	mfgDate, err := optionalDate(req.MfgDate)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("mfg_date must look like 2026-04-30"))
		return
	}
	productUnitID, err := optionalUUID(req.ProductUnitID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("product_unit_id is not a valid identifier"))
		return
	}

	in := service.AdjustInput{
		ProductID:  productID,
		StoreID:    storeID,
		UserID:     userID,
		BatchNo:    req.BatchNo,
		Quantity:   req.Quantity,
		UnitQty:    req.UnitQty,
		Type:       domain.AdjustmentType(req.Type),
		Reason:     req.Reason,
		CostPrice:  req.CostPrice,
		ExpiryDate: expiryDate,
		MfgDate:    mfgDate,
	}
	if productUnitID != nil {
		in.ProductUnitID = *productUnitID
	}

	result, err := h.svc.Adjust(c.Request.Context(), in)
	if err != nil {
		respondStock(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// stockTransferRequest moves one batch between stores.
//
// There is no from_store_id: the source is the store the middleware already
// authorised, and a store id in the body is deliberately ignored. Only the
// destination is a caller's choice.
type stockTransferRequest struct {
	ProductID string         `json:"product_id" binding:"required"`
	ToStoreID string         `json:"to_store_id" binding:"required"`
	BatchNo   string         `json:"batch_no"`
	Quantity  money.Quantity `json:"quantity" binding:"required"`
	Reason    string         `json:"reason"`
}

func (h *Stock) Transfer(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	userID, ok := stockActor(c)
	if !ok {
		return
	}

	var req stockTransferRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	productID, err := uuid.Parse(req.ProductID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("product_id is not a valid identifier"))
		return
	}
	toStoreID, err := uuid.Parse(req.ToStoreID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("to_store_id is not a valid identifier"))
		return
	}

	if err := h.svc.Transfer(c.Request.Context(), service.TransferInput{
		ProductID:   productID,
		FromStoreID: storeID,
		ToStoreID:   toStoreID,
		UserID:      userID,
		BatchNo:     req.BatchNo,
		Quantity:    req.Quantity,
		Reason:      req.Reason,
	}); err != nil {
		respondStock(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"product_id":    productID,
		"from_store_id": storeID,
		"to_store_id":   toStoreID,
		"batch_no":      req.BatchNo,
		"quantity":      req.Quantity,
	})
}

// stockReservationRequest holds stock against a pending order, or lets it go.
type stockReservationRequest struct {
	ProductID string         `json:"product_id" binding:"required"`
	BatchNo   string         `json:"batch_no"`
	Quantity  money.Quantity `json:"quantity" binding:"required"`
	Reason    string         `json:"reason"`
}

func (h *Stock) Reserve(c *gin.Context) { h.reservation(c, domain.AdjustReserve) }
func (h *Stock) Release(c *gin.Context) { h.reservation(c, domain.AdjustRelease) }

func (h *Stock) reservation(c *gin.Context, kind domain.AdjustmentType) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	userID, ok := stockActor(c)
	if !ok {
		return
	}

	var req stockReservationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	productID, err := uuid.Parse(req.ProductID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("product_id is not a valid identifier"))
		return
	}

	in := service.ReservationInput{
		ProductID: productID,
		StoreID:   storeID,
		UserID:    userID,
		BatchNo:   req.BatchNo,
		Quantity:  req.Quantity,
		Reason:    req.Reason,
	}
	if kind == domain.AdjustReserve {
		err = h.svc.Reserve(c.Request.Context(), in)
	} else {
		err = h.svc.Release(c.Request.Context(), in)
	}
	if err != nil {
		respondStock(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"product_id": productID,
		"batch_no":   req.BatchNo,
		"quantity":   req.Quantity,
		"type":       kind,
	})
}

// stockLossRequest writes stock off: damage, expiry, theft, spillage.
type stockLossRequest struct {
	ProductID string         `json:"product_id" binding:"required"`
	BatchNo   string         `json:"batch_no"`
	Quantity  money.Quantity `json:"quantity" binding:"required"`
	LossType  string         `json:"loss_type" binding:"required"`
	Reason    string         `json:"reason"`
}

func (h *Stock) RecordLoss(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	userID, ok := stockActor(c)
	if !ok {
		return
	}

	var req stockLossRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	productID, err := uuid.Parse(req.ProductID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("product_id is not a valid identifier"))
		return
	}

	loss, err := h.svc.RecordLoss(c.Request.Context(), service.LossInput{
		ProductID: productID,
		StoreID:   storeID,
		UserID:    userID,
		BatchNo:   req.BatchNo,
		Quantity:  req.Quantity,
		LossType:  req.LossType,
		Reason:    req.Reason,
	})
	if err != nil {
		respondStock(c, err)
		return
	}
	c.JSON(http.StatusCreated, loss)
}

// ---------------------------------------------------------------------------
// Plumbing
// ---------------------------------------------------------------------------

// stockActor returns the user every movement is attributed to. Stock never
// moves anonymously: the audit row carries a user, and the domain refuses to
// build one without.
func stockActor(c *gin.Context) (uuid.UUID, bool) {
	session, ok := scope.SessionFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Unauthorized("no session"))
		return uuid.Nil, false
	}
	return session.UserID, true
}

// respondStock maps the stock-specific errors to statuses a shop floor can act
// on, before falling through to the shared envelope.
func respondStock(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrValidation):
		httperr.Respond(c, httperr.Unprocessable(unwrapValidationMessage(err)))
	case errors.Is(err, domain.ErrInsufficientStock):
		// 422, not 409: the request was understood and is simply asking for
		// goods that are not there. Retrying it unchanged will not help.
		httperr.Respond(c, httperr.Unprocessable(err.Error()))
	case errors.Is(err, domain.ErrInsufficientReserved):
		httperr.Respond(c, httperr.Unprocessable(err.Error()))
	case errors.Is(err, service.ErrSameStore):
		httperr.Respond(c, httperr.Unprocessable(err.Error()))
	case errors.Is(err, service.ErrStoreRequired):
		// A store the middleware resolved is never nil, so this is a wiring
		// mistake reaching the service rather than a client error.
		httperr.Respond(c, httperr.BadRequest(err.Error()))
	default:
		httperr.Respond(c, err)
	}
}
