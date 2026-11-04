package cluster

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRegistrar struct {
	response RegistrationResponse
	err      error
	calls    int
}

func (f *fakeRegistrar) Register(context.Context, Profile) (RegistrationResponse, error) {
	f.calls++
	return f.response, f.err
}

func TestControllerAdmissionStates(t *testing.T) {
	gate := NewGate()
	registrar := &fakeRegistrar{response: RegistrationResponse{SchemaVersion: 1, Status: "ready", ProfileGeneration: 4}}
	controller := NewController([]Target{{AppID: "zentao", Registrar: registrar, Gate: gate}}, time.Minute, 5*time.Minute, 10*time.Minute)
	if !controller.TryAll(context.Background(), Profile{SchemaVersion: 1}) {
		t.Fatal("ready response must open the gate")
	}
	if !gate.Allows() || gate.Status().Generation != 4 {
		t.Fatalf("unexpected gate status: %#v", gate.Status())
	}

	registrar.response = RegistrationResponse{
		SchemaVersion:   1,
		Status:          "mismatch",
		MismatchDomains: []string{"olap", "observability"},
	}
	if controller.TryAll(context.Background(), Profile{SchemaVersion: 1}) {
		t.Fatal("mismatch response must update the diagnostic state")
	}
	if gate.Status().State != StateMismatch {
		t.Fatalf("unexpected mismatch state: %#v", gate.Status())
	}
}

func TestControllerKeepsReadyNodeDuringGrace(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	gate := NewGate()
	registrar := &fakeRegistrar{response: RegistrationResponse{SchemaVersion: 1, Status: "ready", ProfileGeneration: 1}}
	controller := NewController([]Target{{AppID: "zentao", Registrar: registrar, Gate: gate}}, time.Minute, 5*time.Minute, 10*time.Minute)
	controller.Now = func() time.Time { return now }
	controller.TryAll(context.Background(), Profile{SchemaVersion: 1})

	registrar.err = errors.New("temporary bridge failure")
	now = now.Add(5 * time.Minute)
	controller.TryAll(context.Background(), Profile{SchemaVersion: 1})
	if !gate.Allows() {
		t.Fatal("ready node must stay open inside stale grace")
	}

	now = now.Add(6 * time.Minute)
	controller.TryAll(context.Background(), Profile{SchemaVersion: 1})
	if gate.Allows() || gate.Status().State != StateStale {
		t.Fatalf("ready node must close after stale grace: %#v", gate.Status())
	}
}

func TestControllerRebuildsProfileOnIndependentInterval(t *testing.T) {
	controller := NewController(nil, 10*time.Second, time.Minute, 3*time.Minute)
	lastBuild := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

	if controller.shouldRebuildProfile(lastBuild.Add(30*time.Second), lastBuild) {
		t.Fatal("profile must be reused between heartbeat and profile intervals")
	}
	if !controller.shouldRebuildProfile(lastBuild.Add(time.Minute), lastBuild) {
		t.Fatal("profile must be rebuilt when the configured interval is reached")
	}
	if !controller.shouldRebuildProfile(lastBuild.Add(time.Minute+time.Nanosecond), lastBuild) {
		t.Fatal("profile must be rebuilt after the configured interval")
	}
	if !controller.shouldRebuildProfile(lastBuild, time.Time{}) {
		t.Fatal("the first round must always build a profile")
	}
}
