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
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered configuration is missing %s\n%s", want, rendered)
		}
	}
}

func TestConfigWithoutApplicationsServesPlaceholder(t *testing.T) {
	options := testOptions()
	options.Apps = nil
	rendered := renderConfig(t, options)
	if !strings.Contains(rendered, `"http.handlers.static_response"`) ||
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
