package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gbos/internal/auth"
	"gbos/internal/db"
)

type AuthMiddleware struct{}

func NewAuthMiddleware() *AuthMiddleware {
	return &AuthMiddleware{}
}

func (m *AuthMiddleware) RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authorization header required"})
			c.Abort()
			return
		}

		tokenParts := strings.Split(authHeader, " ")
		if len(tokenParts) != 2 || tokenParts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid authorization format"})
			c.Abort()
			return
		}

		claims, err := auth.ParseToken(tokenParts[1])
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			c.Abort()
			return
		}

		headerStore := c.GetHeader("X-Store-ID")
		if headerStore == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "store ID required"})
			c.Abort()
			return
		}
		if headerStore != claims.StoreID {
			if claims.Role != "owner" {
				c.JSON(http.StatusForbidden, gin.H{"error": "store does not match session"})
				c.Abort()
				return
			}
			var n int
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM stores WHERE id = ? AND is_active = 1`, headerStore).Scan(&n); err != nil || n == 0 {
				c.JSON(http.StatusForbidden, gin.H{"error": "invalid or inactive store"})
				c.Abort()
				return
			}
		}

		c.Set("user_id", claims.UserID)
		c.Set("role", claims.Role)
		c.Set("token_store_id", claims.StoreID)
		c.Set("store_id", headerStore)
		c.Next()
	}
}

func (m *AuthMiddleware) RequireRole(allowedRoles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := GetRole(c)
		for _, r := range allowedRoles {
			if role == r {
				c.Next()
				return
			}
		}
		c.JSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
		c.Abort()
	}
}

func GetStoreID(c *gin.Context) string {
	if storeID, exists := c.Get("store_id"); exists {
		if s, ok := storeID.(string); ok {
			return s
		}
	}
	return ""
}

func GetUserRole(c *gin.Context) string {
	return GetRole(c)
}
