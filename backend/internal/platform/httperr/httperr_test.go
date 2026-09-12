package httperr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gbos/internal/platform/postgres"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

// TestStatusForDomainErrors is the sentinel-to-status table, asserted.
func TestStatusForDomainErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"not found", postgres.ErrNotFound, http.StatusNotFound, CodeNotFound},
		{"conflict", postgres.ErrConflict, http.StatusConflict, CodeConflict},
		{"reference", postgres.ErrReference, http.StatusUnprocessableEntity, CodeUnprocessable},
		{"constraint", postgres.ErrConstraint, http.StatusUnprocessableEntity, CodeUnprocessable},
		{"serialization", postgres.ErrSerialization, http.StatusServiceUnavailable, CodeUnavailable},
		{"lock timeout", postgres.ErrLockTimeout, http.StatusServiceUnavailable, CodeUnavailable},
		{"timeout", postgres.ErrTimeout, http.StatusGatewayTimeout, CodeTimeout},
		{"canceled", postgres.ErrCanceled, StatusClientClosedRequest, CodeTimeout},
		{"invalid input", postgres.ErrInvalidInput, http.StatusBadRequest, CodeBadRequest},
		{"unavailable", postgres.ErrUnavailable, http.StatusServiceUnavailable, CodeUnavailable},
		{"privilege", postgres.ErrInsufficientPrivilege, http.StatusInternalServerError, CodeInternal},
		{"ctx deadline", context.DeadlineExceeded, http.StatusGatewayTimeout, CodeTimeout},
		{"ctx canceled", context.Canceled, StatusClientClosedRequest, CodeTimeout},
		{"unknown", errors.New("who knows"), http.StatusInternalServerError, CodeInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := From(tt.err)
			if e.Status != tt.wantStatus {
				t.Errorf("Status = %d, want %d", e.Status, tt.wantStatus)
			}
			if e.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", e.Code, tt.wantCode)
			}
			if e.Message == "" {
				t.Error("Message is empty")
			}
			if got := Status(tt.err); got != tt.wantStatus {
				t.Errorf("Status() = %d, want %d", got, tt.wantStatus)
			}
		})
	}
}

// TestStatusThroughRealDriverErrors goes end to end from a SQLSTATE, proving
// the two mapping tables line up.
func TestStatusThroughRealDriverErrors(t *testing.T) {
	tests := []struct {
		sqlstate   string
		wantStatus int
	}{
		{"23505", http.StatusConflict},
		{"23503", http.StatusUnprocessableEntity},
		{"23514", http.StatusUnprocessableEntity},
		{"23502", http.StatusUnprocessableEntity},
		{"40001", http.StatusServiceUnavailable},
		{"55P03", http.StatusServiceUnavailable},
		{"57014", http.StatusGatewayTimeout},
		{"22P02", http.StatusBadRequest},
		{"53300", http.StatusServiceUnavailable},
		{"42601", http.StatusInternalServerError}, // syntax error: our bug
	}

	for _, tt := range tests {
		t.Run(tt.sqlstate, func(t *testing.T) {
			err := postgres.MapError(&pgconn.PgError{Code: tt.sqlstate, Message: "boom"})
			if got := Status(err); got != tt.wantStatus {
				t.Fatalf("SQLSTATE %s -> %d, want %d", tt.sqlstate, got, tt.wantStatus)
			}
		})
	}
}

func TestWrappedErrorsStillMap(t *testing.T) {
	base := postgres.MapError(&pgconn.PgError{Code: "23505"})
	wrapped := fmt.Errorf("create product: %w", fmt.Errorf("repository: %w", base))

	if got := Status(wrapped); got != http.StatusConflict {
		t.Fatalf("Status through two wraps = %d, want 409", got)
	}
}

// TestExplicitErrorWins checks that a handler's own message survives, even when
// a domain sentinel is further down the chain.
func TestExplicitErrorWins(t *testing.T) {
	base := postgres.MapError(&pgconn.PgError{Code: "23505", ConstraintName: "products_sku_key"})
	specific := Conflict("A product with that SKU already exists.").Wrap(base)

	e := From(fmt.Errorf("service: %w", specific))
	if e.Status != http.StatusConflict {
		t.Errorf("Status = %d, want 409", e.Status)
	}
	if e.Message != "A product with that SKU already exists." {
		t.Errorf("Message = %q, want the handler's own wording", e.Message)
	}
}

func TestFromNil(t *testing.T) {
	if e := From(nil); e != nil {
		t.Errorf("From(nil) = %v, want nil", e)
	}
	if got := Status(nil); got != http.StatusOK {
		t.Errorf("Status(nil) = %d, want 200", got)
	}
}

// newTestContext returns a gin context with a request id already set, plus the
// recorder holding what was written.
func newTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/products", nil)
	c.Set(RequestIDKey, "test-request-id")
	return c, rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) Response {
	t.Helper()
	var got Response
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return got
}

func TestRespondEnvelope(t *testing.T) {
	c, rec := newTestContext(t)

	Respond(c, NotFound("That product does not exist."))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := decodeBody(t, rec)
	if body.Error.Code != CodeNotFound {
		t.Errorf("code = %q, want %q", body.Error.Code, CodeNotFound)
	}
	if body.Error.Message != "That product does not exist." {
		t.Errorf("message = %q", body.Error.Message)
	}
	if body.Error.RequestID != "test-request-id" {
		t.Errorf("request_id = %q, want it stamped from the gin context", body.Error.RequestID)
	}
	if !c.IsAborted() {
		t.Error("Respond did not abort the handler chain")
	}
}

