package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadAppsConfig(t *testing.T, body string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config", "runtime.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	content := "schemaVersion = 1\n\n[web]\nlisten = \"127.0.0.1:8080\"\n" + body
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path, nil)
}

func TestLoadApplicationsAppliesDefaults(t *testing.T) {
	config, err := loadAppsConfig(t, `
[[apps]]
id = "zentao-ipd"
path = "/ipd"
dir = "apps/zentao-ipd-22.6"

[[apps]]
id = "zentao-open"
path = "/"
dir = "apps/zentao-open-22.6"
env = { ZT_DB_NAME = "zentao_open" }
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps) != 2 {
		t.Fatalf("expected 2 applications, got %d", len(config.Apps))
	}
	ipd := config.Apps[0]
	if ipd.WebRoot != "www" || !filepath.IsAbs(ipd.Dir) || !filepath.IsAbs(ipd.DataDir) || !filepath.IsAbs(ipd.TmpDir) {
		t.Fatalf("application paths were not resolved: %#v", ipd)
	}
	if !strings.HasSuffix(ipd.Dir, filepath.Join("apps", "zentao-ipd-22.6")) {
		t.Fatalf("unexpected application directory: %q", ipd.Dir)
	}
	if !strings.HasSuffix(ipd.DataDir, filepath.Join("data", "apps", "zentao-ipd")) {
		t.Fatalf("unexpected data directory: %q", ipd.DataDir)
	}
	if !strings.HasSuffix(ipd.TmpDir, filepath.Join("tmp-local", "zentao-ipd")) {
		t.Fatalf("unexpected temp directory: %q", ipd.TmpDir)
	}
	if got := config.Apps[1].Env["ZT_DB_NAME"]; got != "zentao_open" {
		t.Fatalf("application environment was not loaded: %q", got)
	}
	if !config.Apps[0].Enabled || !config.Apps[1].Enabled {
		t.Fatal("applications should be enabled by default")
	}
}

func TestLoadRejectsInvalidApplicationLayouts(t *testing.T) {
	cases := map[string]string{
		"duplicate path": `
[[apps]]
id = "a"
path = "/x"
dir = "apps/a"

[[apps]]
id = "b"
path = "/x"
dir = "apps/b"
`,
		"nested paths": `
[[apps]]
id = "a"
path = "/x"
dir = "apps/a"

[[apps]]
id = "b"
path = "/x/y"
dir = "apps/b"
`,
		"reserved prefix": `
[[apps]]
id = "a"
path = "/_runtime/x"
dir = "apps/a"
`,
		"missing id": `
[[apps]]
path = "/x"
dir = "apps/a"
`,
		"missing dir": `
[[apps]]
id = "a"
path = "/x"
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadAppsConfig(t, body); err == nil {
				t.Fatalf("expected validation error for %s", name)
			}
		})
	}
}

func TestEmptyApplicationListIsAllowed(t *testing.T) {
	config, err := loadAppsConfig(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps) != 0 {
		t.Fatalf("empty apps must stay empty: %#v", config.Apps)
	}
}

func TestWebRootFieldIsRejected(t *testing.T) {
	_, err := loadAppsConfig(t, `root = "app/zentaopms/www"`)
	if err == nil || !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("expected removed web.root field to be rejected, got %v", err)
	}
}

func TestLoadAppQueueConfiguration(t *testing.T) {
	config, err := loadAppsConfig(t, `
[[apps]]
id = "zentao"
path = "/"
dir = "apps/zentaopms"

[apps.queue]
enabled = true
bridgeUrlTemplate = "/bridge.php?endpoint={endpoint}"
token = "fixed-token"
`)
	if err != nil {
		t.Fatal(err)
	}
	queue := config.Apps[0].Queue
	if !queue.Enabled || queue.BridgeURLTemplate != "/bridge.php?endpoint={endpoint}" || queue.Token != "fixed-token" {
		t.Fatalf("unexpected application queue configuration: %#v", queue)
	}
}

func TestLoadAppQueueAppliesDefaultTemplate(t *testing.T) {
	config, err := loadAppsConfig(t, `
[[apps]]
id = "zentao"
path = "/"
dir = "apps/zentaopms"
queue = { enabled = true }
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Apps[0].Queue.BridgeURLTemplate; got != DefaultAppQueueBridgeURLTemplate {
		t.Fatalf("default bridge template mismatch: %q", got)
	}
}

func TestLoadRejectsInvalidAppQueueTemplates(t *testing.T) {
	cases := map[string]string{
		"missing placeholder": `
[[apps]]
id = "zentao"
path = "/"
dir = "apps/zentaopms"
queue = { enabled = true, bridgeUrlTemplate = "/bridge.php" }
`,
		"absolute url": `
[[apps]]
id = "zentao"
path = "/"
dir = "apps/zentaopms"
queue = { enabled = true, bridgeUrlTemplate = "http://127.0.0.1/queue-{endpoint}" }
`,
		"missing leading slash": `
[[apps]]
id = "zentao"
path = "/"
dir = "apps/zentaopms"
queue = { enabled = true, bridgeUrlTemplate = "queue-{endpoint}" }
`,
		"parent traversal": `
[[apps]]
id = "zentao"
path = "/"
dir = "apps/zentaopms"
queue = { enabled = true, bridgeUrlTemplate = "/../queue-{endpoint}" }
`,
	}
	for name, body := range cases {
		if _, err := loadAppsConfig(t, body); err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}
}
