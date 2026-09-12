package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	domain "gbos/internal/domain/analytics"
	userdomain "gbos/internal/domain/user"
	scope "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	service "gbos/internal/service/analytics"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Analytics serves the reports: the dashboard, the sales breakdowns, the
// trading result, the receivables ladder and the GST summary.
type Analytics struct {
	svc *service.Service
}

// NewAnalytics returns a reporting handler.
func NewAnalytics(svc *service.Service) *Analytics {
	return &Analytics{svc: svc}
}

// Register mounts the store-scoped reports. Every route here reads one store's
// figures, so all of them belong behind the store middleware.
//
// The multi-store rollup is deliberately not here — it is the one report that
// crosses the store boundary, and it is mounted by RegisterUnscoped.
func (h *Analytics) Register(r gin.IRoutes) {
	r.GET("/analytics/dashboard", h.Dashboard)
	r.GET("/analytics/sales/category", h.SalesByCategory)
	r.GET("/analytics/sales/top", h.TopProducts)
	r.GET("/analytics/sales/hourly", h.HourlySales)
	r.GET("/analytics/sales/payment", h.PaymentModes)

	r.GET("/reports/profit-loss", h.ProfitAndLoss)
	r.GET("/reports/credit-aging", h.CreditAging)
	r.GET("/reports/gstr-3b", h.GSTR3B)
}

// RegisterUnscoped mounts the owner's rollup across several shops.
//
// It is separate because RequireStore resolves exactly one store, and this
// report reads several. Mount it on an authenticated group *without* the store
// middleware; MultiStore does its own authorisation, and does not weaken the
// store boundary because it admits only an owner — the one role RequireStore
// itself lets act on any active store — and only over a store list the caller
// names explicitly.
func (h *Analytics) RegisterUnscoped(r gin.IRoutes) {
	r.GET("/analytics/multi-store", h.MultiStore)
}

// ---------------------------------------------------------------------------
// Dashboard and sales breakdowns
// ---------------------------------------------------------------------------

func (h *Analytics) Dashboard(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	rng, period, explicit, err := analyticsWindow(c)
	if err != nil {
		respondAnalytics(c, err)
		return
	}

	// The clock stays in the service. When the caller named a relative period
	// the service resolves it against its own now; only an explicit pair of
	// dates goes through the range form.
	ctx := c.Request.Context()
	var dashboard domain.Dashboard
	if explicit {
		dashboard, err = h.svc.DashboardForRange(ctx, storeID, rng)
	} else {
		dashboard, err = h.svc.Dashboard(ctx, storeID, period)
	}
	if err != nil {
		respondAnalytics(c, err)
		return
	}
	c.JSON(http.StatusOK, dashboard)
}

func (h *Analytics) SalesByCategory(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	rng, err := analyticsRange(c)
	if err != nil {
		respondAnalytics(c, err)
		return
	}
	categories, err := h.svc.SalesByCategory(c.Request.Context(), storeID, rng)
	if err != nil {
		respondAnalytics(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"date_range": rng, "categories": categories})
}

// defaultTopProducts is how many best sellers the report returns when the
// caller does not say. It matches what the screen this feeds has always shown.
const defaultTopProducts = 20

func (h *Analytics) TopProducts(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	rng, err := analyticsRange(c)
	if err != nil {
		respondAnalytics(c, err)
		return
	}
	products, err := h.svc.TopProducts(c.Request.Context(), storeID, rng, intQuery(c, "limit", defaultTopProducts))
	if err != nil {
		respondAnalytics(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"date_range": rng, "products": products})
}

