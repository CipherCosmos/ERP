package handler

import (
	"errors"
	"net/http"
	"time"

	domain "gbos/internal/domain/billing"
	"gbos/internal/domain/money"
	stockdomain "gbos/internal/domain/stock"
	mw "gbos/internal/http/middleware"
	scope "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	"gbos/internal/rbac"
	repo "gbos/internal/repo/postgres"
	service "gbos/internal/service/billing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// idempotencyHeader is the conventional header a client sends so a retried
// request is not applied twice. It is a header rather than a body field
// because it describes the request, not the sale.
const idempotencyHeader = "Idempotency-Key"

// Billing serves the point-of-sale endpoints.
type Billing struct {
	svc *service.Service
}

// NewBilling returns a billing handler.
func NewBilling(svc *service.Service) *Billing {
	return &Billing{svc: svc}
}

// Register mounts the sales routes. Every one is store-scoped, so they belong
// behind the store middleware.
func (h *Billing) Register(r gin.IRoutes) {
	r.POST("/invoices", mw.RequirePermission(rbac.PermInvoiceCreate), h.Create)
	r.GET("/invoices", mw.RequirePermission(rbac.PermInvoiceRead), h.List)
	r.GET("/invoices/gstr1", mw.RequirePermission(rbac.PermReportView), h.GSTR1)
	r.POST("/invoices/gstr1/mark-filed", mw.RequirePermission(rbac.PermGSTR1File), h.MarkGSTR1Filed)
	r.GET("/invoices/:id", mw.RequirePermission(rbac.PermInvoiceRead), h.Get)
	r.POST("/invoices/:id/void", mw.RequirePermission(rbac.PermInvoiceVoid), h.Void)
}

type saleLineRequest struct {
	ProductID string `json:"product_id" binding:"required"`
	// ProductUnitID names what the customer asked for — the 25 kg sack rather
	// than the loose kilo. Absent means the product's default unit, which is
	// what a till written before units existed implies and what the migration
	// backfilled every existing line to.
	ProductUnitID *string `json:"product_unit_id"`
	// UnitQty is how many of that unit. `quantity` is the older spelling of the
	// same field and is still accepted, because it always meant "how many of
	// the thing you are buying" and the default unit is what it meant it in.
	// Neither carries binding:"required": a missing or zero quantity gets the
	// domain's own message rather than a generic field error.
	UnitQty  *money.Quantity `json:"unit_qty"`
	Quantity *money.Quantity `json:"quantity"`
	BatchNo  string          `json:"batch_no"`
	Discount money.Money     `json:"discount"`
	// Rate is optional and is a staleness check, not an instruction: if it
	// disagrees with the shop's price the sale is refused so the till can
	// refresh, rather than charging whatever the client asked for. It is the
	// price of one selling unit — one sack — not a price per kilo.
	Rate *money.Money `json:"rate"`
}

// qty resolves the two spellings. A line stating neither is left at zero, which
// the domain refuses with "quantity must be greater than zero".
func (r saleLineRequest) qty() money.Quantity {
	switch {
	case r.UnitQty != nil:
		return *r.UnitQty
	case r.Quantity != nil:
		return *r.Quantity
	default:
		return money.ZeroQuantity()
	}
}

// unitID parses the optional unit. An absent one is the nil uuid, which the
// service reads as "the product's default unit".
func (r saleLineRequest) unitID() (uuid.UUID, error) {
	if r.ProductUnitID == nil || *r.ProductUnitID == "" {
		return uuid.Nil, nil
	}
	return uuid.Parse(*r.ProductUnitID)
}

type saleRequest struct {
	TerminalID   string            `json:"terminal_id" binding:"required"`
	CustomerID   *string           `json:"customer_id"`
	PaymentMode  string            `json:"payment_mode" binding:"required"`
	Items        []saleLineRequest `json:"items" binding:"required"`
	BillDiscount money.Money       `json:"discount"`
	RoundTotal   bool              `json:"round_total"`
	DueDate      *string           `json:"due_date"`

	// InvoiceNo is the number the till allocated from its own terminal's series.
	// A till has to be able to bill with the connection down, and a tax invoice
	// carries a number, so it draws its own — online too, so the path is the one
	// exercised every day. Omitted, the server allocates.
	InvoiceNo string `json:"invoice_no"`
	// BilledOffline and BilledAt are what the till says about when and how this
	// sale happened. The server cannot know either.
	BilledOffline bool    `json:"billed_offline"`
	BilledAt      *string `json:"billed_at"`
}

func (h *Billing) Create(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	session, ok := scope.SessionFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Unauthorized("no session"))
		return
	}

	var req saleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	terminalID, err := uuid.Parse(req.TerminalID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("terminal_id is not a valid identifier"))
		return
	}
	customerID, err := optionalUUID(req.CustomerID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("customer_id is not a valid identifier"))
		return
	}
	dueDate, err := optionalDate(req.DueDate)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("due_date must look like 2026-04-30"))
		return
	}
	// A full timestamp, not a date: a till syncing a backlog has to say which
	// minute each sale happened, and the day's takings are counted by it.
	var billedAt *time.Time
	if req.BilledAt != nil && *req.BilledAt != "" {
		parsed, err := time.Parse(time.RFC3339, *req.BilledAt)
		if err != nil {
			httperr.Respond(c, httperr.BadRequest("billed_at must be a timestamp like 2026-04-30T18:45:00+05:30"))
			return
		}
		billedAt = &parsed
	}

	lines := make([]domain.LineInput, len(req.Items))
	for i, item := range req.Items {
		productID, err := uuid.Parse(item.ProductID)
		if err != nil {
			httperr.Respond(c, httperr.BadRequest("item product_id is not a valid identifier"))
			return
		}
		productUnitID, err := item.unitID()
		if err != nil {
			httperr.Respond(c, httperr.BadRequest("item product_unit_id is not a valid identifier"))
			return
		}
		lines[i] = domain.LineInput{
			ProductID:     productID,
			ProductUnitID: productUnitID,
			UnitQty:       item.qty(),
			BatchHint:     item.BatchNo,
			Discount:      item.Discount,
			Rate:          item.Rate,
		}
	}

	result, err := h.svc.Create(c.Request.Context(), service.CreateInput{
		StoreID:        storeID,
		UserID:         session.UserID,
		TerminalID:     terminalID,
		CustomerID:     customerID,
		Lines:          lines,
		BillDiscount:   req.BillDiscount,
		PaymentMode:    domain.PaymentMode(req.PaymentMode),
		RoundTotal:     req.RoundTotal,
		DueDate:        dueDate,
		IdempotencyKey: c.GetHeader(idempotencyHeader),
		InvoiceNo:      req.InvoiceNo,
		BilledOffline:  req.BilledOffline,
		BilledAt:       billedAt,
	})
	if err != nil {
		respondBilling(c, err)
		return
	}

	// A replay is answered 200, not 201: this request created nothing.
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	c.JSON(status, result)
}

