package observability

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingWriter struct {
	mu      sync.Mutex
	written []string
}

func (w *recordingWriter) WriteParquet(_ context.Context, kind Kind, targetPath string, events []Event) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.written = append(w.written, targetPath)
	data, _ := json.Marshal(events)
	if err := os.WriteFile(targetPath, data, 0o600); err != nil {
		return 0, err
	}
	return int64(len(data)), nil
}

type flakyWriter struct {
	mu       sync.Mutex
	failures int
	delegate *recordingWriter
}

func (w *flakyWriter) WriteParquet(ctx context.Context, kind Kind, targetPath string, events []Event) (int64, error) {
	w.mu.Lock()
	if w.failures > 0 {
		w.failures--
		w.mu.Unlock()
		return 0, errors.New("simulated NFS failure")
	}
	w.mu.Unlock()
	return w.delegate.WriteParquet(ctx, kind, targetPath, events)
}

func TestNormalizeFillsEnvelopeAndRedacts(t *testing.T) {
	event, err := Normalize(Event{
		ClusterID: "cluster-a",
		NodeID:    "node-a",
		BootID:    "boot-a",
		Source:    "php",
		Kind:      KindLogs,
		Message:   strings.Repeat("x", 5000),
		Fields:    map[string]string{"db_password": "hunter2", "user": "zentao"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.EventID == "" || event.EventTime.IsZero() || event.IngestTime.IsZero() {
		t.Fatalf("envelope not filled: %#v", event)
	}
	if len(event.Message) != maxMessageLength {
		t.Fatalf("message length = %d, want %d", len(event.Message), maxMessageLength)
	}
	if event.Fields["db_password"] != "[REDACTED]" || event.Fields["user"] != "zentao" {
		t.Fatalf("redaction failed: %#v", event.Fields)
	}
}

func TestPublisherWritesImmutablePartFilesAndRecovers(t *testing.T) {
	root := t.TempDir()
	writer := &recordingWriter{}
	publisher, err := NewPublisher(Config{
		DatasetRoot:   filepath.Join(root, "dataset"),
		SpoolPath:     filepath.Join(root, "spool"),
		NodeID:        "node-a",
		ClusterID:     "cluster-a",
		BootID:        "boot-a",
		MaxBatchRows:  2,
		MaxBatchBytes: 1 << 20,
	}, writer)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		if err := publisher.Add(ctx, Event{ClusterID: "cluster-a", NodeID: "node-a", BootID: "boot-a", Source: "runtime", Kind: KindLogs, EventTime: now, Message: "m"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := publisher.FlushAll(ctx); err != nil {
		t.Fatal(err)
	}
	writer.mu.Lock()
	written := len(writer.written)
	writer.mu.Unlock()
	if written == 0 {
		t.Fatal("no parquet files written")
	}

	entries, err := os.ReadDir(filepath.Join(root, "spool"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("spool not emptied, found %d files", len(entries))
	}

	// Simulate a crash between spool and rename: create a spool record whose
	// final file does not exist.
	spoolData, _ := json.Marshal(spoolFile{
		Record: BatchRecord{BatchID: "node-a-boot-a-00000000000000000042", Kind: KindLogs, Partition: PartitionFor(now, "node-a"), Rows: 1, FinalPath: filepath.Join(root, "dataset", "logs", "schema=v1", "date="+now.Format("2006-01-02"), "hour="+now.Format("15"), "node=node-a", "part-node-a-boot-a-00000000000000000042.parquet"), SpoolPath: filepath.Join(root, "spool", "logs-node-a-boot-a-00000000000000000042.json")},
		Events: []Event{{ClusterID: "cluster-a", NodeID: "node-a", BootID: "boot-a", Source: "runtime", Kind: KindLogs, EventTime: now, Message: "recovered"}},
	})
	spoolPath := filepath.Join(root, "spool", "logs-node-a-boot-a-00000000000000000042.json")
	if err := os.WriteFile(spoolPath, spoolData, 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewPublisher(Config{
		DatasetRoot:   filepath.Join(root, "dataset"),
		SpoolPath:     filepath.Join(root, "spool"),
		NodeID:        "node-a",
		ClusterID:     "cluster-a",
		BootID:        "boot-a",
		MaxBatchRows:  2,
		MaxBatchBytes: 1 << 20,
	}, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spoolPath); !os.IsNotExist(err) {
		t.Fatal("spool entry must be removed after recovery")
	}
	if _, err := os.Stat(filepath.Join(root, "dataset", "logs", "schema=v1", "date="+now.Format("2006-01-02"), "hour="+now.Format("15"), "node=node-a", "part-node-a-boot-a-00000000000000000042.parquet")); err != nil {
		t.Fatalf("recovered final file missing: %v", err)
	}
}

func TestPublisherDoesNotExposeTempFilesAsParquet(t *testing.T) {
	root := t.TempDir()
	writer := &recordingWriter{}
	publisher, err := NewPublisher(Config{
		DatasetRoot: filepath.Join(root, "dataset"), SpoolPath: filepath.Join(root, "spool"),
		NodeID: "node-a", ClusterID: "cluster-a", BootID: "boot-a",
		MaxBatchRows: 1, MaxBatchBytes: 1 << 20,
	}, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Add(context.Background(), Event{ClusterID: "cluster-a", NodeID: "node-a", BootID: "boot-a", Source: "runtime", Kind: KindMetrics, EventTime: time.Now().UTC(), MetricName: "http.requests"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash before rename: the temp file must not match *.parquet.
	writer.mu.Lock()
	temp := writer.written[0]
	writer.mu.Unlock()
	if !strings.HasSuffix(temp, ".tmp") {
		t.Fatalf("temp path %q must carry .tmp suffix", temp)
	}
}

func TestPublisherRepublishesSpoolAfterRecovery(t *testing.T) {
	root := t.TempDir()
	delegate := &recordingWriter{}
	flaky := &flakyWriter{failures: 1, delegate: delegate}
	publisher, err := NewPublisher(Config{
		DatasetRoot: filepath.Join(root, "dataset"), SpoolPath: filepath.Join(root, "spool"),
		NodeID: "node-a", ClusterID: "cluster-a", BootID: "boot-a",
		MaxBatchRows: 2, MaxBatchBytes: 1 << 20,
	}, flaky)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := publisher.Add(ctx, Event{ClusterID: "cluster-a", NodeID: "node-a", BootID: "boot-a", Source: "runtime", Kind: KindLogs, EventTime: time.Now().UTC(), Message: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Flush(ctx, KindLogs); err == nil {
		t.Fatal("first publish must fail with simulated NFS outage")
	}
	spoolEntries, err := os.ReadDir(filepath.Join(root, "spool"))
	if err != nil || len(spoolEntries) != 1 {
		t.Fatalf("spool must retain the failed batch: %v %d", err, len(spoolEntries))
	}
	if err := publisher.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	spoolEntries, err = os.ReadDir(filepath.Join(root, "spool"))
	if err != nil {
		t.Fatal(err)
	}
	if len(spoolEntries) != 0 {
		t.Fatalf("spool must be empty after recovery, found %d entries", len(spoolEntries))
	}
}
