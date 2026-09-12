package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"gbos/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const maxAuditBodyBytes = 1 << 16 // 64 KiB

// sensitiveKeys never reach the audit log. Redaction is by key name rather
// than by request path so that a new endpoint accepting a credential is
// covered the day it is written, instead of the day someone remembers to add
// it to an exclusion list.
var sensitiveKeys = map[string]bool{
	"password":         true,
	"old_password":     true,
	"new_password":     true,
	"confirm_password": true,
	"password_hash":    true,
	"token":            true,
	"refresh_token":    true,
	"secret":           true,
	"api_key":          true,
	"authorization":    true,
}

// AuditMutationRequests logs successful mutating API calls with a bounded,
// credential-redacted JSON body snapshot.
func AuditMutationRequests() gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			c.Next()
			return
		}
		if c.Request.URL.Path == "/api/v1/health" {
			c.Next()
			return
		}

		var bodySnap string
		ct := c.GetHeader("Content-Type")
		if strings.Contains(strings.ToLower(ct), "application/json") && c.Request.Body != nil {
			buf, err := io.ReadAll(io.LimitReader(c.Request.Body, maxAuditBodyBytes))
			_ = c.Request.Body.Close()
			if err == nil {
				c.Request.Body = io.NopCloser(bytes.NewReader(buf))
				bodySnap = redactBody(buf)
			}
		}

		c.Next()

		if c.Writer.Status() < 200 || c.Writer.Status() > 299 {
			return
		}
		uid := GetUserID(c)
		storeID := GetStoreID(c)
		if uid == "" || storeID == "" {
			return
		}

		if len(bodySnap) > 8000 {
			bodySnap = bodySnap[:8000] + "…(truncated)"
		}
		action := "api." + method + " " + c.Request.URL.Path
		_, _ = db.DB.Exec(`INSERT INTO audit_logs (id, user_id, store_id, action, table_name, record_id, old_value, new_value)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.New().String(), uid, storeID, action, "http_request", "", "", bodySnap)
	}
}

// redactBody returns a credential-free rendering of a JSON request body. A
// body that cannot be parsed is dropped rather than stored unexamined, since
// an unparsed body cannot be shown to be free of credentials.
func redactBody(raw []byte) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var parsed interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "[unparsed body omitted]"
	}
	out, err := json.Marshal(redactValue(parsed))
	if err != nil {
		return "[unserialisable body omitted]"
	}
	return string(out)
}

func redactValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, val := range t {
			if sensitiveKeys[strings.ToLower(k)] {
				t[k] = "[redacted]"
				continue
			}
			t[k] = redactValue(val)
		}
		return t
	case []interface{}:
		for i, val := range t {
			t[i] = redactValue(val)
		}
		return t
	default:
		return v
	}
}
