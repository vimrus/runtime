package bi

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BI staging is produced by the PHP exporter before the publish request. The
// cron timeout is two hours, so staging retention must exceed that even when
// the caller keeps a shorter grace period for replaced Parquet parts.
const minimumStagingGrace = 3 * time.Hour

// CleanupResult summarizes unreferenced BI files removed by one cleanup pass.
type CleanupResult struct {
	RemovedParts   int
	RemovedTemp    int
	RemovedStaging int
	RemovedBytes   int64
}

// Cleanup removes unreferenced parts, stale temporary files and abandoned
// staging directories older than the query grace period.
func (p *Publisher) Cleanup(now time.Time, grace time.Duration) (CleanupResult, error) {
	if now.IsZero() {
		now = time.Now()
	}
	if grace < 0 {
		return CleanupResult{}, errors.New("BI cleanup grace period must not be negative")
	}

	manifests, err := LoadManifests(p.config.DatasetRoot)
	if err != nil {
		return CleanupResult{}, err
	}
	active := make(map[string]struct{})
	for _, manifest := range manifests {
		for _, part := range manifest.PartPaths() {
			active[filepath.ToSlash(filepath.Clean(filepath.FromSlash(part)))] = struct{}{}
		}
	}

	cutoff := now.Add(-grace)
	stagingGrace := grace
	if stagingGrace < minimumStagingGrace {
		stagingGrace = minimumStagingGrace
	}
	stagingCutoff := now.Add(-stagingGrace)
	var result CleanupResult
	tablesRoot := filepath.Join(p.config.DatasetRoot, "tables")
	err = filepath.WalkDir(tablesRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.ModTime().Before(cutoff) {
			return nil
		}

		relative, err := filepath.Rel(p.config.DatasetRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(filepath.Clean(relative))
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".parquet" {
			if _, exists := active[relative]; exists {
				return nil
			}
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove unreferenced BI part: %w", err)
			}
			result.RemovedParts++
			result.RemovedBytes += info.Size()
			return nil
		}
		if strings.HasSuffix(strings.ToLower(path), ".parquet.tmp") {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove stale BI temporary part: %w", err)
			}
			result.RemovedTemp++
			result.RemovedBytes += info.Size()
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}

	entries, err := os.ReadDir(p.config.StagingRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(p.config.StagingRoot, entry.Name())
		newest, size, err := directoryUsage(path)
		if err != nil {
			return result, err
		}
		if newest.Before(stagingCutoff) {
			if err := os.RemoveAll(path); err != nil {
				return result, fmt.Errorf("remove stale BI staging: %w", err)
			}
			result.RemovedStaging++
			result.RemovedBytes += size
		}
	}

	return result, nil
}

func directoryUsage(root string) (time.Time, int64, error) {
	var newest time.Time
	var size int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if !entry.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return newest, size, err
}
