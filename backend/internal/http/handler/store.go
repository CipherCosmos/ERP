package handler

import (
	"errors"
	"net/http"

	domain "gbos/internal/domain/store"
	userdomain "gbos/internal/domain/user"
	mw "gbos/internal/http/middleware"
	scope "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	"gbos/internal/rbac"
	service "gbos/internal/service/store"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Store serves store administration: the shops themselves and the billing
// terminals inside them.
type Store struct {
	svc *service.Service
}

// NewStore returns a store administration handler.
func NewStore(svc *service.Service) *Store {
	return &Store{svc: svc}
}

// Register mounts the store and terminal routes behind the store middleware.
//
// The :id in a path is never the authority for which store is acted on — see
// storeAdminTarget. The middleware has already decided which store this caller
// may touch, and a path that names a different one is refused.
func (h *Store) Register(r gin.IRoutes) {
	r.GET("/stores", mw.RequirePermission(rbac.PermCatalogRead), h.List)
	r.POST("/stores", mw.RequirePermission(rbac.PermStoreCreate), h.Create)
	r.GET("/stores/:id", mw.RequirePermission(rbac.PermCatalogRead), h.Get)
	r.PUT("/stores/:id", mw.RequirePermission(rbac.PermStoreUpdate), h.Update)
	r.POST("/stores/:id/deactivate", mw.RequirePermission(rbac.PermStoreUpdate), h.Deactivate)

	// Terminals are nested under their store because a terminal only means
	// anything inside one: its code is a segment of every invoice number the
	// counter issues.
	r.GET("/stores/:id/terminals", mw.RequirePermission(rbac.PermCatalogRead), h.ListTerminals)
	r.POST("/stores/:id/terminals", mw.RequirePermission(rbac.PermStoreUpdate), h.RegisterTerminal)
	r.POST("/stores/:id/terminals/:terminal_id/deactivate", mw.RequirePermission(rbac.PermStoreUpdate), h.DeactivateTerminal)
}

// ---------------------------------------------------------------------------
// Stores
// ---------------------------------------------------------------------------

// List returns the stores the caller may see.
//
// An owner sees every active shop, which is what the store switcher and the
// multi-store rollup are built from. Everybody else sees the one store their
// session was issued for, because RequireStore will refuse them any other and
// listing the rest only leaks the group's shape — names, addresses and GSTINs
// of shops they cannot read a single figure from.
func (h *Store) List(c *gin.Context) {
	storeID, ok := scope.MustStore(c)
	if !ok {
		return
	}
	session, ok := scope.SessionFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Unauthorized("no session"))
		return
	}

	stores, err := h.svc.List(c.Request.Context())
	if err != nil {
		respondStoreAdmin(c, err)
		return
	}
	if session.Role != userdomain.RoleOwner {
		stores = storeAdminOnly(stores, storeID)
	}
	c.JSON(http.StatusOK, gin.H{"stores": stores})
}

func (h *Store) Get(c *gin.Context) {
	storeID, ok := storeAdminTarget(c)
	if !ok {
		return
	}
	st, err := h.svc.Get(c.Request.Context(), storeID)
	if err != nil {
		respondStoreAdmin(c, err)
		return
	}
	c.JSON(http.StatusOK, st)
}

// storeAdminRequest is the wire shape for creating or updating a shop.
type storeAdminRequest struct {
	Name    string `json:"name" binding:"required"`
	Address string `json:"address"`
	Phone   string `json:"phone"`
	// GSTIN may be blank: a shop below the registration threshold has none,
	// and refusing that would stop it using the system at all.
	GSTIN string `json:"gstin"`
	// LocationType is "store" or "warehouse". Blank means a store.
	LocationType string `json:"location_type"`
}

