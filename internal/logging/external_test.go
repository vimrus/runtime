package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeExternalBackup(t *testing.T, path string, at time.Time) string {
	t.Helper()
	backup := path + "-" + at.UTC().Format("20060102T150405.000000000Z") + ".log"
	if err := os.WriteFile(backup, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return backup
}

func TestExternalRotatorCopiesAndTruncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "php-error.log")
	content := strings.Repeat("x", 2048) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rotator := NewExternalRotator(path, 1024, 5, 0)

	rotated, err := rotator.RotateIfNeeded()
	if err != nil || !rotated {
		t.Fatalf("expected rotation, got rotated=%v err=%v", rotated, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("active file was not truncated: %d bytes", info.Size())
	}
	backups, err := filepath.Glob(path + "-*.log")
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected exactly one backup, got %v (err=%v)", backups, err)
	}
	data, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatalf("backup content mismatch: %d bytes", len(data))
	}

	rotated, err = rotator.RotateIfNeeded()
	if err != nil || rotated {
		t.Fatalf("small file must not rotate again: rotated=%v err=%v", rotated, err)
	}
}

func TestExternalRotatorMissingFileIsNoop(t *testing.T) {
	rotator := NewExternalRotator(filepath.Join(t.TempDir(), "missing.log"), 1024, 5, 0)
	if rotated, err := rotator.RotateIfNeeded(); err != nil || rotated {
		t.Fatalf("missing file must be a no-op: rotated=%v err=%v", rotated, err)
	}
	if err := rotator.Prune(); err != nil {
		t.Fatalf("prune on missing file must be a no-op: %v", err)
	}
}

func TestExternalRotatorPruneKeepsNewestBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "php-error.log")
	now := time.Now().UTC()
	oldest := writeExternalBackup(t, path, now.Add(-3*time.Hour))
	middle := writeExternalBackup(t, path, now.Add(-2*time.Hour))
	newest := writeExternalBackup(t, path, now.Add(-1*time.Hour))

	rotator := NewExternalRotator(path, 0, 2, 0)
	if err := rotator.Prune(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Fatalf("oldest backup should be removed: %v", err)
	}
	for _, keep := range []string{middle, newest} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("backup %s should be kept: %v", keep, err)
		}
	}
}

func TestExternalRotatorPruneByDays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "php-error.log")
	now := time.Now().UTC()
	expired := writeExternalBackup(t, path, now.Add(-48*time.Hour))
	fresh := writeExternalBackup(t, path, now.Add(-1*time.Hour))

	rotator := NewExternalRotator(path, 0, 0, 1)
	if err := rotator.Prune(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("expired backup should be removed: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh backup should be kept: %v", err)
	}
}
