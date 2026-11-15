package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testOptions() Options {
	return Options{
		Apps: []App{
			{ID: "app-a", Prefix: "/", Root: "/srv/app-a/www", Env: map[string]string{"ZT_APP_ID": "app-a"}, Available: true},
			{ID: "app-b", Prefix: "/ipd", Root: "/srv/app-b/www", Env: map[string]string{"ZT_APP_ID": "app-b", "ZT_WEB_ROOT": "ipd"}, Available: true},
		},
		Listen:            "127.0.0.1:8080",
		Threads:           4,
		MaxWaitTime:       10 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16384,
	}
}

func renderConfig(t *testing.T, options Options) string {
	t.Helper()
	data, err := json.Marshal(Config(options))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestConfigRoutesSubPathApplication(t *testing.T) {
	rendered := renderConfig(t, testOptions())
	for _, want := range []string{
		`"strip_path_prefix":"/ipd"`,
		`"path":["/ipd","/ipd/*"]`,
		`"ZT_WEB_ROOT":"ipd"`,
		`"root":"/srv/app-b/www"`,
		`"listen":["127.0.0.1:8080"]`,
		`"max_wait_time":10000000000`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered configuration is missing %s\n%s", want, rendered)
		}
	}
}

func TestConfigCompressesApplicationResponses(t *testing.T) {
	rendered := renderConfig(t, testOptions())
	for _, want := range []string{
		`"handler":"encode"`,
		`"zstd":`,
		`"gzip":`,
		`"prefer":["zstd","gzip"]`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered configuration is missing %s\n%s", want, rendered)
		}
	}
}

func TestConfigEnablesApplicationWorker(t *testing.T) {
	options := testOptions()
	options.Apps[0].Worker = true
	options.Apps[0].WorkerThreads = map[string]int{"index.php": 3}
	rendered := renderConfig(t, options)
	for _, want := range []string{
		`"workers":[{"file_name":"index.php","num":3}]`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered configuration is missing %s\n%s", want, rendered)
		}
	}
}

func TestConfigEnablesRuntimeFileIntercept(t *testing.T) {
	options := testOptions()
	options.Apps[0].Env["ZT_DATA_ROOT"] = "/srv/app-a/data"
	options.Apps[0].Env["ZT_TMP_ROOT"] = "/srv/app-a/tmp"
	rendered := renderConfig(t, options)
	for _, want := range []string{
		`"handler":"zentao_file_intercept"`,
		`"roots":["/srv/app-a/www","/srv/app-a/data","/srv/app-a/tmp"]`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered configuration is missing %s\n%s", want, rendered)
		}
	}
}

func TestConfigEnablesMultipleApplicationWorkers(t *testing.T) {
	options := testOptions()
	options.Apps[0].Worker = true
	options.Apps[0].WorkerEntries = []string{"index.php", "api.php", "bridge.php"}
	options.Apps[0].WorkerThreads = map[string]int{"index.php": 10, "api.php": 3, "bridge.php": 2}
	rendered := renderConfig(t, options)
	for _, want := range []string{
		`{"file_name":"index.php","num":10}`,
		`{"file_name":"api.php","num":3}`,
		`{"file_name":"bridge.php","num":2}`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered configuration is missing %s\n%s", want, rendered)
		}
	}
}

func TestConfigPartitionsWorkerEntriesBetweenPublicAppAndQueueBridge(t *testing.T) {
	options := testOptions()
	options.Apps[0].Worker = true
	options.Apps[0].WorkerEntries = []string{"index.php", "api.php", "bridge.php"}
	options.Apps[0].WorkerThreads = map[string]int{"index.php": 10, "api.php": 3, "bridge.php": 2}
	options.QueueBridge = QueueBridgeOptions{
		Enabled: true,
		Listen:  "127.0.0.1:8081",
		Apps: []QueueBridgeApp{
			{ID: options.Apps[0].ID, Prefix: options.Apps[0].Prefix, Token: "token-a", Template: "/bridge.php?endpoint={endpoint}"},
		},
	}
	rendered := renderConfig(t, options)
	for _, want := range []string{
		`{"file_name":"index.php","num":10}`,
		`{"file_name":"api.php","num":3}`,
		`{"file_name":"bridge.php","num":2}`,
	} {
		if count := strings.Count(rendered, want); count != 1 {
			t.Fatalf("worker %s rendered %d times, want exactly once\n%s", want, count, rendered)
		}
	}
}

