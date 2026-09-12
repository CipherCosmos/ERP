package handler

import (
	"errors"
	"net/http"
	"time"

	domain "gbos/internal/domain/credit"
	"gbos/internal/domain/money"
	scope "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	platform "gbos/internal/platform/postgres"
	service "gbos/internal/service/credit"

	"github.com/gin-gonic/gin"
)

// Credit serves the customer and credit-ledger endpoints.
type Credit struct {
	svc *service.Service
}

// NewCredit returns a credit handler.
func NewCredit(svc *service.Service) *Credit {
	return &Credit{svc: svc}
}

// Register mounts the customer and credit routes.
//
// Every one is store-scoped and belongs behind the store middleware: a customer
// ledger is one shop's money at risk, and there is no unscoped variant of any
// of these reads or writes. The paths are the ones the existing frontend
// already calls, so the cutover is a wiring change rather than a client change.
func (h *Credit) Register(r gin.IRoutes) {
	r.GET("/customers", h.ListCustomers)
	r.POST("/customers", h.CreateCustomer)
	r.GET("/customers/:id", h.GetCustomer)
	r.PUT("/customers/:id", h.UpdateCustomer)
	r.GET("/customers/:id/outstanding", h.Outstanding)

	r.GET("/credit", h.ListEntries)
	r.GET("/credit/overdue", h.Overdue)
	r.POST("/credit/payment", h.RecordPayment)
	r.GET("/credit/:id", h.GetEntry)
}

// ---------------------------------------------------------------------------
// Customers
// ---------------------------------------------------------------------------

func (h *Credit) ListCustomers(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	accounts, err := h.svc.ListAccounts(c.Request.Context(), domain.CustomerFilter{
		StoreID:         storeID,
		Search:          c.Query("search"),
		Limit:           intQuery(c, "limit", 0),
		IncludeInactive: c.Query("include_inactive") == "true",
	})
	if err != nil {
		respondCredit(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"customers": accounts})
}

// GetCustomer returns the customer together with what they owe. The account is
// rendered flat — the customer's own fields plus outstanding and advance —
// because those are the two figures every screen that shows a customer needs.
func (h *Credit) GetCustomer(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	account, err := h.svc.GetAccount(c.Request.Context(), storeID, id)
	if err != nil {
		respondCredit(c, err)
		return
	}
	c.JSON(http.StatusOK, account)
}

// creditCustomerRequest is the wire shape for creating a customer.
//
// There is deliberately no store_id here. The store came from the session or
// the query string and was authorised by the middleware; a body naming its own
// store is a body deciding its own authorisation. A client that still sends one
// is simply ignored rather than refused, so the existing frontend keeps working.
type creditCustomerRequest struct {
	Name        string      `json:"name" binding:"required"`
	NameHi      string      `json:"name_hi"`
	Phone       string      `json:"phone"`
	Address     string      `json:"address"`
	GSTIN       string      `json:"gstin"`
	CreditLimit money.Money `json:"credit_limit"`
	IsWholesale bool        `json:"is_wholesale"`
}

