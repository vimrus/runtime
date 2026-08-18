package engine

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/vimrus/runtime/internal/health"
	"github.com/vimrus/runtime/internal/queue/fake"
	"github.com/vimrus/runtime/internal/queue/worker"
)

func TestEngineStartStopAndHealth(t *testing.T) {
	queueBridge := fake.New()
	engine, err := New(Config{
		NodeID:            "node-a",
		InstanceID:        "instance-a",
		WorkerID:          "worker-a",
		ClaimBatch:        1,
		LeaseSeconds:      60,
		HeartbeatInterval: time.Minute,
		ReapInterval:      time.Minute,
		ReapBatch:         10,
		DrainTimeout:      2 * time.Second,
		Workers:           []worker.QueueConfig{{Name: "mail", Concurrency: 1, MinPoll: 10 * time.Millisecond, MaxPoll: 20 * time.Millisecond}},
		Logger:            slog.New(slog.NewTextHandler(testWriter{}, nil)),
	}, queueBridge)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if result := engine.Health(); result.Status != health.StatusOK {
		t.Fatalf("health = %#v", result)
	}
	if len(queueBridge.CapabilitiesRequests) != 1 {
		t.Fatalf("capabilities requests = %d, want 1", len(queueBridge.CapabilitiesRequests))
	}
	if err := engine.Stop(); err != nil {
		t.Fatal(err)
	}
	if result := engine.Health(); result.Status != health.StatusUnknown {
		t.Fatalf("health after stop = %#v", result)
	}
}

func TestEngineRejectsBadCapabilities(t *testing.T) {
	queueBridge := fake.New()
	queueBridge.CapabilitiesResponse.ClaimMode = "unknown"
	engine, err := New(Config{
		NodeID: "node-a", InstanceID: "instance-a", WorkerID: "worker-a",
		ClaimBatch: 1, LeaseSeconds: 60, HeartbeatInterval: time.Minute, ReapInterval: time.Minute, ReapBatch: 10,
		DrainTimeout: time.Second,
		Workers:      []worker.QueueConfig{{Name: "mail", Concurrency: 1}},
		Logger:       slog.New(slog.NewTextHandler(testWriter{}, nil)),
	}, queueBridge)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background()); err == nil {
		t.Fatal("invalid capabilities must fail startup")
	}
}

type testWriter struct{}

func (testWriter) Write(data []byte) (int, error) { return len(data), nil }
