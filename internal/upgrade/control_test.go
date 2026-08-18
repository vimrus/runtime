package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestControllerPrepareApplyStatusRollback(t *testing.T) {
	root := t.TempDir()
	controller, err := NewController(root)
	if err != nil {
		t.Fatal(err)
	}
	releaseV1 := filepath.Join(controller.Layout().AppsDir, "v1")
	releaseV2 := filepath.Join(controller.Layout().AppsDir, "v2")
	for _, release := range []string{releaseV1, releaseV2} {
		if err := os.MkdirAll(filepath.Join(release, "www"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	payload, _ := json.Marshal(map[string]string{"release": releaseV1})
	if _, err := controller.Handle("prepare", payload); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Handle("apply", nil); err != nil {
		t.Fatal(err)
	}
	status, err := controller.Handle("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if status["current"] != releaseV1 {
		t.Fatalf("current = %v, want %s", status["current"], releaseV1)
	}
	if _, err := controller.Handle("rollback", nil); err != nil {
		t.Fatal(err)
	}
	status, err = controller.Handle("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if status["state"] != StateIdle {
		t.Fatalf("state after rollback = %v, want %s", status["state"], StateIdle)
	}
}

func TestControllerRejectsBadPayloads(t *testing.T) {
	root := t.TempDir()
	controller, err := NewController(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Handle("prepare", json.RawMessage(`{"release":""}`)); err == nil {
		t.Fatal("empty release must be rejected")
	}
	if _, err := controller.Handle("apply", nil); err == nil {
		t.Fatal("apply without prepare must be rejected")
	}
	if _, err := controller.Handle("unknown", nil); err == nil {
		t.Fatal("unknown action must be rejected")
	}
}
