package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"xi_monitor/backend/internal/httpapi"
)

func TestServeFrontend(t *testing.T) {
	gin.SetMode(gin.TestMode)
	distDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte("<main>app shell</main>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	assetsDir := filepath.Join(distDir, "assets")
	if err := os.Mkdir(assetsDir, 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assetsDir, "app.js"), []byte("console.log('ok')"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	router := gin.New()
	httpapi.ServeFrontend(router, distDir)

	tests := []struct {
		name       string
		path       string
		statusCode int
		want       string
	}{
		{name: "root", path: "/", statusCode: http.StatusOK, want: "app shell"},
		{name: "spa route", path: "/dashboard", statusCode: http.StatusOK, want: "app shell"},
		{name: "asset", path: "/assets/app.js", statusCode: http.StatusOK, want: "console.log('ok')"},
		{name: "api not found", path: "/api/missing", statusCode: http.StatusNotFound, want: `"success":false`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.statusCode {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.statusCode, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.want) {
				t.Fatalf("body = %q, want substring %q", rec.Body.String(), tt.want)
			}
		})
	}
}
