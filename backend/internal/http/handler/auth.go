package handler

import (
	"errors"
	"net/http"

	domain "gbos/internal/domain/user"
	mw "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	"gbos/internal/rbac"
	service "gbos/internal/service/auth"

	"github.com/gin-gonic/gin"
)

// Auth serves sign-in and account management.
type Auth struct {
	svc *service.Service
}

// NewAuth returns an auth handler.
func NewAuth(svc *service.Service) *Auth {
	return &Auth{svc: svc}
}

// RegisterAuthenticated mounts the routes that require a session. Login is
// mounted separately, because it is how a session is obtained.
func (h *Auth) RegisterAuthenticated(r gin.IRoutes) {
	// These two carry no permission, deliberately: they act on the caller's own
	// session and nothing else. Gating them would mean a role that cannot sign
	// out or change its own password, which is worse than useless — a shop that
	// suspects a cashier's password is known could not have them change it.
	// TestEveryRouteCarriesAPermission lists them as the only exemptions.
	r.POST("/auth/logout", h.Logout)
	r.POST("/auth/change-password", h.ChangePassword)
	r.GET("/auth/users", mw.RequirePermission(rbac.PermUserList), h.ListUsers)
	r.POST("/auth/register", mw.RequirePermission(rbac.PermUserRegister), h.Register)
	r.POST("/auth/users/:id/deactivate", mw.RequirePermission(rbac.PermUserDeactivate), h.Deactivate)
	r.POST("/auth/users/:id/activate", mw.RequirePermission(rbac.PermUserDeactivate), h.Activate)
}

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func (h *Auth) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}

	token, user, err := h.svc.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, domain.ErrBadCredentials) {
			httperr.Respond(c, httperr.Unauthorized("invalid username or password"))
			return
		}
		respondAuth(c, err)
		return
	}

	// permissions ships with the session so the client can hide the controls it
	// would only be refused on, without keeping its own copy of the matrix. The
	// web client did keep one, as a role list per screen, and the two had drifted
	// in both directions — a warehouse hand who may write off damage had no menu
	// entry for it, and a helper whose job is reading the reports could not see
	// them. This is advisory only: the server refuses on its own account either
	// way, and a client that ignores this list gets 403s rather than access.
	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user": gin.H{
			"id":        user.ID,
			"username":  user.Username,
			"role":      user.Role,
			"store_id":  user.StoreID,
			"is_active": user.IsActive,
		},
		"permissions": rbac.PermissionsFor(string(user.Role)),
	})
}

// Logout invalidates every session the caller holds, not only the token they
// presented. A token cannot be un-issued, so signing out has to mean
// invalidating the ones already in circulation.
func (h *Auth) Logout(c *gin.Context) {
	s, ok := mw.SessionFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Unauthorized("no session"))
		return
	}
	if err := h.svc.SignOutEverywhere(c.Request.Context(), s.UserID); err != nil {
		respondAuth(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"signed_out": true})
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

func (h *Auth) ChangePassword(c *gin.Context) {
	s, ok := mw.SessionFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Unauthorized("no session"))
		return
	}
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}

	if err := h.svc.ChangePassword(c.Request.Context(), s.UserID, req.OldPassword, req.NewPassword); err != nil {
		if errors.Is(err, domain.ErrBadCredentials) {
			httperr.Respond(c, httperr.Unauthorized("the current password is not correct"))
			return
		}
		respondAuth(c, err)
		return
	}
	// Every session is now invalid, including the one that made this request.
	c.JSON(http.StatusOK, gin.H{"changed": true, "sessions_ended": true})
}

func (h *Auth) ListUsers(c *gin.Context) {
	s, ok := mw.SessionFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Unauthorized("no session"))
		return
	}
	users, err := h.svc.ListByStore(c.Request.Context(), s.StoreID)
	if err != nil {
		respondAuth(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": users})
}

type registerRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Role     string `json:"role" binding:"required"`
}

func (h *Auth) Register(c *gin.Context) {
	s, ok := mw.SessionFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Unauthorized("no session"))
		return
	}
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httperr.Respond(c, httperr.FromBinding(err))
		return
	}

	// The new user joins the caller's store. Taking the store from the request
	// would let an administrator plant an account in somebody else's shop.
	created, err := h.svc.Register(c.Request.Context(), req.Username, req.Password, domain.Role(req.Role), s.StoreID)
	if err != nil {
		respondAuth(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id": created.ID, "username": created.Username, "role": created.Role, "store_id": created.StoreID,
	})
}

func (h *Auth) Deactivate(c *gin.Context) { h.setActive(c, false) }
func (h *Auth) Activate(c *gin.Context)   { h.setActive(c, true) }

func (h *Auth) setActive(c *gin.Context, active bool) {
	s, ok := mw.SessionFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Unauthorized("no session"))
		return
	}
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	if !active && id == s.UserID {
		httperr.Respond(c, httperr.BadRequest("you cannot deactivate your own account"))
		return
	}

	if err := h.svc.SetActive(c.Request.Context(), id, active); err != nil {
		if errors.Is(err, service.ErrLastOwner) {
			httperr.Respond(c, httperr.Unprocessable("this is the last active owner; promote another owner first"))
			return
		}
		respondAuth(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "is_active": active})
}

// respondAuth maps user-domain validation errors before falling through to the
// shared error envelope.
func respondAuth(c *gin.Context, err error) {
	if errors.Is(err, domain.ErrValidation) {
		httperr.Respond(c, httperr.Unprocessable(unwrapValidationMessage(err)))
		return
	}
	if errors.Is(err, domain.ErrInactive) {
		httperr.Respond(c, httperr.Unauthorized("your account is no longer active"))
		return
	}
	httperr.Respond(c, err)
}
