package health

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Kind classifies a health component. Liveness is the Runtime event loop,
// readiness covers components required before traffic is accepted, and deep
// components are only evaluated on explicit deep health requests.
type Kind string

const (
	KindRuntime    Kind = "runtime"
	KindPHP        Kind = "php"
	KindApp        Kind = "app"
	KindDependency Kind = "dependency"
	KindShared     Kind = "shared"
)

type Status string

const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded"
	StatusFailed   Status = "failed"
	StatusUnknown  Status = "unknown"
)

// Result is the outcome of a single component check.
type Result struct {
	Name      string    `json:"name"`
	Kind      Kind      `json:"kind"`
	Status    Status    `json:"status"`
	Message   string    `json:"message,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// CheckFunc performs one bounded check. It must honor ctx cancellation.
type CheckFunc func(ctx context.Context) Result

// Registry holds named component probes and derives layered reports.
type Registry struct {
	mu    sync.RWMutex
	items map[string]component
}

type component struct {
	name      string
	kind      Kind
	required  bool // required for Readiness
	deep      bool // only evaluated on deep health
	check     CheckFunc
	last      Result
	lastError error
}

func NewRegistry() *Registry {
	return &Registry{items: make(map[string]component)}
}

// Register adds or replaces a component probe. required components gate
// Readiness; deep components are only probed by DeepReport.
func (r *Registry) Register(name string, kind Kind, required bool, deep bool, check CheckFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[name] = component{name: name, kind: kind, required: required, deep: deep, check: check}
}

// Component returns the last known result without running the probe.
func (r *Registry) Component(name string) (Result, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.items[name]
	if !ok {
		return Result{}, false
	}
	if item.last.Name == "" {
		return Result{Name: name, Kind: item.kind, Status: StatusUnknown, CheckedAt: time.Now().UTC()}, true
	}
	return item.last, true
}

// Probe runs all non-deep components once and returns a snapshot.
func (r *Registry) Probe(ctx context.Context) Snapshot {
	return r.probe(ctx, false)
}

// DeepProbe runs every component, including deep-only probes.
func (r *Registry) DeepProbe(ctx context.Context) Snapshot {
	return r.probe(ctx, true)
}

func (r *Registry) probe(ctx context.Context, deep bool) Snapshot {
	r.mu.RLock()
	items := make([]component, 0, len(r.items))
	for _, item := range r.items {
		if deep || !item.deep {
			items = append(items, item)
		}
	}
	r.mu.RUnlock()

	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	results := make([]Result, 0, len(items))
	requiredFailed := false
	anyFailed := false
	anyDegraded := false
	checked := 0
	for _, item := range items {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		result := item.check(ctx)
		cancel()
		now := time.Now().UTC()
		if result.CheckedAt.IsZero() {
			result.CheckedAt = now
		}
		r.mu.Lock()
		item.last = result
		r.items[item.name] = item
		r.mu.Unlock()
		checked++
		results = append(results, result)
		switch result.Status {
		case StatusFailed:
			anyFailed = true
			if item.required {
				requiredFailed = true
			}
		case StatusDegraded:
			anyDegraded = true
		}
	}
	return Snapshot{
		CheckedAt:      time.Now().UTC(),
		Components:     results,
		Checked:        checked,
		RequiredFailed: requiredFailed,
		AnyFailed:      anyFailed,
		AnyDegraded:    anyDegraded,
	}
}

// Snapshot is a point-in-time view of component health.
type Snapshot struct {
	CheckedAt      time.Time `json:"checkedAt"`
	Components     []Result  `json:"components"`
	Checked        int       `json:"checked"`
	RequiredFailed bool      `json:"requiredFailed"`
	AnyFailed      bool      `json:"anyFailed"`
	AnyDegraded    bool      `json:"anyDegraded"`
}

// Ready is true when every required component is healthy.
func (s Snapshot) Ready() bool { return !s.RequiredFailed }

// Live is true when the Runtime event loop is still functioning. It is
// derived by the host lifecycle rather than component probes; this helper
// treats any non-failed snapshot as live for callers that only have probes.
func (s Snapshot) Live() bool { return !s.AnyFailed || s.AnyDegraded }
