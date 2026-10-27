package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAppliesDefaultsAndEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.toml")
	content := `schemaVersion = 1

[runtime]
controlSocket = "/tmp/zentao.sock"
pidFile = "/tmp/zentao.pid"
drainTimeout = "15s"

[web]
listen = "127.0.0.1:8081"
threads = 8
idleTimeout = "45s"`
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

func TestDefaultWebListenBindsAllIPv4Interfaces(t *testing.T) {
	if got := Default().Web.Listen; got != "0.0.0.0:8080" {
		t.Fatalf("default web listen = %q, want 0.0.0.0:8080", got)
	}
}

func TestLoadExampleTOML(t *testing.T) {
	config, err := Load(filepath.Join("..", "..", "config", "runtime.example.toml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if config.SchemaVersion != SchemaVersion || config.Web.Threads != 4 || len(config.Queue.Workers) != 1 {
		t.Fatalf("example configuration was not loaded: %#v", config)
	}
	if len(config.Apps) != 1 || !filepath.IsAbs(config.Apps[0].Dir) ||
		!filepath.IsAbs(config.Runtime.LogPath) || !filepath.IsAbs(config.Observability.DatasetRoot) {
		t.Fatalf("relative paths were not resolved: %#v", config)
	}
}

func TestLoadRuntimeFixtureUsesApplicationsOnly(t *testing.T) {
	loaded, err := Load(filepath.Join("..", "..", "tests", "fixtures", "runtime", "runtime.toml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Apps) != 1 || loaded.Apps[0].ID != "zentao" || loaded.Apps[0].WebRoot != "." {
		t.Fatalf("unexpected runtime fixture applications: %#v", loaded.Apps)
	}
}

func TestLoadMissingConfigurationUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "runtime.toml")
	config, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(path))
	if len(config.Apps) != 0 {
		t.Fatalf("missing configuration must not synthesize an application: %#v", config.Apps)
	}
	if config.Runtime.LogPath != filepath.Join(root, "logs/runtime.log") {
		t.Fatalf("unexpected default log path: %q", config.Runtime.LogPath)
	}
	if config.Runtime.ControlSocket != filepath.Join(root, "run/runtime.sock") {
		t.Fatalf("unexpected default control socket: %q", config.Runtime.ControlSocket)
	}
	if config.Runtime.PIDFile != filepath.Join(root, "run/runtime.pid") {
		t.Fatalf("unexpected default PID file: %q", config.Runtime.PIDFile)
	}
	if config.Observability.DatasetRoot != filepath.Join(root, "data", "observability") {
		t.Fatalf("unexpected default observability path: %q", config.Observability.DatasetRoot)
	}
	if !config.Observability.Enabled {
		t.Fatal("observability should be enabled by default")
	}
}

func TestLoadPartialConfigurationRetainsRootedDefaults(t *testing.T) {
	path := pathWithContent(t, `schemaVersion = 1

[web]
listen = "127.0.0.1:8090"
`)
	loaded, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(path))
	if loaded.Runtime.ControlSocket != filepath.Join(root, "run/runtime.sock") ||
		loaded.Runtime.PIDFile != filepath.Join(root, "run/runtime.pid") {
		t.Fatalf("partial configuration did not retain rooted defaults: %#v", loaded)
	}
}

func TestLoadIgnoresRemovedLegacyOverrides(t *testing.T) {
	loaded, err := Load(pathWithContent(t, `schemaVersion = 1

[web]
listen = "127.0.0.1:8090"
`), []string{
		"ZENTAO_RUNTIME_ROOT=/srv/legacy-app",
		"ZENTAO_QUEUE_TOKEN=legacy-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Apps) != 0 {
		t.Fatalf("removed root override must not synthesize an application: %#v", loaded.Apps)
	}
	if loaded.Queue.BridgePort != Default().Queue.BridgePort {
		t.Fatalf("removed global queue token override changed queue settings: %#v", loaded.Queue)
	}
}

func TestLoadRejectsJSONConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.toml")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, nil); err == nil {
		t.Fatal("expected legacy JSON configuration to be rejected")
	}
}

func TestLoadRejectsUnknownAndInvalidFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.toml")
	content := "schemaVersion = 1\n[web]\nunknown = true\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, nil)
	if err == nil || (!strings.Contains(err.Error(), "unknown field") && !strings.Contains(err.Error(), "missing in the target struct")) {
		t.Fatalf("expected unknown-field error, got %v", err)
	}

	content = "schemaVersion = 2\n[web]\nlisten = \"127.0.0.1:8080\"\n"
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
	path := filepath.Join(t.TempDir(), "runtime.toml")
	content := `schemaVersion = 1

[runtime]
controlSocket = "/run/zentao/runtime.sock"
pidFile = "/run/zentao/runtime.pid"
auditLog = "/var/log/zentao/audit.log"
logLevel = "debug"
logPath = "/var/log/zentao/runtime.log"
logMaxBytes = 1048576
logMaxBackups = 3
auditLogMaxBytes = 2097152
auditLogMaxBackups = 4
auditLogKeepDays = 14
nodeID = "node-a"
clusterID = "cluster-prod-1"

[web]
listen = "127.0.0.1:8080"`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path, []string{"ZENTAO_RUNTIME_NODE_ID=node-b"})
	if err != nil {
		t.Fatal(err)
	}
	if config.Runtime.AuditLog != "/var/log/zentao/audit.log" || config.Runtime.LogLevel != "debug" ||
		config.Runtime.LogMaxBytes != 1048576 || config.Runtime.LogMaxBackups != 3 ||
		config.Runtime.AuditLogMaxBytes != 2097152 || config.Runtime.AuditLogMaxBackups != 4 || config.Runtime.AuditLogKeepDays != 14 {
		t.Fatalf("log settings not loaded: %#v", config.Runtime)
	}
	if config.Runtime.NodeID != "node-b" || config.Runtime.ClusterID != "cluster-prod-1" {
		t.Fatalf("identity settings not applied: %#v", config.Runtime)
	}
}

func TestValidateRejectsInvalidIdentityAndRelativeLogPath(t *testing.T) {
	config := Default()
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
	config.Runtime.LogLevel = "trace"
	if err := config.Validate(); err == nil {
		t.Fatal("expected invalid logLevel to be rejected")
	}
	config.Runtime.LogLevel = "info"
	config.Web.AccessLog = "logs/access.log"
	if err := config.Validate(); err == nil {
		t.Fatal("expected relative accessLog to be rejected")
	}
}

func TestObservabilityBridgePortValidation(t *testing.T) {
	config := DefaultForRoot("/opt/zentao")
	config.Observability.Enabled = true
	config.Observability.BridgePort = 0
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "bridgePort") {
		t.Fatalf("invalid observability bridge port must be rejected, got %v", err)
	}
	config.Observability.BridgePort = 8082
	if err := config.Validate(); err != nil {
		t.Fatalf("valid observability bridge port must be accepted: %v", err)
	}
	loaded, err := Load(pathWithContent(t, "schemaVersion = 1\n"), []string{"ZENTAO_OBSERVABILITY_BRIDGE_PORT=9082"})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Observability.BridgePort != 9082 {
		t.Fatalf("observability bridge port override not applied: %#v", loaded.Observability)
	}
}

func TestBridgePortsMustDiffer(t *testing.T) {
	config := DefaultForRoot("/opt/zentao")
	config.Queue.Enabled = true
	config.Observability.Enabled = true
	config.Observability.BridgePort = config.Queue.BridgePort
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "must be different") {
		t.Fatalf("colliding bridge ports must be rejected, got %v", err)
	}
	config.Observability.BridgePort = 8082
	config.Queue.BridgePort = 8080
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "web.listen") {
		t.Fatalf("bridge port colliding with the web listener must be rejected, got %v", err)
	}
}

