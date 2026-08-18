package lifecycle

import "testing"

func TestStateMachine(t *testing.T) {
	machine := New()
	for _, state := range []State{ConfigLoaded, DependenciesStarting, CaddyStarting, Ready, Reloading, Ready, Draining, Stopped} {
		if err := machine.Transition(state, "test"); err != nil {
			t.Fatalf("transition to %s: %v", state, err)
		}
	}
	if got := machine.Snapshot().State; got != Stopped {
		t.Fatalf("state = %s, want %s", got, Stopped)
	}
}

func TestStateMachineRejectsInvalidTransition(t *testing.T) {
	machine := New()
	if err := machine.Transition(Ready, "skip bootstrap"); err == nil {
		t.Fatal("expected invalid transition error")
	}
}
