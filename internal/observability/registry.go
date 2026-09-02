package observability

import "sort"

// metricRegistry is the fixed, schema-versioned registry of metric names that
// may enter the observability pipeline. Names are deliberately not derived
// from user input; extending the registry requires a code change so that all
// nodes in a cluster agree on the same set.
var metricRegistry = map[string]struct{}{
	"http.request.duration":                  {},
	"observability_events_received_total":    {},
	"observability_events_dropped_total":     {},
	"observability_buffer_items":             {},
	"observability_spool_bytes":              {},
	"observability_spool_batches":            {},
	"observability_batch_publish_total":      {},
	"observability_batch_rows":               {},
	"observability_batch_bytes":              {},
	"observability_last_published_timestamp": {},
	"observability_parquet_files":            {},
	"observability_query_total":              {},
	"observability_nfs_available":            {},
}

// AllowedMetricName reports whether name is part of the fixed registry.
func AllowedMetricName(name string) bool {
	_, ok := metricRegistry[name]
	return ok
}

// MetricNames returns the registered metric names in stable order.
func MetricNames() []string {
	names := make([]string, 0, len(metricRegistry))
	for name := range metricRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