func TestBIValidationAndEnvironment(t *testing.T) {
	config := DefaultForRoot("/opt/zentao")
	config.BI.Enabled = true
	config.BI.BridgePort = 8083
	if err := config.Validate(); err != nil {
		t.Fatalf("valid BI config must be accepted: %v", err)
	}

	config.BI.BridgePort = config.Queue.BridgePort
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "queue.bridgePort") {
		t.Fatalf("BI/queue bridge port collision must be rejected, got %v", err)
	}

	loaded, err := Load(pathWithContent(t, "schemaVersion = 1\n"), []string{
		"ZENTAO_BI_ENABLED=true",
		"ZENTAO_BI_BRIDGE_PORT=9083",
		"ZENTAO_BI_MAX_ROWS=2000",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.BI.Enabled || loaded.BI.BridgePort != 9083 || loaded.BI.MaxRows != 2000 {
		t.Fatalf("BI environment overrides not applied: %#v", loaded.BI)
	}
}

func TestObservabilityBatchValidation(t *testing.T) {
	config := DefaultForRoot("/opt/zentao")
	config.Observability.Enabled = true
	config.Observability.MinBatchRows = -1
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "minimum batch") {
		t.Fatalf("negative minBatchRows must be rejected, got %v", err)
	}
	config.Observability.MinBatchRows = 1000
	config.Observability.MaxFlushInterval = 0
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "maxFlushInterval") {
		t.Fatalf("zero maxFlushInterval must be rejected, got %v", err)
	}
}

func TestQueueValidationAndEnvironmentOverride(t *testing.T) {
	config := Default()
	config.Queue.Enabled = true
	config.Queue.BridgePort = 0
	if err := config.Validate(); err == nil {
		t.Fatal("queue enabled with invalid bridge port must be rejected")
	}
	config.Queue.BridgePort = 8081
	config.Queue.Workers = nil
	if err := config.Validate(); err == nil {
		t.Fatal("queue enabled without workers must be rejected")
	}
	loaded, err := Load(pathWithContent(t, `schemaVersion = 1

[runtime]
controlSocket = "/run/zentao/runtime.sock"
pidFile = "/run/zentao/runtime.pid"

[web]
listen = "127.0.0.1:8080"

[queue]
enabled = true
bridgePort = 9081

[[queue.workers]]
name = "mail"
concurrency = 2
`), []string{"ZENTAO_QUEUE_BRIDGE_PORT=9082"})
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Queue.Enabled || loaded.Queue.BridgePort != 9082 {
		t.Fatalf("queue settings not applied: %#v", loaded.Queue)
	}
}

func TestObservabilityValidation(t *testing.T) {
	config := Default()
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
	loaded, err := Load(pathWithContent(t, `schemaVersion = 1

[runtime]
controlSocket = "/run/zentao/runtime.sock"
pidFile = "/run/zentao/runtime.pid"

[web]
listen = "127.0.0.1:8080"

[observability]
enabled = true
datasetRoot = "/opt/zentao/observability"
spoolPath = "/opt/zentao/spool/observability"
jsonlConvertInterval = "30m"
jsonlConvertSources = ["access"]
jsonlKeepDays = 3
`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Observability.JSONLConvertInterval != 30*time.Minute || loaded.Observability.JSONLKeepDays != 3 || len(loaded.Observability.JSONLConvertSources) != 1 {
		t.Fatalf("jsonl settings not loaded: %#v", loaded.Observability)
	}
}

func pathWithContent(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadPHPErrorLogRotationOptions(t *testing.T) {
	loaded, err := Load(pathWithContent(t, "schemaVersion = 1\n\n[runtime]\nphpErrorLogMaxBytes = 1024\nphpErrorLogMaxBackups = 2\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Runtime.PHPErrorLogMaxBytes != 1024 || loaded.Runtime.PHPErrorLogMaxBackups != 2 {
		t.Fatalf("php error log settings not loaded: %#v", loaded.Runtime)
	}

	defaults := Default()
	if defaults.Runtime.PHPErrorLogMaxBytes != 16*1024*1024 || defaults.Runtime.PHPErrorLogMaxBackups != 5 {
		t.Fatalf("unexpected php error log defaults: %#v", defaults.Runtime)
	}
}

func TestLoadRejectsNegativePHPErrorLogRotationOptions(t *testing.T) {
	if _, err := Load(pathWithContent(t, "schemaVersion = 1\n\n[runtime]\nphpErrorLogMaxBytes = -1\n"), nil); err == nil {
		t.Fatal("negative phpErrorLogMaxBytes must be rejected")
	}
}
