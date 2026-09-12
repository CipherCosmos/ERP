// Package log provides structured logging, request-id propagation, and the Gin
// middleware that replaces gin.Default().
//
// gin.Default() writes unstructured lines to stdout with no request id, no
// caller, and no way to correlate a client's report with a server error. That
// makes an incident undiagnosable: given "it failed at about 3pm" there is
// nothing to grep for. This package fixes that by giving every request an id
// that appears in the access log, in any error log the request produces, and in
// the error envelope the client receives.
//
// Wiring, in place of gin.Default():
//
//	logger := log.New(log.Options{Level: cfg.LogLevel, Format: log.FormatJSON})
//	log.Install(logger) // slog default + httperr routing
//
//	r := gin.New()
//	r.Use(log.RequestID(), log.Logger(logger), log.Recovery(logger))
package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Output formats.
const (
	// FormatJSON emits one JSON object per line, for a log shipper to parse.
	FormatJSON = "json"

	// FormatText emits key=value pairs, readable in a terminal.
	FormatText = "text"
)

// Options configures New.
type Options struct {
	// Level is one of debug, info, warn, error. Case-insensitive. Empty means
	// info.
	Level string

	// Format is FormatJSON or FormatText. Empty means FormatText, on the
	// assumption that an unconfigured process is a developer's laptop.
	Format string

	// Output defaults to os.Stdout. Logs go to stdout, not stderr: they are
	// this process's normal output, and a container runtime collects both the
	// same way.
	Output io.Writer

	// AddSource includes file:line on every record. Useful at debug level,
	// costly in a hot path because it walks the stack.
	AddSource bool

	// Service is attached to every record as service=... so that logs from
	// several processes can be told apart in one stream.
	Service string
}

// New builds a logger. An unrecognised level falls back to info rather than
// failing: a typo in LOG_LEVEL should not stop the server from booting, but it
// should be visible, so the logger reports it on its first record.
func New(opts Options) *slog.Logger {
	out := opts.Output
	if out == nil {
		out = os.Stdout
	}

	level, levelErr := ParseLevel(opts.Level)

	handlerOpts := &slog.HandlerOptions{
		Level:     level,
		AddSource: opts.AddSource,
	}

	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(opts.Format)) {
	case FormatJSON:
		handler = slog.NewJSONHandler(out, handlerOpts)
	case FormatText, "":
		handler = slog.NewTextHandler(out, handlerOpts)
	default:
		handler = slog.NewTextHandler(out, handlerOpts)
	}

	logger := slog.New(handler)
	if opts.Service != "" {
		logger = logger.With(slog.String("service", opts.Service))
	}

	if levelErr != nil {
		logger.Warn("invalid log level, defaulting to info",
			slog.String("configured", opts.Level))
	}
	return logger
}

// ParseLevel converts a level name to a slog.Level. It returns slog.LevelInfo
// along with an error for anything it does not recognise, so a caller may
// either surface the error or ignore it and take the default.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error", "err":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("log: unknown level %q", name)
	}
}

// Discard is a logger that drops everything. For tests.
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}
