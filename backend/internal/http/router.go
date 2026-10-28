// Package http assembles the API: middleware chain, routes, and the services
// behind them.
package http

import (
	"fmt"
	"net/http"

	"gbos/internal/app"
	"gbos/internal/http/handler"
	mw "gbos/internal/http/middleware"
	"gbos/internal/platform/httperr"
	platform "gbos/internal/platform/postgres"

	"github.com/gin-gonic/gin"
)

// Config describes the HTTP surface.
type Config struct {
	// CORSOrigin is the exact origin the browser loads the frontend from.
	CORSOrigin string
	// DB backs the health check.
	DB *platform.DB
	// TrustedProxies are the proxies whose X-Forwarded-For may be believed.
	// Empty trusts none, which is right for a directly exposed server: rate
	// limiting keys on client IP, and an untrusted forwarding header lets a
	// caller rotate straight past the login limiter.
	TrustedProxies []string
}

// Limits are the request rates the API enforces.
type Limits struct {
	// Global applies to every request, per client IP.
	GlobalPerSecond float64
	GlobalBurst     float64
	// Login is far tighter, because that endpoint is the one worth guessing at.
	LoginPerSecond float64
	LoginBurst     float64
}

// DefaultLimits are sized for a shop: generous for a till working quickly,
// tight enough that guessing a password is not practical.
func DefaultLimits() Limits {
	return Limits{
		GlobalPerSecond: 100,
		GlobalBurst:     100,
		LoginPerSecond:  10.0 / 60.0, // ten attempts a minute
		LoginBurst:      10,
	}
}

