package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/vimrus/runtime/internal/observability"
)

const (
	defaultObservabilityMaxBytes  = 256 * 1024
	defaultObservabilityMaxEvents = 100
)

var observabilityIngestSink EventSink

// SetObservabilitySink registers the process-wide telemetry sink. The sink is
// not serializable into the Caddy JSON configuration, so the ingest handler
// resolves it from this registry at request time.
func SetObservabilitySink(sink EventSink) {
	observabilityIngestSink = sink
}

// EventSink accepts normalized observability events from the local PHP
// Runtime Event Client. Implementations must never block business requests
// indefinitely; failures are expected to be bounded and quick.
type EventSink interface {
	AddBatch(ctx context.Context, events []observability.Event) (int, error)
}

// ObservabilityIngest protects and serves the private PHP telemetry
// endpoint. It only listens on loopback and never exposes DuckDB, paths or
// arbitrary SQL.
type ObservabilityIngest struct {
	Token     string    `json:"token,omitempty"`
	MaxBytes  int64     `json:"maxBytes,omitempty"`
	MaxEvents int       `json:"maxEvents,omitempty"`
	Sink      EventSink `json:"-"`
}

func (ObservabilityIngest) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.zentao_observability_ingest", New: func() caddy.Module { return new(ObservabilityIngest) }}
}

func (h *ObservabilityIngest) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
	maxBytes := h.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultObservabilityMaxBytes
	}
	if r.ContentLength > maxBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	if h.Token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Zentao-Observability-Token")), []byte(h.Token)) != 1 {
		writeObservabilityError(w, http.StatusUnauthorized, "unauthenticated", "observability authentication required")
		return nil
	}
	if r.Header.Get("X-Zentao-Observability-Schema") != strconv.Itoa(observability.SchemaVersion) {
		writeObservabilityError(w, http.StatusBadRequest, "unsupported_schema", "unsupported observability schema")
		return nil
	}
	sink := h.Sink
	if sink == nil {
		sink = observabilityIngestSink
	}
	if sink == nil {
		writeObservabilityError(w, http.StatusServiceUnavailable, "unavailable", "observability pipeline is not available")
		return nil
	}

	var request struct {
		SchemaVersion int                   `json:"schemaVersion"`
		Events        []observability.Event `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeObservabilityError(w, http.StatusBadRequest, "invalid_request", "invalid observability event payload")
		return nil
	}
	if request.SchemaVersion != observability.SchemaVersion {
		writeObservabilityError(w, http.StatusBadRequest, "unsupported_schema", "unsupported observability schema")
		return nil
	}
	maxEvents := h.MaxEvents
	if maxEvents <= 0 {
		maxEvents = defaultObservabilityMaxEvents
	}
	if len(request.Events) == 0 || len(request.Events) > maxEvents {
		writeObservabilityError(w, http.StatusBadRequest, "invalid_batch", "observability batch size is out of range")
		return nil
	}

	accepted, err := sink.AddBatch(r.Context(), request.Events)
	if err != nil {
		writeObservabilityError(w, http.StatusBadRequest, "invalid_event", "observability event was rejected")
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schemaVersion": observability.SchemaVersion,
		"accepted":      accepted,
	})
	return nil
}

func writeObservabilityError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

var (
	_ caddy.Module                = (*ObservabilityIngest)(nil)
	_ caddyhttp.MiddlewareHandler = (*ObservabilityIngest)(nil)
)

func init() { caddy.RegisterModule(ObservabilityIngest{}) }
