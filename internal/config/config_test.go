package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAppliesDefaultsAndEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	content := `{
  "schemaVersion": 1,
  "runtime": {"controlSocket": "/tmp/zentao.sock", "pidFile": "/tmp/zentao.pid", "drainTimeout": "15s"},
  "web": {"root": "/srv/zentao/www", "listen": "127.0.0.1:8081", "threads": 8, "idleTimeout": "45s"}
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path, []string{"ZENTAO_RUNTIME_THREADS=12", "ZENTAO_RUNTIME_LISTEN=127.0.0.1:8090"})
	if err != nil {
		t.Fatal(err)
	}
	if config.Web.Threads != 12 || config.Web.Listen != "127.0.0.1:8090" {
		t.Fatalf("environment overrides were not applied: %#v", config.Web)
	}
	if config.Web.ReadHeaderTimeout.String() != "10s" || config.Runtime.DrainTimeout.String() != "15s" {
		t.Fatalf("defaults or duration parsing failed: %#v", config)
	}
}

func TestLoadRejectsUnknownAndInvalidFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	content := `{"schemaVersion":1,"web":{"root":"/app","unknown":true}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}

	content = `{"schemaVersion":2,"web":{"root":"/app"}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Load(path, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected schema error, got %v", err)
	}
}

func TestRestartRequired(t *testing.T) {
	current := Default()
	current.Web.Root = "/app"
	current.Runtime.NodeID = "node-generated"
	candidate := current
	candidate.Web.IdleTimeout *= 2
	if RestartRequired(current, candidate) {
		t.Fatal("timeout-only update must be hot reloadable")
	}
	unset := candidate
	unset.Runtime.NodeID = ""
	if RestartRequired(current, unset) {
		t.Fatal("missing generated nodeID in candidate must not force restart")
	}
	threads := current
	threads.Web.Threads++
	if !RestartRequired(current, threads) {
		t.Fatal("thread count update must require restart")
	}
	jsonl := candidate
	jsonl.Observability.JSONLKeepDays = 14
	if !RestartRequired(current, jsonl) {
		t.Fatal("jsonl retention change must require restart")
	}
}

func TestLoadAppliesLogAndIdentitySettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	content := `{
  "schemaVersion": 1,
  "runtime": {
    "controlSocket": "/run/zentao/runtime.sock",
    "pidFile": "/run/zentao/runtime.pid",
    "auditLog": "/var/log/zentao/audit.log",
    "logPath": "/var/log/zentao/runtime.log",
    "logMaxBytes": 1048576,
    "logMaxBackups": 3,
    "nodeID": "node-a",
    "clusterID": "cluster-prod-1"
  },
  "web": {"root": "/opt/zentao/app/current/www", "listen": "127.0.0.1:8080"}
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path, []string{"ZENTAO_RUNTIME_NODE_ID=node-b"})
	if err != nil {
		t.Fatal(err)
	}
	if config.Runtime.AuditLog != "/var/log/zentao/audit.log" || config.Runtime.LogMaxBytes != 1048576 || config.Runtime.LogMaxBackups != 3 {
		t.Fatalf("log settings not loaded: %#v", config.Runtime)
	}
	if config.Runtime.NodeID != "node-b" || config.Runtime.ClusterID != "cluster-prod-1" {
		t.Fatalf("identity settings not applied: %#v", config.Runtime)
	}
}

func TestValidateRejectsInvalidIdentityAndRelativeLogPath(t *testing.T) {
	config := Default()
	config.Web.Root = "/opt/zentao/app/current/www"
	config.Runtime.NodeID = "node with spaces"
	if err := config.Validate(); err == nil {
		t.Fatal("expected invalid nodeID to be rejected")
	}
	config.Runtime.NodeID = "node-a"
	config.Runtime.LogPath = "logs/runtime.log"
	if err := config.Validate(); err == nil {
		t.Fatal("expected relative logPath to be rejected")
	}
	config.Runtime.LogPath = "/var/log/zentao/runtime.log"
	config.Web.AccessLog = "logs/access.log"
	if err := config.Validate(); err == nil {
		t.Fatal("expected relative accessLog to be rejected")
	}
}

func TestQueueValidationAndEnvironmentOverride(t *testing.T) {
	config := Default()
	config.Web.Root = "/opt/zentao/app/current/www"
	config.Queue.Enabled = true
	if err := config.Validate(); err == nil {
		t.Fatal("queue enabled without bridge URL must be rejected")
	}
	config.Queue.BridgeBaseURL = "http://127.0.0.1:8081/internal/runtime/queue/v1"
	config.Queue.Workers = nil
	if err := config.Validate(); err == nil {
		t.Fatal("queue enabled without workers must be rejected")
	}
	loaded, err := Load(pathWithContent(t, `{
  "schemaVersion": 1,
  "runtime": {"controlSocket": "/run/zentao/runtime.sock", "pidFile": "/run/zentao/runtime.pid"},
  "web": {"root": "/opt/zentao/app/current/www", "listen": "127.0.0.1:8080"},
  "queue": {
    "enabled": true,
    "bridgeBaseURL": "http://127.0.0.1:8081/internal/runtime/queue/v1",
    "workers": [{"name": "mail", "concurrency": 2}]
  }
}`), []string{"ZENTAO_QUEUE_TOKEN=secret"})
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Queue.Enabled || loaded.Queue.BridgeToken != "secret" {
		t.Fatalf("queue settings not applied: %#v", loaded.Queue)
	}
}

func TestObservabilityValidation(t *testing.T) {
	config := Default()
	config.Web.Root = "/opt/zentao/app/current/www"
	config.Observability.Enabled = true
	if err := config.Validate(); err == nil {
		t.Fatal("observability enabled without dataset root must be rejected")
	}
	config.Observability.DatasetRoot = "/opt/zentao/observability"
	config.Observability.SpoolPath = "spool/observability"
	if err := config.Validate(); err == nil {
		t.Fatal("relative spool path must be rejected")
	}
}

func TestObservabilityJSONLDefaultsAndValidation(t *testing.T) {
	config := Default()
	config.Web.Root = "/opt/zentao/app/current/www"
	config.Observability.Enabled = true
	config.Observability.DatasetRoot = "/opt/zentao/observability"
	config.Observability.SpoolPath = "/opt/zentao/spool/observability"
	if config.Observability.JSONLConvertInterval != time.Hour || config.Observability.JSONLKeepDays != 7 {
		t.Fatalf("jsonl defaults not applied: %#v", config.Observability)
	}
	config.Observability.JSONLConvertSources = []string{"access", "cache"}
	if err := config.Validate(); err == nil {
		t.Fatal("unsupported jsonl source must be rejected")
	}
	loaded, err := Load(pathWithContent(t, `{
  "schemaVersion": 1,
  "runtime": {"controlSocket": "/run/zentao/runtime.sock", "pidFile": "/run/zentao/runtime.pid"},
  "web": {"root": "/opt/zentao/app/current/www", "listen": "127.0.0.1:8080"},
  "observability": {
    "enabled": true,
    "datasetRoot": "/opt/zentao/observability",
    "spoolPath": "/opt/zentao/spool/observability",
    "jsonlConvertInterval": "30m",
    "jsonlConvertSources": ["access"],
    "jsonlKeepDays": 3
  }
}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Observability.JSONLConvertInterval != 30*time.Minute || loaded.Observability.JSONLKeepDays != 3 || len(loaded.Observability.JSONLConvertSources) != 1 {
		t.Fatalf("jsonl settings not loaded: %#v", loaded.Observability)
	}
}

func pathWithContent(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
