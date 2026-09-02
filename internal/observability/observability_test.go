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

func TestNormalizeRedactsCredentialsInMessage(t *testing.T) {
	event, err := Normalize(Event{ClusterID: "c", NodeID: "n", BootID: "b", Source: "php", Kind: KindLogs, Message: "Authorization Bearer abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(event.Message, "abc123") {
		t.Fatalf("credential remained in message: %q", event.Message)
	}
}

func TestNormalizeRejectsUnregisteredMetricName(t *testing.T) {
	_, err := Normalize(Event{
		ClusterID: "c", NodeID: "n", BootID: "b", Source: "php",
		Kind: KindMetrics, MetricName: "user.supplied.name", MetricKind: "counter",
	})
	if err == nil {
		t.Fatal("unregistered metric name must be rejected")
	}
}

func TestNormalizeRejectsInvalidMetricKind(t *testing.T) {
	_, err := Normalize(Event{
		ClusterID: "c", NodeID: "n", BootID: "b", Source: "php",
		Kind: KindMetrics, MetricName: "observability_events_received_total", MetricKind: "histogram",
	})
	if err == nil {
		t.Fatal("metric kind histogram must be rejected until schema support exists")
	}
}

func TestMetricRegistryIsStableAndRejectsUnknownNames(t *testing.T) {
	names := MetricNames()
	if len(names) == 0 {
		t.Fatal("metric registry must not be empty")
	}
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Fatalf("metric registry is not sorted: %v", names)
		}
	}
	if !AllowedMetricName("observability_events_received_total") {
		t.Fatal("registered metric name was rejected")
	}
	if AllowedMetricName("user.supplied.name") {
		t.Fatal("unregistered metric name was accepted")
	}
}

func TestPublisherRejectsNilWriter(t *testing.T) {
	root := t.TempDir()
	_, err := NewPublisher(Config{DatasetRoot: filepath.Join(root, "dataset"), SpoolPath: filepath.Join(root, "spool"), NodeID: "n", ClusterID: "c", BootID: "b"}, nil)
	if err == nil {
		t.Fatal("publisher must reject a nil parquet writer")
	}
}

func TestPublisherWritesImmutablePartFilesAndRecovers(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dataset"), 0o700); err != nil {
		t.Fatal(err)
	}
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
	publisher.mu.Lock()
	spoolIndexEntries := len(publisher.spool)
	publisher.mu.Unlock()
	if spoolIndexEntries != 0 {
		t.Fatalf("spool index retained %d published entries", spoolIndexEntries)
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

func TestPublisherAddBatchRejectsPartialAcceptance(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dataset"), 0o700); err != nil {
		t.Fatal(err)
	}
	writer := &recordingWriter{}
	publisher, err := NewPublisher(Config{
		DatasetRoot:   filepath.Join(root, "dataset"),
		SpoolPath:     filepath.Join(root, "spool"),
		NodeID:        "node-a",
		ClusterID:     "cluster-a",
		BootID:        "boot-a",
		MaxBatchRows:  100,
		MaxBatchBytes: 1 << 20,
	}, writer)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	valid := Event{ClusterID: "cluster-a", NodeID: "node-a", BootID: "boot-a", Source: "php", Kind: KindLogs, EventTime: now, Message: "ok"}
	invalid := valid
	invalid.ClusterID = ""

	if _, err := publisher.AddBatch(ctx, []Event{valid, invalid}); err == nil {
		t.Fatal("batch containing an invalid event must be rejected")
	}
	publisher.mu.Lock()
	buffered := len(publisher.buffer[KindLogs])
	publisher.mu.Unlock()
	if buffered != 0 {
		t.Fatalf("rejected batch left %d events buffered", buffered)
	}

	accepted, err := publisher.AddBatch(ctx, []Event{valid, valid})
	if err != nil || accepted != 2 {
		t.Fatalf("valid batch not accepted: accepted=%d err=%v", accepted, err)
	}
	if err := publisher.FlushAll(ctx); err != nil {
		t.Fatal(err)
	}
	writer.mu.Lock()
	written := len(writer.written)
	writer.mu.Unlock()
	if written == 0 {
		t.Fatal("valid batch was not published")
	}
}

