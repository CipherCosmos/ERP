package handler

import (
	"errors"
	"net/http"

	domain "gbos/internal/domain/audit"
	mw "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	"gbos/internal/rbac"
	service "gbos/internal/service/audit"

	"github.com/gin-gonic/gin"
)

// Audit serves the change trail.
type Audit struct {
	svc *service.Service
}

// NewAudit returns an audit handler.
func NewAudit(svc *service.Service) *Audit {
	return &Audit{svc: svc}
}

// Register mounts the trail listing.
//
// Reading the trail is gated on audit.view and not merely on holding a
// session. The entries name who did what and carry request bodies, so the list
// of people who may read it is deliberately shorter than the list of people
// who generate it — a cashier fills the trail without being able to read it.
func (h *Audit) Register(r gin.IRoutes) {
	r.GET("/audit", mw.RequirePermission(rbac.PermAuditView), h.List)
	r.GET("/audit/tables", mw.RequirePermission(rbac.PermAuditView), h.Tables)
}

// Tables lists the resources the trail mentions, so the screen's filter offers
// what is actually there instead of a list maintained by hand in the client.
func (h *Audit) Tables(c *gin.Context) {
	storeID, ok := mw.MustStore(c)
	if !ok {
		return
	}
	tables, err := h.svc.Tables(c.Request.Context(), storeID)
	if err != nil {
		respondAudit(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"tables": tables})
}

func (h *Audit) List(c *gin.Context) {
	storeID, ok := mw.MustStore(c)
	if !ok {
		return
	}
	userID, err := optionalUUID(queryPtr(c, "user_id"))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest("user_id is not a valid identifier"))
		return
	}

	entries, err := h.svc.List(c.Request.Context(), domain.Filter{
		StoreID: storeID,
		UserID:  userID,
		// The screen sends this as `table`; the column is table_name.
		TableName: c.Query("table"),
		Limit:     intQuery(c, "limit", 0),
	})
	if err != nil {
		respondAudit(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries})
}

// respondAudit maps the trail errors onto statuses before falling through to
// the shared envelope.
func respondAudit(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrValidation):
		httperr.Respond(c, httperr.Unprocessable(unwrapValidationMessage(err)))
	case errors.Is(err, service.ErrStoreRequired):
		httperr.Respond(c, httperr.BadRequest(err.Error()))
	default:
		httperr.Respond(c, err)
	}
}
