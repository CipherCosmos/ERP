package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gbos/internal/domain/money"
	purchasingdomain "gbos/internal/domain/purchasing"
	stockdomain "gbos/internal/domain/stock"
	platform "gbos/internal/platform/postgres"
	purchasingsvc "gbos/internal/service/purchasing"
	stocksvc "gbos/internal/service/stock"

	"github.com/gin-gonic/gin"
)

// TestStockAndPurchasingRoutesRegister mounts both handlers on one engine.
//
// It is worth a test because gin panics when a static segment collides with a
// wildcard in the same method tree — /purchases/receive next to
// /purchases/cancel/:id is exactly that shape — and a panic at route-building
// time takes the whole server down at start-up rather than failing one request.
func TestStockAndPurchasingRoutesRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	group := r.Group("/api/v1")

	NewStock(nil).Register(group)
	NewPurchasing(nil).Register(group)

	registered := make(map[string]bool)
	for _, route := range r.Routes() {
		registered[route.Method+" "+route.Path] = true
	}

	want := []string{
		"GET /api/v1/stock",
		"GET /api/v1/stock/low",
		"GET /api/v1/stock/adjustments",
		"GET /api/v1/stock/product/:id",
		"POST /api/v1/stock/adjust",
		"POST /api/v1/stock/transfer",
		"POST /api/v1/stock/reserve",
		"POST /api/v1/stock/release",
		"GET /api/v1/losses",
		"POST /api/v1/losses",
		"GET /api/v1/expiry/expiring",
		"GET /api/v1/expiry/expired",

		"GET /api/v1/vendors",
		"GET /api/v1/vendors/:id",
		"POST /api/v1/vendors",
		"PUT /api/v1/vendors/:id",
		"POST /api/v1/vendors/:id/deactivate",
		"GET /api/v1/purchases",
		"GET /api/v1/purchases/:id",
		"POST /api/v1/purchases",
		"POST /api/v1/purchases/receive",
		"POST /api/v1/purchases/cancel/:id",
		"GET /api/v1/grn",
		"GET /api/v1/grn/:id",
		"POST /api/v1/grn",
		"GET /api/v1/purchase-returns",
		"GET /api/v1/purchase-returns/:id",
		"POST /api/v1/purchase-returns",
		"POST /api/v1/purchase-returns/:id/approve",
		"GET /api/v1/vendor-payments",
		"POST /api/v1/vendor-payments",
		"GET /api/v1/vendor-payments/summary",
	}
	for _, route := range want {
		if !registered[route] {
			t.Errorf("route %s was not registered", route)
		}
	}

	// /stock/convert is gone and must stay gone. Stock is ONE pool held in the
	// product's base unit, so there is nothing to convert it into: receiving ten
	// sacks adds 250 kg to that pool and selling 250 g takes 0.25 away from the
	// same pool. The endpoint divided a batch by a packing unit's size to create
	// a second batch, which under one pool would double-count the same goods.
	if registered["POST /api/v1/stock/convert"] {
		t.Error("POST /api/v1/stock/convert is registered again — see the stock service for why there is nothing to convert")
	}
}

// TestCatalogueRoutesRegister covers the same gin wildcard hazard for the
// catalogue: /products/price is a static sibling of /products/:id, and the
// product-unit routes add a second parameter below that same :id. Gin panics at
// route-building time on a conflict, which takes the server down at start-up
// rather than failing one request.
func TestCatalogueRoutesRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	group := r.Group("/api/v1")

	NewCatalogue(nil).Register(group)

	registered := make(map[string]bool)
	for _, route := range r.Routes() {
		registered[route.Method+" "+route.Path] = true
	}

	want := []string{
		"GET /api/v1/unit-families",
		"POST /api/v1/unit-families",
		"PUT /api/v1/unit-families/:id",
		"POST /api/v1/unit-families/:id/base-unit",
		"POST /api/v1/unit-families/:id/deactivate",
		"POST /api/v1/unit-families/:id/activate",

		"GET /api/v1/uoms",
		"POST /api/v1/uoms",
		"PUT /api/v1/uoms/:id",
		"POST /api/v1/uoms/:id/deactivate",
		"POST /api/v1/uoms/:id/activate",

		"POST /api/v1/products/price",
		"GET /api/v1/products/:id/prices",

		"GET /api/v1/products/:id/units",
		"POST /api/v1/products/:id/units",
		"PUT /api/v1/products/:id/units/:unit_id",
		"POST /api/v1/products/:id/units/:unit_id/default",
		"POST /api/v1/products/:id/units/:unit_id/deactivate",
		"POST /api/v1/products/:id/units/:unit_id/activate",

		"GET /api/v1/barcodes/lookup/:barcode",
	}
	for _, route := range want {
		if !registered[route] {
			t.Errorf("route %s was not registered", route)
		}
	}

	// The old tree-shaped packing units are gone with the type behind them.
	for _, gone := range []string{
		"GET /api/v1/products/:id/packing-units",
		"POST /api/v1/products/:id/packing-units",
	} {
		if registered[gone] {
			t.Errorf("%s is registered again — packing units were replaced by product units", gone)
		}
	}
}

