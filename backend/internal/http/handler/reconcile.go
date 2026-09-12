package handler

import (
	"errors"
	"net/http"

	domain "gbos/internal/domain/reconcile"
	mw "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	platform "gbos/internal/platform/postgres"
	"gbos/internal/rbac"
	service "gbos/internal/service/reconcile"

	"github.com/gin-gonic/gin"
)

// Reconcile serves the working list of what a shop has to look at after a till
// has been offline.
type Reconcile struct {
	svc *service.Service
}

// NewReconcile returns a reconciliation handler.
func NewReconcile(svc *service.Service) *Reconcile { return &Reconcile{svc: svc} }

// Register mounts the reconciliation routes.
//
// Reading is behind the loss permission and resolving behind stock movement:
// both are what an exception asks somebody to do — count a shelf, or decide
// about a debt — and neither is a cashier's job.
func (h *Reconcile) Register(r gin.IRoutes) {
	r.GET("/reconciliation", mw.RequirePermission(rbac.PermLossRead), h.List)
	r.GET("/reconciliation/count", mw.RequirePermission(rbac.PermLossRead), h.CountOpen)
	r.POST("/reconciliation/:id/resolve", mw.RequirePermission(rbac.PermStockMove), h.Resolve)
}

func (h *Reconcile) List(c *gin.Context) {
	storeID, ok := mw.MustStore(c)
	if !ok {
		return
	}
	entries, err := h.svc.List(c.Request.Context(), domain.Filter{
		StoreID: storeID,
		// ?resolved=true shows the ones already dealt with. The default is the
		// working list, because that is what somebody opens this screen to do.
		IncludeResolved: c.Query("resolved") == "true",
		Kind:            domain.Kind(c.Query("kind")),
		Limit:           intQuery(c, "limit", 0),
	})
	if err != nil {
		respondReconcile(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"exceptions": entries})
}

func (h *Reconcile) CountOpen(c *gin.Context) {
	storeID, ok := mw.MustStore(c)
	if !ok {
		return
	}
	n, err := h.svc.CountOpen(c.Request.Context(), storeID)
	if err != nil {
		respondReconcile(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"open": n})
}

type resolveRequest struct {
	// Note is what the person did about it. Optional, and worth writing: the
	// next person to see a resolved exception has only this to go on.
	Note string `json:"note"`
}

func (h *Reconcile) Resolve(c *gin.Context) {
	storeID, ok := mw.MustStore(c)
	if !ok {
		return
	}
	session, ok := mw.SessionFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Unauthorized("no session"))
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	var req resolveRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			httperr.Respond(c, httperr.FromBinding(err))
			return
		}
	}
	if err := h.svc.Resolve(c.Request.Context(), storeID, id, session.UserID, req.Note); err != nil {
		respondReconcile(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"resolved": true})
}

func respondReconcile(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrStoreRequired):
		httperr.Respond(c, httperr.BadRequest("store_id is required"))
	case errors.Is(err, service.ErrAlreadyResolved):
		httperr.Respond(c, httperr.Conflict("somebody has already resolved this"))
	case errors.Is(err, domain.ErrValidation):
		httperr.Respond(c, httperr.Unprocessable(unwrapValidationMessage(err)))
	case errors.Is(err, platform.ErrNotFound):
		httperr.Respond(c, httperr.NotFound("no such exception"))
	default:
		httperr.Respond(c, err)
	}
}
