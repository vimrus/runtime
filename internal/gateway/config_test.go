package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestConfigUsesLeastConnWithoutAutomaticRetry(t *testing.T) {
	config, err := Config(Options{
		Listen: ":80",
		Nodes: []Node{
			{ID: "node-a", Address: "192.168.1.101:8080"},
			{ID: "node-b", Address: "192.168.1.102:8080"},
		},
		HealthPath:      "/_runtime/readiness",
		HealthInterval:  5 * time.Second,
		HealthTimeout:   2 * time.Second,
		UnhealthyStatus: 503,
		MaxFails:        3,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(data)
	if !strings.Contains(rendered, "least_conn") {
		t.Fatalf("gateway must use least_conn policy: %s", rendered)
	}
	var decoded struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Handle []struct {
							LoadBalancing map[string]any `json:"load_balancing"`
						} `json:"handle"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	loadBalancing := decoded.Apps.HTTP.Servers["gateway"].Routes[0].Handle[0].LoadBalancing
	if retries, ok := loadBalancing["retries"]; ok && retries != float64(0) {
		t.Fatalf("gateway must not retry automatically: %v", loadBalancing)
	}
	if !strings.Contains(rendered, "/_runtime/readiness") {
		t.Fatalf("gateway must probe readiness: %s", rendered)
	}
	if !strings.Contains(rendered, "192.168.1.101:8080") || !strings.Contains(rendered, "192.168.1.102:8080") {
		t.Fatalf("gateway must include both nodes: %s", rendered)
	}
}

func TestConfigRejectsInvalidNodes(t *testing.T) {
	if _, err := Config(Options{Listen: ":80", Nodes: []Node{{ID: "bad", Address: "not-an-address"}}}); err == nil {
		t.Fatal("invalid node address must be rejected")
	}
	if _, err := Config(Options{Listen: ":80"}); err == nil {
		t.Fatal("missing nodes must be rejected")
	}
}
