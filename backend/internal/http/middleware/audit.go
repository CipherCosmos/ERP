package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"gbos/internal/domain/audit"
	"gbos/internal/platform/log"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// MutationRecorder writes one trail entry. The audit service supplies it; this
// package does not talk to a database.
//
// The signature takes primitives rather than an entry type so that the
// middleware chain does not depend on the audit aggregate's shape.
type MutationRecorder interface {
	RecordRequest(ctx context.Context, userID, storeID uuid.UUID, action, table, recordID, body string) error
}

// AuditMutations records every successful mutating request made by a signed-in
// user, with a bounded, credential-redacted snapshot of the request body.
//
// Use it after RequireAuth: an unauthenticated request has nobody to attribute
// the change to, and the user and store columns are both NOT NULL.
//
// Two things about it are load-bearing.
//
// The body is redacted by key name, in the audit domain, and never by URL
// path. A path exclusion list covers a new endpoint that accepts a credential
// only from the day somebody remembers to add it; matching on the key means
// the coverage arrives with the field.
//
// A failure to write the entry does not fail the request. The change has
// already happened by the time this runs — returning an error here would
// answer 500 for a write that succeeded, which is worse than a gap in the
// trail. The gap is logged instead.
func AuditMutations(recorder MutationRecorder) gin.HandlerFunc {
	if recorder == nil {
		// Nothing wired: stay out of the chain entirely rather than paying for
		// a body copy on every request to throw it away.
		return func(c *gin.Context) { c.Next() }
	}

	return func(c *gin.Context) {
		if !auditWorthRecording(c.Request.Method) {
			c.Next()
			return
		}

		// The body has to be captured before the handler consumes it, and put
		// back so the handler still sees it.
		body := auditCaptureBody(c)

		c.Next()

		if c.Writer.Status() < http.StatusOK || c.Writer.Status() >= http.StatusMultipleChoices {
			return
		}
		session, ok := SessionFrom(c.Request.Context())
		if !ok || session.UserID == uuid.Nil {
			return
		}
		// Prefer the store the request actually acted on. On a scoped route an
		// owner may be working in a store other than the one their session was
		// issued for, and attributing the change to their home store would put
		// it in the wrong shop's trail.
		storeID := session.StoreID
		if resolved, ok := StoreFrom(c.Request.Context()); ok {
			storeID = resolved
		}
		if storeID == uuid.Nil {
			return
		}

		// The route template, not the request path: it is what distinguishes an
		// identifier from a literal segment, so the trail records one row per
		// resource rather than one per id it has ever seen.
		table, recordID, action := audit.DescribeRoute(
			c.Request.Method, c.FullPath(), c.Request.URL.Path)

		if err := recorder.RecordRequest(c.Request.Context(),
			session.UserID, storeID, action, table, recordID, body); err != nil {
			log.FromContext(c.Request.Context()).ErrorContext(c.Request.Context(),
				"audit entry was not recorded",
				"method", c.Request.Method,
				"path", c.Request.URL.Path,
				"error", err.Error())
		}
	}
}

// auditWorthRecording reports whether a method changes anything. A read is not
// a change, and recording one would bury the changes in noise.
func auditWorthRecording(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// auditCaptureBody reads a bounded prefix of a JSON request body, restores the
// body for the handler, and returns the redacted rendering.
//
// A non-JSON body is not captured at all: the trail exists to show what was
// asked for, and an uploaded file is not that.
func auditCaptureBody(c *gin.Context) string {
	if c.Request.Body == nil {
		return ""
	}
	if !strings.Contains(strings.ToLower(c.GetHeader("Content-Type")), "application/json") {
		return ""
	}

	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, audit.MaxBodyBytes))
	_ = c.Request.Body.Close()
	if err != nil {
		// The handler will fail on the truncated body and answer 4xx or 5xx,
		// at which point nothing is recorded anyway.
		c.Request.Body = io.NopCloser(bytes.NewReader(raw))
		return ""
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return audit.RedactBody(raw)
}
