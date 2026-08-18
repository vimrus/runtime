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
