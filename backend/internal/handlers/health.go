package handlers

import (
	"context"
	"net/http"
	"time"

	"gbos/internal/db"
	"github.com/gin-gonic/gin"
)

func Health(c *gin.Context) {
	status := "ok"
	dbStatus := "up"
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if db.DB == nil {
		dbStatus = "down"
		status = "degraded"
	} else if err := db.DB.PingContext(ctx); err != nil {
		dbStatus = "down"
		status = "degraded"
	}
	c.JSON(http.StatusOK, gin.H{
		"status":   status,
		"database": dbStatus,
		"service":  "gbos-api",
	})
}
