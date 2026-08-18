// Package lifecycle owns the Runtime Host state machine.
package lifecycle

import (
	"fmt"
	"sync"
	"time"
)

type State string

const (
	Created              State = "created"
	ConfigLoaded         State = "config_loaded"
	DependenciesStarting State = "dependencies_starting"
	CaddyStarting        State = "caddy_starting"
	Ready                State = "ready"
	Reloading            State = "reloading"
	Degraded             State = "degraded"
	Draining             State = "draining"
	Stopped              State = "stopped"
	Failed               State = "failed"
)

type Snapshot struct {
	State     State     `json:"state"`
	ChangedAt time.Time `json:"changedAt"`
	Reason    string    `json:"reason,omitempty"`
}

type Machine struct {
	mu       sync.RWMutex
	snapshot Snapshot
}

func New() *Machine {
	return &Machine{snapshot: Snapshot{State: Created, ChangedAt: time.Now().UTC()}}
}

func (m *Machine) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshot
}

func (m *Machine) Transition(next State, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !allowed(m.snapshot.State, next) {
		return fmt.Errorf("invalid lifecycle transition %q -> %q", m.snapshot.State, next)
	}
	m.snapshot = Snapshot{State: next, ChangedAt: time.Now().UTC(), Reason: reason}
	return nil
}

func (m *Machine) Fail(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshot = Snapshot{State: Failed, ChangedAt: time.Now().UTC(), Reason: reason}
}

func allowed(current, next State) bool {
	if current == next {
		return true
	}
	switch current {
	case Created:
		return next == ConfigLoaded || next == Failed
	case ConfigLoaded:
		return next == DependenciesStarting || next == Failed
	case DependenciesStarting:
		return next == CaddyStarting || next == Failed
	case CaddyStarting:
		return next == Ready || next == Failed
	case Ready:
		return next == Reloading || next == Draining
	case Reloading:
		return next == Ready || next == Degraded
	case Degraded:
		return next == Reloading || next == Draining
	case Draining:
		return next == Stopped || next == Failed
	}
	return false
}
