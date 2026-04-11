package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"gbos/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const maxAuditBodyBytes = 1 << 16 // 64 KiB

// AuditMutationRequests logs successful mutating API calls with a bounded JSON body snapshot.
func AuditMutationRequests() gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/api/v1/auth/login") || strings.HasPrefix(path, "/api/v1/auth/register") {
			c.Next()
			return
		}
		if path == "/api/v1/health" {
			c.Next()
			return
		}

		var bodySnap string
		ct := c.GetHeader("Content-Type")
		if strings.Contains(strings.ToLower(ct), "application/json") && c.Request.Body != nil {
			buf, err := io.ReadAll(io.LimitReader(c.Request.Body, maxAuditBodyBytes))
			_ = c.Request.Body.Close()
			if err == nil {
				bodySnap = string(buf)
				c.Request.Body = io.NopCloser(bytes.NewReader(buf))
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
		action := "api." + method + " " + path
		_, _ = db.DB.Exec(`INSERT INTO audit_logs (id, user_id, store_id, action, table_name, record_id, old_value, new_value)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.New().String(), uid, storeID, action, "http_request", "", "", bodySnap)
	}
}
