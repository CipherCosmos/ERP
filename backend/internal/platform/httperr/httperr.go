// Package httperr is the single place an error becomes an HTTP response.
//
// The rule it exists to enforce: a driver error never reaches a client. The
// application currently answers failures with
//
//	c.JSON(500, gin.H{"error": err.Error()})
//
// which hands the caller the table name, the column name and the constraint
// that failed. Every handler should instead end with httperr.Respond(c, err),
// which picks a status from the error's kind, writes a fixed envelope, and
// sends the real error to the log keyed by request id.
//
// The package is intentionally boring. It has three moving parts: the Error
// type, the sentinel-to-status table in statusFor, and Respond.
package httperr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"gbos/internal/platform/postgres"

	"github.com/gin-gonic/gin"
)

// RequestIDKey is the gin.Context key holding the current request id.
// internal/platform/log sets it; Respond reads it to stamp the envelope.
//
// It is declared here rather than in the log package so that this package
// depends on nothing but gin, and log can import it without a cycle.
const RequestIDKey = "request_id"

// RequestIDHeader is the inbound and outbound header carrying the request id.
const RequestIDHeader = "X-Request-ID"

// Machine-readable codes. Clients switch on these; they are part of the API
// contract and must stay stable even if the human message is reworded.
const (
	CodeBadRequest    = "bad_request"
	CodeValidation    = "validation_failed"
	CodeUnauthorized  = "unauthorized"
	CodeForbidden     = "forbidden"
	CodeNotFound      = "not_found"
	CodeConflict      = "conflict"
	CodeUnprocessable = "unprocessable_entity"
	CodeRateLimited   = "rate_limited"
	CodeTimeout       = "timeout"
	CodeUnavailable   = "service_unavailable"
	CodeInternal      = "internal_error"
)

// genericMessage is what a client sees when we do not recognise the error. It
// says nothing, on purpose.
const genericMessage = "An unexpected error occurred. Please try again."

// Logger returns the logger Respond uses for 5xx responses. Replace it to route
// through a request-scoped logger:
//
//	httperr.Logger = log.FromContext
//
// internal/platform/log.Install does exactly that.
var Logger = func(ctx context.Context) *slog.Logger { return slog.Default() }

// Response is the body of every error the API returns.
//
//	{"error": {"code": "conflict",
//	           "message": "A product with that SKU already exists.",
//	           "request_id": "0f7c..."}}
type Response struct {
	Error Body `json:"error"`
}

// Body carries the parts a client may act on.
type Body struct {
	// Code is stable and machine-readable.
	Code string `json:"code"`

	// Message is safe to display to an end user. It never contains a driver
	// message, a SQL fragment, a table name or a constraint name.
	Message string `json:"message"`

	// RequestID correlates this response with the server log.
	RequestID string `json:"request_id,omitempty"`

	// Fields maps a request field to the reason it was rejected. Populated for
	// validation failures only.
	Fields map[string]string `json:"fields,omitempty"`
}

// Error is an error that already knows its HTTP representation. Handlers and
// services return it when they have a specific message worth showing.
type Error struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string

	// err is the underlying cause. It is logged, never serialised.
	err error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	s := fmt.Sprintf("%s (%d): %s", e.Code, e.Status, e.Message)
	if e.err != nil {
		s += ": " + e.err.Error()
	}
	return s
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// Wrap attaches an underlying cause for the log without changing what the
// client sees.
func (e *Error) Wrap(err error) *Error {
	if e == nil {
		return nil
	}
	clone := *e
	clone.err = err
	return &clone
}

// WithFields attaches per-field validation reasons.
func (e *Error) WithFields(fields map[string]string) *Error {
	if e == nil {
		return nil
	}
	clone := *e
	clone.Fields = fields
	return &clone
}

// New builds an Error with an explicit status and code.
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Constructors for the statuses handlers actually produce. The message is
// supplied by the caller because only the caller knows the domain wording.
func BadRequest(msg string) *Error   { return New(http.StatusBadRequest, CodeBadRequest, msg) }
func Unauthorized(msg string) *Error { return New(http.StatusUnauthorized, CodeUnauthorized, msg) }
func Forbidden(msg string) *Error    { return New(http.StatusForbidden, CodeForbidden, msg) }
func NotFound(msg string) *Error     { return New(http.StatusNotFound, CodeNotFound, msg) }
func Conflict(msg string) *Error     { return New(http.StatusConflict, CodeConflict, msg) }
func RateLimited(msg string) *Error  { return New(http.StatusTooManyRequests, CodeRateLimited, msg) }
func Internal(msg string) *Error     { return New(http.StatusInternalServerError, CodeInternal, msg) }

// Unprocessable reports a request that parsed correctly but asks for something
// the domain will not do.
func Unprocessable(msg string) *Error {
	return New(http.StatusUnprocessableEntity, CodeUnprocessable, msg)
}

// Validation reports malformed or missing input, with per-field reasons.
//
// 400 rather than 422: the request could not be understood as a well-formed
// instance of what the endpoint accepts — a missing required field, a string
// where a number belongs. Compare Unprocessable, which is for a request that
// was understood and refused.
func Validation(msg string, fields map[string]string) *Error {
	if msg == "" {
		msg = "The request contains invalid fields."
	}
	return &Error{
		Status:  http.StatusBadRequest,
		Code:    CodeValidation,
		Message: msg,
		Fields:  fields,
	}
}

