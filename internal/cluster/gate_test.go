package cluster

import "testing"

func TestGatesRequireEveryApplication(t *testing.T) {
	gates := NewGates([]string{"zentao", "admin"})
	if gates.AllReady() {
		t.Fatal("new gates must start closed")
	}
	gates.SetReady("zentao", 3)
	if gates.AllReady() {
		t.Fatal("one ready application must not open the whole node")
	}
	gates.SetReady("admin", 4)
	if !gates.AllReady() {
		t.Fatal("all ready applications must open the node")
	}
	select {
	case <-gates.Ready():
	default:
		t.Fatal("ready channel must close after initial readiness")
	}
	gates.Set("admin", StateStale, "heartbeat expired")
	if gates.For("admin").Allows() {
		t.Fatal("stale gate must reject traffic")
	}
}
