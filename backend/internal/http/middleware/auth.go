package middleware

import (
	"context"
	"errors"
	"strings"

	domain "gbos/internal/domain/user"
	"gbos/internal/platform/httperr"
	"gbos/internal/rbac"
	authsvc "gbos/internal/service/auth"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Session is the authenticated identity behind a request.
type Session struct {
	UserID  uuid.UUID
	Role    domain.Role
	StoreID uuid.UUID
}

// Authenticator validates a bearer token. The auth service satisfies it.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (authsvc.Claims, domain.User, error)
}

// RequireAuth rejects a request without a valid session and makes the identity
// available downstream.
//
// Unlike the implementation it replaces, this does not decide anything about
// stores: that belongs to RequireStore, which runs after it. Splitting the two
// is what stops an endpoint accidentally authenticating without scoping.
func RequireAuth(a Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c)
		if !ok {
			httperr.Respond(c, httperr.Unauthorized("a bearer token is required"))
			c.Abort()
			return
		}

		claims, _, err := a.Authenticate(c.Request.Context(), token)
		if err != nil {
			// Deliberately uniform: distinguishing "expired" from "revoked"
			// from "unknown user" tells an attacker which tokens are worth
			// replaying.
			if errors.Is(err, domain.ErrInactive) {
				httperr.Respond(c, httperr.Unauthorized("your account is no longer active"))
			} else {
				httperr.Respond(c, httperr.Unauthorized("your session is not valid"))
			}
			c.Abort()
			return
		}

		s := Session{UserID: claims.UserID, Role: claims.Role, StoreID: claims.StoreID}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), sessionKey, s))
		c.Next()
	}
}

func bearerToken(c *gin.Context) (string, bool) {
	header := c.GetHeader("Authorization")
	if header == "" {
		return "", false
	}
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// SessionFrom returns the authenticated identity, if RequireAuth ran.
func SessionFrom(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionKey).(Session)
	return s, ok
}

// RequirePermission enforces the role/permission table. Use after RequireAuth.
func RequirePermission(permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		s, ok := SessionFrom(c.Request.Context())
		if !ok {
			httperr.Respond(c, httperr.Internal("permission check ran without authentication"))
			c.Abort()
			return
		}
		if !rbac.RoleHas(string(s.Role), permission) {
			httperr.Respond(c, httperr.Forbidden("your role does not permit this"))
			c.Abort()
			return
		}
		c.Next()
	}
}

// ContextSession adapts the request-scoped session to the SessionReader that
// RequireStore needs, so the two middlewares share one source of identity
// rather than each reading the request their own way.
type ContextSession struct{}

// Role returns the authenticated role, or empty when unauthenticated.
func (ContextSession) Role(c *gin.Context) string {
	if s, ok := SessionFrom(c.Request.Context()); ok {
		return string(s.Role)
	}
	return ""
}

// StoreID returns the session's home store, or empty when unauthenticated.
func (ContextSession) StoreID(c *gin.Context) string {
	if s, ok := SessionFrom(c.Request.Context()); ok {
		return s.StoreID.String()
	}
	return ""
}

var _ SessionReader = ContextSession{}
