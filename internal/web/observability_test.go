package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/vimrus/runtime/internal/observability"
)

type recordingEventSink struct {
	accepted int
	err      error
}

func (s *recordingEventSink) AddBatch(_ context.Context, events []observability.Event) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	s.accepted += len(events)
	return len(events), nil
}

func newIngestRequest(body string, token string, schema string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/internal/runtime/observability/v1/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Zentao-Observability-Token", token)
	}
	if schema != "" {
		req.Header.Set("X-Zentao-Observability-Schema", schema)
	}
	return req
}

func TestObservabilityIngestRejectsMissingOrWrongToken(t *testing.T) {
	for _, token := range []string{"", "wrong"} {
		req := newIngestRequest(`{"schemaVersion":1,"events":[]}`, token, strconv.Itoa(observability.SchemaVersion))
		rec := httptest.NewRecorder()
		handler := &ObservabilityIngest{Token: "expected", Sink: &recordingEventSink{}}
		if err := handler.ServeHTTP(rec, req, caddyhttp.HandlerFunc(nil)); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q returned %d, want 401", token, rec.Code)
		}
	}
}

func TestObservabilityIngestRejectsUnsupportedSchema(t *testing.T) {
	req := newIngestRequest(`{"schemaVersion":2,"events":[]}`, "expected", "2")
	rec := httptest.NewRecorder()
	handler := &ObservabilityIngest{Token: "expected", Sink: &recordingEventSink{}}
	if err := handler.ServeHTTP(rec, req, caddyhttp.HandlerFunc(nil)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported schema returned %d, want 400", rec.Code)
	}
}

func TestObservabilityIngestAcceptsValidBatch(t *testing.T) {
	sink := &recordingEventSink{}
	body, _ := json.Marshal(map[string]any{
		"schemaVersion": observability.SchemaVersion,
		"events": []observability.Event{{
			ClusterID: "cluster-a", NodeID: "node-a", BootID: "boot-a",
			Source: "php", Kind: observability.KindLogs, Message: "hello",
		}},
	})
	req := newIngestRequest(string(body), "expected", strconv.Itoa(observability.SchemaVersion))
	rec := httptest.NewRecorder()
	handler := &ObservabilityIngest{Token: "expected", Sink: sink}
	if err := handler.ServeHTTP(rec, req, caddyhttp.HandlerFunc(nil)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("valid batch returned %d, want 200", rec.Code)
	}
	if sink.accepted != 1 {
		t.Fatalf("accepted = %d, want 1", sink.accepted)
	}
	var response struct {
		Accepted int `json:"accepted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Accepted != 1 {
		t.Fatalf("unexpected response %q: %v", rec.Body.String(), err)
	}
}

func TestObservabilityIngestRejectsInvalidEvent(t *testing.T) {
	handler := &ObservabilityIngest{Token: "expected", Sink: &recordingEventSink{err: errors.New("rejected")}}
	body, _ := json.Marshal(map[string]any{
		"schemaVersion": observability.SchemaVersion,
		"events":        []observability.Event{{}},
	})
	req := newIngestRequest(string(body), "expected", strconv.Itoa(observability.SchemaVersion))
	rec := httptest.NewRecorder()
	if err := handler.ServeHTTP(rec, req, caddyhttp.HandlerFunc(nil)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid event returned %d, want 400", rec.Code)
	}
}

func TestObservabilityIngestRejectsEmptyBatch(t *testing.T) {
	req := newIngestRequest(`{"schemaVersion":1,"events":[]}`, "expected", strconv.Itoa(observability.SchemaVersion))
	rec := httptest.NewRecorder()
	handler := &ObservabilityIngest{Token: "expected", Sink: &recordingEventSink{}}
	if err := handler.ServeHTTP(rec, req, caddyhttp.HandlerFunc(nil)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty batch returned %d, want 400", rec.Code)
	}
}

func TestObservabilityIngestFallsBackToRegisteredSink(t *testing.T) {
	sink := &recordingEventSink{}
	previous := observabilityIngestSink
	observabilityIngestSink = sink
	defer func() { observabilityIngestSink = previous }()

	body, _ := json.Marshal(map[string]any{
		"schemaVersion": observability.SchemaVersion,
		"events": []observability.Event{{
			ClusterID: "cluster-a", NodeID: "node-a", BootID: "boot-a",
			Source: "php", Kind: observability.KindLogs, Message: "hello",
		}},
	})
	req := newIngestRequest(string(body), "expected", strconv.Itoa(observability.SchemaVersion))
	rec := httptest.NewRecorder()
	handler := &ObservabilityIngest{Token: "expected"}
	if err := handler.ServeHTTP(rec, req, caddyhttp.HandlerFunc(nil)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || sink.accepted != 1 {
		t.Fatalf("registered sink not used: code=%d accepted=%d", rec.Code, sink.accepted)
	}
}

func TestAccessLogEncoderRedactsSensitiveQueryParams(t *testing.T) {
	var encoder struct {
		Format string                     `json:"format"`
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(accessLogEncoderRaw(), &encoder); err != nil {
		t.Fatal(err)
	}
	if encoder.Format != "filter" {
		t.Fatalf("encoder format = %q, want filter", encoder.Format)
	}
	field, ok := encoder.Fields["request>uri"]
	if !ok {
		t.Fatal("request>uri filter is missing")
	}
	var query struct {
		Filter  string `json:"filter"`
		Actions []struct {
			Type      string `json:"type"`
			Parameter string `json:"parameter"`
			Value     string `json:"value"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(field, &query); err != nil {
		t.Fatal(err)
	}
	if query.Filter != "query" {
		t.Fatalf("uri filter = %q, want query", query.Filter)
	}
	found := false
	for _, action := range query.Actions {
		if action.Parameter == "token" && action.Type == "replace" && action.Value == "[REDACTED]" {
			found = true
		}
	}
	if !found {
		t.Fatalf("token query action missing: %#v", query.Actions)
	}
}