func TestWorkerEntriesPartitionForPrivateBridge(t *testing.T) {
	app := App{
		Worker:        true,
		WorkerEntries: []string{"index.php", "api.php", "bridge.php"},
	}
	public := publicWorkerEntries(app, true)
	private := bridgeWorkerEntries(app)
	if got, want := strings.Join(public, ","), "index.php,api.php"; got != want {
		t.Fatalf("public worker entries = %q, want %q", got, want)
	}
	if got, want := strings.Join(private, ","), "bridge.php"; got != want {
		t.Fatalf("private bridge worker entries = %q, want %q", got, want)
	}
	if got, want := strings.Join(publicWorkerEntries(app, false), ","), "index.php,api.php,bridge.php"; got != want {
		t.Fatalf("worker entries without private bridge = %q, want %q", got, want)
	}
}

func TestConfigWithoutApplicationsServesPlaceholder(t *testing.T) {
	options := testOptions()
	options.Apps = nil
	rendered := renderConfig(t, options)
	if !strings.Contains(rendered, `"handler":"static_response"`) ||
		!strings.Contains(rendered, `no application configured`) {
		t.Fatalf("expected a placeholder response without applications\n%s", rendered)
	}
}

func TestConfigUnavailableApplicationServesPlaceholder(t *testing.T) {
	options := testOptions()
	options.Apps[0].Available = false
	options.Apps[1].Available = false
	rendered := renderConfig(t, options)
	if strings.Contains(rendered, `"http.handlers.php"`) {
		t.Fatalf("unavailable applications must not register a PHP handler\n%s", rendered)
	}
	if !strings.Contains(rendered, `application app-b is not installed`) {
		t.Fatalf("expected an unavailable placeholder for the sub-path application\n%s", rendered)
	}
}

func TestConfigRoutesPerApplicationQueueBridge(t *testing.T) {
	options := testOptions()
	options.QueueBridge = QueueBridgeOptions{
		Enabled: true,
		Listen:  "127.0.0.1:8081",
		Apps: []QueueBridgeApp{
			{ID: "app-b", Prefix: "/ipd", Token: "token-b", Template: "/bridge.php?endpoint={endpoint}"},
		},
	}
	rendered := renderConfig(t, options)
	for _, want := range []string{
		`"path":["/internal/runtime/queue/v1/apps/app-b","/internal/runtime/queue/v1/apps/app-b/*"]`,
		`"uri":"/ipd/bridge.php?endpoint=capabilities"`,
		`"token":"token-b"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered configuration is missing %s\n%s", want, rendered)
		}
	}
}

func TestConfigRoutesOLAPBridge(t *testing.T) {
	options := testOptions()
	options.OLAPBridge = OLAPBridgeOptions{
		Enabled: true,
		Listen:  "127.0.0.1:8083",
		Tokens:  []string{"olap-token"},
	}
	rendered := renderConfig(t, options)
	for _, want := range []string{
		`"listen":["127.0.0.1:8083"]`,
		`"/internal/runtime/olap/v1/publish"`,
		`"/internal/runtime/olap/v1/query"`,
		`"tokens":["olap-token"]`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered configuration is missing %s\n%s", want, rendered)
		}
	}
}

func TestConfigDoesNotRouteClusterGate(t *testing.T) {
	options := testOptions()
	rendered := renderConfig(t, options)
	if strings.Contains(rendered, "zentao_cluster_gate") {
		t.Fatalf("rendered configuration must not include the cluster gate\n%s", rendered)
	}
}
