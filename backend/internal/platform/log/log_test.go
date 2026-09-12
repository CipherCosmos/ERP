package log

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gbos/internal/platform/httperr"

	"github.com/gin-gonic/gin"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"debug", slog.LevelDebug, false},
		{"DEBUG", slog.LevelDebug, false},
		{"  info  ", slog.LevelInfo, false},
		{"", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"err", slog.LevelError, false},
		{"verbose", slog.LevelInfo, true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseLevel(tt.in)
			if got != tt.want {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseLevel(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
		})
	}
}

func TestNewJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Options{Level: "info", Format: FormatJSON, Output: &buf, Service: "gbos-api"})

	logger.Info("hello", slog.String("k", "v"))

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("output is not JSON: %q: %v", buf.String(), err)
	}
	if record["msg"] != "hello" {
		t.Errorf("msg = %v", record["msg"])
	}
	if record["service"] != "gbos-api" {
		t.Errorf("service = %v, want it on every record", record["service"])
	}
	if record["k"] != "v" {
		t.Errorf("attr k = %v", record["k"])
	}
}

func TestNewRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Options{Level: "warn", Format: FormatJSON, Output: &buf})

	logger.Debug("debug")
	logger.Info("info")
	if buf.Len() != 0 {
		t.Fatalf("records below the configured level were emitted: %s", buf.String())
	}

	logger.Warn("warn")
	if !strings.Contains(buf.String(), "warn") {
		t.Errorf("warn record missing: %s", buf.String())
	}
}

// TestNewReportsBadLevel checks that a typo in LOG_LEVEL is visible rather than
// silently downgrading the process to info.
func TestNewReportsBadLevel(t *testing.T) {
	var buf bytes.Buffer
	New(Options{Level: "trace", Format: FormatText, Output: &buf})

	if !strings.Contains(buf.String(), "invalid log level") {
		t.Errorf("no warning for an unknown level: %q", buf.String())
	}
}

