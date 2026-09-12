package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	scope "gbos/internal/http/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type fakeSession struct {
	role    string
	storeID string
}

func (f fakeSession) Role(*gin.Context) string    { return f.role }
func (f fakeSession) StoreID(*gin.Context) string { return f.storeID }

type fakeStores struct {
	active map[uuid.UUID]bool
	err    error
}

func (f fakeStores) StoreIsActive(_ context.Context, id uuid.UUID) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.active[id], nil
}

// newRouter mounts RequireStore in front of a handler that reports the store
// it was given, so a test can tell "rejected" from "authorised as X".
func newRouter(session scope.SessionReader, stores scope.StoreChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(scope.RequireStore(session, stores))
	r.Any("/probe", func(c *gin.Context) {
		id, ok := scope.MustStore(c)
		if !ok {
			return
		}
		c.String(http.StatusOK, id.String())
	})
	return r
}

func do(r *gin.Engine, method, target string, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestNonOwnerIsPinnedToTheirOwnStore(t *testing.T) {
	own := uuid.New()
	other := uuid.New()
	r := newRouter(
		fakeSession{role: "manager", storeID: own.String()},
		fakeStores{active: map[uuid.UUID]bool{own: true, other: true}},
	)

	// This is the defect the middleware exists to close: a store-A manager
	// naming store B in the query string could previously read store B's
	// customer ledger, because six endpoints simply never checked.
	if got := do(r, http.MethodGet, "/probe?store_id="+other.String(), ""); got.Code != http.StatusForbidden {
		t.Errorf("manager reaching another store: status %d, want 403", got.Code)
	}
	if got := do(r, http.MethodGet, "/probe?store_id="+own.String(), ""); got.Code != http.StatusOK {
		t.Errorf("manager reaching their own store: status %d, want 200", got.Code)
	}
}

func TestOwnerMayReachAnyActiveStore(t *testing.T) {
	own := uuid.New()
	other := uuid.New()
	closed := uuid.New()
	r := newRouter(
		fakeSession{role: "owner", storeID: own.String()},
		fakeStores{active: map[uuid.UUID]bool{own: true, other: true, closed: false}},
	)

	if got := do(r, http.MethodGet, "/probe?store_id="+other.String(), ""); got.Code != http.StatusOK {
		t.Errorf("owner reaching a second store: status %d, want 200", got.Code)
	}
	if got := do(r, http.MethodGet, "/probe?store_id="+closed.String(), ""); got.Code != http.StatusForbidden {
		t.Errorf("owner reaching a closed store: status %d, want 403", got.Code)
	}
}

func TestStoreLookupFailureFailsClosed(t *testing.T) {
	own := uuid.New()
	r := newRouter(
		fakeSession{role: "owner", storeID: own.String()},
		fakeStores{err: errors.New("database unavailable")},
	)

	// A failed lookup must deny, not allow. Several checks in the old code
	// discarded this error, which left the caller with whatever the zero value
	// happened to mean.
	if got := do(r, http.MethodGet, "/probe?store_id="+own.String(), ""); got.Code != http.StatusForbidden {
		t.Errorf("status %d on a failed store lookup, want 403", got.Code)
	}
}

func TestStoreDefaultsToTheSession(t *testing.T) {
	own := uuid.New()
	r := newRouter(
		fakeSession{role: "cashier", storeID: own.String()},
		fakeStores{active: map[uuid.UUID]bool{own: true}},
	)

	got := do(r, http.MethodGet, "/probe", "")
	if got.Code != http.StatusOK {
		t.Fatalf("status %d without an explicit store_id, want 200", got.Code)
	}
	if got.Body.String() != own.String() {
		t.Errorf("resolved store %s, want the session store %s", got.Body.String(), own)
	}
}

func TestStoreIDInTheBodyIsIgnored(t *testing.T) {
	own := uuid.New()
	other := uuid.New()
	r := newRouter(
		fakeSession{role: "cashier", storeID: own.String()},
		fakeStores{active: map[uuid.UUID]bool{own: true, other: true}},
	)

	// Two sources of truth for the same value is how the original mismatch
	// arose: a body store_id was trusted on some endpoints and a query
	// store_id on others. The body is not consulted.
	got := do(r, http.MethodPost, "/probe", `{"store_id":"`+other.String()+`"}`)
	if got.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", got.Code)
	}
	if got.Body.String() != own.String() {
		t.Errorf("resolved store %s from the body; the session store %s must win", got.Body.String(), own)
	}
}

func TestMissingAndMalformedStoreAreRejected(t *testing.T) {
	r := newRouter(fakeSession{role: "cashier"}, fakeStores{})

	if got := do(r, http.MethodGet, "/probe", ""); got.Code != http.StatusBadRequest {
		t.Errorf("no store anywhere: status %d, want 400", got.Code)
	}
	if got := do(r, http.MethodGet, "/probe?store_id=not-a-uuid", ""); got.Code != http.StatusBadRequest {
		t.Errorf("malformed store_id: status %d, want 400", got.Code)
	}
}

func TestUnscopedRouteIsAWiringError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Deliberately mounted without RequireStore.
	r.GET("/probe", func(c *gin.Context) {
		if _, ok := scope.MustStore(c); !ok {
			return
		}
		c.String(http.StatusOK, "should not reach here")
	})

	got := do(r, http.MethodGet, "/probe", "")
	if got.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500 — a route without RequireStore is a wiring mistake, not a client error", got.Code)
	}
}
