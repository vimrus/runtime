package query

import (
	"strings"
	"testing"
	"time"

	"github.com/vimrus/runtime/internal/observability"
)

func TestBuildLogsTemplate(t *testing.T) {
	config := Config{DatasetRoot: "/var/lib/zentao/observability", MaxDays: 7, MaxRows: 1000, Threads: 2, Timeout: 5 * time.Second}
	template, params, limit, err := Build(config, Options{
		Kind:  observability.KindLogs,
		Start: time.Now().UTC().Add(-time.Hour),
		End:   time.Now().UTC(),
		Node:  "node-a",
		Level: "error",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(template, "read_parquet('/var/lib/zentao/observability/logs/schema=v1/date=*/hour=*/node=*/part-*.parquet'") {
		t.Fatalf("template must pin dataset root: %s", template)
	}
	if strings.Contains(template, "rm -rf") || strings.Contains(template, "secret") {
		t.Fatalf("template must not contain user-controlled paths or commands: %s", template)
	}
	if len(params) != 4 || limit != 1000 {
		t.Fatalf("unexpected params/limit: %v %d", params, limit)
	}
}

func TestBuildRejectsWideRangeAndArbitraryPaths(t *testing.T) {
	config := Config{DatasetRoot: "/var/lib/zentao/observability", MaxDays: 1, MaxRows: 10, Threads: 1, Timeout: time.Second}
	if _, _, _, err := Build(config, Options{Kind: observability.KindLogs, Start: time.Now().Add(-48 * time.Hour), End: time.Now()}); err == nil {
		t.Fatal("range wider than maxDays must be rejected")
	}
}

func TestBuildMetricsTemplateFiltersByName(t *testing.T) {
	config := Config{DatasetRoot: "/data", MaxDays: 7, MaxRows: 10, Threads: 1, Timeout: time.Second}
	template, params, _, err := Build(config, Options{
		Kind: observability.KindMetrics, MetricName: "http.request.duration",
		Start: time.Now().Add(-time.Hour), End: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(template, "metricName = ?") || len(params) != 3 {
		t.Fatalf("metric filter missing: %s %v", template, params)
	}
}

func TestBuildDefaultsZeroTimeRange(t *testing.T) {
	config := Config{DatasetRoot: "/data", MaxDays: 2, MaxRows: 10, Threads: 1, Timeout: time.Second}
	_, params, _, err := Build(config, Options{Kind: observability.KindLogs})
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 2 {
		t.Fatalf("params = %v, want start and end", params)
	}
	start, ok := params[0].(time.Time)
	if !ok {
		t.Fatalf("start type = %T", params[0])
	}
	end, ok := params[1].(time.Time)
	if !ok || end.Before(start) || end.Sub(start) != 24*time.Hour {
		t.Fatalf("unexpected range: %v - %v", start, end)
	}
}

func TestBuildLogsFieldFiltersAndOffset(t *testing.T) {
	config := Config{DatasetRoot: "/data", MaxDays: 7, MaxRows: 1000, Threads: 2, Timeout: 5 * time.Second}
	template, params, limit, err := Build(config, Options{
		Kind:            observability.KindLogs,
		Start:           time.Now().UTC().Add(-time.Hour),
		End:             time.Now().UTC(),
		FieldValues:     map[string]string{"topic": "error", "module": "common", "requestID": "req-1"},
		MessageContains: "boom",
		Limit:           20,
		Offset:          40,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"json_extract_string(fields, '$.topic') = ?", "json_extract_string(fields, '$.module') = ?", "json_extract_string(fields, '$.requestID') = ?", "contains(lower(message), lower(?))", "OFFSET 40"} {
		if !strings.Contains(template, fragment) {
			t.Fatalf("template missing %q: %s", fragment, template)
		}
	}
	if limit != 20 {
		t.Fatalf("limit = %d, want 20", limit)
	}
	if len(params) != 6 {
		t.Fatalf("params = %v, want 6", params)
	}
	countTemplate, _, err := BuildCount(config, Options{Kind: observability.KindLogs, Start: time.Now().Add(-time.Hour), End: time.Now(), FieldValues: map[string]string{"topic": "cron"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(countTemplate, "SELECT COUNT(*)") || !strings.Contains(countTemplate, "json_extract_string(fields, '$.topic') = ?") {
		t.Fatalf("count template invalid: %s", countTemplate)
	}
}

func TestBuildRejectsUnsafeFieldKey(t *testing.T) {
	config := Config{DatasetRoot: "/data", MaxDays: 7, MaxRows: 10, Threads: 1, Timeout: time.Second}
	if _, _, _, err := Build(config, Options{Kind: observability.KindLogs, FieldValues: map[string]string{"topic'; DROP TABLE x;--": "error"}}); err == nil {
		t.Fatal("unsafe field key must be rejected")
	}
}
