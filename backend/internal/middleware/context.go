package middleware

import "github.com/gin-gonic/gin"

func GetUserID(c *gin.Context) string {
	if v, ok := c.Get("user_id"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func GetRole(c *gin.Context) string {
	if v, ok := c.Get("role"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func GetTokenStoreID(c *gin.Context) string {
	if v, ok := c.Get("token_store_id"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
