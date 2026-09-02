package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileAuditorRotatesAndSyncs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit-node-a.jsonl")
	auditor, err := NewFileAuditorWithRetention(path, 128, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		auditor.Record(AuditEntry{
			Time:       time.Now().UTC(),
			Operation:  "status",
			Peer:       Peer{UID: 1000, GID: 1000, PID: 1},
			OK:         true,
			DurationMs: 1,
		})
	}
	if err := auditor.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	rotated := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "audit-node-a.jsonl-") && strings.HasSuffix(entry.Name(), ".log") {
			rotated++
		}
	}
	if rotated == 0 {
		t.Fatal("audit log did not rotate")
	}
	if rotated > 2 {
		t.Fatalf("audit rotated backups = %d, want at most 2", rotated)
	}
}

func TestFileAuditorDiscardWithoutPath(t *testing.T) {
	auditor, err := NewFileAuditorWithRetention("", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	auditor.Record(AuditEntry{Time: time.Now().UTC(), Operation: "status", OK: true})
	if err := auditor.Close(); err != nil {
		t.Fatal(err)
	}
}
