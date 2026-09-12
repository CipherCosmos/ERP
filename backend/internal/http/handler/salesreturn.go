package handler

import (
	"errors"
	"net/http"
	"time"

	"gbos/internal/domain/money"
	domain "gbos/internal/domain/salesreturn"
	stockdomain "gbos/internal/domain/stock"
	mw "gbos/internal/http/middleware"
	scope "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	"gbos/internal/rbac"
	service "gbos/internal/service/salesreturn"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// SalesReturn serves the two ways goods move without a sale: a customer
// bringing something back, and stock written off.
type SalesReturn struct {
	svc *service.Service
}

// NewSalesReturn returns a returns-and-losses handler.
func NewSalesReturn(svc *service.Service) *SalesReturn {
	return &SalesReturn{svc: svc}
}

// Register mounts the customer-return routes. Every one is store-scoped and
// belongs behind the store middleware — approving a return moves stock, so the
// store it moves into must have been authorised before the handler runs.
//
// Inventory losses are deliberately not here. A write-off is a stock movement
// — batch selection, cost layering, the non-negative invariant — so it belongs
// to the stock aggregate, which also records a stock_adjustments row for the
// audit trail. Both aggregates grew a RecordLoss during the port; stock's is
// the one mounted, and this handler's ListLosses and CreateLoss are unused.
func (h *SalesReturn) Register(r gin.IRoutes) {
	r.GET("/returns", mw.RequirePermission(rbac.PermReturnRead), h.ListReturns)
	r.POST("/returns", mw.RequirePermission(rbac.PermReturnCreate), h.CreateReturn)
	r.POST("/returns/:id/approve", mw.RequirePermission(rbac.PermReturnApprove), h.ApproveReturn)
}

// ---------------------------------------------------------------------------
// Customer returns
// ---------------------------------------------------------------------------

// salesReturnRequest is the wire shape for recording a customer return.
//
// There is no store_id field. A store id in the body is deliberately ignored:
// the middleware has already resolved and authorised one, and letting a body
// name a second is how a return could be credited to somebody else's shelf.
type salesReturnRequest struct {
	ProductID string  `json:"product_id" binding:"required"`
	InvoiceID *string `json:"invoice_id"`

	Quantity money.Quantity `json:"quantity" binding:"required"`
	// Amount is what the customer was refunded. Zero is legitimate — an
	// exchange, or goods taken back as a goodwill gesture — so it is not
	// required and only a negative value is refused, by the domain.
	Amount money.Money `json:"amount"`

	Reason     string `json:"reason" binding:"required"`
	RefundMode string `json:"refund_mode"`

	// RaisedOffline and RaisedAt are what the till says about when and how this
	// return was taken. The server cannot know either: it hears about an offline
	// return later, and a day's refunds are counted by when they happened.
	RaisedOffline bool    `json:"raised_offline"`
	RaisedAt      *string `json:"raised_at"`
}

func (h *SalesReturn) CreateReturn(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	var req salesReturnRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	productID, err := uuid.Parse(req.ProductID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("product_id is not a valid identifier"))
		return
	}
	invoiceID, err := optionalUUID(req.InvoiceID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("invoice_id is not a valid identifier"))
		return
	}

	var raisedAt *time.Time
	if req.RaisedAt != nil && *req.RaisedAt != "" {
		parsed, err := time.Parse(time.RFC3339, *req.RaisedAt)
		if err != nil {
			httperr.Respond(c, httperr.BadRequest("raised_at must be a timestamp like 2026-04-30T18:45:00+05:30"))
			return
		}
		raisedAt = &parsed
	}

	created, err := h.svc.CreateReturn(c.Request.Context(), domain.ReturnInput{
		InvoiceID:     invoiceID,
		ProductID:     productID,
		StoreID:       storeID,
		Quantity:      req.Quantity,
		Amount:        req.Amount,
		Reason:        req.Reason,
		RefundMode:    req.RefundMode,
		RaisedOffline: req.RaisedOffline,
		RaisedAt:      raisedAt,
	})
	if err != nil {
		respondSalesReturn(c, err)
		return
	}
	// 201, and pending: recording a return moves no stock. The goods go back
	// only when it is approved.
	c.JSON(http.StatusCreated, created)
}