func TestSanitizeRequestID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain uuid", "0f7c1b2e-6a3d-4f5e-8b9c-1d2e3f4a5b6c", "0f7c1b2e-6a3d-4f5e-8b9c-1d2e3f4a5b6c"},
		{"alphanumeric", "abc123", "abc123"},
		{"underscore and dot", "svc_1.2", "svc_1.2"},
		{"trimmed", "  abc  ", "abc"},
		{"empty", "", ""},
		{"newline injection", "abc\ndef", ""},
		{"carriage return", "abc\r\nlevel=ERROR msg=fake", ""},
		{"space", "abc def", ""},
		{"quotes", `abc"def`, ""},
		{"too long", strings.Repeat("a", maxRequestIDLen+1), ""},
		{"at limit", strings.Repeat("a", maxRequestIDLen), strings.Repeat("a", maxRequestIDLen)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeRequestID(tt.in); got != tt.want {
				t.Errorf("sanitizeRequestID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// newRouter wires the middleware the way main is expected to.
func newRouter(logger *slog.Logger) *gin.Engine {
	r := gin.New()
	r.Use(RequestID(), Logger(logger), Recovery(logger))
	return r
}

func TestRequestIDGenerated(t *testing.T) {
	r := newRouter(Discard())

	var fromCtx, fromGin string
	r.GET("/x", func(c *gin.Context) {
		fromGin = c.GetString(httperr.RequestIDKey)
		fromCtx = RequestIDFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	header := rec.Header().Get(httperr.RequestIDHeader)
	if header == "" {
		t.Fatal("no X-Request-ID on the response")
	}
	if fromGin != header {
		t.Errorf("gin context id %q != response header %q", fromGin, header)
	}
	if fromCtx != header {
		t.Errorf("std context id %q != response header %q", fromCtx, header)
	}
}

func TestRequestIDHonoursInboundHeader(t *testing.T) {
	r := newRouter(Discard())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(httperr.RequestIDHeader, "upstream-trace-1")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get(httperr.RequestIDHeader); got != "upstream-trace-1" {
		t.Errorf("request id = %q, want the inbound value preserved", got)
	}
}

// TestRequestIDRejectsForgedHeader checks that an attacker cannot inject log
// records through the one header we echo into every line.
func TestRequestIDRejectsForgedHeader(t *testing.T) {
	r := newRouter(Discard())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	// http.Header.Set would reject a literal newline, so set it directly the
	// way a raw client could.
	req.Header[httperr.RequestIDHeader] = []string{"aaa\nlevel=ERROR msg=\"forged\""}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	got := rec.Header().Get(httperr.RequestIDHeader)
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("request id %q still contains a line break", got)
	}
	if got == "" {
		t.Fatal("no replacement id was generated")
	}
}

func TestAccessLogRecord(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Options{Level: "debug", Format: FormatJSON, Output: &buf})

	r := newRouter(logger)
	r.GET("/api/v1/products/:id", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/products/42?full=1", nil))

	record := lastRecord(t, &buf)
	if record["msg"] != "http request" {
		t.Fatalf("msg = %v", record["msg"])
	}
	for key, want := range map[string]any{
		"method": "GET",
		"path":   "/api/v1/products/42",
		"status": float64(http.StatusOK),
		"route":  "/api/v1/products/:id",
		"query":  "full=1",
	} {
		if record[key] != want {
			t.Errorf("%s = %v, want %v", key, record[key], want)
		}
	}
	if record["request_id"] == "" || record["request_id"] == nil {
		t.Error("access log record has no request id")
	}
	if _, ok := record["duration"]; !ok {
		t.Error("access log record has no duration")
	}
}

func TestAccessLogLevelTracksStatus(t *testing.T) {
	tests := []struct {
		status    int
		wantLevel string
	}{
		{http.StatusOK, "INFO"},
		{http.StatusNotFound, "WARN"},
		{http.StatusInternalServerError, "ERROR"},
	}

	for _, tt := range tests {
		t.Run(tt.wantLevel, func(t *testing.T) {
			var buf bytes.Buffer
			logger := New(Options{Level: "debug", Format: FormatJSON, Output: &buf})

			r := newRouter(logger)
			r.GET("/x", func(c *gin.Context) { c.Status(tt.status) })

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

			if got := lastRecord(t, &buf)["level"]; got != tt.wantLevel {
				t.Errorf("level = %v, want %v", got, tt.wantLevel)
			}
		})
	}
}

func TestAccessLogSkipPaths(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Options{Level: "info", Format: FormatJSON, Output: &buf})

	r := gin.New()
	r.Use(RequestID(), LoggerWithOptions(logger, LoggerOptions{SkipPaths: []string{"/health"}}))
	r.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	// The record is emitted at debug, which this info-level logger drops.
	if buf.Len() != 0 {
		t.Errorf("a skipped path was logged at info: %s", buf.String())
	}
}

// TestRecoveryReturnsCleanEnvelope is the replacement for gin.Recovery(), which
// prints an unstructured stack to stdout and can leak it to the client.
func TestRecoveryReturnsCleanEnvelope(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Options{Level: "debug", Format: FormatJSON, Output: &buf})
	Install(logger)
	t.Cleanup(func() { httperr.Logger = func(context.Context) *slog.Logger { return slog.Default() } })

	r := newRouter(logger)
	r.GET("/boom", func(c *gin.Context) {
		var m map[string]string
		m["nil"] = "write" // panics
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	var body httperr.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not the error envelope: %q", rec.Body.String())
	}
	if body.Error.Code != httperr.CodeInternal {
		t.Errorf("code = %q, want %q", body.Error.Code, httperr.CodeInternal)
	}
	if body.Error.RequestID == "" {
		t.Error("panic response carries no request id to correlate with the log")
	}

	// The stack belongs in the log, never in the response.
	if strings.Contains(rec.Body.String(), "goroutine") ||
		strings.Contains(rec.Body.String(), "middleware.go") {
		t.Errorf("the panic stack leaked to the client: %s", rec.Body.String())
	}

	logged := buf.String()
	if !strings.Contains(logged, "panic recovered") {
		t.Fatalf("the panic was not logged: %s", logged)
	}
	if !strings.Contains(logged, "goroutine") {
		t.Error("the log record carries no stack trace")
	}
	if !strings.Contains(logged, body.Error.RequestID) {
		t.Error("the log record cannot be found from the client's request id")
	}
}

func TestRecoveryLetsNormalRequestsThrough(t *testing.T) {
	r := newRouter(Discard())
	r.GET("/ok", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ok", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestFromContext(t *testing.T) {
	// A bare context still yields a usable logger rather than nil.
	if FromContext(context.Background()) == nil {
		t.Fatal("FromContext(background) = nil")
	}

	var buf bytes.Buffer
	logger := New(Options{Level: "info", Format: FormatJSON, Output: &buf})
	ctx := WithLogger(context.Background(), logger)

	if FromContext(ctx) != logger {
		t.Error("FromContext did not return the installed logger")
	}
}

// TestFromContextFallsBackToRequestID covers code that has the id but not the
// logger, e.g. a value re-derived from a background context.
func TestFromContextFallsBackToRequestID(t *testing.T) {
	var buf bytes.Buffer
	slog.SetDefault(New(Options{Level: "info", Format: FormatJSON, Output: &buf}))

	ctx := WithRequestID(context.Background(), "abc-123")
	FromContext(ctx).Info("something happened")

	if !strings.Contains(buf.String(), "abc-123") {
		t.Errorf("request id was not attached: %s", buf.String())
	}
}

func TestWithLoggerNil(t *testing.T) {
	ctx := WithLogger(context.Background(), nil)
	if FromContext(ctx) == nil {
		t.Error("a nil logger poisoned the context")
	}
}

// TestInstallRoutesHTTPErrLogging checks the wiring that makes an error
// envelope and its log line share a request id.
func TestInstallRoutesHTTPErrLogging(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Options{Level: "debug", Format: FormatJSON, Output: &buf})
	Install(logger)
	t.Cleanup(func() { httperr.Logger = func(context.Context) *slog.Logger { return slog.Default() } })

	r := newRouter(logger)
	r.GET("/fail", func(c *gin.Context) {
		httperr.Respond(c, errors.New("internal detail nobody should see"))
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fail", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "internal detail") {
		t.Errorf("the cause leaked to the client: %s", rec.Body.String())
	}

	requestID := rec.Header().Get(httperr.RequestIDHeader)
	logged := buf.String()
	if !strings.Contains(logged, "internal detail") {
		t.Errorf("the cause never reached the log: %s", logged)
	}
	if !strings.Contains(logged, requestID) {
		t.Errorf("the log line is not correlatable with %q: %s", requestID, logged)
	}
}

// lastRecord decodes the final JSON record in buf.
func lastRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("no log records were written")
	}
	var record map[string]any
	last := lines[len(lines)-1]
	if err := json.Unmarshal([]byte(last), &record); err != nil {
		t.Fatalf("record %q is not JSON: %v", last, err)
	}
	return record
}
