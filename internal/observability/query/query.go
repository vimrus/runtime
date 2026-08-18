// Package query provides fixed, resource-limited observability queries over
// the shared Parquet dataset. It never accepts arbitrary SQL or paths.
package query

import (
	"context"
	"errors"
	"fmt"
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
	Limit      int
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

// Build produces the fixed SQL template and bound parameters for the request.
// Only the configured dataset root and allowlisted fields can appear.
func Build(config Config, options Options) (string, []any, int, error) {
	if err := config.validate(); err != nil {
		return "", nil, 0, err
	}
	if options.Kind != observability.KindLogs && options.Kind != observability.KindMetrics {
		return "", nil, 0, errors.New("query kind must be logs or metrics")
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
	if options.End.IsZero() {
		options.End = time.Now().UTC()
	}
	if options.Start.IsZero() {
		options.Start = options.End.Add(-24 * time.Hour)
	}

	root := config.DatasetRoot + "/" + string(options.Kind) + "/schema=v1"
	params := []any{options.Start.UTC(), options.End.UTC()}
	where := "eventTime >= ? AND eventTime < ?"
	if options.Node != "" {
		where += " AND node = ?"
		params = append(params, options.Node)
	}
	selectFields := "eventTime, eventID, node, source, level, message, traceID"
	if options.Kind == observability.KindMetrics {
		selectFields = "eventTime, eventID, node, metricName, metricKind, value, count, labels"
		if options.MetricName != "" {
			where += " AND metricName = ?"
			params = append(params, options.MetricName)
		}
	} else if options.Level != "" {
		where += " AND level = ?"
		params = append(params, options.Level)
	}
	template := "SELECT " + selectFields + " FROM read_parquet('" + root + "/date=*/hour=*/node=*/part-*.parquet', hive_partitioning=true, union_by_name=true) WHERE " + where + " ORDER BY eventTime DESC LIMIT " + fmt.Sprintf("%d", limit)
	return template, params, limit, nil
}