// HourlySales returns one trading day bucketed by hour. An absent date means
// today; a malformed one is refused rather than quietly becoming today.
func (h *Analytics) HourlySales(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	day, err := optionalDate(queryPtr(c, "date"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("date must look like 2026-04-15"))
		return
	}

	var on time.Time
	if day != nil {
		on = *day
	}
	hours, err := h.svc.HourlySales(c.Request.Context(), storeID, on)
	if err != nil {
		respondAnalytics(c, err)
		return
	}

	body := gin.H{"hours": hours}
	if day != nil {
		body["date"] = day.Format(domain.DateLayout)
	}
	c.JSON(http.StatusOK, body)
}

func (h *Analytics) PaymentModes(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	rng, err := analyticsRange(c)
	if err != nil {
		respondAnalytics(c, err)
		return
	}
	modes, err := h.svc.PaymentModes(c.Request.Context(), storeID, rng)
	if err != nil {
		respondAnalytics(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"date_range": rng, "payment_modes": modes})
}

// ---------------------------------------------------------------------------
// The owner's rollup
// ---------------------------------------------------------------------------

// MultiStore returns one owner's figures across several shops.
//
// # How it gets its store list, safely
//
// Two gates, and both are needed.
//
// First, the role: only an owner may run it. That is not an arbitrary choice —
// it is the same rule RequireStore already applies, where an owner may act on
// any active store and everyone else is pinned to the store their session was
// issued for. A cashier or manager therefore has no set of stores wider than
// the one they are already scoped to, and admitting them here would be the
// only way in the API to read another shop's takings.
//
// Second, the list: the stores are named explicitly in store_ids and an empty
// list is refused. The handler this replaces treated an absent parameter as
// "every store in the system", so a request that dropped its parameter — a
// mistyped query string, a client bug — silently widened into a group-wide
// report. The service refuses an empty list too; this is the message that says
// so in terms the caller can fix.
func (h *Analytics) MultiStore(c *gin.Context) {
	session, ok := scope.SessionFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Unauthorized("no session"))
		return
	}
	if session.Role != userdomain.RoleOwner {
		httperr.Respond(c, httperr.Forbidden("only an owner may read a multi-store rollup"))
		return
	}

	storeIDs, err := analyticsStoreIDs(c)
	if err != nil {
		respondAnalytics(c, err)
		return
	}
	rng, period, explicit, err := analyticsWindow(c)
	if err != nil {
		respondAnalytics(c, err)
		return
	}

	ctx := c.Request.Context()
	var rollup domain.MultiStore
	if explicit {
		rollup, err = h.svc.MultiStoreForRange(ctx, storeIDs, rng)
	} else {
		rollup, err = h.svc.MultiStore(ctx, storeIDs, period)
	}
	if err != nil {
		respondAnalytics(c, err)
		return
	}
	c.JSON(http.StatusOK, rollup)
}

// ---------------------------------------------------------------------------
// Profit and loss, receivables, GST
// ---------------------------------------------------------------------------