// TestRespondNeverLeaksDriverDetail is the reason this package exists. The
// application currently answers with c.JSON(500, gin.H{"error": err.Error()}),
// which hands the client the table, column and constraint that failed.
func TestRespondNeverLeaksDriverDetail(t *testing.T) {
	// Route 5xx logging somewhere we can inspect, and keep it off stderr.
	var logBuf bytes.Buffer
	original := Logger
	Logger = func(context.Context) *slog.Logger {
		return slog.New(slog.NewTextHandler(&logBuf, nil))
	}
	t.Cleanup(func() { Logger = original })

	secrets := []string{
		"products_sku_key",
		"products",
		"Key (sku)=(ABC-123) already exists.",
		"duplicate key value violates unique constraint",
		"SQLSTATE",
	}

	driverErr := postgres.MapError(&pgconn.PgError{
		Code:           "23505",
		Message:        "duplicate key value violates unique constraint",
		Detail:         "Key (sku)=(ABC-123) already exists.",
		ConstraintName: "products_sku_key",
		TableName:      "products",
	})

	c, rec := newTestContext(t)
	Respond(c, fmt.Errorf("create product: %w", driverErr))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	body := rec.Body.String()
	for _, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Errorf("response body leaked %q\nbody: %s", secret, body)
		}
	}
}

// TestUnknownErrorIsGenericButLogged checks the other half of the contract: the
// client learns nothing, and the operator learns everything.
func TestUnknownErrorIsGenericButLogged(t *testing.T) {
	var logBuf bytes.Buffer
	original := Logger
	Logger = func(context.Context) *slog.Logger {
		return slog.New(slog.NewTextHandler(&logBuf, nil))
	}
	t.Cleanup(func() { Logger = original })

	c, rec := newTestContext(t)
	Respond(c, errors.New("connection to 10.0.0.5:5432 refused"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	body := decodeBody(t, rec)
	if body.Error.Message != genericMessage {
		t.Errorf("message = %q, want the generic message", body.Error.Message)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Errorf("response leaked the internal address: %s", rec.Body.String())
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "10.0.0.5") {
		t.Errorf("the real error never reached the log: %s", logged)
	}
	if !strings.Contains(logged, "test-request-id") {
		t.Errorf("the log line is not correlatable: %s", logged)
	}
}

// Test4xxIsNotLogged checks that a client's own mistake cannot fill the disk.
func Test4xxIsNotLogged(t *testing.T) {
	var logBuf bytes.Buffer
	original := Logger
	Logger = func(context.Context) *slog.Logger {
		return slog.New(slog.NewTextHandler(&logBuf, nil))
	}
	t.Cleanup(func() { Logger = original })

	c, _ := newTestContext(t)
	Respond(c, NotFound("nope"))

	if logBuf.Len() != 0 {
		t.Errorf("a 404 was logged: %s", logBuf.String())
	}
}

func TestRespondSetsRetryAfter(t *testing.T) {
	var logBuf bytes.Buffer
	original := Logger
	Logger = func(context.Context) *slog.Logger {
		return slog.New(slog.NewTextHandler(&logBuf, nil))
	}
	t.Cleanup(func() { Logger = original })

	c, rec := newTestContext(t)
	Respond(c, postgres.MapError(&pgconn.PgError{Code: "40001"}))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want %q", got, "1")
	}
}

// TestRespondClientClosedWritesNoBody: nobody is listening on a 499, and
// writing would only produce a broken-pipe error in the log.
func TestRespondClientClosedWritesNoBody(t *testing.T) {
	c, rec := newTestContext(t)
	Respond(c, context.Canceled)

	if rec.Code != StatusClientClosedRequest {
		t.Fatalf("status = %d, want %d", rec.Code, StatusClientClosedRequest)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestRespondNilIsNoOp(t *testing.T) {
	c, rec := newTestContext(t)
	Respond(c, nil)

	if rec.Body.Len() != 0 {
		t.Errorf("Respond(nil) wrote %q", rec.Body.String())
	}
	if c.IsAborted() {
		t.Error("Respond(nil) aborted the chain")
	}
}

func TestValidationFields(t *testing.T) {
	c, rec := newTestContext(t)
	Respond(c, Validation("", map[string]string{
		"sku":   "this field is required",
		"price": "must be greater than 0",
	}))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	body := decodeBody(t, rec)
	if body.Error.Code != CodeValidation {
		t.Errorf("code = %q, want %q", body.Error.Code, CodeValidation)
	}
	if body.Error.Fields["sku"] != "this field is required" {
		t.Errorf("fields = %v", body.Error.Fields)
	}
}

// TestWrapDoesNotMutateTheOriginal guards the constructors, which return
// package-level shapes that callers decorate.
func TestWrapDoesNotMutateTheOriginal(t *testing.T) {
	base := NotFound("gone")
	wrapped := base.Wrap(errors.New("cause"))

	if base.Unwrap() != nil {
		t.Error("Wrap mutated the receiver")
	}
	if wrapped.Unwrap() == nil {
		t.Error("Wrap did not attach the cause")
	}
	if wrapped.Message != base.Message {
		t.Error("Wrap changed the message")
	}
}

func TestErrorImplementsErrorInterface(t *testing.T) {
	cause := errors.New("underlying")
	e := Conflict("taken").Wrap(cause)

	if !errors.Is(e, cause) {
		t.Error("errors.Is cannot reach the cause")
	}
	if !strings.Contains(e.Error(), "underlying") {
		t.Errorf("Error() = %q, should carry the cause for logs", e.Error())
	}
}
