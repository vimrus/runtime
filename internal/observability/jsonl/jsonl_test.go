package jsonl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vimrus/runtime/internal/observability"
)

type recordingWriter struct {
	events []observability.Event
}

func (w *recordingWriter) WriteParquet(_ context.Context, _ observability.Kind, targetPath string, events []observability.Event) (int64, error) {
	w.events = append(w.events, events...)
	data, _ := json.Marshal(events)
	return int64(len(data)), os.WriteFile(targetPath, data, 0o600)
}

func TestParseAccessLine(t *testing.T) {
	line := `{"level":"info","ts":1787190000.5,"logger":"http.log.access.access","msg":"handled request","request":{"method":"GET","uri":"/index.php?x=1","host":"127.0.0.1:8080","remote_ip":"127.0.0.1"},"duration":0.25,"size":42,"status":200}`
	event, ok, err := parseLine(line, "caddy.access", "cluster-a", "node-a", "boot-a")
	if err != nil || !ok {
		t.Fatalf("parse error: %v ok=%v", err, ok)
	}
	if event.Source != "caddy.access" || event.StatusCode == nil || *event.StatusCode != 200 {
		t.Fatalf("unexpected event: %#v", event)
	}
	if event.DurationMs == nil || *event.DurationMs != 250 {
		t.Fatalf("durationMs = %v, want 250", event.DurationMs)
	}
	if event.Fields["uri"] != "/index.php?x=1" || event.Fields["method"] != "GET" {
		t.Fatalf("fields missing: %#v", event.Fields)
	}
	want := time.Unix(0, int64(1787190000.5*1e9)).UTC()
	if !event.EventTime.Equal(want) {
		t.Fatalf("eventTime = %v, want %v", event.EventTime, want)
	}
}

func TestParseRuntimeLineWithTimeKey(t *testing.T) {
	line := `{"time":"2026-08-20T01:00:00.123Z","level":"error","msg":"queue bridge failed","node_id":"node-a","component":"queue","error":"unavailable: broker down"}`
	event, ok, err := parseLine(line, "runtime", "cluster-a", "node-a", "boot-a")
	if err != nil || !ok {
		t.Fatalf("parse error: %v ok=%v", err, ok)
	}
	if event.Source != "runtime" || event.Fields["node_id"] != "node-a" || event.Fields["component"] != "queue" {
		t.Fatalf("unexpected runtime event: %#v", event)
	}
	if event.EventTime.UTC().Format(time.RFC3339Nano) != "2026-08-20T01:00:00.123Z" {
		t.Fatalf("eventTime = %v", event.EventTime)
	}
}

func TestParseMalformedAndUnrelatedLines(t *testing.T) {
	if _, _, err := parseLine("not-json", "caddy.access", "c", "n", "b"); err == nil {
		t.Fatal("malformed line must be rejected")
	}
	line := `{"ts":1,"logger":"admin","msg":"admin endpoint disabled"}`
	if _, ok, err := parseLine(line, "caddy.access", "c", "n", "b"); err != nil || ok {
		t.Fatalf("non-access logger must be skipped: ok=%v err=%v", ok, err)
	}
}

func TestConvertOnceProcessesOwnNodeSegmentsIdempotently(t *testing.T) {
	root := t.TempDir()
	logsDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	access := filepath.Join(logsDir, "access-node-a-2026-08-20T01-00-00.000-time.jsonl")
	runtime := filepath.Join(logsDir, "runtime-node-a.jsonl-20260820T010000.000000000Z.log")
	other := filepath.Join(logsDir, "access-node-b-2026-08-20T01-00-00.000-time.jsonl")
	ambiguousAccess := filepath.Join(logsDir, "access-node-a-1-2026-08-20T01-00-00.000-time.jsonl")
	ambiguousRuntime := filepath.Join(logsDir, "runtime-node-a-1.jsonl-20260820T010000.000000000Z.log")
	active := filepath.Join(logsDir, "access-node-a.jsonl")
	unrelated := filepath.Join(logsDir, "access-node-a-notes.txt")
	writeLines := func(path string, lines ...string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeLines(access, `{"ts":1,"logger":"http.log.access","msg":"handled request","request":{"uri":"/a"},"status":200,"duration":0.1}`)
	writeLines(runtime, `{"ts":2,"level":"info","msg":"runtime started"}`)
	writeLines(other, `{"ts":3,"logger":"http.log.access","msg":"other node","request":{"uri":"/b"},"status":200,"duration":0.1}`)
	writeLines(ambiguousAccess, `{"ts":4,"logger":"http.log.access","msg":"ambiguous access","request":{"uri":"/d"},"status":200,"duration":0.1}`)
	writeLines(ambiguousRuntime, `{"ts":5,"level":"info","msg":"ambiguous runtime"}`)
	writeLines(active, `{"ts":6,"logger":"http.log.access","msg":"active segment must be ignored","request":{"uri":"/c"},"status":200,"duration":0.1}`)
	writeLines(unrelated, `ignored`)

	writer := &recordingWriter{}
	publisher, err := observability.NewPublisher(observability.Config{
		DatasetRoot: filepath.Join(root, "dataset"), SpoolPath: filepath.Join(root, "spool"),
		NodeID: "node-a", ClusterID: "cluster-a", BootID: "boot-a",
		MaxBatchRows: 100, MaxBatchBytes: 1 << 20,
	}, writer)
	if err != nil {
		t.Fatal(err)
	}
	converter, err := New(Config{
		LogsDir: logsDir, NodeID: "node-a", ClusterID: "cluster-a", BootID: "boot-a",
		Sources: []string{"access", "runtime"}, Publisher: publisher,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := converter.ConvertOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Segments != 2 || result.Events != 2 {
		t.Fatalf("unexpected first conversion: %#v", result)
	}
	if len(writer.events) != 2 {
		t.Fatalf("published events = %d, want 2", len(writer.events))
	}
	second, err := converter.ConvertOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Segments != 0 || second.Events != 0 {
		t.Fatalf("second conversion must be idempotent: %#v", second)
	}
}
