package engine

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/vimrus/runtime/internal/health"
	"github.com/vimrus/runtime/internal/queue/bridge"
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

func TestEngineStopsWhenSchedulerLosesExecutionMode(t *testing.T) {
	queueBridge := fake.New()
	queueBridge.ScheduleResponse.Error = &bridge.Error{Code: bridge.ErrorExecutionModeConflict, Message: "runtime lease lost"}
	engine := newTestEngine(t, queueBridge, time.Hour)

	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForEngineStopped(t, engine)
	if len(queueBridge.ScheduleRequests) == 0 {
		t.Fatal("scheduler was not called")
	}
}

func TestEngineStopsWhenReaperLosesExecutionMode(t *testing.T) {
	queueBridge := fake.New()
	queueBridge.ReapResponse.Error = &bridge.Error{Code: bridge.ErrorExecutionModeConflict, Message: "runtime lease lost"}
	engine := newTestEngine(t, queueBridge, 5*time.Millisecond)

	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForEngineStopped(t, engine)
	if len(queueBridge.ReapRequests) == 0 {
		t.Fatal("reaper was not called")
	}
}

func TestEngineStopsWhenHeartbeatLosesExecutionMode(t *testing.T) {
	queueBridge := &heartbeatConflictBridge{
		Bridge: fake.New(),
		lease: bridge.Lease{
			UUID: "job-heartbeat", Channel: "mail", Handler: "mail.send", Attempt: 1,
			LeaseToken: "token-heartbeat", LeaseEnd: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
			TimeoutSeconds: 30, TraceID: "trace-heartbeat",
		},
		executeStarted: make(chan struct{}),
		release:        make(chan struct{}),
	}
	queueBridge.ClaimResponse = bridge.ClaimResponse{Schema: bridge.SchemaVersion, Leases: []bridge.Lease{queueBridge.lease}}
	queueBridge.HeartbeatResponse = bridge.HeartbeatResponse{Schema: bridge.SchemaVersion, Error: &bridge.Error{Code: bridge.ErrorExecutionModeConflict, Message: "runtime lease lost"}}
	engine := newTestEngineWithHeartbeat(t, queueBridge, 5*time.Millisecond)

	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-queueBridge.executeStarted:
	case <-time.After(time.Second):
		t.Fatal("worker did not start the claimed job")
	}
	waitForEngineStopped(t, engine)
	close(queueBridge.release)
}

func TestEngineResetsFatalSignalsOnRestart(t *testing.T) {
	queueBridge := fake.New()
	queueBridge.ScheduleResponse.Error = &bridge.Error{Code: bridge.ErrorExecutionModeConflict, Message: "runtime lease lost"}
	engine := newTestEngine(t, queueBridge, time.Hour)

	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForEngineStopped(t, engine)

	queueBridge.ScheduleResponse.Error = nil
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if result := engine.Health(); result.Status != health.StatusOK {
		t.Fatalf("health after restart = %#v", result)
	}
	if err := engine.Stop(); err != nil {
		t.Fatal(err)
	}
}

func newTestEngine(t *testing.T, queueBridge *fake.Bridge, reapInterval time.Duration) *Engine {
	t.Helper()
	engine, err := New(Config{
		NodeID: "node-a", InstanceID: "instance-a", WorkerID: "worker-a",
		ClaimBatch: 1, LeaseSeconds: 60, HeartbeatInterval: time.Hour,
		ReapInterval: reapInterval, ReapBatch: 10, DrainTimeout: time.Second,
		Workers: []worker.QueueConfig{{Name: "mail", Concurrency: 1, MinPoll: time.Hour, MaxPoll: time.Hour}},
		Logger:  slog.New(slog.NewTextHandler(testWriter{}, nil)),
	}, queueBridge)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func waitForEngineStopped(t *testing.T, engine *Engine) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if engine.Health().Status == health.StatusUnknown {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("engine did not stop after execution mode conflict")
}

func newTestEngineWithHeartbeat(t *testing.T, queueBridge *heartbeatConflictBridge, heartbeatInterval time.Duration) *Engine {
	t.Helper()
	engine, err := New(Config{
		NodeID: "node-a", InstanceID: "instance-a", WorkerID: "worker-a",
		ClaimBatch: 1, LeaseSeconds: 60, HeartbeatInterval: heartbeatInterval,
		ReapInterval: time.Hour, ReapBatch: 10, DrainTimeout: time.Second,
		Workers: []worker.QueueConfig{{Name: "mail", Concurrency: 1, MinPoll: time.Millisecond, MaxPoll: time.Millisecond}},
		Logger:  slog.New(slog.NewTextHandler(testWriter{}, nil)),
	}, queueBridge)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

type heartbeatConflictBridge struct {
	*fake.Bridge
	lease          bridge.Lease
	executeStarted chan struct{}
	release        chan struct{}
}

func (b *heartbeatConflictBridge) Execute(ctx context.Context, request bridge.ExecuteRequest) (bridge.ExecuteResponse, error) {
	select {
	case <-b.executeStarted:
	default:
		close(b.executeStarted)
	}
	select {
	case <-ctx.Done():
		return bridge.ExecuteResponse{}, ctx.Err()
	case <-b.release:
		return bridge.ExecuteResponse{Schema: bridge.SchemaVersion, Result: bridge.ExecutionSuccess}, nil
	}
}

type testWriter struct{}

func (testWriter) Write(data []byte) (int, error) { return len(data), nil }
