package worker

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/vimrus/runtime/internal/queue/bridge"
	"github.com/vimrus/runtime/internal/queue/lease"
)

type fakeBridge struct {
	mu          sync.Mutex
	claimCount  int
	claims      []bridge.ClaimRequest
	execute     []bridge.ExecuteRequest
	lease       bridge.Lease
	claimResp   bridge.ClaimResponse
	executeResp bridge.ExecuteResponse
}

func (b *fakeBridge) Claim(_ context.Context, request bridge.ClaimRequest) (bridge.ClaimResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.claimCount++
	b.claims = append(b.claims, request)
	if b.claimResp.Error != nil {
		return b.claimResp, nil
	}
	if b.claimCount > 1 {
		return bridge.ClaimResponse{Schema: bridge.SchemaVersion}, nil
	}
	return bridge.ClaimResponse{Schema: bridge.SchemaVersion, Leases: []bridge.Lease{b.lease}}, nil
}

func TestPoolStopsOnExecutionModeConflict(t *testing.T) {
	queueBridge := &fakeBridge{claimResp: bridge.ClaimResponse{
		Schema: bridge.SchemaVersion,
		Error:  &bridge.Error{Code: bridge.ErrorExecutionModeConflict, Message: "runtime lease lost"},
	}}
	manager := lease.NewManager("node-a", "instance-a", "worker-a", queueBridge, time.Minute, 1)
	p, err := NewPool(queueBridge, manager, Config{
		NodeID: "node-a", InstanceID: "instance-a", WorkerID: "worker-a", ClaimBatch: 1,
		DrainTimeout: time.Second, Channels: []QueueConfig{{Name: "mail", MinPoll: time.Millisecond, MaxPoll: time.Millisecond}},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	p.Run(context.Background())
	select {
	case <-p.Fatal():
	case <-time.After(time.Second):
		t.Fatal("pool did not signal execution mode conflict")
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	queueBridge.mu.Lock()
	defer queueBridge.mu.Unlock()
	if queueBridge.claimCount != 1 {
		t.Fatalf("claim calls after mode conflict = %d, want 1", queueBridge.claimCount)
	}
}

func (b *fakeBridge) Execute(_ context.Context, request bridge.ExecuteRequest) (bridge.ExecuteResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.execute = append(b.execute, request)
	return b.executeResp, nil
}

func (b *fakeBridge) Heartbeat(_ context.Context, request bridge.HeartbeatRequest) (bridge.HeartbeatResponse, error) {
	return bridge.HeartbeatResponse{Schema: bridge.SchemaVersion}, nil
}

func TestPoolClaimsExecutesAndDrains(t *testing.T) {
	claimed := bridge.Lease{
		UUID: "job-1", Channel: "mail", Handler: "mail.send", Attempt: 1,
		LeaseToken: "token-1", LeaseEnd: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
		TimeoutSeconds: 10, TraceID: "trace-1",
	}
	queueBridge := &fakeBridge{
		lease:       claimed,
		executeResp: bridge.ExecuteResponse{Schema: bridge.SchemaVersion, Result: bridge.ExecutionSuccess},
	}
	manager := lease.NewManager("node-a", "instance-a", "worker-a", queueBridge, time.Minute, 8)
	pool, err := NewPool(queueBridge, manager, Config{
		NodeID:       "node-a",
		InstanceID:   "instance-a",
		WorkerID:     "worker-a",
		Channels:     []QueueConfig{{Name: "mail", Concurrency: 2, MinPoll: 10 * time.Millisecond, MaxPoll: 20 * time.Millisecond}},
		ClaimBatch:   1,
		LeaseSeconds: 60,
		DrainTimeout: 2 * time.Second,
	}, slog.New(slog.NewTextHandler(testWriter{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		queueBridge.mu.Lock()
		executed := len(queueBridge.execute)
		queueBridge.mu.Unlock()
		if executed == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := pool.Stop(); err != nil {
		t.Fatal(err)
	}
	queueBridge.mu.Lock()
	defer queueBridge.mu.Unlock()
	if len(queueBridge.execute) != 1 {
		t.Fatalf("executions = %d, want 1", len(queueBridge.execute))
	}
	if queueBridge.execute[0].LeaseToken != "token-1" || queueBridge.execute[0].TraceID != "trace-1" {
		t.Fatalf("fencing token or trace not forwarded: %#v", queueBridge.execute[0])
	}
	if manager.Count() != 0 {
		t.Fatalf("active leases after completion = %d, want 0", manager.Count())
	}
}

func TestNewPoolRejectsDuplicateQueues(t *testing.T) {
	queueBridge := &fakeBridge{}
	manager := lease.NewManager("node-a", "instance-a", "worker-a", queueBridge, time.Minute, 1)
	_, err := NewPool(queueBridge, manager, Config{
		NodeID: "node-a", InstanceID: "instance-a", WorkerID: "worker-a", ClaimBatch: 1,
		Channels: []QueueConfig{{Name: "mail"}, {Name: "mail"}},
	}, slog.Default())
	if err == nil {
		t.Fatal("duplicate queues must be rejected")
	}
}

func TestPoolCanRestartAfterStop(t *testing.T) {
	queueBridge := &fakeBridge{}
	manager := lease.NewManager("node-a", "instance-a", "worker-a", queueBridge, time.Minute, 1)
	p, err := NewPool(queueBridge, manager, Config{
		NodeID: "node-a", InstanceID: "instance-a", WorkerID: "worker-a", ClaimBatch: 1,
		DrainTimeout: time.Second, Channels: []QueueConfig{{Name: "mail", MinPoll: time.Millisecond, MaxPoll: time.Millisecond}},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.Run(ctx)
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	p.Run(ctx2)
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	cancel()
}

type testWriter struct{}

func (testWriter) Write(data []byte) (int, error) { return len(data), nil }
