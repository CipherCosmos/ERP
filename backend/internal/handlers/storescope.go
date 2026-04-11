package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gbos/internal/db"
	"gbos/internal/middleware"
)

func sessionStoreID(c *gin.Context) string {
	return middleware.GetStoreID(c)
}

func sessionRole(c *gin.Context) string {
	return middleware.GetRole(c)
}

// sessionMayAccessStore is true if the active user may act on targetStoreID.
// Non-owners are limited to their session store (X-Store-ID); owners may use any active store.
func sessionMayAccessStore(c *gin.Context, targetStoreID string) bool {
	if targetStoreID == "" {
		return false
	}
	if sessionRole(c) == "owner" {
		return storeExistsActive(targetStoreID)
	}
	return targetStoreID == sessionStoreID(c)
}

func storeExistsActive(id string) bool {
	var n int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM stores WHERE id = ? AND is_active = 1`, id).Scan(&n)
	return n == 1
}

func abortStoreMismatch(c *gin.Context) {
	c.JSON(http.StatusForbidden, gin.H{"error": "store_id does not match session"})
}

// transferStoresAllowed: non-owners may only move stock between batches at the same store;
// owners may move between any two active stores.
func transferStoresAllowed(c *gin.Context, fromID, toID string) bool {
	if fromID == "" || toID == "" {
		return false
	}
	if sessionRole(c) == "owner" {
		return storeExistsActive(fromID) && storeExistsActive(toID)
	}
	return fromID == sessionStoreID(c) && toID == sessionStoreID(c)
}
