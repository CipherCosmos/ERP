package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCreditList_RequiresStoreID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/credit", nil)

	h := NewCreditHandler()
	h.List(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d", w.Code)
	}
}
