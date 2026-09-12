package handler

import (
	"errors"
	"net/http"
	"time"

	domain "gbos/internal/domain/expense"
	"gbos/internal/domain/money"
	mw "gbos/internal/http/middleware"
	scope "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	"gbos/internal/rbac"
	service "gbos/internal/service/expense"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Expense serves the shop's running costs.
type Expense struct {
	svc *service.Service
}

// NewExpense returns an expense handler.
func NewExpense(svc *service.Service) *Expense {
	return &Expense{svc: svc}
}

// Register mounts the expense routes.
//
// The static /expenses/categories and /expenses/summary segments are declared
// alongside /expenses itself and never under a wildcard, so gin's route tree
// has no static-versus-parameter collision to panic on at start-up.
func (h *Expense) Register(r gin.IRoutes) {
	r.GET("/expenses", mw.RequirePermission(rbac.PermExpenseRead), h.List)
	r.POST("/expenses", mw.RequirePermission(rbac.PermExpenseWrite), h.Create)
	r.GET("/expenses/categories", mw.RequirePermission(rbac.PermExpenseRead), h.ListCategories)
	// There is no category list in the schema and nothing seeds one, so
	// without this route a fresh installation can never record an expense at
	// all: the category is a required foreign key with no way to create a
	// target for it.
	r.POST("/expenses/categories", mw.RequirePermission(rbac.PermExpenseWrite), h.CreateCategory)
	r.GET("/expenses/summary", mw.RequirePermission(rbac.PermExpenseRead), h.Summary)
}

func (h *Expense) ListCategories(c *gin.Context) {
	categories, err := h.svc.ListCategories(c.Request.Context())
	if err != nil {
		respondExpense(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"categories": categories})
}

type createExpenseCategoryRequest struct {
	Name string `json:"name" binding:"required"`
}

func (h *Expense) CreateCategory(c *gin.Context) {
	var req createExpenseCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	created, err := h.svc.CreateCategory(c.Request.Context(), req.Name)
	if err != nil {
		respondExpense(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (h *Expense) List(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	filter, ok := expenseFilter(c, storeID)
	if !ok {
		return
	}
	expenses, err := h.svc.List(c.Request.Context(), filter)
	if err != nil {
		respondExpense(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"expenses": expenses})
}

func (h *Expense) Summary(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	filter, ok := expenseFilter(c, storeID)
	if !ok {
		return
	}
	summary, err := h.svc.Summarise(c.Request.Context(), filter)
	if err != nil {
		respondExpense(c, err)
		return
	}
	c.JSON(http.StatusOK, summary)
}

// createExpenseRequest is the wire shape for recording an expense.
//
// store_id is absent on purpose even though the client still sends it: the
// store comes from the scope middleware, which has already authorised it. Two
// sources of truth for the same value is how the original mismatch arose.
type createExpenseRequest struct {
	CategoryID  string      `json:"category_id" binding:"required"`
	Amount      money.Money `json:"amount"`
	Description string      `json:"description"`
	ExpenseDate string      `json:"expense_date" binding:"required"`
}

func (h *Expense) Create(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	session, ok := scope.SessionFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Internal("the session was not resolved for this route"))
		return
	}

	var req createExpenseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}
	categoryID, err := uuid.Parse(req.CategoryID)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("category_id is not a valid identifier"))
		return
	}
	date, err := time.Parse(domain.DateLayout, req.ExpenseDate)
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("expense_date must look like 2026-04-01"))
		return
	}

	created, err := h.svc.Record(c.Request.Context(), domain.NewExpenseInput{
		StoreID:     storeID,
		CategoryID:  categoryID,
		Amount:      req.Amount,
		Description: req.Description,
		ExpenseDate: date,
		UserID:      session.UserID,
	})
	if err != nil {
		respondExpense(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

// expenseFilter reads the window and category a listing or summary covers.
// A malformed date is refused rather than silently ignored: a filter that
// quietly did not apply shows the wrong period's spending with nothing on
// screen to say so.
func expenseFilter(c *gin.Context, storeID uuid.UUID) (domain.Filter, bool) {
	categoryID, err := optionalUUID(queryPtr(c, "category_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("category_id is not a valid identifier"))
		return domain.Filter{}, false
	}
	from, err := optionalDate(queryPtr(c, "start_date"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("start_date must look like 2026-04-01"))
		return domain.Filter{}, false
	}
	to, err := optionalDate(queryPtr(c, "end_date"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("end_date must look like 2026-04-30"))
		return domain.Filter{}, false
	}
	return domain.Filter{
		StoreID:    storeID,
		CategoryID: categoryID,
		From:       from,
		To:         to,
		Limit:      intQuery(c, "limit", 0),
	}, true
}

// respondExpense maps the expense errors onto statuses before falling through
// to the shared envelope.
func respondExpense(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrValidation):
		httperr.Respond(c, httperr.Unprocessable(unwrapValidationMessage(err)))
	case errors.Is(err, service.ErrUnknownCategory):
		// 422 rather than 404: the request itself is well formed and the thing
		// that is missing is a value inside it, not the endpoint's subject.
		httperr.Respond(c, httperr.Unprocessable(err.Error()))
	case errors.Is(err, service.ErrStoreRequired):
		httperr.Respond(c, httperr.BadRequest(err.Error()))
	default:
		httperr.Respond(c, err)
	}
}
