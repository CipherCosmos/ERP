package handler

import (
	"errors"
	"net/http"
	"time"

	billingdomain "gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	domain "gbos/internal/domain/payment"
	scope "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	platform "gbos/internal/platform/postgres"
	service "gbos/internal/service/payment"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Payment serves the settlement endpoints: how a sale was paid for, and what a
// day's takings came to.
type Payment struct {
	svc *service.Service
}

// NewPayment returns a payment handler.
func NewPayment(svc *service.Service) *Payment {
	return &Payment{svc: svc}
}

// Register mounts the settlement routes. Every one is store-scoped and belongs
// behind the store middleware: an invoice id on its own is otherwise a bearer
// token for another shop's money.
func (h *Payment) Register(r gin.IRoutes) {
	r.POST("/payments/split", h.Settle)
	r.GET("/payments/invoice/:id", h.ForInvoice)
	r.GET("/payments/settlement", h.Settlement)
}

// paymentTenderRequest is one part of a settlement: this much, this way.
type paymentTenderRequest struct {
	Mode      string      `json:"payment_mode" binding:"required"`
	Amount    money.Money `json:"amount" binding:"required"`
	Reference string      `json:"reference"`
}

// paymentSplitRequest settles an invoice. One entry in Payments is an ordinary
// sale; several is a split. The parts must sum to the invoice total exactly,
// which the domain enforces — there is no tolerance to configure here.
type paymentSplitRequest struct {
	InvoiceID string                 `json:"invoice_id" binding:"required"`
	Payments  []paymentTenderRequest `json:"payments" binding:"required"`
}

func (h *Payment) Settle(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	var req paymentSplitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	invoiceID, err := uuid.Parse(req.InvoiceID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("invoice_id is not a valid identifier"))
		return
	}

	// The mode is passed through as written. Whether it is a payment mode at
	// all, and whether it is one money actually crosses the counter in, is the
	// domain's decision — not a second, drifting list kept here.
	tenders := make([]domain.TenderInput, len(req.Payments))
	for i, p := range req.Payments {
		tenders[i] = domain.TenderInput{
			Mode:      billingdomain.PaymentMode(p.Mode),
			Amount:    p.Amount,
			Reference: p.Reference,
		}
	}

	result, err := h.svc.Settle(c.Request.Context(), service.SettleInput{
		StoreID:        storeID,
		InvoiceID:      invoiceID,
		Tenders:        tenders,
		IdempotencyKey: c.GetHeader(idempotencyHeader),
	})
	if err != nil {
		respondPayment(c, err)
		return
	}

	// A replay is answered 200, not 201: this request settled nothing, it only
	// repeated the answer the original request already earned.
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	c.JSON(status, result)
}

func (h *Payment) ForInvoice(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	invoiceID, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	payments, err := h.svc.ForInvoice(c.Request.Context(), storeID, invoiceID)
	if err != nil {
		respondPayment(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"invoice_id": invoiceID, "payments": payments})
}

// Settlement reports a store's takings for one trading day. The day is read
// from `date` as 2006-01-02; an absent date means today, which the service
// decides because it owns the clock.
func (h *Payment) Settlement(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}

	var (
		report domain.Settlement
		err    error
	)
	if date := c.Query("date"); date != "" {
		report, err = h.svc.SettlementOn(c.Request.Context(), storeID, date)
	} else {
		report, err = h.svc.Settlement(c.Request.Context(), storeID, time.Time{})
	}
	if err != nil {
		respondPayment(c, err)
		return
	}
	c.JSON(http.StatusOK, report)
}

// respondPayment maps the settlement errors to statuses a till can act on,
// before falling through to the shared envelope.
func respondPayment(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrValidation):
		httperr.Respond(c, httperr.Unprocessable(unwrapValidationMessage(err)))
	case errors.Is(err, domain.ErrAmountMismatch):
		// 422 rather than 400: the request parsed fine and the split is
		// arithmetically wrong. The message says which way it is out, because
		// "short" and "over" call for different actions at the counter.
		httperr.Respond(c, httperr.Unprocessable(err.Error()))
	case errors.Is(err, domain.ErrAlreadySettled):
		// 409: the money crossed the counter once. Repeating the request will
		// not help, and nothing about the request itself is wrong.
		httperr.Respond(c, httperr.Conflict(err.Error()))
	case errors.Is(err, domain.ErrNotSettleable):
		httperr.Respond(c, httperr.Conflict(err.Error()))
	case errors.Is(err, service.ErrStoreRequired), errors.Is(err, service.ErrInvoiceRequired):
		httperr.Respond(c, httperr.BadRequest(err.Error()))
	case errors.Is(err, platform.ErrNotFound):
		// Deliberately the same answer for "no such invoice" and "that invoice
		// belongs to another store", as the service documents.
		httperr.Respond(c, httperr.NotFound("no such invoice in this store"))
	default:
		httperr.Respond(c, err)
	}
}
