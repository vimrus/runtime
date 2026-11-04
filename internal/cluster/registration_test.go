package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProfileClientRegistrationAndEndpoint(t *testing.T) {
	endpoint, err := ProfileEndpoint("0.0.0.0:8080", "/zentao")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "http://127.0.0.1:8080/zentao/index.php?m=runtime&f=profile" {
		t.Fatalf("endpoint = %q", endpoint)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get(ProfileTokenHeader) != "token-a" ||
			request.Header.Get(ProfileSchemaHeader) != "1" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		var profile Profile
		if err := json.NewDecoder(request.Body).Decode(&profile); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		if profile.Node.NodeID != "node-a" {
			http.Error(writer, "wrong profile", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(writer).Encode(RegistrationResponse{
			SchemaVersion:     ProfileSchemaVersion,
			Status:            "ready",
			ProfileGeneration: 2,
		})
	}))
	defer server.Close()

	client := NewProfileClient(server.URL, "token-a", time.Second)
	result, err := client.Register(context.Background(), Profile{
		SchemaVersion: ProfileSchemaVersion,
		Node:          NodeProfile{NodeID: "node-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ready" || result.ProfileGeneration != 2 {
		t.Fatalf("unexpected response: %#v", result)
	}
}
