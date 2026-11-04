package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/vimrus/runtime/internal/olap"
)

const (
	olapTokenHeader  = "X-Zentao-OLAP-Token"
	olapSchemaHeader = "X-Zentao-OLAP-Schema"
	olapMaxBytes     = 256 * 1024
)

type olapHandlers struct {
	publish func(context.Context, olap.PublishRequest) (olap.PublishResult, error)
	query   func(context.Context, olap.QueryRequest) (olap.QueryResult, error)
}

var (
	olapHandlersMu      sync.RWMutex
	olapHandlersByToken = map[string]olapHandlers{}
)

// SetOLAPHandlersFor registers the private OLAP publish and query handlers for one
// application token.
func SetOLAPHandlersFor(token string, publish func(context.Context, olap.PublishRequest) (olap.PublishResult, error), query func(context.Context, olap.QueryRequest) (olap.QueryResult, error)) {
	olapHandlersMu.Lock()
	defer olapHandlersMu.Unlock()
	if token == "" || publish == nil || query == nil {
		delete(olapHandlersByToken, token)
		return
	}
	olapHandlersByToken[token] = olapHandlers{publish: publish, query: query}
}

func olapHandlersFor(token string) (olapHandlers, bool) {
	olapHandlersMu.RLock()
	defer olapHandlersMu.RUnlock()
	handlers, ok := olapHandlersByToken[token]
	return handlers, ok
}

func olapTokenAllowed(tokens []string, requestToken string) bool {
	if requestToken == "" {
		return false
	}
	for _, token := range tokens {
		if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(requestToken)) == 1 {
			return true
		}
	}
	return false
}

// OLAPPublish is the private loopback OLAP Manifest publication endpoint.
type OLAPPublish struct {
	Tokens []string `json:"tokens,omitempty"`
}

func (OLAPPublish) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.zentao_olap_publish", New: func() caddy.Module { return new(OLAPPublish) }}
}

func (h *OLAPPublish) ServeHTTP(writer http.ResponseWriter, request *http.Request, _ caddyhttp.Handler) error {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
	request.Body = http.MaxBytesReader(writer, request.Body, olapMaxBytes)
	token := request.Header.Get(olapTokenHeader)
	if !olapTokenAllowed(h.Tokens, token) {
		writeOLAPError(writer, http.StatusUnauthorized, "unauthenticated", "OLAP authentication required")
		return nil
	}
	if request.Header.Get(olapSchemaHeader) != "1" {
		writeOLAPError(writer, http.StatusBadRequest, "unsupported_schema", "unsupported OLAP schema")
		return nil
	}
	handlers, ok := olapHandlersFor(token)
	if !ok {
		writeOLAPError(writer, http.StatusServiceUnavailable, "unavailable", "OLAP publisher is not available")
		return nil
	}
	var payload olap.PublishRequest
	if err := decodeOLAPRequest(request, &payload); err != nil {
		writeOLAPError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return nil
	}
	result, err := handlers.publish(request.Context(), payload)
	if err != nil {
		writeOLAPError(writer, http.StatusBadRequest, "publish_failed", err.Error())
		return nil
	}
	writeOLAPJSON(writer, http.StatusOK, result)
	return nil
}

// OLAPQuery is the private loopback OLAP read-only query endpoint.
type OLAPQuery struct {
	Tokens []string `json:"tokens,omitempty"`
}

func (OLAPQuery) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.zentao_olap_query", New: func() caddy.Module { return new(OLAPQuery) }}
}

func (h *OLAPQuery) ServeHTTP(writer http.ResponseWriter, request *http.Request, _ caddyhttp.Handler) error {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
	request.Body = http.MaxBytesReader(writer, request.Body, olapMaxBytes)
	token := request.Header.Get(olapTokenHeader)
	if !olapTokenAllowed(h.Tokens, token) {
		writeOLAPError(writer, http.StatusUnauthorized, "unauthenticated", "OLAP authentication required")
		return nil
	}
	if request.Header.Get(olapSchemaHeader) != "1" {
		writeOLAPError(writer, http.StatusBadRequest, "unsupported_schema", "unsupported OLAP schema")
		return nil
	}
	handlers, ok := olapHandlersFor(token)
	if !ok {
		writeOLAPError(writer, http.StatusServiceUnavailable, "unavailable", "OLAP query engine is not available")
		return nil
	}
	var payload olap.QueryRequest
	if err := decodeOLAPRequest(request, &payload); err != nil {
		writeOLAPError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return nil
	}
	result, err := handlers.query(request.Context(), payload)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		} else if errors.Is(err, olap.ErrDatasetUnavailable) {
			status = http.StatusServiceUnavailable
		}
		writeOLAPError(writer, status, "query_failed", err.Error())
		return nil
	}
	writeOLAPJSON(writer, http.StatusOK, result)
	return nil
}

func decodeOLAPRequest(request *http.Request, target any) error {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid OLAP request payload")
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("OLAP request must contain exactly one JSON object")
	} else if !errors.Is(err, io.EOF) {
		return errors.New("OLAP request must contain exactly one JSON object")
	}
	return nil
}

func writeOLAPJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeOLAPError(writer http.ResponseWriter, status int, code, message string) {
	writeOLAPJSON(writer, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

var (
	_ caddy.Module                = (*OLAPPublish)(nil)
	_ caddyhttp.MiddlewareHandler = (*OLAPPublish)(nil)
	_ caddy.Module                = (*OLAPQuery)(nil)
	_ caddyhttp.MiddlewareHandler = (*OLAPQuery)(nil)
)

func init() {
	caddy.RegisterModule(OLAPPublish{})
	caddy.RegisterModule(OLAPQuery{})
}
