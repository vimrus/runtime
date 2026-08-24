package web

import (
	"net/http"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// QueueAuth protects the private Queue Bridge before requests reach PHP.
type QueueAuth struct {
	Token string `json:"token,omitempty"`
}

func (QueueAuth) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.zentao_queue_auth", New: func() caddy.Module { return new(QueueAuth) }}
}

func (a *QueueAuth) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
	if r.ContentLength > 64*1024 {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if a.Token == "" || r.Header.Get("X-Zentao-Queue-Token") != a.Token {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthenticated","message":"queue bridge authentication required"}}`))
		return nil
	}
	return next.ServeHTTP(w, r)
}

var (
	_ caddy.Module                = (*QueueAuth)(nil)
	_ caddyhttp.MiddlewareHandler = (*QueueAuth)(nil)
)

func init() { caddy.RegisterModule(QueueAuth{}) }
