package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	creditdomain "gbos/internal/domain/credit"
	paymentdomain "gbos/internal/domain/payment"
	platform "gbos/internal/platform/postgres"
	creditsvc "gbos/internal/service/credit"
	paymentsvc "gbos/internal/service/payment"

	"github.com/gin-gonic/gin"
)

// TestCreditAndPaymentRoutesRegister pins the route tables and, more usefully,
// proves they can coexist: gin panics when a static segment and a wildcard
// collide, and /credit/overdue next to /credit/:id is exactly that shape.
func TestCreditAndPaymentRoutesRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	group := r.Group("")

	// Register does not touch the service, so a nil one is enough to assemble
	// the routing table.
	(&Credit{}).Register(group)
	(&Payment{}).Register(group)

	want := map[string]bool{
		"GET /customers":                 false,
		"POST /customers":                false,
		"GET /customers/:id":             false,
		"PUT /customers/:id":             false,
		"GET /customers/:id/outstanding": false,
		"GET /credit":                    false,
		"GET /credit/overdue":            false,
		"POST /credit/payment":           false,
		"GET /credit/:id":                false,
		"POST /payments/split":           false,
		"GET /payments/invoice/:id":      false,
		"GET /payments/settlement":       false,
	}
	for _, route := range r.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected route registered: %s", key)
			continue
		}
		want[key] = true
	}
	for key, found := range want {
		if !found {
			t.Errorf("route not registered: %s", key)
		}
	}
}

func TestRespondCreditStatuses(t *testing.T) {
	limitExceeded := fmt.Errorf("%w: 900.00 outstanding plus 200.00 requested is over the 1000.00 limit",
		creditdomain.ErrLimitExceeded)

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"validation", fmt.Errorf("%w: customer name is required", creditdomain.ErrValidation), http.StatusUnprocessableEntity},
		{"limit exceeded", limitExceeded, http.StatusUnprocessableEntity},
		{"store required", creditsvc.ErrStoreRequired, http.StatusBadRequest},
		{"customer required", creditsvc.ErrCustomerRequired, http.StatusBadRequest},
		{"not found", fmt.Errorf("load customer: %w", platform.ErrNotFound), http.StatusNotFound},
		{"unrecognised", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusFromResponder(respondCredit, tc.err); got != tc.want {
				t.Errorf("respondCredit(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestRespondPaymentStatuses(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"validation", fmt.Errorf("%w: a settlement needs at least one payment", paymentdomain.ErrValidation), http.StatusUnprocessableEntity},
		{"amount mismatch", fmt.Errorf("%w: short", paymentdomain.ErrAmountMismatch), http.StatusUnprocessableEntity},
		{"already settled", paymentdomain.ErrAlreadySettled, http.StatusConflict},
		{"voided invoice", paymentdomain.ErrNotSettleable, http.StatusConflict},
		{"store required", paymentsvc.ErrStoreRequired, http.StatusBadRequest},
		{"invoice required", paymentsvc.ErrInvoiceRequired, http.StatusBadRequest},
		{"not found", fmt.Errorf("%w: no such invoice in this store", platform.ErrNotFound), http.StatusNotFound},
		{"unrecognised", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusFromResponder(respondPayment, tc.err); got != tc.want {
				t.Errorf("respondPayment(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// statusFromResponder runs an error mapper against a throwaway context and
// reports the status it wrote.
func statusFromResponder(respondWith func(*gin.Context, error), err error) int {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	respondWith(c, err)
	return rec.Code
}
