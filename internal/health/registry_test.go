package health

import (
	"context"
	"testing"
	"time"
)

func TestProbeDistinguishesRequiredAndDeepComponents(t *testing.T) {
	registry := NewRegistry()
	registry.Register("runtime", KindRuntime, true, false, func(ctx context.Context) Result {
		return Result{Name: "runtime", Kind: KindRuntime, Status: StatusOK}
	})
	registry.Register("php", KindPHP, true, false, func(ctx context.Context) Result {
		return Result{Name: "php", Kind: KindPHP, Status: StatusOK}
	})
	registry.Register("session-nfs", KindShared, true, true, func(ctx context.Context) Result {
		return Result{Name: "session-nfs", Kind: KindShared, Status: StatusFailed, Message: "NFS read-only"}
	})

	probe := registry.Probe(context.Background())
	if !probe.Ready() || len(probe.Components) != 2 {
		t.Fatalf("shallow probe must skip deep components: %#v", probe)
	}
	deep := registry.DeepProbe(context.Background())
	if deep.Ready() || len(deep.Components) != 3 {
		t.Fatalf("deep probe must include shared dependency: %#v", deep)
	}
}

func TestProbeCachesLastResult(t *testing.T) {
	registry := NewRegistry()
	var calls int
	registry.Register("app", KindApp, false, false, func(ctx context.Context) Result {
		calls++
		return Result{Name: "app", Kind: KindApp, Status: StatusOK, CheckedAt: time.Now().UTC()}
	})
	registry.Probe(context.Background())
	registry.Probe(context.Background())
	if calls != 2 {
		t.Fatalf("probe calls = %d, want 2", calls)
	}
	component, ok := registry.Component("app")
	if !ok || component.Status != StatusOK {
		t.Fatalf("cached component missing: %#v", component)
	}
}
