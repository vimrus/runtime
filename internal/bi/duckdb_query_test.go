//go:build duckdb

package bi

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDuckDBPublisherAndQueryEngine(t *testing.T) {
	root := t.TempDir()
	datasetRoot := filepath.Join(root, "dataset")
	stagingRoot := filepath.Join(root, "staging")
	writer, err := NewDuckDBWriter(2, "256MB")
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(PublisherConfig{DatasetRoot: datasetRoot, StagingRoot: stagingRoot}, writer)
	if err != nil {
		t.Fatal(err)
	}
	stagingDir := filepath.Join(stagingRoot, "sync-1")
	if err := os.MkdirAll(stagingDir, 0o750); err != nil {
		t.Fatal(err)
	}
	data := `{"id":1,"title":"one","lastEditedDate":"2026-10-26 10:00:00","estimate":"12.34"}` + "\n" +
		`{"id":2,"title":"two","lastEditedDate":"2026-10-27 10:00:00","estimate":"5.60"}` + "\n"
	if err := os.WriteFile(filepath.Join(stagingDir, "data.jsonl"), []byte(data), 0o640); err != nil {
		t.Fatal(err)
	}
	watermark := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	result, err := publisher.Publish(context.Background(), PublishRequest{
		SchemaVersion:      DatasetSchemaVersion,
		Table:              "zt_bug",
		Mode:               ModeSnapshot,
		SyncID:             "sync-1",
		ExpectedGeneration: 0,
		WatermarkTime:      &watermark,
		Schema: Schema{
			Version:    DatasetSchemaVersion,
			Table:      "zt_bug",
			PrimaryKey: "id",
			Columns: []Column{
				{Name: "id", Type: "BIGINT"},
				{Name: "title", Type: "VARCHAR"},
				{Name: "lastEditedDate", Type: "TIMESTAMP"},
				{Name: "deleted", Type: "BIGINT"},
				{Name: "execution", Type: "BIGINT"},
				{Name: "product", Type: "BIGINT"},
				{Name: "resolution", Type: "VARCHAR"},
				{Name: "severity", Type: "BIGINT"},
				{Name: "estimate", Type: "DECIMAL(10,2)"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows != 2 || result.Manifest.Generation != 1 {
		t.Fatalf("unexpected publish result: %#v", result)
	}

	engine, err := NewDuckDBQueryEngine(2, "256MB", 10*time.Second, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	query, err := engine.Query(context.Background(), datasetRoot, QueryRequest{
		SchemaVersion: QuerySchemaVersion,
		SQL:           `SELECT "id", "title", "estimate", datediff(DATE '2026-10-27', "lastEditedDate") AS days FROM "zt_bug" ORDER BY "id"`,
		MaxRows:       10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if query.Total != 2 || len(query.Rows) != 2 || query.Generations["zt_bug"] != 1 {
		t.Fatalf("unexpected query result: %#v", query)
	}
	if query.Rows[0]["title"] != "one" {
		t.Fatalf("unexpected first row: %#v", query.Rows[0])
	}
	if query.ColumnTypes["id"] != "BIGINT" || query.ColumnTypes["title"] != "VARCHAR" {
		t.Fatalf("unexpected column types: %#v", query.ColumnTypes)
	}
	if _, err := json.Marshal(query); err != nil {
		t.Fatalf("query result must be JSON serializable: %v", err)
	}

	page, err := engine.Query(context.Background(), datasetRoot, QueryRequest{
		SchemaVersion: QuerySchemaVersion,
		SQL:           `SELECT "id", "title" FROM "zt_bug" ORDER BY "id"`,
		MaxRows:       1,
		Offset:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Rows) != 1 || page.Rows[0]["id"] != int64(2) {
		t.Fatalf("unexpected paged query: %#v", page)
	}

	if _, err := engine.Query(context.Background(), datasetRoot, QueryRequest{
		SchemaVersion: QuerySchemaVersion,
		SQL:           `SELECT "id" FROM "zt_bug"`,
		Offset:        MaxQueryOffset + 1,
	}); err == nil {
		t.Fatal("oversized query offset must be rejected")
	}
}

func TestDuckDBWriterSupportsEmptyBucket(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "empty.jsonl")
	if err := os.WriteFile(staging, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	writer, err := NewDuckDBWriter(2, "256MB")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "empty.parquet.tmp")
	rows, err := writer.WriteParquet(context.Background(), Schema{
		Version:    DatasetSchemaVersion,
		Table:      "zt_bug",
		PrimaryKey: "id",
		Columns: []Column{
			{Name: "id", Type: "BIGINT"},
			{Name: "title", Type: "VARCHAR"},
		},
	}, []string{staging}, target)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("empty bucket wrote %d rows", rows)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("empty bucket Parquet must retain its schema")
	}

	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM read_parquet(?)`, target).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("empty bucket contains %d rows", count)
	}
}

func TestRestrictDuckDBFilesystem(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "dataset")
	if err := os.MkdirAll(allowed, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := restrictDuckDBFilesystem(context.Background(), db, []string{allowed}); err != nil {
		t.Fatal(err)
	}
	var value string
	err = db.QueryRow(`SELECT content FROM read_text(?)`, outside).Scan(&value)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "file system operations are disabled") {
		t.Fatalf("outside file access must be denied, got %v", err)
	}
}
