package log

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"gbos/internal/platform/httperr"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// maxRequestIDLen bounds an inbound request id. It is echoed into every log
// record for the request, so an unbounded one is free log volume for anyone who
// can reach the server.
const maxRequestIDLen = 128

// RequestID assigns every request an id and makes it retrievable three ways:
// from the Gin context (httperr.RequestIDKey), from the standard context
// (RequestID), and from the response header (X-Request-ID) so a client can
// quote it in a bug report.
//
// It also installs a request-scoped logger on the context, so FromContext
// anywhere downstream returns a logger already tagged with the id.
//
// An inbound X-Request-ID is honoured to preserve a trace across services, but
// only after sanitising: the header is attacker-controlled and lands in the
// logs, so a value containing a newline could forge log records. Anything that
// is not a short run of [A-Za-z0-9._-] is discarded and replaced.
//
// Register it first, ahead of Logger and Recovery, so both can use the id.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := sanitizeRequestID(c.GetHeader(httperr.RequestIDHeader))
		if id == "" {
			id = uuid.NewString()
		}

		c.Set(httperr.RequestIDKey, id)
		c.Header(httperr.RequestIDHeader, id)

		ctx := WithRequestID(c.Request.Context(), id)
		ctx = WithLogger(ctx, slog.Default().With(slog.String("request_id", id)))
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

// sanitizeRequestID returns raw if it is a plausible correlation id, else "".
func sanitizeRequestID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxRequestIDLen {
		return ""
	}
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return ""
		}
	}
	return raw
}

// LoggerOptions configures the access log.
type LoggerOptions struct {
	// SkipPaths are logged at debug level instead of info. Point it at
	// /api/v1/health so that an uptime check polling every 10 seconds does not
	// bury the real traffic.
	SkipPaths []string

	// SlowRequest promotes any request at or above this duration to warn level.
	// Zero disables the check.
	SlowRequest time.Duration
}

// Logger returns the access-log middleware, replacing gin.Default()'s.
//
// One record per request with method, path, status, duration and request id.
// The level tracks the outcome — 5xx logs at error, 4xx at warn, the rest at
// info — so that "show me today's failures" is a level filter rather than a
// text search.
func Logger(logger *slog.Logger) gin.HandlerFunc {
	return LoggerWithOptions(logger, LoggerOptions{})
}

// LoggerWithOptions is Logger with path skipping and a slow-request threshold.
func LoggerWithOptions(logger *slog.Logger, opts LoggerOptions) gin.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	skip := make(map[string]struct{}, len(opts.SkipPaths))
	for _, p := range opts.SkipPaths {
		skip[p] = struct{}{}
	}

	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		rawQuery := c.Request.URL.RawQuery

		c.Next()

		duration := time.Since(start)
		status := c.Writer.Status()

		attrs := []slog.Attr{
			slog.String("request_id", c.GetString(httperr.RequestIDKey)),
			slog.String("method", c.Request.Method),
			slog.String("path", path),
			slog.Int("status", status),
			slog.Duration("duration", duration),
			slog.String("client_ip", c.ClientIP()),
			slog.Int("bytes", c.Writer.Size()),
		}
		// The matched route, e.g. /api/v1/products/:id, groups requests that
		// differ only by id. Absent for a 404.
		if route := c.FullPath(); route != "" {
			attrs = append(attrs, slog.String("route", route))
		}
		// Query strings can carry identifiers but not bodies, so they are safe
		// to log here; anything sensitive belongs in a body, not a query.
		if rawQuery != "" {
			attrs = append(attrs, slog.String("query", rawQuery))
		}
		// Errors Gin collected via c.Error(), which handlers use for context
		// they do not want in the response.
		if len(c.Errors) > 0 {
			attrs = append(attrs, slog.String("errors", c.Errors.String()))
		}

		level := slog.LevelInfo
		switch {
		case status >= http.StatusInternalServerError:
			level = slog.LevelError
		case status >= http.StatusBadRequest:
			level = slog.LevelWarn
		default:
			if _, skipped := skip[path]; skipped {
				level = slog.LevelDebug
			} else if opts.SlowRequest > 0 && duration >= opts.SlowRequest {
				level = slog.LevelWarn
				attrs = append(attrs, slog.Bool("slow", true))
			}
		}

		logger.LogAttrs(c.Request.Context(), level, "http request", attrs...)
	}
}

// Recovery turns a panic into a logged stack trace and a clean 500 in the
// httperr envelope, replacing gin.Recovery() — which prints the stack to stdout
// unstructured and, in debug mode, can leak it to the client.
//
// The response carries the request id, so the log line holding the stack can be
// found from the client's report alone.
func Recovery(logger *slog.Logger) gin.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(c *gin.Context) {
		defer func() {
			r := recover()
			if r == nil {
				return
			}

			requestID := c.GetString(httperr.RequestIDKey)

			// A broken pipe is the client hanging up mid-write, not a bug.
			// Writing a response would panic again, so record it and stop.
			if isBrokenPipe(r) {
				logger.WarnContext(c.Request.Context(), "client disconnected",
					slog.String("request_id", requestID),
					slog.String("method", c.Request.Method),
					slog.String("path", c.Request.URL.Path),
				)
				c.Abort()
				return
			}

			logger.ErrorContext(c.Request.Context(), "panic recovered",
				slog.String("request_id", requestID),
				slog.String("method", c.Request.Method),
				slog.String("path", c.Request.URL.Path),
				slog.String("client_ip", c.ClientIP()),
				slog.Any("panic", r),
				slog.String("stack", string(debug.Stack())),
			)

			httperr.Respond(c, httperr.Internal(
				"An unexpected error occurred. Please try again.").
				Wrap(panicError(r)))
		}()

		c.Next()
	}
}

// panicError turns a recovered value into an error for the envelope's cause.
// The cause is logged and never serialised, so it may carry the raw value.
func panicError(r any) error {
	if err, ok := r.(error); ok {
		return err
	}
	return fmt.Errorf("panic: %v", r)
}

// isBrokenPipe reports whether the recovered value is a write to a connection
// the peer already closed.
func isBrokenPipe(r any) bool {
	ne, ok := r.(*net.OpError)
	if !ok {
		return false
	}
	var se *os.SyscallError
	if !errors.As(ne.Err, &se) {
		return false
	}
	msg := strings.ToLower(se.Error())
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer")
}
