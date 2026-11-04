package main

import (
	"strings"
	"testing"
)

func TestInstanceIDIsStableAndFitsNodeColumn(t *testing.T) {
	first := instanceID()
	second := instanceID()
	if first == "" || first != second {
		t.Fatalf("instance ID must be stable within one process: %q %q", first, second)
	}
	if len(first) > 64 {
		t.Fatalf("instance ID exceeds zt_servernode.instanceID: len=%d value=%q", len(first), first)
	}
	if !strings.Contains(first, "-") {
		t.Fatalf("instance ID should include a boot suffix: %q", first)
	}
}

func TestServeRejectsRemovedRootFlag(t *testing.T) {
	err := serve([]string{"--root", "/srv/legacy-app"})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("serve --root must be rejected, got %v", err)
	}
}
