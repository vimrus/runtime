package bi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

const DatasetSchemaVersion = 1

type Mode string

const (
	ModeSnapshot Mode = "snapshot"
	ModeAppend   Mode = "append"
	ModeRewrite  Mode = "rewrite"
)

var (
	columnNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	syncIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// Column describes one staged BI column using a restricted DuckDB type.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable,omitempty"`
}

// Schema describes the staged rows of one logical table.
type Schema struct {
	Version    int      `json:"version"`
	Table      string   `json:"table"`
	PrimaryKey string   `json:"primaryKey"`
	Columns    []Column `json:"columns"`
}

// Validate checks table, primary key, column names and SQL types.
func (s Schema) Validate() error {
	if s.Version != DatasetSchemaVersion {
		return fmt.Errorf("unsupported BI schema version %d", s.Version)
	}
	if !tableNamePattern.MatchString(s.Table) {
		return fmt.Errorf("invalid BI table %q", s.Table)
	}
	if !columnNamePattern.MatchString(s.PrimaryKey) {
		return fmt.Errorf("invalid BI primary key %q", s.PrimaryKey)
	}
	if len(s.Columns) == 0 {
		return errors.New("BI schema must define at least one column")
	}

	seen := make(map[string]struct{})
	hasPrimaryKey := false
	for _, column := range s.Columns {
		if !columnNamePattern.MatchString(column.Name) {
			return fmt.Errorf("invalid BI column name %q", column.Name)
		}
		if _, exists := seen[column.Name]; exists {
			return fmt.Errorf("duplicate BI column %q", column.Name)
		}
		seen[column.Name] = struct{}{}
		if column.Name == s.PrimaryKey {
			hasPrimaryKey = true
		}
		if _, err := normalizeColumnType(column.Type); err != nil {
			return fmt.Errorf("column %s: %w", column.Name, err)
		}
	}
	if !hasPrimaryKey {
		return fmt.Errorf("BI primary key %q is not present in columns", s.PrimaryKey)
	}
	return nil
}

func normalizeColumnType(value string) (string, error) {
	upper := strings.ToUpper(strings.TrimSpace(value))
	switch upper {
	case "BOOLEAN", "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT", "DOUBLE", "VARCHAR", "JSON", "DATE", "TIMESTAMP":
		return upper, nil
	}
	if match := regexp.MustCompile(`^DECIMAL\(([1-9][0-9]?),(0|[1-9][0-9]?)\)$`).FindStringSubmatch(upper); match != nil {
		precision, _ := strconv.Atoi(match[1])
		scale, _ := strconv.Atoi(match[2])
		if precision >= 1 && precision <= 38 && scale <= precision {
			return upper, nil
		}
	}
	return "", fmt.Errorf("unsupported BI column type %q", value)
}

// PublishRequest describes one immutable staging publish.
type PublishRequest struct {
	SchemaVersion      int        `json:"schemaVersion"`
	Table              string     `json:"table"`
	Mode               Mode       `json:"mode"`
	SyncID             string     `json:"syncID"`
	Bucket             int64      `json:"bucket,omitempty"`
	ExpectedGeneration uint64     `json:"expectedGeneration"`
	WatermarkID        int64      `json:"watermarkID,omitempty"`
	WatermarkTime      *time.Time `json:"watermarkTime,omitempty"`
	Schema             Schema     `json:"schema"`
}

// PublishResult returns the newly visible Manifest.
type PublishResult struct {
	Manifest Manifest `json:"manifest"`
	Rows     uint64   `json:"rows"`
}

// ParquetWriter converts staged newline-delimited JSON files into one part.
type ParquetWriter interface {
	WriteParquet(ctx context.Context, schema Schema, stagingFiles []string, targetPath string) (uint64, error)
}

type PublisherConfig struct {
	DatasetRoot     string
	StagingRoot     string
	MaxStagingBytes int64
}

