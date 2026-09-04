// Package query provides fixed, resource-limited observability queries over
// the shared Parquet dataset. It never accepts arbitrary SQL or paths.
package query

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vimrus/runtime/internal/observability"
)

type Options struct {
	Kind       observability.Kind
	Start      time.Time
	End        time.Time
	Node       string
	Level      string // logs only
	MetricName string // metrics only
	EventID    string // logs only
	// FieldValues filters the JSON `fields` column of log events. Keys are
	// allowlisted identifiers; typical keys: topic/module/method/account/
	// requestID/uri/file/line. The Runtime log viewer maps these to UI filters.
	FieldValues map[string]string
	// MessageContains is a case-insensitive substring filter on message.
	MessageContains string
	Limit           int
	Offset          int
}

type Row map[string]any

// Engine executes a validated query against the dataset. Implementations must
// honor context cancellation and apply the resource limits from Config.
type Engine interface {
	Query(ctx context.Context, template string, rows int, params []any) ([]Row, error)
}

type Config struct {
	DatasetRoot string
	MaxDays     int
	MaxRows     int
	Threads     int
	MemoryLimit string
	Timeout     time.Duration
}

func (c Config) validate() error {
	if c.DatasetRoot == "" {
		return errors.New("dataset root is required")
	}
	if c.MaxDays <= 0 {
		return errors.New("maxDays must be positive")
	}
	if c.MaxDays > 3650 {
		return errors.New("maxDays must not exceed 3650")
	}
	if c.MaxRows <= 0 {
		return errors.New("maxRows must be positive")
	}
	if c.Threads <= 0 {
		return errors.New("threads must be positive")
	}
	if c.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	return nil
}

var fieldKeyPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Build produces the fixed SQL template and bound parameters for the request.
// Only the configured dataset root and allowlisted fields can appear.
func Build(config Config, options Options) (string, []any, int, error) {
	where, params, limit, err := buildWhere(config, options)
	if err != nil {
		return "", nil, 0, err
	}
	if options.Kind == observability.KindMetrics {
		return buildMetricsTemplate(config, options, where, params, limit), params, limit, nil
	}
	return buildLogsTemplate(config, where, params, limit, options.Offset), params, limit, nil
}

// BuildCount produces a COUNT query with the same WHERE constraints.
func BuildCount(config Config, options Options) (string, []any, error) {
	where, params, _, err := buildWhere(config, options)
	if err != nil {
		return "", nil, err
	}
	kind := "logs"
	if options.Kind == observability.KindMetrics {
		kind = "metrics"
	}
	root := config.DatasetRoot + "/" + kind + "/schema=v1"
	root = strings.ReplaceAll(root, "'", "''")
	template := "SELECT COUNT(*) AS total_count FROM read_parquet('" + root + "/date=*/hour=*/node=*/part-*.parquet', hive_partitioning=true, union_by_name=true) WHERE " + where
	return template, params, nil
}

func buildWhere(config Config, options Options) (string, []any, int, error) {
	if err := config.validate(); err != nil {
		return "", nil, 0, err
	}
	if options.Kind != observability.KindLogs && options.Kind != observability.KindMetrics {
		return "", nil, 0, errors.New("query kind must be logs or metrics")
	}
	if options.End.IsZero() {
		options.End = time.Now().UTC()
	}
	if options.Start.IsZero() {
		options.Start = options.End.Add(-24 * time.Hour)
	}
	if options.Start.After(options.End) {
		return "", nil, 0, errors.New("query start must not be after end")
	}
	if options.End.Sub(options.Start) > time.Duration(config.MaxDays)*24*time.Hour {
		return "", nil, 0, fmt.Errorf("query range exceeds %d days", config.MaxDays)
	}
	limit := options.Limit
	if limit <= 0 || limit > config.MaxRows {
		limit = config.MaxRows
	}
	if options.Offset < 0 || options.Offset > 100000 {
		return "", nil, 0, errors.New("offset out of range")
	}

	params := []any{options.Start.UTC(), options.End.UTC()}
	where := "eventTime >= ? AND eventTime < ?"
	if options.Node != "" {
		where += " AND node = ?"
		params = append(params, options.Node)
	}
	if options.Kind == observability.KindMetrics {
		if options.MetricName != "" {
			where += " AND metricName = ?"
			params = append(params, options.MetricName)
		}
	} else {
		if options.Level != "" {
			where += " AND level = ?"
			params = append(params, options.Level)
		}
		if options.EventID != "" {
			where += " AND eventID = ?"
			params = append(params, options.EventID)
		}
		if options.MessageContains != "" {
			where += " AND contains(lower(message), lower(?))"
			params = append(params, options.MessageContains)
		}
		keys := make([]string, 0, len(options.FieldValues))
		for key := range options.FieldValues {
			if !fieldKeyPattern.MatchString(key) {
				return "", nil, 0, fmt.Errorf("field key %q is not allowlisted", key)
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			where += " AND json_extract_string(fields, '$." + key + "') = ?"
			params = append(params, options.FieldValues[key])
		}
	}
	return where, params, limit, nil
}

func buildLogsTemplate(config Config, where string, params []any, limit int, offset int) string {
	root := config.DatasetRoot + "/logs/schema=v1"
	root = strings.ReplaceAll(root, "'", "''")
	template := "SELECT eventTime, eventID, node, source, level, message, traceID, fields FROM read_parquet('" +
		root + "/date=*/hour=*/node=*/part-*.parquet', hive_partitioning=true, union_by_name=true) WHERE " + where +
		" ORDER BY eventTime DESC LIMIT " + fmt.Sprintf("%d", limit)
	if offset > 0 {
		template += " OFFSET " + fmt.Sprintf("%d", offset)
	}
	return template
}

func buildMetricsTemplate(config Config, options Options, where string, params []any, limit int) string {
	root := config.DatasetRoot + "/metrics/schema=v1"
	root = strings.ReplaceAll(root, "'", "''")
	_ = options
	_ = params
	return "SELECT eventTime, eventID, node, metricName, metricKind, value, count, labels FROM read_parquet('" +
		root + "/date=*/hour=*/node=*/part-*.parquet', hive_partitioning=true, union_by_name=true) WHERE " + where +
		" ORDER BY eventTime DESC LIMIT " + fmt.Sprintf("%d", limit)
}