func (h *Billing) List(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	customerID, err := optionalUUID(queryPtr(c, "customer_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("customer_id is not a valid identifier"))
		return
	}
	terminalID, err := optionalUUID(queryPtr(c, "terminal_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("terminal_id is not a valid identifier"))
		return
	}
	from, err := optionalDate(queryPtr(c, "start_date"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("start_date must look like 2026-04-01"))
		return
	}
	to, err := optionalDate(queryPtr(c, "end_date"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("end_date must look like 2026-04-30"))
		return
	}

	invoices, err := h.svc.List(c.Request.Context(), repo.InvoiceFilter{
		StoreID:    storeID,
		CustomerID: customerID,
		TerminalID: terminalID,
		From:       from,
		To:         to,
		Status:     domain.Status(c.Query("status")),
		Limit:      intQuery(c, "limit", 0),
	})
	if err != nil {
		respondBilling(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"invoices": invoices})
}

func (h *Billing) Get(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	result, err := h.svc.Get(c.Request.Context(), storeID, id)
	if err != nil {
		respondBilling(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Billing) Void(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Void(c.Request.Context(), storeID, id); err != nil {
		respondBilling(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "status": domain.StatusVoided})
}

func (h *Billing) GSTR1(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	rows, err := h.svc.GSTR1(c.Request.Context(), storeID, c.Query("month"))
	if err != nil {
		respondBilling(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"month": c.Query("month"), "invoices": rows})
}

type markFiledRequest struct {
	Month string `json:"month" binding:"required"`
}

func (h *Billing) MarkGSTR1Filed(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	var req markFiledRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	marked, err := h.svc.MarkGSTR1Filed(c.Request.Context(), storeID, req.Month)
	if err != nil {
		respondBilling(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"month": req.Month, "marked": marked})
}

// respondBilling maps the billing-specific errors to statuses a till can act
// on, before falling through to the shared envelope.
func respondBilling(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrValidation):
		httperr.Respond(c, httperr.Unprocessable(unwrapValidationMessage(err)))
	case errors.Is(err, service.ErrPriceChanged):
		// 409, so the till knows to refresh its price list and retry rather
		// than treating this as a bad request it should not repeat.
		httperr.Respond(c, httperr.Conflict(err.Error()))
	case errors.Is(err, service.ErrAlreadyVoided):
		httperr.Respond(c, httperr.Conflict("this invoice has already been voided"))
	case errors.Is(err, service.ErrTerminalRequired):
		httperr.Respond(c, httperr.BadRequest(err.Error()))
	case errors.Is(err, stockdomain.ErrInsufficientStock):
		httperr.Respond(c, httperr.Unprocessable(err.Error()))
	default:
		httperr.Respond(c, err)
	}
}

// optionalDate parses a yyyy-mm-dd field that may be absent or blank.
func optionalDate(s *string) (*time.Time, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	d, err := time.Parse("2006-01-02", *s)
	if err != nil {
		return nil, err
	}
	return &d, nil
}