func (h *SalesReturn) ListReturns(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	productID, err := optionalUUID(queryPtr(c, "product_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("product_id is not a valid identifier"))
		return
	}
	invoiceID, err := optionalUUID(queryPtr(c, "invoice_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("invoice_id is not a valid identifier"))
		return
	}

	returns, err := h.svc.ListReturns(c.Request.Context(), domain.ReturnFilter{
		StoreID:   storeID,
		ProductID: productID,
		InvoiceID: invoiceID,
		Status:    domain.Status(c.Query("status")),
		Limit:     intQuery(c, "limit", 0),
	})
	if err != nil {
		respondSalesReturn(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"returns": returns})
}

// ApproveReturn accepts a return and puts the goods back on the shelf.
//
// The store is the authorised one, never a store named in the request: the
// implementation this replaces restored the goods into whatever store_id the
// query string carried, so one shop's approval credited another shop's shelf.
func (h *SalesReturn) ApproveReturn(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	approved, err := h.svc.Approve(c.Request.Context(), storeID, id)
	if err != nil {
		respondSalesReturn(c, err)
		return
	}
	c.JSON(http.StatusOK, approved)
}

// ---------------------------------------------------------------------------
// Inventory losses
// ---------------------------------------------------------------------------

// salesReturnLossRequest is the wire shape for writing stock off.
type salesReturnLossRequest struct {
	ProductID string `json:"product_id" binding:"required"`
	// BatchNo names a lot to take the goods from first. It is a hint: a
	// write-off larger than that lot falls through to a
	// first-expired-first-out walk across as many batches as it needs.
	BatchNo  string         `json:"batch_no"`
	Quantity money.Quantity `json:"quantity" binding:"required"`
	LossType string         `json:"loss_type" binding:"required"`
	Reason   string         `json:"reason"`
}

func (h *SalesReturn) CreateLoss(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	// A write-off is attributed to whoever made it. An unattributed write-off
	// is indistinguishable from theft, so the identity comes from the session
	// and is never taken from the body.
	session, ok := scope.SessionFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Unauthorized("no session"))
		return
	}

	var req salesReturnLossRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	productID, err := uuid.Parse(req.ProductID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("product_id is not a valid identifier"))
		return
	}

	loss, err := h.svc.RecordLoss(c.Request.Context(), domain.LossInput{
		ProductID: productID,
		StoreID:   storeID,
		UserID:    session.UserID,
		BatchNo:   req.BatchNo,
		Quantity:  req.Quantity,
		LossType:  req.LossType,
		Reason:    req.Reason,
	})
	if err != nil {
		respondSalesReturn(c, err)
		return
	}
	// The body carries the lots the goods actually came out of, which is not
	// necessarily the batch that was asked for.
	c.JSON(http.StatusCreated, loss)
}

func (h *SalesReturn) ListLosses(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	productID, err := optionalUUID(queryPtr(c, "product_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("product_id is not a valid identifier"))
		return
	}

	losses, err := h.svc.ListLosses(c.Request.Context(), domain.LossFilter{
		StoreID:   storeID,
		ProductID: productID,
		LossType:  c.Query("loss_type"),
		Limit:     intQuery(c, "limit", 0),
	})
	if err != nil {
		respondSalesReturn(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"losses": losses})
}

// respondSalesReturn maps this aggregate's errors onto statuses a till can act
// on, before falling through to the shared envelope. A return that does not
// exist, or belongs to another store, arrives as postgres.ErrNotFound and is
// rendered 404 by httperr — which is also the truth from where the caller is
// standing.
func respondSalesReturn(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrValidation):
		httperr.Respond(c, httperr.Unprocessable(unwrapValidationMessage(err)))
	case errors.Is(err, service.ErrAlreadyApproved):
		// 409, not 400: the request was fine, the return has simply moved on.
		// A double-clicked approve button lands here, and the goods have gone
		// back exactly once.
		httperr.Respond(c, httperr.Conflict("this return has already been approved"))
	case errors.Is(err, service.ErrInvoiceVoided):
		// 409: voiding the invoice already put these goods back, so approving
		// the return would invent stock. Retrying will not help.
		httperr.Respond(c, httperr.Conflict(err.Error()))
	case errors.Is(err, service.ErrNotOnInvoice), errors.Is(err, service.ErrOverReturn):
		// 422: understood and refused. The invoice never sold this, or did not
		// sell this much of it.
		httperr.Respond(c, httperr.Unprocessable(err.Error()))
	case errors.Is(err, stockdomain.ErrInsufficientStock):
		httperr.Respond(c, httperr.Unprocessable(err.Error()))
	case errors.Is(err, service.ErrStoreRequired):
		httperr.Respond(c, httperr.BadRequest(err.Error()))
	default:
		httperr.Respond(c, err)
	}
}
