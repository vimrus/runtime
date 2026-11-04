package cluster

import (
	"sync"
	"time"
)

// AdmissionState is the local configuration-diagnostic state for an application.
type AdmissionState string

const (
	StateDisabled    AdmissionState = "disabled"
	StateStarting    AdmissionState = "starting"
	StateRegistering AdmissionState = "registering"
	StateChecking    AdmissionState = "checking"
	StateBlocked     AdmissionState = "blocked"
	StateMismatch    AdmissionState = "mismatch"
	StateReady       AdmissionState = "ready"
	StateStale       AdmissionState = "stale"
	StateDraining    AdmissionState = "draining"
)

// AdmissionStatus is a point-in-time Gate snapshot.
type AdmissionStatus struct {
	State       AdmissionState `json:"state"`
	Reason      string         `json:"reason,omitempty"`
	UpdatedAt   time.Time      `json:"updatedAt"`
	LastSuccess time.Time      `json:"lastSuccess,omitempty"`
	Generation  uint64         `json:"generation,omitempty"`
}

// Gate stores one application's current local admission decision.
type Gate struct {
	mu     sync.RWMutex
	status AdmissionStatus
}

func NewGate() *Gate {
	return &Gate{status: AdmissionStatus{State: StateStarting, Reason: "node registration has not completed"}}
}

func (g *Gate) Set(state AdmissionState, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.status.State = state
	g.status.Reason = reason
	g.status.UpdatedAt = time.Now().UTC()
	if state == StateReady {
		g.status.LastSuccess = g.status.UpdatedAt
	}
}

func (g *Gate) SetReady(generation uint64) {
	g.SetReadyAt(generation, time.Now().UTC())
}

func (g *Gate) SetReadyAt(generation uint64, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.status.State = StateReady
	g.status.Reason = ""
	g.status.Generation = generation
	g.status.UpdatedAt = now
	g.status.LastSuccess = now
}

func (g *Gate) Status() AdmissionStatus {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.status
}

func (g *Gate) Allows() bool {
	status := g.Status()
	return status.State == StateReady || status.State == StateDisabled
}

// Gates owns all per-application gates for one Runtime node.
type Gates struct {
	mu        sync.RWMutex
	gates     map[string]*Gate
	ready     chan struct{}
	readyOnce sync.Once
}

func NewGates(appIDs []string) *Gates {
	gates := &Gates{gates: make(map[string]*Gate, len(appIDs)), ready: make(chan struct{})}
	for _, appID := range appIDs {
		gates.gates[appID] = NewGate()
	}
	return gates
}

func (g *Gates) For(appID string) *Gate {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.gates[appID]
}

func (g *Gates) Statuses() map[string]AdmissionStatus {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make(map[string]AdmissionStatus, len(g.gates))
	for appID, gate := range g.gates {
		result[appID] = gate.Status()
	}
	return result
}

func (g *Gates) AllReady() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if len(g.gates) == 0 {
		return false
	}
	for _, gate := range g.gates {
		if !gate.Allows() {
			return false
		}
	}
	return true
}

// Ready is closed after every configured application has become ready once.
func (g *Gates) Ready() <-chan struct{} {
	g.notifyIfReady()
	return g.ready
}

func (g *Gates) notifyIfReady() {
	if g.AllReady() {
		g.readyOnce.Do(func() { close(g.ready) })
	}
}

func (g *Gates) SetReady(appID string, generation uint64) {
	if gate := g.For(appID); gate != nil {
		gate.SetReady(generation)
		g.notifyIfReady()
	}
}

func (g *Gates) Set(appID string, state AdmissionState, reason string) {
	if gate := g.For(appID); gate != nil {
		gate.Set(state, reason)
	}
}
