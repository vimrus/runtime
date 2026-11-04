package olap

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

type recordingOLAPWriter struct {
	rows uint64
}

func (w *recordingOLAPWriter) WriteParquet(_ context.Context, _ Schema, _ []string, targetPath string) (uint64, error) {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		return 0, err
	}
	if err := os.WriteFile(targetPath, []byte("parquet-fixture"), 0o640); err != nil {
		return 0, err
	}
	return w.rows, nil
}

func testOLAPSchema() Schema {
	return Schema{
		Version:    DatasetSchemaVersion,
		Table:      "zt_bug",
		PrimaryKey: "id",
		Columns: []Column{
			{Name: "id", Type: "BIGINT"},
			{Name: "title", Type: "VARCHAR", Nullable: true},
		},
	}
}

func newTestPublisher(t *testing.T, rows uint64) (*Publisher, string, string) {
	t.Helper()
	root := t.TempDir()
	datasetRoot := filepath.Join(root, "dataset")
	stagingRoot := filepath.Join(root, "staging")
	writer := &recordingOLAPWriter{rows: rows}
	publisher, err := NewPublisher(PublisherConfig{DatasetRoot: datasetRoot, StagingRoot: stagingRoot}, writer)
	if err != nil {
		t.Fatal(err)
	}
	publisher.now = func() time.Time { return time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC) }
	return publisher, datasetRoot, stagingRoot
}

