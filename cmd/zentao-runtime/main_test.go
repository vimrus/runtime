package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vimrus/runtime/internal/config"
	"github.com/vimrus/runtime/internal/control"
)

func TestWorkerRestartRequiresWorkerMode(t *testing.T) {
	host := &host{config: config.Default()}
	response := host.HandleControl(context.Background(), control.Request{Operation: "worker-restart"})
	if response.OK || response.Error == nil || response.Error.Code != "unavailable" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestWorkerRestartReportsConfiguredThreads(t *testing.T) {
	cfg := config.Default()
	cfg.Apps = []config.App{{
		ID:            "zentao",
		Worker:        true,
		WorkerEntries: []string{"index.php", "api.php", "bridge.php"},
		WorkerThreads: map[string]int{"index.php": 2, "api.php": 2, "bridge.php": 2},
		Enabled:       true,
	}}
	host := &host{config: cfg}
	response := host.HandleControl(context.Background(), control.Request{Operation: "worker-restart"})
	if !response.OK {
		t.Fatalf("unexpected response: %#v", response)
	}
	var result struct {
		Restarted     bool `json:"restarted"`
		WorkerThreads int  `json:"workerThreads"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Restarted || result.WorkerThreads != 6 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestApplicationSummaryReportsWorkerConfiguration(t *testing.T) {
	cfg := config.Default()
	cfg.Apps = []config.App{{
		ID:            "zentao",
		Path:          "/",
		Worker:        true,
		WorkerEntries: []string{"index.php", "api.php"},
		WorkerThreads: map[string]int{"index.php": 2, "api.php": 2},
		Enabled:       true,
	}}
	apps := appSummary(cfg)
	if len(apps) != 1 {
		t.Fatalf("unexpected app summary: %#v", apps)
	}
	app := apps[0]
	entries, ok := app["workerEntries"].([]string)
	if !ok || len(entries) != 2 || app["worker"] != true || app["workerThreads"] != 4 {
		t.Fatalf("worker summary is incomplete: %#v", app)
	}
}
