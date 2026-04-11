package handlers

import (
	"gbos/internal/db"
	"gbos/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func auditAction(c *gin.Context, action, tableName, recordID, oldVal, newVal string) {
	uid := middleware.GetUserID(c)
	storeID := middleware.GetStoreID(c)
	if uid == "" || storeID == "" {
		return
	}
	_, _ = db.DB.Exec(`INSERT INTO audit_logs (id, user_id, store_id, action, table_name, record_id, old_value, new_value)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.New().String(), uid, storeID, action, tableName, recordID, oldVal, newVal)
}
