package web

import (
	"crypto/subtle"
	"io"
	"net/http"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/vimrus/runtime/internal/cache"
)

var cacheService *cache.Cache

// SetCacheService registers the process-wide Cache Service for the private
// Cache Bridge. The cache is not serializable into Caddy JSON, so the handler
// resolves it from this registry at request time.
func SetCacheService(service *cache.Cache) {
	cacheService = service
}

// CacheBridge protects and serves the private Runtime Cache Bridge.
type CacheBridge struct {
	Token string `json:"token,omitempty"`
}

func (CacheBridge) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.zentao_cache_bridge", New: func() caddy.Module { return new(CacheBridge) }}
}

func (b *CacheBridge) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
	if r.ContentLength > cache.MaxRequestBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return nil
	}
	if b.Token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Zentao-Cache-Token")), []byte(b.Token)) != 1 {
		writeCacheError(w, http.StatusUnauthorized, "unauthenticated", "cache authentication required")
		return nil
	}
	if r.Header.Get("X-Zentao-Cache-Schema") != "1" {
		writeCacheError(w, http.StatusBadRequest, "unsupported_schema", "unsupported cache schema")
		return nil
	}
	if cacheService == nil {
		writeCacheError(w, http.StatusServiceUnavailable, "unavailable", "cache service is not available")
		return nil
	}

	operation := operationFromPath(r.URL.Path)
	if operation == "" {
		writeCacheError(w, http.StatusNotFound, "invalid_request", "unsupported cache operation")
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, cache.MaxRequestBytes+1))
	if err != nil || len(body) > cache.MaxRequestBytes {
		writeCacheError(w, http.StatusBadRequest, "request_too_large", "cache request too large")
		return nil
	}
	response, err := cache.Process(cacheService, operation, body)
	if err != nil {
		writeCacheError(w, http.StatusBadRequest, "invalid_request", "invalid cache request")
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response)
	return nil
}

func operationFromPath(path string) string {
	const prefix = "/internal/runtime/cache/v1/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	switch strings.TrimPrefix(path, prefix) {
	case "capabilities", "get", "set", "delete":
		return strings.TrimPrefix(path, prefix)
	default:
		return ""
	}
}

func writeCacheError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"schema":1,"error":{"code":"` + code + `","message":"` + message + `"}}`))
}

var (
	_ caddy.Module                = (*CacheBridge)(nil)
	_ caddyhttp.MiddlewareHandler = (*CacheBridge)(nil)
)

func init() { caddy.RegisterModule(CacheBridge{}) }
