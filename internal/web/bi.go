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
	"github.com/vimrus/runtime/internal/bi"
)

const (
	biTokenHeader  = "X-Zentao-BI-Token"
	biSchemaHeader = "X-Zentao-BI-Schema"
	biMaxBytes     = 256 * 1024
)

type biHandlers struct {
	publish func(context.Context, bi.PublishRequest) (bi.PublishResult, error)
	query   func(context.Context, bi.QueryRequest) (bi.QueryResult, error)
}

var (
	biHandlersMu      sync.RWMutex
	biHandlersByToken = map[string]biHandlers{}
)

// SetBIHandlersFor registers the private BI publish and query handlers for one
// application token.
func SetBIHandlersFor(token string, publish func(context.Context, bi.PublishRequest) (bi.PublishResult, error), query func(context.Context, bi.QueryRequest) (bi.QueryResult, error)) {
	biHandlersMu.Lock()
	defer biHandlersMu.Unlock()
	if token == "" || publish == nil || query == nil {
		delete(biHandlersByToken, token)
		return
	}
	biHandlersByToken[token] = biHandlers{publish: publish, query: query}
}

func biHandlersFor(token string) (biHandlers, bool) {
	biHandlersMu.RLock()
	defer biHandlersMu.RUnlock()
	handlers, ok := biHandlersByToken[token]
	return handlers, ok
}

func biTokenAllowed(tokens []string, requestToken string) bool {
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

// BIPublish is the private loopback BI Manifest publication endpoint.
type BIPublish struct {
	Tokens []string `json:"tokens,omitempty"`
}

func (BIPublish) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.zentao_bi_publish", New: func() caddy.Module { return new(BIPublish) }}
}

func (h *BIPublish) ServeHTTP(writer http.ResponseWriter, request *http.Request, _ caddyhttp.Handler) error {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
	request.Body = http.MaxBytesReader(writer, request.Body, biMaxBytes)
	token := request.Header.Get(biTokenHeader)
	if !biTokenAllowed(h.Tokens, token) {
		writeBIError(writer, http.StatusUnauthorized, "unauthenticated", "BI authentication required")
		return nil
	}
	if request.Header.Get(biSchemaHeader) != "1" {
		writeBIError(writer, http.StatusBadRequest, "unsupported_schema", "unsupported BI schema")
		return nil
	}
	handlers, ok := biHandlersFor(token)
	if !ok {
		writeBIError(writer, http.StatusServiceUnavailable, "unavailable", "BI publisher is not available")
		return nil
	}
	var payload bi.PublishRequest
	if err := decodeBIRequest(request, &payload); err != nil {
		writeBIError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return nil
	}
	result, err := handlers.publish(request.Context(), payload)
	if err != nil {
		writeBIError(writer, http.StatusBadRequest, "publish_failed", err.Error())
		return nil
	}
	writeBIJSON(writer, http.StatusOK, result)
	return nil
}

// BIQuery is the private loopback BI read-only query endpoint.
type BIQuery struct {
	Tokens []string `json:"tokens,omitempty"`
}

func (BIQuery) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.zentao_bi_query", New: func() caddy.Module { return new(BIQuery) }}
}

func (h *BIQuery) ServeHTTP(writer http.ResponseWriter, request *http.Request, _ caddyhttp.Handler) error {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
	request.Body = http.MaxBytesReader(writer, request.Body, biMaxBytes)
	token := request.Header.Get(biTokenHeader)
	if !biTokenAllowed(h.Tokens, token) {
		writeBIError(writer, http.StatusUnauthorized, "unauthenticated", "BI authentication required")
		return nil
	}
	if request.Header.Get(biSchemaHeader) != "1" {
		writeBIError(writer, http.StatusBadRequest, "unsupported_schema", "unsupported BI schema")
		return nil
	}
	handlers, ok := biHandlersFor(token)
	if !ok {
		writeBIError(writer, http.StatusServiceUnavailable, "unavailable", "BI query engine is not available")
		return nil
	}
	var payload bi.QueryRequest
	if err := decodeBIRequest(request, &payload); err != nil {
		writeBIError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return nil
	}
	result, err := handlers.query(request.Context(), payload)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		} else if errors.Is(err, bi.ErrDatasetUnavailable) {
			status = http.StatusServiceUnavailable
		}
		writeBIError(writer, status, "query_failed", err.Error())
		return nil
	}
	writeBIJSON(writer, http.StatusOK, result)
	return nil
}

func decodeBIRequest(request *http.Request, target any) error {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid BI request payload")
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("BI request must contain exactly one JSON object")
	} else if !errors.Is(err, io.EOF) {
		return errors.New("BI request must contain exactly one JSON object")
	}
	return nil
}

func writeBIJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeBIError(writer http.ResponseWriter, status int, code, message string) {
	writeBIJSON(writer, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

var (
	_ caddy.Module                = (*BIPublish)(nil)
	_ caddyhttp.MiddlewareHandler = (*BIPublish)(nil)
	_ caddy.Module                = (*BIQuery)(nil)
	_ caddyhttp.MiddlewareHandler = (*BIQuery)(nil)
)

func init() {
	caddy.RegisterModule(BIPublish{})
	caddy.RegisterModule(BIQuery{})
}
