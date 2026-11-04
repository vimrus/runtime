package olap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const ManifestSchemaVersion = 1

var (
	tableNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	bucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	sha256Pattern     = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// Part describes one immutable Parquet file visible through a Manifest.
type Part struct {
	Path   string `json:"path"`
	Rows   uint64 `json:"rows"`
	SHA256 string `json:"sha256"`
}

// Manifest atomically selects the visible parts for one logical OLAP table.
type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	Table         string            `json:"table"`
	Generation    uint64            `json:"generation"`
	PublishedAt   time.Time         `json:"publishedAt"`
	SyncID        string            `json:"syncID,omitempty"`
	RowCount      uint64            `json:"rowCount"`
	WatermarkID   int64             `json:"watermarkID,omitempty"`
	WatermarkTime *time.Time        `json:"watermarkTime,omitempty"`
	Buckets       map[string][]Part `json:"buckets"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// Validate checks all fields required by the query and retention components.
func (m Manifest) Validate() error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("unsupported OLAP manifest schema version %d", m.SchemaVersion)
	}
	if !tableNamePattern.MatchString(m.Table) {
		return fmt.Errorf("invalid OLAP table name %q", m.Table)
	}
	if m.Generation == 0 {
		return errors.New("OLAP manifest generation must be positive")
	}
	if m.PublishedAt.IsZero() {
		return errors.New("OLAP manifest publishedAt is required")
	}
	if len(m.Buckets) == 0 {
		return errors.New("OLAP manifest must contain at least one bucket")
	}

	var totalRows uint64
	seen := make(map[string]struct{})
	for bucket, parts := range m.Buckets {
		if !bucketNamePattern.MatchString(bucket) {
			return fmt.Errorf("invalid OLAP bucket name %q", bucket)
		}
		if len(parts) == 0 {
			return fmt.Errorf("OLAP bucket %q has no parts", bucket)
		}
		for _, part := range parts {
			if err := validatePartPath(part.Path); err != nil {
				return fmt.Errorf("bucket %q: %w", bucket, err)
			}
			if !sha256Pattern.MatchString(part.SHA256) {
				return fmt.Errorf("part %q has invalid sha256", part.Path)
			}
			if _, exists := seen[part.Path]; exists {
				return fmt.Errorf("duplicate OLAP part %q", part.Path)
			}
			seen[part.Path] = struct{}{}
			totalRows += part.Rows
		}
	}
	if m.RowCount != totalRows {
		return fmt.Errorf("OLAP manifest rowCount %d does not match part rows %d", m.RowCount, totalRows)
	}
	return nil
}

func validatePartPath(path string) error {
	if path == "" || filepath.IsAbs(path) {
		return errors.New("OLAP part path must be relative")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return fmt.Errorf("OLAP part path must not escape the dataset: %q", path)
	}
	if !strings.HasSuffix(clean, ".parquet") {
		return fmt.Errorf("OLAP part must be a .parquet file: %q", path)
	}
	return nil
}

// ReadManifest loads and validates one table Manifest.
func ReadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode OLAP manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validate OLAP manifest: %w", err)
	}
	return manifest, nil
}

// WriteManifestAtomic writes a validated Manifest and atomically replaces the
// current generation on the same filesystem.
func WriteManifestAtomic(path string, manifest Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if filepath.Ext(path) != ".json" {
		return errors.New("OLAP manifest target must end with .json")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create OLAP manifest directory: %w", err)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode OLAP manifest: %w", err)
	}
	data = append(data, '\n')

	temp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create OLAP manifest temp file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if err := temp.Chmod(0o640); err != nil {
		temp.Close()
		return fmt.Errorf("set OLAP manifest permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write OLAP manifest: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync OLAP manifest: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close OLAP manifest: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish OLAP manifest: %w", err)
	}
	return nil
}

// PartPaths returns the sorted active part paths of a Manifest.
func (m Manifest) PartPaths() []string {
	paths := make([]string, 0)
	for _, parts := range m.Buckets {
		for _, part := range parts {
			paths = append(paths, part.Path)
		}
	}
	sort.Strings(paths)
	return paths
}
