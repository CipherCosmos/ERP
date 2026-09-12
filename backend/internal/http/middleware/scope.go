// Package middleware holds the request-scoped concerns of the HTTP layer.
package middleware

import (
	"context"

	"gbos/internal/platform/httperr"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type contextKey int

// Context keys are declared together and given explicit values, rather than
// each file starting its own iota block, so two of them cannot silently
// collide on zero.
const (
	storeKey   contextKey = 1
	sessionKey contextKey = 2
)

// StoreChecker reports whether a store exists and is active. The store
// aggregate supplies it; this package does not talk to a database.
type StoreChecker interface {
	StoreIsActive(ctx context.Context, storeID uuid.UUID) (bool, error)
}

// SessionReader exposes the identity the auth middleware established.
//
// It is an interface rather than a direct dependency so that this package does
// not import the legacy auth middleware, which is still tied to SQLite.
type SessionReader interface {
	Role(c *gin.Context) string
	StoreID(c *gin.Context) string
}

// RequireStore resolves the store a request acts on and rejects a caller who
// may not act on it.
//
// This is middleware rather than a helper each handler calls, because calling
// it was optional and six endpoints forgot: a store-A manager could read store
// B's customer ledger, and a cashier could raise any customer's credit limit.
// A handler downstream of this can only obtain a store id that has already
// been authorised, so forgetting the check is no longer expressible.
//
// The store is taken from the `store_id` query parameter when present, and
// otherwise from the session. A store id in a request *body* is deliberately
// ignored: two sources of truth for the same value is how the original
// mismatch arose.
func RequireStore(session SessionReader, stores StoreChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		requested := c.Query("store_id")
		if requested == "" {
			requested = session.StoreID(c)
		}
		if requested == "" {
			httperr.Respond(c, httperr.BadRequest("store_id is required"))
			c.Abort()
			return
		}

		storeID, err := uuid.Parse(requested)
		if err != nil {
			httperr.Respond(c, httperr.BadRequest("store_id is not a valid identifier"))
			c.Abort()
			return
		}

		if !mayAccess(c, session, stores, storeID) {
			// 403 rather than 404: the caller named a store explicitly, so
			// confirming it exists tells them nothing they did not supply.
			httperr.Respond(c, httperr.Forbidden("you do not have access to this store"))
			c.Abort()
			return
		}

		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), storeKey, storeID))
		c.Next()
	}
}

func mayAccess(c *gin.Context, session SessionReader, stores StoreChecker, target uuid.UUID) bool {
	// An owner may act on any active store. Everyone else is pinned to the
	// store their session was issued for.
	if session.Role(c) == "owner" {
		active, err := stores.StoreIsActive(c.Request.Context(), target)
		if err != nil {
			// Fail closed. The check this replaces discarded the error and
			// treated a failed lookup as "no such store", which at least
			// failed closed by accident; here it is deliberate.
			return false
		}
		return active
	}
	return target.String() == session.StoreID(c)
}

// StoreFrom returns the authorised store for this request. It is only valid
// downstream of RequireStore, which is the only thing that sets it.
func StoreFrom(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(storeKey).(uuid.UUID)
	return id, ok
}

// MustStore returns the authorised store, or writes a 500 and reports false.
// A missing store here is a wiring mistake — a route mounted without
// RequireStore — not something a client can cause.
func MustStore(c *gin.Context) (uuid.UUID, bool) {
	id, ok := StoreFrom(c.Request.Context())
	if !ok {
		httperr.Respond(c, httperr.Internal("store scope was not resolved for this route"))
		c.Abort()
		return uuid.Nil, false
	}
	return id, true
}
