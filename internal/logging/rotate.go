package logging

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RotatingFile is a size-limited, append-only log writer. When the active
// file exceeds MaxBytes it is renamed to <name>.<timestamp>.rotated and the
// oldest backups beyond MaxBackups are removed.
type RotatingFile struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	file       *os.File
	size       int64
}

func NewRotatingFile(path string, maxBytes int64, maxBackups int) (*RotatingFile, error) {
	file, err := openAppend(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	return &RotatingFile{path: path, maxBytes: maxBytes, maxBackups: maxBackups, file: file, size: info.Size()}, nil
}

func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

func (r *RotatingFile) Write(data []byte) (int, error) {
	if r == nil || r.file == nil {
		return 0, errors.New("log file is closed")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxBytes > 0 && r.size+int64(len(data)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.file.Write(data)
	r.size += int64(n)
	return n, err
}

func (r *RotatingFile) rotate() error {
	if err := r.file.Close(); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	backup := r.path + "." + stamp + ".rotated"
	if err := os.Rename(r.path, backup); err != nil {
		r.file, _ = openAppend(r.path)
		return err
	}
	file, err := openAppend(r.path)
	if err != nil {
		return err
	}
	r.file = file
	r.size = 0
	return r.prune(backup)
}

func (r *RotatingFile) prune(_ string) error {
	if r.maxBackups <= 0 {
		return nil
	}
	entries, err := os.ReadDir(filepath.Dir(r.path))
	if err != nil {
		return err
	}
	var rotated []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, filepath.Base(r.path)+".") && strings.HasSuffix(name, ".rotated") {
			rotated = append(rotated, filepath.Join(filepath.Dir(r.path), name))
		}
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
		_ = os.Remove(oldest)
	}
	return nil
}

func rotationStamp(path string) (time.Time, error) {
	prefix := filepath.Base(path)
	start := strings.Index(prefix, ".") + 1
	end := strings.LastIndex(prefix, ".rotated")
	if start <= 0 || end <= start {
		return time.Time{}, fmt.Errorf("invalid rotation name %q", path)
	}
	return time.Parse("20060102T150405.000000000Z", prefix[start:end])
}

func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
