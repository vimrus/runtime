//go:build duckdb

// Package duckdb provides the DuckDB-backed Parquet writer and query engine.
package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2"

	"github.com/vimrus/runtime/internal/observability"
)

type Writer struct {
	memoryLimit string
	threads     int
}

func NewWriter(memoryLimit string, threads int) (*Writer, error) {
	if memoryLimit == "" {
		memoryLimit = "256MB"
	}
	if threads <= 0 {
		threads = 2
	}
	return &Writer{memoryLimit: memoryLimit, threads: threads}, nil
}

func (w *Writer) WriteParquet(ctx context.Context, kind observability.Kind, targetPath string, events []observability.Event) (int64, error) {
	if err := validateTarget(targetPath); err != nil {
		return 0, err
	}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if err := configure(ctx, db, w.memoryLimit, w.threads); err != nil {
		return 0, err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE events (
		schemaVersion INTEGER NOT NULL,
		eventTime TIMESTAMP NOT NULL,
		ingestTime TIMESTAMP NOT NULL,
		clusterID VARCHAR NOT NULL,
		nodeID VARCHAR NOT NULL,
		bootID VARCHAR NOT NULL,
		eventID VARCHAR NOT NULL,
		source VARCHAR NOT NULL,
		kind VARCHAR NOT NULL,
		level VARCHAR,
		message VARCHAR,
		metricName VARCHAR,
		metricKind VARCHAR,
		value DOUBLE,
		count UBIGINT,
		labels JSON,
		traceID VARCHAR,
		durationMs DOUBLE,
		statusCode INTEGER,
		fields JSON
	)`); err != nil {
		return 0, fmt.Errorf("create DuckDB event table: %w", err)
	}
	statement, err := db.PrepareContext(ctx, `INSERT INTO events VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	for _, event := range events {
		normalized, err := observability.Normalize(event)
		if err != nil {
			statement.Close()
			return 0, err
		}
		if _, err := statement.ExecContext(ctx,
			normalized.SchemaVersion, normalized.EventTime, normalized.IngestTime,
			normalized.ClusterID, normalized.NodeID, normalized.BootID, normalized.EventID,
			normalized.Source, string(normalized.Kind), nullString(normalized.Level),
			nullString(normalized.Message), nullString(normalized.MetricName), nullString(normalized.MetricKind),
			nullableFloat(normalized.Value), nullableUint(normalized.Count),
			nullableJSON(normalized.Labels), nullString(normalized.TraceID),
			nullableFloat(normalized.DurationMs), nullableInt(normalized.StatusCode),
			nullableJSON(normalized.Fields),
		); err != nil {
			statement.Close()
			return 0, fmt.Errorf("insert event %s: %w", normalized.EventID, err)
		}
	}
	statement.Close()
	copySQL := fmt.Sprintf(`COPY (SELECT * FROM events ORDER BY eventTime) TO '%s' (FORMAT PARQUET, COMPRESSION ZSTD)`, escapeLiteral(targetPath))
	if _, err := db.ExecContext(ctx, copySQL); err != nil {
		return 0, fmt.Errorf("write Parquet: %w", err)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func configure(ctx context.Context, db *sql.DB, memoryLimit string, threads int) error {
	for _, setting := range []string{
		"SET memory_limit='" + escapeLiteral(memoryLimit) + "'",
		fmt.Sprintf("SET threads=%d", threads),
		"SET autoinstall_known_extensions=false",
		"SET autoload_known_extensions=false",
	} {
		if _, err := db.ExecContext(ctx, setting); err != nil {
			return fmt.Errorf("configure DuckDB: %w", err)
		}
	}
	return nil
}

func validateTarget(path string) error {
	if path == "" || !strings.HasSuffix(path, ".parquet.tmp") {
		return errors.New("DuckDB writer target must be a .parquet.tmp path")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if strings.Contains(absolute, ".."+string(filepath.Separator)) {
		return errors.New("DuckDB writer target must not contain traversal")
	}
	return nil
}

func escapeLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableUint(value *uint64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableJSON(value map[string]string) any {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return string(data)
}
