// Package lease owns the active lease set and batched heartbeat maintenance.
package lease

import (
	"context"
	"sync"
	"time"

	"github.com/vimrus/runtime/internal/queue/bridge"
)

// Heartbeater is the subset of the Queue Bridge used for lease maintenance.
type Heartbeater interface {
	Heartbeat(context.Context, bridge.HeartbeatRequest) (bridge.HeartbeatResponse, error)
}

// Lease is an active execution lease with its worker cancellation handle.
type Lease struct {
	JobUUID        string
	Queue          string
	Handler        string
	Attempt        int
	LeaseToken     string
	LeaseUntil     time.Time
	TimeoutSeconds int
	TraceID        string
	cancel         context.CancelFunc
}

type Manager struct {
	mu         sync.Mutex
	leases     map[string]*Lease
	nodeID     string
	instanceID string
	workerID   string
	heartbeat  Heartbeater
	interval   time.Duration
	batch      int
}

func NewManager(nodeID, instanceID, workerID string, heartbeat Heartbeater, interval time.Duration, batch int) *Manager {
	if interval <= 0 {
		interval = 20 * time.Second
	}
	if batch <= 0 || batch > bridge.MaxBatchSize {
		batch = bridge.MaxBatchSize
	}
	return &Manager{
		leases:     make(map[string]*Lease),
		nodeID:     nodeID,
		instanceID: instanceID,
		workerID:   workerID,
		heartbeat:  heartbeat,
		interval:   interval,
		batch:      batch,
	}
}

func (m *Manager) Add(lease bridge.Lease, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, _ := time.Parse(time.RFC3339Nano, lease.LeaseUntil)
	m.leases[lease.JobUUID] = &Lease{
		JobUUID:        lease.JobUUID,
		Queue:          lease.Queue,
		Handler:        lease.Handler,
		Attempt:        lease.Attempt,
		LeaseToken:     lease.LeaseToken,
		LeaseUntil:     until,
		TimeoutSeconds: lease.TimeoutSeconds,
		TraceID:        lease.TraceID,
		cancel:         cancel,
	}
}

func (m *Manager) Remove(jobUUID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.leases, jobUUID)
}

func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.leases)
}

func (m *Manager) Active() []bridge.LeaseRef {
	m.mu.Lock()
	defer m.mu.Unlock()
	refs := make([]bridge.LeaseRef, 0, len(m.leases))
	for _, lease := range m.leases {
		refs = append(refs, bridge.LeaseRef{JobUUID: lease.JobUUID, Attempt: lease.Attempt, LeaseToken: lease.LeaseToken})
	}
	return refs
}

// Run sends batched heartbeats until ctx is cancelled. A stale, missing or
// erroring lease cancels the local worker for that job so it cannot continue
// after losing its fencing token.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.heartbeatOnce(ctx)
		}
	}
}

func (m *Manager) heartbeatOnce(ctx context.Context) {
	refs := m.Active()
	if len(refs) == 0 {
		return
	}
	for start := 0; start < len(refs); start += m.batch {
		end := start + m.batch
		if end > len(refs) {
			end = len(refs)
		}
		request := bridge.HeartbeatRequest{
			Schema:     bridge.SchemaVersion,
			NodeID:     m.nodeID,
			InstanceID: m.instanceID,
			Leases:     refs[start:end],
		}
		response, err := m.heartbeat.Heartbeat(ctx, request)
		if err != nil {
			m.cancelBatch(refs[start:end], "heartbeat transport failed")
			continue
		}
		if response.Error != nil {
			m.cancelBatch(refs[start:end], response.Error.Code)
			continue
		}
		m.mu.Lock()
		for _, result := range response.Results {
			lease, ok := m.leases[result.JobUUID]
			if !ok {
				continue
			}
			switch result.Status {
			case bridge.LeaseExtended:
				if until, err := time.Parse(time.RFC3339Nano, result.LeaseUntil); err == nil {
					lease.LeaseUntil = until
				}
			case bridge.LeaseStale, bridge.LeaseNotFound, bridge.LeaseError:
				if lease.cancel != nil {
					lease.cancel()
				}
				delete(m.leases, result.JobUUID)
			}
		}
		m.mu.Unlock()
	}
}

func (m *Manager) cancelBatch(refs []bridge.LeaseRef, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ref := range refs {
		if lease, ok := m.leases[ref.JobUUID]; ok && lease.cancel != nil {
			lease.cancel()
		}
		delete(m.leases, ref.JobUUID)
	}
	_ = reason
}

func (m *Manager) WorkerID() string { return m.workerID }
