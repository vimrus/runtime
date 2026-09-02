package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/vimrus/runtime/internal/logging"
)

// AuditEntry records one administrative operation for the local control plane.
// It never contains credentials, tokens, request payloads or configuration
// secrets.
type AuditEntry struct {
	Time       time.Time `json:"time"`
	Operation  string    `json:"operation"`
	Peer       Peer      `json:"peer"`
	OK         bool      `json:"ok"`
	ErrorCode  string    `json:"errorCode,omitempty"`
	DurationMs int64     `json:"durationMs"`
}

// Auditor receives completed control-plane operations.
type Auditor interface {
	Record(AuditEntry)
}

// DiscardAuditor drops audit entries; used when no audit sink is configured.
type DiscardAuditor struct{}

func (DiscardAuditor) Record(AuditEntry) {}

// FileAuditor appends newline-delimited JSON entries to a local audit log.
type FileAuditor struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	rotating *logging.RotatingFile
}

func NewFileAuditor(path string) (*FileAuditor, error) {
	return NewFileAuditorWithRetention(path, 0, 0, 0)
}

// NewFileAuditorWithRetention opens the audit log with built-in size rotation
// and retention. maxBytes <= 0 disables size rotation, maxBackups limits the
// number of rotated segments, and keepDays (when positive) takes precedence
// over maxBackups.
func NewFileAuditorWithRetention(path string, maxBytes int64, maxBackups int, keepDays int) (*FileAuditor, error) {
	if path == "" {
		return &FileAuditor{}, nil
	}
	dir := path[:len(path)-len(baseName(path))]
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create audit log directory: %w", err)
		}
	}
	rotating, err := logging.NewRotatingFile(path, maxBytes, maxBackups, 0, keepDays)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	return &FileAuditor{path: path, rotating: rotating}, nil
}

func (a *FileAuditor) Record(entry AuditEntry) {
	if a == nil || (a.file == nil && a.rotating == nil) {
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rotating != nil {
		if _, err := a.rotating.Write(append(data, '\n')); err == nil {
			_ = a.rotating.Sync()
		}
		return
	}
	if _, err := a.file.Write(append(data, '\n')); err == nil {
		_ = a.file.Sync()
	}
}

func (a *FileAuditor) Close() error {
	if a == nil || a.file == nil {
		if a != nil && a.rotating != nil {
			return a.rotating.Close()
		}
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.file.Close()
	a.file = nil
	if err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	return nil
}

func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}