// From converts any error into an *Error.
//
// Resolution order:
//
//  1. an *Error anywhere in the chain is used as-is;
//  2. a postgres sentinel maps through statusFor;
//  3. anything else becomes a 500 with genericMessage, preserving the cause for
//     the log.
func From(err error) *Error {
	if err == nil {
		return nil
	}

	var httpErr *Error
	if errors.As(err, &httpErr) {
		return httpErr
	}

	if mapped := statusFor(err); mapped != nil {
		return mapped.Wrap(err)
	}

	return Internal(genericMessage).Wrap(err)
}

// statusFor is the sentinel-to-status table. It returns nil for an error it
// does not recognise, which From turns into a 500.
//
// Messages here are deliberately generic. A caller that can say something
// better should test the sentinel itself and return its own *Error — see
// postgres.ConstraintName.
func statusFor(err error) *Error {
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		return NotFound("The requested record was not found.")

	case errors.Is(err, postgres.ErrConflict):
		// 409: the write lost a race with an existing row.
		return Conflict("A record with these details already exists.")

	case errors.Is(err, postgres.ErrReference):
		// 422 rather than 404 or 409. A foreign key violation is ambiguous from
		// the SQLSTATE alone — it is either "the parent you named does not
		// exist" or "children still point at the row you are deleting". Both
		// are well-formed requests that the current data will not support,
		// which is exactly what 422 means. A caller with the context to tell
		// the two apart should return its own 404 or 409.
		return Unprocessable("A referenced record is missing or still in use.")

	case errors.Is(err, postgres.ErrConstraint):
		// 422 rather than 400. Gin has already bound and validated the body, so
		// the request was syntactically fine; it failed a rule the schema
		// encodes (a CHECK such as qty >= 0, or a NOT NULL column). 400 is for
		// input we could not parse, 422 for input we parsed and refused.
		return Unprocessable("The request was rejected by a data validation rule.")

	case errors.Is(err, postgres.ErrSerialization):
		// 503 with Retry-After: the request is fine and would likely succeed if
		// repeated. Respond sets the header.
		return New(http.StatusServiceUnavailable, CodeUnavailable,
			"The server is busy. Please retry.")

	case errors.Is(err, postgres.ErrLockTimeout):
		return New(http.StatusServiceUnavailable, CodeUnavailable,
			"That record is being modified. Please retry.")

	case errors.Is(err, postgres.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return New(http.StatusGatewayTimeout, CodeTimeout,
			"The request took too long to complete.")

	case errors.Is(err, postgres.ErrCanceled), errors.Is(err, context.Canceled):
		// 499 is nginx's client-closed-request. Nobody is listening, so the
		// status only matters in the access log.
		return New(StatusClientClosedRequest, CodeTimeout, "The request was cancelled.")

	case errors.Is(err, postgres.ErrInvalidInput):
		return BadRequest("A supplied value is not valid.")

	case errors.Is(err, postgres.ErrUnavailable):
		return New(http.StatusServiceUnavailable, CodeUnavailable,
			"The service is temporarily unavailable.")

	case errors.Is(err, postgres.ErrInsufficientPrivilege):
		// An operator misconfiguration, not a client problem. 500 with the
		// generic message; the log carries the truth.
		return Internal(genericMessage)
	}
	return nil
}

// StatusClientClosedRequest is nginx's non-standard 499.
const StatusClientClosedRequest = 499

// Respond writes err as the API error envelope and aborts the handler chain.
//
// It is the only function a handler needs:
//
//	if err := svc.Create(ctx, in); err != nil {
//	    httperr.Respond(c, err)
//	    return
//	}
//
// 5xx responses are logged at error level with the request id and the full
// cause; 4xx are not logged, because they are the client's problem and logging
// them lets a caller fill the disk.
func Respond(c *gin.Context, err error) {
	if err == nil {
		return
	}
	e := From(err)
	requestID := c.GetString(RequestIDKey)

	if e.Status >= http.StatusInternalServerError {
		cause := e.Unwrap()
		if cause == nil {
			cause = err
		}
		Logger(c.Request.Context()).ErrorContext(c.Request.Context(),
			"request failed",
			slog.String("request_id", requestID),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", e.Status),
			slog.String("code", e.Code),
			slog.String("error", cause.Error()),
			slog.String("sqlstate", postgres.SQLState(err)),
			slog.String("constraint", postgres.ConstraintName(err)),
		)
	}

	if e.Status == http.StatusServiceUnavailable {
		c.Header("Retry-After", "1")
	}

	// 499 means the client is gone; writing a body is pointless and Gin would
	// log a broken pipe. Record the status and stop.
	if e.Status == StatusClientClosedRequest {
		c.AbortWithStatus(e.Status)
		return
	}

	c.AbortWithStatusJSON(e.Status, Response{Error: Body{
		Code:      e.Code,
		Message:   e.Message,
		RequestID: requestID,
		Fields:    e.Fields,
	}})
}

// Status reports the HTTP status err would produce, without writing anything.
// Useful in tests and in middleware that needs to branch before responding.
func Status(err error) int {
	if err == nil {
		return http.StatusOK
	}
	return From(err).Status
}