// NewRouter builds the API.
//
// The middleware order is load-bearing: authenticate, then resolve the store,
// then check the permission. Store resolution needs an identity to compare
// against, and a permission check on an unauthenticated request is a wiring
// bug rather than a client error.
//
// There are three groups, and which one a route sits in is a security
// decision:
//
//   - public: health and login only
//   - authenticated: identity established, no single store implied
//   - scoped: identity plus exactly one authorised store
//
// Almost everything is scoped. A handler in that group cannot obtain a store
// id that has not already been authorised, which is what makes forgetting the
// check unexpressible — six endpoints in the previous implementation forgot.
func NewRouter(svcs app.Services, cfg Config) (*gin.Engine, func(), error) {
	r := gin.New()
	r.Use(gin.Recovery())

	// Validation errors must name the JSON key the client sent, not the Go
	// field. Registered here because this is the one place that runs before
	// any handler binds a body.
	httperr.UseJSONFieldNames()

	// Client IP decides who a rate limit applies to, so forwarding headers
	// must only be honoured from proxies we name. Gin's default is to trust
	// every proxy, which makes the limiter bypassable by anyone who can set a
	// header.
	if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		return nil, nil, fmt.Errorf("trusted proxies: %w", err)
	}

	limits := DefaultLimits()
	global := mw.NewRateLimiter(limits.GlobalPerSecond, limits.GlobalBurst)
	login := mw.NewRateLimiter(limits.LoginPerSecond, limits.LoginBurst)
	shutdown := func() {
		global.Close()
		login.Close()
	}

	// CORS first, and the rate limiter after it. Two reasons, and the second is
	// the one that cost an afternoon:
	//
	//   - A refusal has to be comprehensible. A 429 written before the CORS
	//     headers are set reaches the browser as "blocked by CORS policy", which
	//     says nothing about rate limiting and sends whoever reads it looking at
	//     the wrong thing entirely. The shop sees a blank screen either way; the
	//     difference is whether anyone can find out why.
	//   - A preflight is not a request the shop made. The browser sends one
	//     before most of them, so counting them halves the real limit — and a
	//     till working quickly is exactly when that starts to bite. CORS answers
	//     OPTIONS and aborts, so the limiter never sees one.
	if cfg.CORSOrigin != "" {
		r.Use(corsMiddleware(cfg.CORSOrigin))
	}

	r.Use(global.Limit())

	health := healthHandler(cfg.DB)
	r.GET("/health", health)
	r.HEAD("/health", health)

	api := r.Group("/api/v1")
	api.GET("/health", health)
	api.HEAD("/health", health)

	if svcs.Auth == nil {
		return r, shutdown, nil
	}

	auth := handler.NewAuth(svcs.Auth)
	api.POST("/auth/login", login.Limit(), auth.Login)

	// The trail records every mutation made by a signed-in user, so it goes on
	// both authenticated groups: gin copies a group's middleware at creation,
	// so a Use on one of them does not reach the other. It is deliberately
	// outside the login route — a failed sign-in has nobody to attribute — and
	// it redacts by field name, so the change-password body below is covered
	// without this file having to know that endpoint exists.
	//
	// The nil check is on the concrete service and not inside the middleware:
	// a nil *Service placed in an interface is not a nil interface, so testing
	// it there would wire a recorder that panics on first use.
	recordMutations := func(c *gin.Context) { c.Next() }
	if svcs.Audit != nil {
		recordMutations = mw.AuditMutations(svcs.Audit)
	}

	authed := api.Group("")
	authed.Use(mw.RequireAuth(svcs.Auth), recordMutations)
	auth.RegisterAuthenticated(authed)

	if svcs.Analytics != nil {
		// The one report that crosses the store boundary. It authorises
		// itself against the store list the caller names.
		handler.NewAnalytics(svcs.Analytics).RegisterUnscoped(authed)
	}

	scoped := api.Group("")
	scoped.Use(mw.RequireAuth(svcs.Auth), mw.RequireStore(mw.ContextSession{}, svcs.Store), recordMutations)

	if svcs.Store != nil {
		handler.NewStore(svcs.Store).Register(scoped)
	}
	if svcs.Catalogue != nil {
		handler.NewCatalogue(svcs.Catalogue).Register(scoped)
	}
	if svcs.Stock != nil {
		handler.NewStock(svcs.Stock).Register(scoped)
	}
	if svcs.Billing != nil {
		handler.NewBilling(svcs.Billing).Register(scoped)
	}
	if svcs.Credit != nil {
		handler.NewCredit(svcs.Credit).Register(scoped)
	}
	if svcs.Payment != nil {
		handler.NewPayment(svcs.Payment).Register(scoped)
	}
	if svcs.Purchasing != nil {
		handler.NewPurchasing(svcs.Purchasing).Register(scoped)
	}
	if svcs.SalesReturn != nil {
		handler.NewSalesReturn(svcs.SalesReturn).Register(scoped)
	}
	if svcs.Analytics != nil {
		handler.NewAnalytics(svcs.Analytics).Register(scoped)
	}
	if svcs.Expense != nil {
		handler.NewExpense(svcs.Expense).Register(scoped)
	}
	if svcs.Audit != nil {
		handler.NewAudit(svcs.Audit).Register(scoped)
	}
	if svcs.Reconcile != nil {
		handler.NewReconcile(svcs.Reconcile).Register(scoped)
	}

	return r, shutdown, nil
}

// healthHandler reports readiness. It queries the database rather than
// returning 200 unconditionally, so the check cannot stay green while the
// database is unreachable — which is exactly what the previous one did.
func healthHandler(db *platform.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if db == nil {
			c.JSON(http.StatusOK, gin.H{"status": "ok", "database": "not configured"})
			return
		}
		if err := db.Ping(c.Request.Context()); err != nil {
			httperr.Respond(c, httperr.New(http.StatusServiceUnavailable, "database_unavailable",
				"the database is not reachable"))
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "database": "ok"})
	}
}

func corsMiddleware(origin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		// X-Store-ID is still allowed because the existing frontend sends it on
		// every request. The server no longer reads it — RequireStore takes
		// the store from the query string or the session — but refusing it
		// here would fail the browser's preflight and break the app outright.
		c.Header("Access-Control-Allow-Headers",
			"Content-Type, Authorization, X-Request-ID, X-Store-ID, Idempotency-Key")
		c.Header("Vary", "Origin")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
