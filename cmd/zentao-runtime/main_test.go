package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vimrus/runtime/internal/config"
)

func TestResolveNodeIDUsesConfiguredValue(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{}
	cfg.Runtime.NodeID = "node-a"

	id, err := resolveNodeID(cfg, root)
	if err != nil {
		t.Fatalf("resolveNodeID() error = %v", err)
	}
	if id != "node-a" {
		t.Fatalf("resolveNodeID() = %q, want %q", id, "node-a")
	}
	if _, err := os.Stat(filepath.Join(root, "run")); !os.IsNotExist(err) {
		t.Fatalf("run directory should not be created when node ID is configured, stat err = %v", err)
	}
}

func TestResolveNodeIDGeneratesAndReusesRunFile(t *testing.T) {
	root := t.TempDir()

	id, err := resolveNodeID(config.Config{}, root)
	if err != nil {
		t.Fatalf("resolveNodeID() error = %v", err)
	}
	if !strings.HasPrefix(id, "node-") {
		t.Fatalf("resolveNodeID() = %q, want node- prefix", id)
	}

	nodeFile := filepath.Join(root, "run", "node-id")
	raw, err := os.ReadFile(nodeFile)
	if err != nil {
		t.Fatalf("read node file: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != id {
		t.Fatalf("node file content = %q, want %q", got, id)
	}
	info, err := os.Stat(nodeFile)
	if err != nil {
		t.Fatalf("stat node file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("node file mode = %v, want %v", info.Mode().Perm(), os.FileMode(0o600))
	}

	again, err := resolveNodeID(config.Config{}, root)
	if err != nil {
		t.Fatalf("resolveNodeID() second call error = %v", err)
	}
	if again != id {
		t.Fatalf("resolveNodeID() second call = %q, want %q", again, id)
	}
}

func TestResolveNodeIDRejectsInvalidFile(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "node-id"), []byte("bad id!\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveNodeID(config.Config{}, root); err == nil {
		t.Fatal("resolveNodeID() expected error for invalid node id file")
	}
}