func TestPublisherDoesNotExposeTempFilesAsParquet(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dataset"), 0o700); err != nil {
		t.Fatal(err)
	}
	writer := &recordingWriter{}
	publisher, err := NewPublisher(Config{
		DatasetRoot: filepath.Join(root, "dataset"), SpoolPath: filepath.Join(root, "spool"),
		NodeID: "node-a", ClusterID: "cluster-a", BootID: "boot-a",
		MaxBatchRows: 1, MaxBatchBytes: 1 << 20,
	}, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Add(context.Background(), Event{ClusterID: "cluster-a", NodeID: "node-a", BootID: "boot-a", Source: "runtime", Kind: KindMetrics, EventTime: time.Now().UTC(), MetricName: "observability_events_received_total", MetricKind: "counter"}); err != nil {
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
	if err := os.Mkdir(filepath.Join(root, "dataset"), 0o700); err != nil {
		t.Fatal(err)
	}
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

func TestPublisherDoesNotCreateMissingDatasetRoot(t *testing.T) {
	root := t.TempDir()
	dataset := filepath.Join(root, "dataset")
	publisher, err := NewPublisher(Config{DatasetRoot: dataset, SpoolPath: filepath.Join(root, "spool"), NodeID: "n", ClusterID: "c", BootID: "b"}, &recordingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataset); !os.IsNotExist(err) {
		t.Fatalf("dataset root was created: %v", err)
	}
	if err := publisher.Add(context.Background(), Event{ClusterID: "c", NodeID: "n", BootID: "b", Source: "runtime", Kind: KindLogs, Message: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Flush(context.Background(), KindLogs); err == nil {
		t.Fatal("publish must fail while dataset root is unavailable")
	}
	if _, err := os.Stat(filepath.Join(root, "spool")); err != nil {
		t.Fatal(err)
	}
}

func TestPublisherDropsBatchWhenSpoolQuotaExceeded(t *testing.T) {
	root := t.TempDir()
	dataset := filepath.Join(root, "dataset")
	if err := os.Mkdir(dataset, 0o700); err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(Config{
		DatasetRoot:   dataset,
		SpoolPath:     filepath.Join(root, "spool"),
		NodeID:        "node-a",
		ClusterID:     "cluster-a",
		BootID:        "boot-a",
		MaxSpoolBytes: 1,
		MaxBatchRows:  1,
		MaxBatchBytes: 1 << 20,
	}, &recordingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	err = publisher.Add(context.Background(), Event{
		ClusterID: "cluster-a", NodeID: "node-a", BootID: "boot-a",
		Source: "runtime", Kind: KindLogs, Message: "quota test",
	})
	if err == nil {
		t.Fatal("publish must fail when spool quota is exceeded")
	}
	if publisher.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", publisher.Dropped())
	}
	entries, err := os.ReadDir(filepath.Join(root, "spool"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("quota-rejected batch must not remain in spool, found %d files", len(entries))
	}
}

func TestPublisherSelfMetricEventsAreRegisteredAndDeltaBased(t *testing.T) {
	root := t.TempDir()
	dataset := filepath.Join(root, "dataset")
	if err := os.Mkdir(dataset, 0o700); err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(Config{
		DatasetRoot:   dataset,
		SpoolPath:     filepath.Join(root, "spool"),
		NodeID:        "node-a",
		ClusterID:     "cluster-a",
		BootID:        "boot-a",
		MaxBatchRows:  100,
		MaxBatchBytes: 1 << 20,
	}, &recordingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := publisher.Add(ctx, Event{
		ClusterID: "cluster-a", NodeID: "node-a", BootID: "boot-a",
		Source: "runtime", Kind: KindLogs, Message: "self metric test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := publisher.FlushAll(ctx); err != nil {
		t.Fatal(err)
	}

	first := publisher.SelfMetricEvents(time.Now())
	if len(first) == 0 {
		t.Fatal("self metric events must not be empty")
	}
	values := make(map[string]float64)
	for _, event := range first {
		if !AllowedMetricName(event.MetricName) {
			t.Fatalf("self metric %q is not registered", event.MetricName)
		}
		if event.Value != nil {
			values[event.MetricName] = *event.Value
		}
	}
	if values["observability_events_received_total"] < 1 {
		t.Fatalf("received delta = %v, want >= 1", values["observability_events_received_total"])
	}
	if values["observability_batch_rows"] != 1 {
		t.Fatalf("batch rows = %v, want 1", values["observability_batch_rows"])
	}

	second := publisher.SelfMetricEvents(time.Now())
	for _, event := range second {
		if event.MetricName == "observability_events_received_total" && event.Value != nil && *event.Value != 0 {
			t.Fatalf("second received delta = %v, want 0", *event.Value)
		}
	}
}

func TestPublisherShouldFlushBatchingPolicy(t *testing.T) {
	root := t.TempDir()
	dataset := filepath.Join(root, "dataset")
	if err := os.Mkdir(dataset, 0o700); err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(Config{
		DatasetRoot:      dataset,
		SpoolPath:        filepath.Join(root, "spool"),
		NodeID:           "node-a",
		ClusterID:        "cluster-a",
		BootID:           "boot-a",
		MaxBatchRows:     100,
		MaxBatchBytes:    1 << 20,
		MinBatchRows:     10,
		MinBatchBytes:    1 << 20,
		FlushInterval:    time.Second,
		MaxFlushInterval: 10 * time.Second,
	}, &recordingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	if publisher.shouldFlush(0) {
		t.Fatal("empty buffer must not flush")
	}
	event := Event{
		ClusterID: "cluster-a", NodeID: "node-a", BootID: "boot-a",
		Source: "runtime", Kind: KindLogs, Message: "batch policy",
	}
	publisher.mu.Lock()
	publisher.buffer[KindLogs] = append(publisher.buffer[KindLogs], event)
	publisher.mu.Unlock()
	if publisher.shouldFlush(0) {
		t.Fatal("small buffer must not flush before minimum batch size")
	}
	publisher.mu.Lock()
	for i := 0; i < 9; i++ {
		publisher.buffer[KindLogs] = append(publisher.buffer[KindLogs], event)
	}
	publisher.mu.Unlock()
	if !publisher.shouldFlush(0) {
		t.Fatal("buffer at minimum rows must flush")
	}
	publisher.mu.Lock()
	publisher.buffer[KindLogs] = nil
	publisher.mu.Unlock()
	if !publisher.shouldFlush(20 * time.Second) {
		t.Fatal("max flush interval must force a flush")
	}
}

func TestPublisherQuarantinesCorruptSpool(t *testing.T) {
	root := t.TempDir()
	dataset := filepath.Join(root, "dataset")
	if err := os.Mkdir(dataset, 0o700); err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(root, "spool")
	if err := os.Mkdir(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(spool, "logs-bad.json")
	if err := os.WriteFile(bad, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(Config{DatasetRoot: dataset, SpoolPath: spool, NodeID: "n", ClusterID: "c", BootID: "b"}, &recordingWriter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatalf("corrupt spool remains: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(spool, "quarantine"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("quarantine entries = %d, err=%v", len(entries), err)
	}
}