// Publisher owns Manifest replacement for one application dataset.
type Publisher struct {
	config PublisherConfig
	writer ParquetWriter
	now    func() time.Time
	locks  sync.Map
}

func NewPublisher(config PublisherConfig, writer ParquetWriter) (*Publisher, error) {
	if config.DatasetRoot == "" || config.StagingRoot == "" {
		return nil, errors.New("BI dataset and staging roots are required")
	}
	if !filepath.IsAbs(config.DatasetRoot) || !filepath.IsAbs(config.StagingRoot) {
		return nil, errors.New("BI dataset and staging roots must be absolute")
	}
	if writer == nil {
		return nil, errors.New("BI Parquet writer is required")
	}
	if config.MaxStagingBytes <= 0 {
		config.MaxStagingBytes = 8 * 1024 * 1024 * 1024
	}
	for _, dir := range []string{filepath.Join(config.DatasetRoot, "manifests"), config.StagingRoot} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create BI publisher directory: %w", err)
		}
	}
	return &Publisher{config: config, writer: writer, now: time.Now}, nil
}

// Publish converts one staging directory and atomically advances one table
// Manifest. Retrying the same SyncID with the expected generation is idempotent.
func (p *Publisher) Publish(ctx context.Context, request PublishRequest) (PublishResult, error) {
	if request.SchemaVersion != DatasetSchemaVersion {
		return PublishResult{}, fmt.Errorf("unsupported BI publish schema version %d", request.SchemaVersion)
	}
	if request.Table != request.Schema.Table {
		return PublishResult{}, errors.New("BI publish table and schema table differ")
	}
	if err := request.Schema.Validate(); err != nil {
		return PublishResult{}, err
	}
	if !syncIDPattern.MatchString(request.SyncID) {
		return PublishResult{}, fmt.Errorf("invalid BI syncID %q", request.SyncID)
	}
	if request.Mode != ModeSnapshot && request.Mode != ModeAppend && request.Mode != ModeRewrite {
		return PublishResult{}, fmt.Errorf("unsupported BI sync mode %q", request.Mode)
	}
	if request.Mode == ModeRewrite && request.Bucket < 0 {
		return PublishResult{}, errors.New("BI rewrite bucket must not be negative")
	}

	lockValue, _ := p.locks.LoadOrStore(request.Table, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	manifestPath := filepath.Join(p.config.DatasetRoot, "manifests", request.Table+".json")
	fileLock := flock.New(manifestPath + ".lock")
	lockContext, cancelLock := context.WithTimeout(ctx, 30*time.Second)
	defer cancelLock()
	locked, err := fileLock.TryLockContext(lockContext, 50*time.Millisecond)
	if err != nil {
		return PublishResult{}, fmt.Errorf("lock BI table: %w", err)
	}
	if !locked {
		return PublishResult{}, errors.New("BI table publish lock is busy")
	}
	defer fileLock.Unlock()

	current, exists, err := readOptionalManifest(manifestPath)
	if err != nil {
		return PublishResult{}, err
	}
	if exists && current.SyncID == request.SyncID && current.Generation == request.ExpectedGeneration+1 {
		return PublishResult{Manifest: current, Rows: current.RowCount}, nil
	}
	if exists && current.Generation != request.ExpectedGeneration {
		return PublishResult{}, fmt.Errorf("BI generation conflict for %s: expected %d, current %d", request.Table, request.ExpectedGeneration, current.Generation)
	}
	if !exists && request.ExpectedGeneration != 0 {
		return PublishResult{}, fmt.Errorf("BI manifest for %s does not exist", request.Table)
	}

	stagingDir := filepath.Join(p.config.StagingRoot, request.SyncID)
	files, err := stagingFiles(p.config.StagingRoot, stagingDir, p.config.MaxStagingBytes)
	if err != nil {
		return PublishResult{}, err
	}

	nextGeneration := request.ExpectedGeneration + 1
	bucket := "all"
	if request.Mode == ModeRewrite {
		bucket = fmt.Sprintf("%06d", request.Bucket)
	}
	relativeDir := filepath.Join("tables", request.Table, fmt.Sprintf("schema=v%d", DatasetSchemaVersion), "bucket="+bucket)
	finalDir := filepath.Join(p.config.DatasetRoot, relativeDir)
	if err := os.MkdirAll(finalDir, 0o750); err != nil {
		return PublishResult{}, fmt.Errorf("create BI part directory: %w", err)
	}
	tempPath := filepath.Join(finalDir, fmt.Sprintf(".part-g%d-%s.parquet.tmp", nextGeneration, request.SyncID))
	_ = os.Remove(tempPath)

	rows, err := p.writer.WriteParquet(ctx, request.Schema, files, tempPath)
	if err != nil {
		return PublishResult{}, fmt.Errorf("write BI Parquet: %w", err)
	}
	checksum, err := fileSHA256(tempPath)
	if err != nil {
		return PublishResult{}, err
	}
	relativePath := filepath.ToSlash(filepath.Join(relativeDir, fmt.Sprintf("part-g%d-%s-%s.parquet", nextGeneration, request.SyncID, checksum[:16])))
	finalPath := filepath.Join(p.config.DatasetRoot, filepath.FromSlash(relativePath))
	if existingChecksum, statErr := fileSHA256(finalPath); statErr == nil {
		if existingChecksum != checksum {
			return PublishResult{}, errors.New("BI part already exists with a different checksum")
		}
		_ = os.Remove(tempPath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return PublishResult{}, statErr
	} else if err := os.Rename(tempPath, finalPath); err != nil {
		return PublishResult{}, fmt.Errorf("publish BI Parquet part: %w", err)
	}
	_ = os.Remove(tempPath)

	part := Part{Path: relativePath, Rows: rows, SHA256: checksum}
	next := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Table:         request.Table,
		Generation:    nextGeneration,
		PublishedAt:   p.now().UTC(),
		SyncID:        request.SyncID,
		WatermarkID:   request.WatermarkID,
		WatermarkTime: request.WatermarkTime,
		Buckets:       map[string][]Part{},
	}
	if exists {
		for name, parts := range current.Buckets {
			next.Buckets[name] = append([]Part(nil), parts...)
		}
	}
	switch request.Mode {
	case ModeSnapshot:
		next.Buckets = map[string][]Part{"all": {part}}
	case ModeAppend:
		next.Buckets["all"] = append(next.Buckets["all"], part)
	case ModeRewrite:
		next.Buckets[bucket] = []Part{part}
	}
	for _, parts := range next.Buckets {
		for _, item := range parts {
			next.RowCount += item.Rows
		}
	}
	if err := WriteManifestAtomic(manifestPath, next); err != nil {
		return PublishResult{}, err
	}
	return PublishResult{Manifest: next, Rows: rows}, nil
}

func readOptionalManifest(path string) (Manifest, bool, error) {
	manifest, err := ReadManifest(path)
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, err
	}
	return manifest, true, nil
}

func stagingFiles(root, directory string, maxBytes int64) ([]string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dirAbs, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if filepath.Dir(dirAbs) != rootAbs {
		return nil, errors.New("BI staging directory escapes staging root")
	}
	entries, err := os.ReadDir(dirAbs)
	if err != nil {
		return nil, fmt.Errorf("read BI staging directory: %w", err)
	}
	files := make([]string, 0)
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".jsonl" && ext != ".ndjson" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		total += info.Size()
		if total > maxBytes {
			return nil, fmt.Errorf("BI staging exceeds %d bytes", maxBytes)
		}
		files = append(files, filepath.Join(dirAbs, entry.Name()))
	}
	if len(files) == 0 {
		return nil, errors.New("BI staging contains no JSONL files")
	}
	sort.Strings(files)
	return files, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