func (h *Credit) CreateCustomer(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	var req creditCustomerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}

	created, err := h.svc.CreateCustomer(c.Request.Context(), storeID, domain.NewCustomerInput{
		Name:        req.Name,
		NameHi:      req.NameHi,
		Phone:       req.Phone,
		Address:     req.Address,
		GSTIN:       req.GSTIN,
		CreditLimit: req.CreditLimit,
		IsWholesale: req.IsWholesale,
	})
	if err != nil {
		respondCredit(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

// creditCustomerUpdateRequest is a partial edit: every field is a pointer, so
// an absent field is left alone. A screen that only edits the phone number must
// not silently reset the credit limit to zero, which is exactly what a
// non-pointer body would do.
type creditCustomerUpdateRequest struct {
	Name        *string      `json:"name"`
	NameHi      *string      `json:"name_hi"`
	Phone       *string      `json:"phone"`
	Address     *string      `json:"address"`
	GSTIN       *string      `json:"gstin"`
	CreditLimit *money.Money `json:"credit_limit"`
	IsWholesale *bool        `json:"is_wholesale"`
	IsActive    *bool        `json:"is_active"`
}

func (h *Credit) UpdateCustomer(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	var req creditCustomerUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}

	updated, err := h.svc.UpdateCustomer(c.Request.Context(), storeID, id, domain.CustomerUpdate{
		Name:        req.Name,
		NameHi:      req.NameHi,
		Phone:       req.Phone,
		Address:     req.Address,
		GSTIN:       req.GSTIN,
		CreditLimit: req.CreditLimit,
		IsWholesale: req.IsWholesale,
		IsActive:    req.IsActive,
	})
	if err != nil {
		respondCredit(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (h *Credit) Outstanding(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	outstanding, err := h.svc.Outstanding(c.Request.Context(), storeID, id)
	if err != nil {
		respondCredit(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"customer_id": id, "outstanding": outstanding})
}

// ---------------------------------------------------------------------------
// Ledger
// ---------------------------------------------------------------------------

func (h *Credit) ListEntries(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	customerID, err := optionalUUID(queryPtr(c, "customer_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("customer_id is not a valid identifier"))
		return
	}

	// An unrecognised type is refused rather than passed through: a filter the
	// ledger cannot match would answer "no entries", which reads as a fact
	// about the customer instead of a typo in the request.
	var entryType *domain.Type
	if raw := queryPtr(c, "type"); raw != nil {
		t := domain.Type(*raw)
		if !t.Valid() {
			httperr.Respond(c, httperr.BadRequest("type must be issue, payment or advance"))
			return
		}
		entryType = &t
	}

	entries, err := h.svc.ListEntries(c.Request.Context(), domain.EntryFilter{
		StoreID:    storeID,
		CustomerID: customerID,
		Type:       entryType,
		OpenOnly:   c.Query("open_only") == "true",
		Limit:      intQuery(c, "limit", 0),
	})
	if err != nil {
		respondCredit(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries})
}

func (h *Credit) GetEntry(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	entry, err := h.svc.GetEntry(c.Request.Context(), storeID, id)
	if err != nil {
		respondCredit(c, err)
		return
	}
	c.JSON(http.StatusOK, entry)
}

func (h *Credit) Overdue(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	asOf, err := optionalDate(queryPtr(c, "as_of"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("as_of must look like 2026-04-30"))
		return
	}
	// A zero time means "now" to the service, which owns the clock.
	var at time.Time
	if asOf != nil {
		at = *asOf
	}

	customers, err := h.svc.Overdue(c.Request.Context(), storeID, at)
	if err != nil {
		respondCredit(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"customers": customers})
}

// ---------------------------------------------------------------------------
// Payments
// ---------------------------------------------------------------------------

// creditPaymentRequest is money received from a customer.
//
// The tender used and any note are deliberately absent: credit_entries has no
// column for either, so binding them here would accept a field the ledger then
// silently drops. The legacy client sends `method` and `notes`; they are
// ignored rather than rejected, so the cutover does not need a client change.
type creditPaymentRequest struct {
	CustomerID string      `json:"customer_id" binding:"required"`
	Amount     money.Money `json:"amount" binding:"required"`
}

func (h *Credit) RecordPayment(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	var req creditPaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	customerID, err := optionalUUID(&req.CustomerID)
	if err != nil || customerID == nil {
		httperr.Respond(c, httperr.BadRequest("customer_id is not a valid identifier"))
		return
	}

	result, err := h.svc.RecordPayment(c.Request.Context(), service.PaymentInput{
		StoreID:    storeID,
		CustomerID: *customerID,
		Amount:     req.Amount,
	})
	if err != nil {
		respondCredit(c, err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

// respondCredit maps the credit-specific errors to statuses a counter can act
// on, before falling through to the shared envelope.
func respondCredit(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrValidation):
		httperr.Respond(c, httperr.Unprocessable(unwrapValidationMessage(err)))
	case errors.Is(err, domain.ErrLimitExceeded):
		// 422, not 403: the request is well formed and the caller is allowed to
		// make it — the customer's balance is what refuses it. The message
		// carries the three figures behind the refusal so the counter can
		// explain it rather than just report it.
		httperr.Respond(c, httperr.Unprocessable(err.Error()))
	case errors.Is(err, service.ErrStoreRequired), errors.Is(err, service.ErrCustomerRequired):
		httperr.Respond(c, httperr.BadRequest(err.Error()))
	case errors.Is(err, platform.ErrNotFound):
		// Deliberately the same answer whether the row does not exist or belongs
		// to another store. Distinguishing them would confirm the existence of
		// another shop's customer to anyone holding a guessed id.
		httperr.Respond(c, httperr.NotFound("no such customer or credit entry in this store"))
	default:
		httperr.Respond(c, err)
	}
}
