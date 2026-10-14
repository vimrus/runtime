package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

const (
	defaultQueryMaxRows   = 200
	defaultQueryMaxBytes  = 64 * 1024
	defaultQueryMaxOffset = 100000
)

// QueryFilters mirrors the log viewer filters. All values are exact matches
// except Message (substring) and the time range.
type QueryFilters struct {
	Start     string `json:"start"`
	End       string `json:"end"`
	FromDate  string `json:"fromDate"` // YYYYMMDD local-day shortcuts
	ToDate    string `json:"toDate"`
	Node      string `json:"node"`
	Level     string `json:"level"`
	EventID   string `json:"eventID"`
	Topic     string `json:"topic"`
	Module    string `json:"module"`
	Method    string `json:"method"`
	Account   string `json:"account"`
	RequestID string `json:"requestID"`
	Message   string `json:"message"`
	// Fields carries arbitrary allowlisted `fields.*` equality filters
	// (e.g. objectType/objectID/action for integration logs).
	Fields map[string]string `json:"fields"`
	Offset int               `json:"offset"`
	Limit  int               `json:"limit"`
}

// QueryRequest is the versioned request envelope accepted by the bridge.
type QueryRequest struct {
	SchemaVersion int          `json:"schemaVersion"`
	Filters       QueryFilters `json:"filters"`
}

// QueryResponse returns normalized log events, a total count and truncation
// flag. Rows share the shape of JSONL events so the PHP viewer can render
// file mode and flush mode identically.
type QueryResponse struct {
	SchemaVersion int              `json:"schemaVersion"`
	List          []map[string]any `json:"list"`
	Total         int              `json:"total"`
	Truncated     bool             `json:"truncated"`
}

// ObservabilityQueryHandler executes one read query against the merged
// dataset. It is registered process-wide because the DuckDB engine and node
// state live outside the serializable Caddy config.
type ObservabilityQueryHandler func(ctx context.Context, request QueryRequest) (QueryResponse, error)

var (
	observabilityQueryMu      sync.RWMutex
	observabilityQueryBridge  ObservabilityQueryHandler
	observabilityQueryBridges = map[string]ObservabilityQueryHandler{}
)

// SetObservabilityQueryHandler registers the read-only query executor.
func SetObservabilityQueryHandler(handler ObservabilityQueryHandler) {
	observabilityQueryMu.Lock()
	defer observabilityQueryMu.Unlock()
	observabilityQueryBridge = handler
}

// SetObservabilityQueryHandlerFor registers the read-only query executor for
// one bridge token. Each application has its own token so that queries are
// scoped to that application's dataset.
func SetObservabilityQueryHandlerFor(token string, handler ObservabilityQueryHandler) {
	observabilityQueryMu.Lock()
	defer observabilityQueryMu.Unlock()
	if token == "" || handler == nil {
		delete(observabilityQueryBridges, token)
		return
	}
	observabilityQueryBridges[token] = handler
}

func observabilityQueryHandlerFor(token string) ObservabilityQueryHandler {
	observabilityQueryMu.RLock()
	defer observabilityQueryMu.RUnlock()
	if handler, ok := observabilityQueryBridges[token]; ok {
		return handler
	}
	return observabilityQueryBridge
}

// ObservabilityQuery protects and serves the private PHP log-viewer query
// endpoint. It only listens on loopback; queries are bounded and never accept
// arbitrary SQL or paths.
type ObservabilityQuery struct {
	Token    string   `json:"token,omitempty"`
	Tokens   []string `json:"tokens,omitempty"`
	MaxRows  int      `json:"maxRows,omitempty"`
	MaxBytes int      `json:"maxBytes,omitempty"`
}

func (ObservabilityQuery) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.zentao_observability_query", New: func() caddy.Module { return new(ObservabilityQuery) }}
}

func (h *ObservabilityQuery) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
	maxBytes := h.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultQueryMaxBytes
	}
	limitBytes := int64(maxBytes)
	if r.ContentLength > limitBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, limitBytes)

	requestToken := r.Header.Get("X-Zentao-Observability-Token")
	if !observabilityTokenAllowed(h.Token, h.Tokens, requestToken) {
		writeObservabilityError(w, http.StatusUnauthorized, "unauthenticated", "observability query authentication required")
		return nil
	}
	if r.Header.Get("X-Zentao-Observability-Schema") != "1" {
		writeObservabilityError(w, http.StatusBadRequest, "unsupported_schema", "unsupported observability schema")
		return nil
	}
	handler := observabilityQueryHandlerFor(requestToken)
	if handler == nil {
		writeObservabilityError(w, http.StatusServiceUnavailable, "unavailable", "observability query is not available")
		return nil
	}

	var request QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeObservabilityError(w, http.StatusBadRequest, "invalid_request", "invalid observability query payload")
		return nil
	}
	if request.SchemaVersion != 1 {
		writeObservabilityError(w, http.StatusBadRequest, "unsupported_schema", "unsupported observability schema")
		return nil
	}
	maxRows := h.MaxRows
	if maxRows <= 0 {
		maxRows = defaultQueryMaxRows
	}
	if request.Filters.Limit <= 0 || request.Filters.Limit > maxRows {
		request.Filters.Limit = maxRows
	}
	if request.Filters.Offset < 0 || request.Filters.Offset > defaultQueryMaxOffset {
		writeObservabilityError(w, http.StatusBadRequest, "invalid_request", "offset out of range")
		return nil
	}

	response, err := handler(r.Context(), request)
	if err != nil {
		writeObservabilityError(w, http.StatusBadRequest, "query_failed", err.Error())
		return nil
	}
	response.SchemaVersion = 1
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
	return nil
}

var (
	_ caddy.Module                = (*ObservabilityQuery)(nil)
	_ caddyhttp.MiddlewareHandler = (*ObservabilityQuery)(nil)
)

func init() { caddy.RegisterModule(ObservabilityQuery{}) }
