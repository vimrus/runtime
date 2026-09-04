package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/vimrus/runtime/internal/cache"
)

func TestCacheBridgeRejectsMissingToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/internal/runtime/cache/v1/capabilities", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	(&CacheBridge{Token: "expected"}).ServeHTTP(rec, req, caddyhttp.HandlerFunc(nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestCacheBridgeCapabilities(t *testing.T) {
	service, err := cache.New(1<<20, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	previous := cacheService
	cacheService = service
	defer func() { cacheService = previous }()

	req := httptest.NewRequest(http.MethodPost, "/internal/runtime/cache/v1/capabilities", strings.NewReader(`{}`))
	req.Header.Set("X-Zentao-Cache-Token", "expected")
	req.Header.Set("X-Zentao-Cache-Schema", "1")
	rec := httptest.NewRecorder()
	(&CacheBridge{Token: "expected"}).ServeHTTP(rec, req, caddyhttp.HandlerFunc(nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"backend":"memory"`) {
		t.Fatalf("unexpected response: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOperationFromPath(t *testing.T) {
	for _, path := range []string{"/internal/runtime/cache/v1/get", "/internal/runtime/cache/v1/set", "/internal/runtime/cache/v1/delete", "/internal/runtime/cache/v1/capabilities"} {
		if operationFromPath(path) == "" {
			t.Fatalf("path %s must resolve to an operation", path)
		}
	}
	if operationFromPath("/internal/runtime/cache/v1/drop") != "" {
		t.Fatal("unsupported operation must be rejected")
	}
}
