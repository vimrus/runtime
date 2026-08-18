package logging

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	rotator, err := NewRotatingFile(path, 64, 2)
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
		if strings.HasSuffix(entry.Name(), ".rotated") {
			rotated++
		}
	}
	if rotated > 2 {
		t.Fatalf("rotated backups = %d, want at most 2", rotated)
	}
}
