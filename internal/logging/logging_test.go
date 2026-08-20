package logging

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedactingHandlerHidesSensitiveAttributes(t *testing.T) {
	var buffer bytes.Buffer
	handler := newRedactingHandler(slog.NewJSONHandler(&buffer, nil))
	logger := slog.New(handler)
	logger.Info("request", slog.String("session_id", "s3cr3t"), slog.String("message", "visible"))
	output := buffer.String()
	if strings.Contains(output, "s3cr3t") {
		t.Fatalf("sensitive session value leaked: %s", output)
	}
	if !strings.Contains(output, "[REDACTED]") || !strings.Contains(output, "visible") {
		t.Fatalf("expected redaction marker and safe value, got %s", output)
	}
}

func TestRedactingHandlerHidesGroupedValues(t *testing.T) {
	var buffer bytes.Buffer
	handler := newRedactingHandler(slog.NewJSONHandler(&buffer, nil))
	logger := slog.New(handler)
	logger.Info("connection", slog.Group("db", slog.String("user", "zentao"), slog.String("password", "hunter2")))
	output := buffer.String()
	if strings.Contains(output, "hunter2") {
		t.Fatalf("grouped password leaked: %s", output)
	}
}

func TestRotatingFileRotatesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.log")
	rotator, err := NewRotatingFile(path, 64, 2, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rotator.Close()
	for i := 0; i < 10; i++ {
		if _, err := rotator.Write([]byte(strings.Repeat("x", 32))); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	rotated := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "runtime.log-") && strings.HasSuffix(entry.Name(), ".log") {
			rotated++
		}
	}
	if rotated > 2 {
		t.Fatalf("rotated backups = %d, want at most 2", rotated)
	}
}

func TestRotatingFileTimeBasedRotationAndKeepDays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-node-a.jsonl")
	rotator, err := NewRotatingFile(path, 0, 0, time.Hour, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer rotator.Close()
	// Simulate one hour having passed.
	rotator.mu.Lock()
	rotator.lastRoll = time.Now().UTC().Add(-2 * time.Hour)
	rotator.mu.Unlock()
	if _, err := rotator.Write([]byte("line\n")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	segment := ""
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "runtime-node-a.jsonl-") && strings.HasSuffix(entry.Name(), ".log") {
			segment = entry.Name()
		}
	}
	if segment == "" {
		t.Fatal("hourly segment was not created")
	}
	if _, err := time.Parse("20060102T150405.000000000Z", strings.TrimSuffix(strings.TrimPrefix(segment, "runtime-node-a.jsonl-"), ".log")); err != nil {
		t.Fatalf("segment timestamp unparsable: %s", segment)
	}
}

func TestRotatingFilePruneRemovesExpiredSegments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-node-a.jsonl")
	rotator, err := NewRotatingFile(path, 0, 0, 0, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer rotator.Close()
	old := filepath.Join(dir, "runtime-node-a.jsonl-20200101T000000.000000000Z.log")
	recent := filepath.Join(dir, "runtime-node-a.jsonl-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".log")
	for _, name := range []string{old, recent} {
		if err := os.WriteFile(name, []byte("line\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rotator.Prune()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expired segment was not pruned: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent segment must be kept: %v", err)
	}
}
