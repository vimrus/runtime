//go:build !windows

package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testHandler struct{}

func (testHandler) HandleControl(_ context.Context, request Request) Response {
	if request.Operation == "status" {
		return Success(map[string]string{"state": "ready"})
	}
	return Failure("unknown_operation", "unsupported")
}

func TestSocketCallAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.sock")
	server, err := Listen(path, testHandler{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions = %o, want 600", info.Mode().Perm())
	}
	response, err := Call(context.Background(), path, Request{Operation: "status"})
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK || string(response.Result) != `{"state":"ready"}` {
		t.Fatalf("unexpected response: %#v", response)
	}
}

type recordingAuditor struct {
	entries []AuditEntry
}

func (a *recordingAuditor) Record(entry AuditEntry) {
	a.entries = append(a.entries, entry)
}

type denyingAuthorizer struct{}

func (denyingAuthorizer) Authorize(Peer) error {
	return os.ErrPermission
}

func TestSocketAuthorizationAndAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.sock")
	auditor := &recordingAuditor{}
	server, err := ListenWithOptions(path, testHandler{}, Options{Authorizer: denyingAuthorizer{}, Auditor: auditor})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	response, err := Call(context.Background(), path, Request{Operation: "status"})
	if err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error == nil || response.Error.Code != "forbidden" {
		t.Fatalf("expected forbidden response, got %#v", response)
	}
	if len(auditor.entries) != 1 || auditor.entries[0].Operation != "status" || auditor.entries[0].OK {
		t.Fatalf("expected one failed status audit entry, got %#v", auditor.entries)
	}
	if auditor.entries[0].Time.IsZero() || auditor.entries[0].DurationMs < 0 {
		t.Fatalf("audit entry missing timing: %#v", auditor.entries[0])
	}
}

func TestFileAuditorWritesJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	auditor, err := NewFileAuditor(path)
	if err != nil {
		t.Fatal(err)
	}
	defer auditor.Close()
	auditor.Record(AuditEntry{Time: time.Now().UTC(), Operation: "status", Peer: Peer{UID: 1000}, OK: true})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("audit log must contain newline-delimited JSON, got %q", data)
	}
}
