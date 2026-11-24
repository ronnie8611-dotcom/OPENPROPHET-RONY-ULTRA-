package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"prophet-trader/config"

	"github.com/gin-gonic/gin"
)

func newAuthTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/protected", func(c *gin.Context) {
		if !RequireAdminToken(c) {
			c.Abort()
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

func TestRequireAdminToken(t *testing.T) {
	old := config.AppConfig
	config.AppConfig = &config.Config{AdminToken: "test-admin-token"}
	t.Cleanup(func() { config.AppConfig = old })

	r := newAuthTestRouter()

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"missing header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic test-admin-token", http.StatusUnauthorized},
		{"wrong token", "Bearer NOPE", http.StatusUnauthorized},
		{"correct token", "Bearer test-admin-token", http.StatusOK},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPut, "/protected", nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Fatalf("%s: status = %d, want %d", c.name, w.Code, c.want)
		}
	}
}

func TestRequireAdminTokenFailsClosedWhenUnset(t *testing.T) {
	old := config.AppConfig
	config.AppConfig = &config.Config{AdminToken: ""}
	t.Cleanup(func() { config.AppConfig = old })

	req := httptest.NewRequest(http.MethodPut, "/protected", nil)
	req.Header.Set("Authorization", "Bearer anything")
	w := httptest.NewRecorder()
	newAuthTestRouter().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("empty ADMIN_TOKEN must fail closed with 403, got %d", w.Code)
	}
}