// TestRespondStockStatuses pins the mapping a shop floor acts on: a shortfall
// is 422 and not a 500 carrying a driver message.
func TestRespondStockStatuses(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"validation", fmt.Errorf("%w: quantity must be greater than zero", stockdomain.ErrValidation), http.StatusUnprocessableEntity},
		{"insufficient", stockdomain.Insufficient(money.QuantityFromInt(5), money.QuantityFromInt(2)), http.StatusUnprocessableEntity},
		{"insufficient reserved", fmt.Errorf("wrapped: %w", stockdomain.ErrInsufficientReserved), http.StatusUnprocessableEntity},
		{"same store", stocksvc.ErrSameStore, http.StatusUnprocessableEntity},
		{"store required", stocksvc.ErrStoreRequired, http.StatusBadRequest},
		{"not found", fmt.Errorf("read batch: %w", platform.ErrNotFound), http.StatusNotFound},
		{"unknown", errors.New("pq: relation \"stock\" does not exist"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stockPurchasingStatusFor(respondStock, tc.err); got != tc.want {
				t.Errorf("respondStock(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestRespondPurchasingStatuses pins the buy-side mapping. A credit limit and a
// bad state transition are conflicts, because both are refusals about the
// current state rather than about the request.
func TestRespondPurchasingStatuses(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"validation", fmt.Errorf("%w: vendor is required", purchasingdomain.ErrValidation), http.StatusUnprocessableEntity},
		{"credit limit", fmt.Errorf("%w: over", purchasingdomain.ErrCreditLimitExceeded), http.StatusConflict},
		{"transition", fmt.Errorf("%w: cancelled", purchasingdomain.ErrInvalidTransition), http.StatusConflict},
		{"over receipt", fmt.Errorf("%w: too many", purchasingdomain.ErrOverReceipt), http.StatusUnprocessableEntity},
		{"insufficient stock", fmt.Errorf("remove: %w", stockdomain.ErrInsufficientStock), http.StatusUnprocessableEntity},
		{"store required", purchasingsvc.ErrStoreRequired, http.StatusBadRequest},
		{"not found", fmt.Errorf("vendor: %w", platform.ErrNotFound), http.StatusNotFound},
		{"conflict", fmt.Errorf("insert: %w", platform.ErrConflict), http.StatusConflict},
		{"unknown", errors.New("pq: deadlock detected"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stockPurchasingStatusFor(respondPurchasing, tc.err); got != tc.want {
				t.Errorf("respondPurchasing(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestRespondStockHidesDriverDetail checks the rule httperr exists for: an
// unrecognised error must not hand the caller the table name.
func TestRespondStockHidesDriverDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/stock/adjust", nil)

	respondStock(c, errors.New(`pq: null value in column "batch_no" of relation "stock"`))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "batch_no") || strings.Contains(body, "relation") {
		t.Errorf("response leaked driver detail: %s", body)
	}
}

// stockPurchasingStatusFor runs one of the two error mappers against a
// throwaway context and reports the status it wrote.
func stockPurchasingStatusFor(respond func(*gin.Context, error), err error) int {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/stock", nil)
	respond(c, err)
	return rec.Code
}
