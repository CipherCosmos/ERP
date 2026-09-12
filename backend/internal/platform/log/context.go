package log

import (
	"context"
	"log/slog"

	"gbos/internal/platform/httperr"
)

type (
	requestIDKey struct{}
	loggerKey    struct{}
)

// Install makes logger the process default and routes httperr's 5xx logging
// through the request-scoped logger, so an error envelope and its log line
// carry the same request id.
//
// Call it once at startup, after New.
func Install(logger *slog.Logger) {
	slog.SetDefault(logger)
	httperr.Logger = FromContext
}

// WithRequestID returns a context carrying id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext returns the request id on ctx, or "" if there is none.
// It is named for the context rather than called RequestID because RequestID is
// the Gin middleware that puts the id there in the first place.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// WithLogger returns a context carrying logger.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey{}, logger)
}

// FromContext returns the request-scoped logger the RequestID middleware
// installed, already tagged with the request id.
//
// It falls back to slog.Default() so that code called outside a request — a
// startup task, a background job, a test — still logs somewhere rather than
// panicking on a nil logger.
func FromContext(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if logger, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
			return logger
		}
		// No request-scoped logger, but there may still be a request id.
		if id := RequestIDFromContext(ctx); id != "" {
			return slog.Default().With(slog.String("request_id", id))
		}
	}
	return slog.Default()
}
