//go:build duckdb

package duckdb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"

	"github.com/vimrus/runtime/internal/observability"
)

func TestDuckDBWriterProducesReadableParquet(t *testing.T) {
	writer, err := NewWriter("128MB", 2)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), ".batch-1.parquet.tmp")
	now := time.Now().UTC()
	events := []observability.Event{
		{SchemaVersion: 1, ClusterID: "cluster-a", NodeID: "node-a", BootID: "boot-a", EventID: "e1", Source: "runtime", Kind: observability.KindLogs, EventTime: now, Level: "info", Message: "hello"},
		{SchemaVersion: 1, ClusterID: "cluster-a", NodeID: "node-a", BootID: "boot-a", EventID: "e2", Source: "runtime", Kind: observability.KindMetrics, EventTime: now, MetricName: "http.requests", MetricKind: "counter", Count: uint64Ptr(1)},
	}
	if _, err := writer.WriteParquet(context.Background(), observability.KindLogs, target, events); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), "SET autoinstall_known_extensions=false"); err != nil {
		t.Fatal(err)
	}
	row := db.QueryRowContext(context.Background(), "SELECT count(*), min(level) FROM read_parquet(?)", target)
	var count int
	var level *string
	if err := row.Scan(&count, &level); err != nil {
		t.Fatal(err)
	}
	if count != 2 || level == nil || *level != "info" {
		t.Fatalf("unexpected parquet content: count=%d level=%v", count, level)
	}
}

func uint64Ptr(value uint64) *uint64 { return &value }