func (h *Analytics) ProfitAndLoss(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	rng, err := analyticsRange(c)
	if err != nil {
		respondAnalytics(c, err)
		return
	}
	result, err := h.svc.ProfitAndLoss(c.Request.Context(), storeID, rng)
	if err != nil {
		respondAnalytics(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// CreditAging returns the store's receivables split by age. An absent as_of
// means today; a malformed one is refused.
func (h *Analytics) CreditAging(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	asOf, err := optionalDate(queryPtr(c, "as_of"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("as_of must look like 2026-04-30"))
		return
	}

	var on time.Time
	if asOf != nil {
		on = *asOf
	}
	report, err := h.svc.CreditAging(c.Request.Context(), storeID, on)
	if err != nil {
		respondAnalytics(c, err)
		return
	}
	c.JSON(http.StatusOK, report)
}

func (h *Analytics) GSTR3B(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	month, ok := analyticsMonth(c)
	if !ok {
		return
	}
	summary, err := h.svc.GSTR3B(c.Request.Context(), storeID, month)
	if err != nil {
		respondAnalytics(c, err)
		return
	}
	c.JSON(http.StatusOK, summary)
}

// ---------------------------------------------------------------------------
// Query parameters
// ---------------------------------------------------------------------------

// analyticsDates reads the optional start_date/end_date pair.
//
// A malformed date is an error, never a silent fall back to today. The handlers
// these replace defaulted anything unparseable to the current date, so a
// mistyped year produced a plausible-looking report of the wrong period with
// nothing on screen to say so.
func analyticsDates(c *gin.Context) (from, to *time.Time, err error) {
	from, err = optionalDate(queryPtr(c, "start_date"))
	if err != nil {
		return nil, nil, httperr.BadRequest("start_date must look like 2026-04-01")
	}
	to, err = optionalDate(queryPtr(c, "end_date"))
	if err != nil {
		return nil, nil, httperr.BadRequest("end_date must look like 2026-04-30")
	}
	if (from == nil) != (to == nil) {
		return nil, nil, httperr.BadRequest("start_date and end_date go together; supply both or neither")
	}
	return from, to, nil
}

// analyticsRange is the window for a report that has no relative period to fall
// back on: both ends are required, and an end before its start is refused by
// the domain.
func analyticsRange(c *gin.Context) (domain.Range, error) {
	from, to, err := analyticsDates(c)
	if err != nil {
		return domain.Range{}, err
	}
	if from == nil {
		return domain.Range{}, httperr.BadRequest("start_date and end_date are required, as 2026-04-01")
	}
	return domain.NewRange(*from, *to)
}

// analyticsWindow is the window for a report that also accepts a relative
// period. explicit reports which of the two the caller asked for, so the
// handler can pick the service method that keeps the clock in the service.
func analyticsWindow(c *gin.Context) (rng domain.Range, period domain.Period, explicit bool, err error) {
	from, to, err := analyticsDates(c)
	if err != nil {
		return domain.Range{}, "", false, err
	}
	if from != nil {
		rng, err = domain.NewRange(*from, *to)
		return rng, "", true, err
	}
	// An unrecognised period is an error too, for the same reason a malformed
	// date is: "quarter" showed one day's takings labelled as a quarter's.
	period, err = domain.ParsePeriod(c.Query("range"))
	return domain.Range{}, period, false, err
}

// analyticsMonth reads the month a monthly return covers. It is required and
// must be well formed: these figures are transcribed onto a statutory return,
// and a month that quietly became "this one" is a return for the wrong period.
func analyticsMonth(c *gin.Context) (string, bool) {
	month := c.Query("month")
	if month == "" {
		httperr.Respond(c, httperr.BadRequest("month is required, as 2026-04"))
		return "", false
	}
	if _, err := time.Parse("2006-01", month); err != nil {
		httperr.Respond(c, httperr.BadRequest("month must look like 2026-04"))
		return "", false
	}
	return month, true
}

// analyticsStoreIDs reads the explicit store list the rollup runs over.
func analyticsStoreIDs(c *gin.Context) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, 4)
	for _, part := range strings.Split(c.Query("store_ids"), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := uuid.Parse(part)
		if err != nil {
			return nil, httperr.BadRequest("store_ids must be a comma-separated list of store identifiers")
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, httperr.BadRequest("store_ids is required: name the stores to include")
	}
	return ids, nil
}

// respondAnalytics maps the reporting errors onto statuses, before falling
// through to the shared envelope.
func respondAnalytics(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrValidation):
		// 422: the request parsed, but asks for a period that is not one — an
		// end date before its start, a month that is not a month.
		httperr.Respond(c, httperr.Unprocessable(unwrapValidationMessage(err)))
	case errors.Is(err, service.ErrStoreRequired):
		httperr.Respond(c, httperr.BadRequest(err.Error()))
	case errors.Is(err, service.ErrNoStores):
		// 400: a required parameter is missing. Never "every store".
		httperr.Respond(c, httperr.BadRequest(err.Error()))
	default:
		httperr.Respond(c, err)
	}
}
