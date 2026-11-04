package appprocess

import (
	"context"
	"testing"
	"time"

	"github.com/vimrus/runtime/internal/config"
	"github.com/vimrus/runtime/internal/health"
)

func TestProcessHealthGatesReadinessOnlyWhenRequired(t *testing.T) {
	registry := health.NewRegistry()
	process := testProcessConfig()
	process.Required = true
	worker := newProcessWorker(config.App{
		ID:      "zentao",
		Path:    "/",
		Dir:     "/tmp/zentao",
		DataDir: "/tmp/zentao-data",
		TmpDir:  "/tmp/zentao-tmp",
	}, process, processWorkerOptions{})
	worker.broker.beginAttempt("instance-a", "token-a", 100, time.Now())
	if err := worker.broker.ready("instance-a", "token-a", time.Now()); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{workers: map[string]*processWorker{worker.key(): worker}}
	manager.RegisterHealth(registry)
	if snapshot := registry.Probe(context.Background()); !snapshot.Ready() || snapshot.AnyFailed {
		t.Fatalf("ready required process must be healthy: %#v", snapshot)
	}

	worker.broker.failAttempt("instance-a", StateFailed, 1, "failed", time.Now())
	snapshot := registry.Probe(context.Background())
	if snapshot.Ready() {
		t.Fatal("failed required process must block readiness")
	}

	process.Required = false
	optional := newProcessWorker(config.App{
		ID:      "zentao",
		Path:    "/",
		Dir:     "/tmp/zentao",
		DataDir: "/tmp/zentao-data",
		TmpDir:  "/tmp/zentao-tmp",
	}, process, processWorkerOptions{})
	optional.broker.beginAttempt("instance-b", "token-b", 101, time.Now())
	optional.broker.failAttempt("instance-b", StateFailed, 1, "failed", time.Now())
	optionalManager := &Manager{workers: map[string]*processWorker{optional.key(): optional}}
	optionalRegistry := health.NewRegistry()
	optionalManager.RegisterHealth(optionalRegistry)
	snapshot = optionalRegistry.Probe(context.Background())
	if !snapshot.Ready() || !snapshot.AnyFailed {
		t.Fatalf("failed optional process must degrade without blocking readiness: %#v", snapshot)
	}
}
