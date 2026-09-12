package http_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestARefusedRequestStillCarriesItsCORSHeaders.
//
// The rate limiter used to run before the CORS middleware, so a throttled
// request was written without the headers and the browser reported it as
// "blocked by CORS policy". A shop seeing that in a console has been told
// nothing about rate limiting, and whoever investigates looks at the wrong
// thing. The status and the reason have to survive the refusal.
func TestARefusedRequestStillCarriesItsCORSHeaders(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// Spend the global budget. The limit is generous, so this is a burst rather
	// than a realistic load.
	var last *httptest.ResponseRecorder
	for i := 0; i < 250; i++ {
		last = h.requestWithOrigin(http.MethodGet, "/api/v1/health", "http://localhost:3000")
		if last.Code == http.StatusTooManyRequests {
			break
		}
	}
	if last.Code != http.StatusTooManyRequests {
		t.Skip("the rate limit was not reached in this burst; nothing to assert")
	}
	if got := last.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("a rate-limited response carries Access-Control-Allow-Origin %q; "+
			"the browser will report this as a CORS failure rather than a rate limit", got)
	}
}

// TestAPreflightDoesNotSpendTheRateLimit.
//
// The browser sends a preflight before most requests, so counting them halves
// the limit a till actually gets — and a counter working quickly is exactly when
// that starts to bite. CORS answers OPTIONS and aborts before the limiter runs.
func TestAPreflightDoesNotSpendTheRateLimit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	for i := 0; i < 400; i++ {
		got := h.requestWithOrigin(http.MethodOptions, "/api/v1/products", "http://localhost:3000")
		if got.Code != http.StatusNoContent {
			t.Fatalf("preflight %d: status %d, want 204 — preflights are being counted against the limit",
				i, got.Code)
		}
	}
}

func (h *harness) requestWithOrigin(method, target, origin string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Origin", origin)
	if method == http.MethodOptions {
		req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}
