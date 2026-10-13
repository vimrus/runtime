package logging

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ExternalRotator bounds a log file written by another process. PHP's
// error_log keeps the file open and cannot be asked to reopen it, so rotation
// copies the current content to a timestamped backup and truncates the active
// file in place (copytruncate). Writes that race the copy/truncate window may
// be lost; that is an accepted trade-off for keeping the file bounded.
type ExternalRotator struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	keepDays   int
}

func NewExternalRotator(path string, maxBytes int64, maxBackups, keepDays int) *ExternalRotator {
	return &ExternalRotator{path: path, maxBytes: maxBytes, maxBackups: maxBackups, keepDays: keepDays}
}

// RotateIfNeeded rotates the active file once its size reaches MaxBytes.
func (r *ExternalRotator) RotateIfNeeded() (bool, error) {
	if r == nil || r.maxBytes <= 0 {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	info, err := os.Stat(r.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.Size() < r.maxBytes {
		return false, nil
	}
	if err := r.rotate(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *ExternalRotator) rotate() error {
	source, err := os.Open(r.path)
	if err != nil {
		return err
	}
	defer source.Close()

	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	backup := r.path + "-" + stamp + ".log"
	target, err := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	return os.Truncate(r.path, 0)
}

// Prune removes rotated backups. KeepDays takes precedence over MaxBackups,
// matching the Runtime JSONL retention semantics.
func (r *ExternalRotator) Prune() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := os.ReadDir(filepath.Dir(r.path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var rotated []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, filepath.Base(r.path)+"-") && strings.HasSuffix(name, ".log") {
			rotated = append(rotated, filepath.Join(filepath.Dir(r.path), name))
		}
	}
	if len(rotated) == 0 {
		return nil
	}

	if r.keepDays > 0 {
		cutoff := time.Now().UTC().Add(-time.Duration(r.keepDays) * 24 * time.Hour)
		for _, backup := range rotated {
			stamp, stampErr := rotationStamp(backup)
			if stampErr == nil && !stamp.Before(cutoff) {
				continue
			}
			if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	}
	if r.maxBackups <= 0 {
		return nil
	}
	sort.Slice(rotated, func(i, j int) bool {
		left, leftErr := rotationStamp(rotated[i])
		right, rightErr := rotationStamp(rotated[j])
		if leftErr != nil || rightErr != nil {
			return rotated[i] < rotated[j]
		}
		return left.Before(right)
	})
	for len(rotated) > r.maxBackups {
		oldest := rotated[0]
		rotated = rotated[1:]
		if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