// Create adds a shop or godown.
//
// The scope check is not about the new store — it has no id yet — but about the
// caller: this route is mounted behind the store middleware like every other,
// so a request that reached it is authenticated and attached to a real shop.
// Which roles may create one is the route's RBAC, not this handler's business.
func (h *Store) Create(c *gin.Context) {
	if _, ok := scope.MustStore(c); !ok {
		return
	}
	var req storeAdminRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}

	created, err := h.svc.Create(c.Request.Context(), req.Name, req.Address, req.Phone, req.GSTIN,
		domain.LocationType(req.LocationType))
	if err != nil {
		respondStoreAdmin(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (h *Store) Update(c *gin.Context) {
	storeID, ok := storeAdminTarget(c)
	if !ok {
		return
	}
	var req storeAdminRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}

	updated, err := h.svc.Update(c.Request.Context(), storeID, req.Name, req.Address, req.Phone, req.GSTIN,
		domain.LocationType(req.LocationType))
	if err != nil {
		respondStoreAdmin(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// Deactivate closes a shop. There is no delete: a store owns invoices, stock
// and a document series, and removing the row would orphan all three.
func (h *Store) Deactivate(c *gin.Context) {
	storeID, ok := storeAdminTarget(c)
	if !ok {
		return
	}
	if err := h.svc.SetActive(c.Request.Context(), storeID, false); err != nil {
		respondStoreAdmin(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": storeID, "is_active": false})
}

// ---------------------------------------------------------------------------
// Terminals
// ---------------------------------------------------------------------------

func (h *Store) ListTerminals(c *gin.Context) {
	storeID, ok := storeAdminTarget(c)
	if !ok {
		return
	}
	terminals, err := h.svc.ListTerminals(c.Request.Context(), storeID)
	if err != nil {
		respondStoreAdmin(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"terminals": terminals})
}

// storeAdminTerminalRequest is the wire shape for registering a counter.
type storeAdminTerminalRequest struct {
	// Code appears inside every invoice number this counter issues, so it is
	// short, upper case and fixed for the life of the terminal.
	Code string `json:"code" binding:"required"`
	Name string `json:"name" binding:"required"`
}

func (h *Store) RegisterTerminal(c *gin.Context) {
	storeID, ok := storeAdminTarget(c)
	if !ok {
		return
	}
	var req storeAdminTerminalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}

	terminal, err := h.svc.RegisterTerminal(c.Request.Context(), storeID, req.Code, req.Name)
	if err != nil {
		respondStoreAdmin(c, err)
		return
	}
	c.JSON(http.StatusCreated, terminal)
}

// DeactivateTerminal retires a counter.
//
// The terminal is checked against the store before it is touched. The service
// retires a terminal by id alone, so without this an id would be enough to
// close another shop's counter — and each counter owns an invoice series, so
// that shop would simply stop being able to bill.
func (h *Store) DeactivateTerminal(c *gin.Context) {
	storeID, ok := storeAdminTarget(c)
	if !ok {
		return
	}
	terminalID, ok := pathUUID(c, "terminal_id")
	if !ok {
		return
	}

	terminals, err := h.svc.ListTerminals(c.Request.Context(), storeID)
	if err != nil {
		respondStoreAdmin(c, err)
		return
	}
	if !storeAdminHasTerminal(terminals, terminalID) {
		// 404, not 403: from where this caller stands there is no such
		// terminal, and saying otherwise confirms that somebody else has one.
		httperr.Respond(c, httperr.NotFound("no such terminal in this store"))
		return
	}

	if err := h.svc.SetTerminalActive(c.Request.Context(), terminalID, false); err != nil {
		respondStoreAdmin(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": terminalID, "store_id": storeID, "is_active": false})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// storeAdminTarget returns the store a request acts on.
//
// The id in the path is a statement of intent, not an authorisation: it is
// compared with the store the middleware already resolved, and a request that
// names a different one is refused. So the path can never widen what a caller
// may reach — an owner who wants another shop asks for it the same way every
// other route does, with ?store_id=, which RequireStore authorises properly.
func storeAdminTarget(c *gin.Context) (uuid.UUID, bool) {
	scoped, ok := scope.MustStore(c)
	if !ok {
		return uuid.Nil, false
	}
	target, ok := pathUUID(c, "id")
	if !ok {
		return uuid.Nil, false
	}
	if target != scoped {
		// The same wording and status RequireStore uses, so a caller cannot
		// tell the two refusals apart and learn which stores exist.
		httperr.Respond(c, httperr.Forbidden("you do not have access to this store"))
		return uuid.Nil, false
	}
	return scoped, true
}

// storeAdminOnly narrows a store list to the one store a caller may see.
func storeAdminOnly(stores []domain.Store, id uuid.UUID) []domain.Store {
	out := make([]domain.Store, 0, 1)
	for _, s := range stores {
		if s.ID == id {
			out = append(out, s)
		}
	}
	return out
}

// storeAdminHasTerminal reports whether a terminal belongs to the store its
// counters were listed from.
func storeAdminHasTerminal(terminals []domain.Terminal, id uuid.UUID) bool {
	for _, t := range terminals {
		if t.ID == id {
			return true
		}
	}
	return false
}

// respondStoreAdmin maps the store aggregate's errors onto statuses, before
// falling through to the shared envelope. A store or terminal that does not
// exist arrives as postgres.ErrNotFound and is rendered 404 there; a duplicate
// terminal code arrives as postgres.ErrConflict and is rendered 409.
func respondStoreAdmin(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrValidation):
		// 422: the body parsed, but a GSTIN is not in the statutory format, or
		// a terminal code would not survive being read out of an invoice
		// number.
		httperr.Respond(c, httperr.Unprocessable(unwrapValidationMessage(err)))
	default:
		httperr.Respond(c, err)
	}
}