func stageOLAPRequest(t *testing.T, stagingRoot, syncID string) {
	t.Helper()
	dir := filepath.Join(stagingRoot, syncID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.jsonl"), []byte(`{"id":1,"title":"bug"}`+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestPublisherSnapshotAppendAndRewrite(t *testing.T) {
	publisher, datasetRoot, stagingRoot := newTestPublisher(t, 1)
	schema := testOLAPSchema()

	stageOLAPRequest(t, stagingRoot, "snapshot-1")
	result, err := publisher.Publish(context.Background(), PublishRequest{
		SchemaVersion:      DatasetSchemaVersion,
		Table:              "zt_bug",
		Mode:               ModeSnapshot,
		SyncID:             "snapshot-1",
		ExpectedGeneration: 0,
		Schema:             schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.Generation != 1 || len(result.Manifest.Buckets["all"]) != 1 {
		t.Fatalf("unexpected snapshot manifest: %#v", result.Manifest)
	}

	stageOLAPRequest(t, stagingRoot, "rewrite-2")
	result, err = publisher.Publish(context.Background(), PublishRequest{
		SchemaVersion:      DatasetSchemaVersion,
		Table:              "zt_bug",
		Mode:               ModeRewrite,
		SyncID:             "rewrite-2",
		Bucket:             2,
		ExpectedGeneration: 1,
		Schema:             schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.Generation != 2 || len(result.Manifest.Buckets["000002"]) != 1 {
		t.Fatalf("unexpected rewrite manifest: %#v", result.Manifest)
	}

	stageOLAPRequest(t, stagingRoot, "append-3")
	result, err = publisher.Publish(context.Background(), PublishRequest{
		SchemaVersion:      DatasetSchemaVersion,
		Table:              "zt_bug",
		Mode:               ModeAppend,
		SyncID:             "append-3",
		ExpectedGeneration: 2,
		Schema:             schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Manifest.Buckets["all"]) != 2 {
		t.Fatalf("append did not retain existing parts: %#v", result.Manifest.Buckets)
	}
	if _, err := os.Stat(filepath.Join(datasetRoot, "manifests", "zt_bug.json")); err != nil {
		t.Fatal(err)
	}
}

func TestPublisherRejectsGenerationConflict(t *testing.T) {
	publisher, _, stagingRoot := newTestPublisher(t, 1)
	stageOLAPRequest(t, stagingRoot, "conflict-1")
	_, err := publisher.Publish(context.Background(), PublishRequest{
		SchemaVersion:      DatasetSchemaVersion,
		Table:              "zt_bug",
		Mode:               ModeSnapshot,
		SyncID:             "conflict-1",
		ExpectedGeneration: 3,
		Schema:             testOLAPSchema(),
	})
	if err == nil {
		t.Fatal("expected missing-manifest generation conflict")
	}
}

func TestPublisherCleanupRemovesOnlyExpiredUnreferencedFiles(t *testing.T) {
	publisher, datasetRoot, stagingRoot := newTestPublisher(t, 1)
	schema := testOLAPSchema()

	stageOLAPRequest(t, stagingRoot, "cleanup-1")
	first, err := publisher.Publish(context.Background(), PublishRequest{
		SchemaVersion:      DatasetSchemaVersion,
		Table:              "zt_bug",
		Mode:               ModeSnapshot,
		SyncID:             "cleanup-1",
		ExpectedGeneration: 0,
		Schema:             schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	stageOLAPRequest(t, stagingRoot, "cleanup-2")
	second, err := publisher.Publish(context.Background(), PublishRequest{
		SchemaVersion:      DatasetSchemaVersion,
		Table:              "zt_bug",
		Mode:               ModeSnapshot,
		SyncID:             "cleanup-2",
		ExpectedGeneration: 1,
		Schema:             schema,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	oldStaging := now.Add(-4 * time.Hour)
	oldPart := filepath.Join(datasetRoot, filepath.FromSlash(first.Manifest.Buckets["all"][0].Path))
	activePart := filepath.Join(datasetRoot, filepath.FromSlash(second.Manifest.Buckets["all"][0].Path))
	for _, path := range []string{oldPart, activePart} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	for _, syncID := range []string{"cleanup-1", "cleanup-2"} {
		dir := filepath.Join(stagingRoot, syncID)
		if err := os.Chtimes(filepath.Join(dir, "data.jsonl"), now, now); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, now, now); err != nil {
			t.Fatal(err)
		}
	}

	staleStaging := filepath.Join(stagingRoot, "stale")
	recentStaging := filepath.Join(stagingRoot, "recent")
	for _, dir := range []string{staleStaging, recentStaging} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data.jsonl"), []byte("{}"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(staleStaging, "data.jsonl"), oldStaging, oldStaging); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staleStaging, oldStaging, oldStaging); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(recentStaging, "data.jsonl"), now, now); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(recentStaging, now, now); err != nil {
		t.Fatal(err)
	}

	result, err := publisher.Cleanup(now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedParts != 1 || result.RemovedStaging != 1 {
		t.Fatalf("unexpected cleanup result: %#v", result)
	}
	if _, err := os.Stat(oldPart); !os.IsNotExist(err) {
		t.Fatalf("unreferenced part was not removed: %v", err)
	}
	if _, err := os.Stat(activePart); err != nil {
		t.Fatalf("active part was removed: %v", err)
	}
	if _, err := os.Stat(recentStaging); err != nil {
		t.Fatalf("recent staging directory was removed: %v", err)
	}
}

func TestPublisherHonorsCrossProcessLock(t *testing.T) {
	publisher, datasetRoot, stagingRoot := newTestPublisher(t, 1)
	if err := os.MkdirAll(filepath.Join(datasetRoot, "manifests"), 0o750); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(datasetRoot, "manifests", "zt_bug.json.lock"))
	locked, err := lock.TryLock()
	if err != nil || !locked {
		t.Fatalf("acquire test lock: locked=%v err=%v", locked, err)
	}
	defer lock.Unlock()

	stageOLAPRequest(t, stagingRoot, "locked-1")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = publisher.Publish(ctx, PublishRequest{
		SchemaVersion:      DatasetSchemaVersion,
		Table:              "zt_bug",
		Mode:               ModeSnapshot,
		SyncID:             "locked-1",
		ExpectedGeneration: 0,
		Schema:             testOLAPSchema(),
	})
	if err == nil {
		t.Fatal("expected cross-process publish lock conflict")
	}
}
