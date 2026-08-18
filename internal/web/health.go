package web

import (
	"encoding/json"
	"net/http"
	"sync/atomic"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// HTTPStatus is the point-in-time host status used by HTTP probes.
type HTTPStatus struct {
	Live     bool `json:"live"`
	Ready    bool `json:"ready"`
	Degraded bool `json:"degraded,omitempty"`
}

// StatusProvider supplies the current host status without exposing internals.
type StatusProvider interface {
	HTTPStatus() HTTPStatus
}

var statusProvider atomic.Pointer[StatusProvider]

// SetHealthProvider installs the host status provider before Caddy starts.
func SetHealthProvider(provider StatusProvider) {
	statusProvider.Store(&provider)
}

// RuntimeHealth serves the runtime liveness and readiness probes.
type RuntimeHealth struct {
	Kind string `json:"kind"`
}

func (RuntimeHealth) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.zentao_runtime_health",
		New: func() caddy.Module { return new(RuntimeHealth) },
	}
}

func (h *RuntimeHealth) ServeHTTP(writer http.ResponseWriter, request *http.Request, _ caddyhttp.Handler) error {
	provider := statusProvider.Load()
	status := HTTPStatus{Live: true, Ready: false}
	if provider != nil && *provider != nil {
		status = (*provider).HTTPStatus()
	}
	healthy := status.Live
	if h.Kind == "readiness" {
		healthy = status.Ready
	}
	statusWord := "ok"
	if h.Kind == "readiness" {
		statusWord = "ready"
	}
	body := map[string]any{"status": statusWord}
	code := http.StatusOK
	if !healthy {
		code = http.StatusServiceUnavailable
		body = map[string]any{"status": "unavailable", "live": status.Live, "ready": status.Ready, "degraded": status.Degraded}
	}
	data, _ := json.Marshal(body)
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(code)
	_, _ = writer.Write(data)
	return nil
}

// Interface guards.
var (
	_ caddy.Module                = (*RuntimeHealth)(nil)
	_ caddyhttp.MiddlewareHandler = (*RuntimeHealth)(nil)
)

func init() {
	caddy.RegisterModule(RuntimeHealth{})
}
