// Package retention deletes only the calling node's expired partitions from
// the shared Parquet dataset.
package retention

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vimrus/runtime/internal/observability"
)

type Config struct {
	DatasetRoot string
	NodeID      string
	MetricsDays int
	LogDays     int
	FileBudget  int
	TimeBudget  time.Duration
}

type Result struct {
	RemovedFiles  int       `json:"removedFiles"`
	RemovedBytes  int64     `json:"removedBytes"`
	DeletedDirs   int       `json:"deletedDirs"`
	OldestRemoved time.Time `json:"oldestRemoved"`
	NewestRemoved time.Time `json:"newestRemoved"`
}

type Cleaner struct {
	config Config
}

func New(config Config) (*Cleaner, error) {
	if config.DatasetRoot == "" || config.NodeID == "" {
		return nil, errors.New("datasetRoot and nodeID are required")
	}
	if config.MetricsDays <= 0 || config.LogDays <= 0 {
		return nil, errors.New("retention days must be positive")
	}
	if config.FileBudget <= 0 {
		config.FileBudget = 5000
	}
	if config.TimeBudget <= 0 {
		config.TimeBudget = 30 * time.Second
	}
	return &Cleaner{config: config}, nil
}

func (c *Cleaner) CleanOwn(ctx context.Context) (Result, error) {
	var result Result
	deadline := time.Now().Add(c.config.TimeBudget)
	for _, kind := range []observability.Kind{observability.KindMetrics, observability.KindLogs} {
		days := c.config.LogDays
		if kind == observability.KindMetrics {
			days = c.config.MetricsDays
		}
		cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
		dateRoot := filepath.Join(c.config.DatasetRoot, string(kind), "schema=v1")
		dates, err := os.ReadDir(dateRoot)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return result, err
		}
		for _, dateEntry := range dates {
			if !dateEntry.IsDir() {
				continue
			}
			date, err := time.Parse("2006-01-02", strings.TrimPrefix(dateEntry.Name(), "date="))
			if err != nil || !date.Before(cutoff) {
				continue
			}
			hourRoot := filepath.Join(dateRoot, dateEntry.Name())
			hours, err := os.ReadDir(hourRoot)
			if err != nil {
				continue
			}
			for _, hourEntry := range hours {
				if !hourEntry.IsDir() {
					continue
				}
				if _, err := strconv.Atoi(strings.TrimPrefix(hourEntry.Name(), "hour=")); err != nil {
					continue
				}
				nodeDir := filepath.Join(hourRoot, hourEntry.Name(), "node="+c.config.NodeID)
				if err := c.removeNodeDir(ctx, nodeDir, &result); err != nil {
					return result, err
				}
				if result.RemovedFiles >= c.config.FileBudget || time.Now().After(deadline) {
					return result, nil
				}
			}
		}
	}
	return result, nil
}

func (c *Cleaner) removeNodeDir(ctx context.Context, path string, result *Result) error {
	expected := filepath.Join("node=" + c.config.NodeID)
	if filepath.Base(path) != expected || strings.Contains(c.config.NodeID, "/") || strings.Contains(c.config.NodeID, "..") {
		return fmt.Errorf("refusing to clean unexpected path %q", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to clean non-directory or symlink %q", path)
	}
	files, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, file := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if file.IsDir() {
			continue
		}
		full := filepath.Join(path, file.Name())
		fileInfo, err := file.Info()
		if err != nil {
			continue
		}
		if err := os.Remove(full); err != nil {
			return err
		}
		result.RemovedFiles++
		result.RemovedBytes += fileInfo.Size()
		t := fileInfo.ModTime().UTC()
		if result.OldestRemoved.IsZero() || t.Before(result.OldestRemoved) {
			result.OldestRemoved = t
		}
		if t.After(result.NewestRemoved) {
			result.NewestRemoved = t
		}
	}
	if err := os.Remove(path); err == nil {
		result.DeletedDirs++
	}
	return nil
}
