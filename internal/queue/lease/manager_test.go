package lease

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vimrus/runtime/internal/queue/bridge"
)

type recordingHeartbeater struct {
	mu        sync.Mutex
	responses []bridge.HeartbeatResponse
	err       error
	requests  []bridge.HeartbeatRequest
}

func (h *recordingHeartbeater) Heartbeat(_ context.Context, request bridge.HeartbeatRequest) (bridge.HeartbeatResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, request)
	if h.err != nil {
		return bridge.HeartbeatResponse{}, h.err
	}
	if len(h.responses) == 0 {
		return bridge.HeartbeatResponse{Schema: bridge.SchemaVersion}, nil
	}
	response := h.responses[0]
	if len(h.responses) > 1 {
		h.responses = h.responses[1:]
	}
	return response, nil
}

func TestManagerExtendsAndCancelsStaleLeases(t *testing.T) {
	heartbeater := &recordingHeartbeater{
		responses: []bridge.HeartbeatResponse{
			{Schema: bridge.SchemaVersion, Results: []bridge.HeartbeatResult{
				{UUID: "job-1", Attempt: 1, Status: bridge.LeaseExtended, LeaseEnd: time.Now().UTC().Add(30 * time.Second).Format(time.RFC3339Nano)},
				{UUID: "job-2", Attempt: 1, Status: bridge.LeaseStale, Code: "lease_lost"},
			}},
		},
	}
	manager := NewManager("node-a", "instance-a", "worker-a", heartbeater, 10*time.Millisecond, 8)
	var cancelled sync.Map
	manager.Add(bridge.Lease{UUID: "job-1", Attempt: 1, LeaseToken: "t1", LeaseEnd: time.Now().UTC().Add(10 * time.Second).Format(time.RFC3339Nano), TimeoutSeconds: 10}, func() { cancelled.Store("job-1", true) })
	manager.Add(bridge.Lease{UUID: "job-2", Attempt: 1, LeaseToken: "t2", LeaseEnd: time.Now().UTC().Add(10 * time.Second).Format(time.RFC3339Nano), TimeoutSeconds: 10}, func() { cancelled.Store("job-2", true) })

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan struct{})
	go func() {
		manager.Run(ctx)
		close(done)
	}()
	time.Sleep(80 * time.Millisecond)
	stop()
	<-done

	if _, ok := cancelled.Load("job-2"); !ok {
		t.Fatal("stale lease was not cancelled")
	}
	if _, ok := cancelled.Load("job-1"); ok {
		t.Fatal("extended lease must not be cancelled")
	}
	if manager.Count() != 1 {
		t.Fatalf("active leases = %d, want 1", manager.Count())
	}
}
