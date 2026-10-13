package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vimrus/runtime/internal/queue/bridge"
)

func TestNewRejectsNonLoopback(t *testing.T) {
	if _, err := New(Config{BaseURL: "http://example.com/internal/runtime/queue/v1", Token: "t"}); err == nil {
		t.Fatal("non-loopback base URL must be rejected")
	}
	if _, err := New(Config{BaseURL: "https://127.0.0.1/x", Token: "t"}); err == nil {
		t.Fatal("non-http scheme must be rejected")
	}
	if _, err := New(Config{BaseURL: "http://127.0.0.1/x", Token: ""}); err == nil {
		t.Fatal("missing token must be rejected")
	}
}

func TestClientSendsSchemaAndAuthAndDecodesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Zentao-Queue-Token") != "secret" {
			http.Error(writer, "missing token", http.StatusUnauthorized)
			return
		}
		if request.Header.Get("X-Zentao-Queue-Schema") != "1" {
			http.Error(writer, "missing schema", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(writer).Encode(bridge.CapabilitiesResponse{
			Schema: bridge.SchemaVersion, QueueSchema: 1, Driver: "mysql", ClaimMode: "portable_cas", MaxClaimBatch: 16,
		})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: strings.Replace(server.URL, "127.0.0.1", "localhost", 1), Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Capabilities(context.Background(), bridge.CapabilitiesRequest{Schema: bridge.SchemaVersion, NodeID: "node-a", InstanceID: "instance-a"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Driver != "mysql" || response.ClaimMode != "portable_cas" || response.MaxClaimBatch != 16 {
		t.Fatalf("unexpected capabilities: %#v", response)
	}
}

func TestClientMapsTransportFailureToUnavailable(t *testing.T) {
	client, err := New(Config{BaseURL: "http://127.0.0.1:1", Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Stats(context.Background(), bridge.StatsRequest{Schema: bridge.SchemaVersion})
	if err == nil {
		t.Fatal("expected transport error")
	}
	queueError, ok := err.(bridge.Error)
	if !ok || queueError.Code != bridge.ErrorUnavailable || !queueError.Retryable {
		t.Fatalf("expected retryable unavailable error, got %#v", err)
	}
}

func TestClientKeepsApplicationPathPrefix(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		_ = json.NewEncoder(writer).Encode(bridge.CapabilitiesResponse{
			Schema: bridge.SchemaVersion, QueueSchema: 1, Driver: "mysql", ClaimMode: "portable_cas", MaxClaimBatch: 16,
		})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL + bridge.AppBasePath("zentao"), Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Capabilities(context.Background(), bridge.CapabilitiesRequest{Schema: bridge.SchemaVersion, NodeID: "node-a", InstanceID: "instance-a"}); err != nil {
		t.Fatal(err)
	}
	if want := bridge.AppBasePath("zentao") + strings.TrimPrefix(bridge.PathCapabilities, "/internal/runtime/queue/v1"); gotPath != want {
		t.Fatalf("request path = %q, want %q", gotPath, want)
	}
}

func TestClientAcceptsLegacyFullPrefix(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		_ = json.NewEncoder(writer).Encode(bridge.StatsResponse{Schema: bridge.SchemaVersion})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL + "/internal/runtime/queue/v1", Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Stats(context.Background(), bridge.StatsRequest{Schema: bridge.SchemaVersion}); err != nil {
		t.Fatal(err)
	}
	if gotPath != bridge.PathStats {
		t.Fatalf("request path = %q, want %q", gotPath, bridge.PathStats)
	}
}
