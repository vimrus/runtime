//go:build duckdb

package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2"

	"github.com/vimrus/runtime/internal/observability/query"
)

// Engine executes generated queries with DuckDB and applies resource limits.
type Engine struct {
	threads     int
	memoryLimit string
}

func NewEngine(threads int, memoryLimit string) (*Engine, error) {
	if threads <= 0 {
		threads = 2
	}
	if memoryLimit == "" {
		memoryLimit = "256MB"
	}
	return &Engine{threads: threads, memoryLimit: memoryLimit}, nil
}

func (e *Engine) Query(ctx context.Context, template string, _ int, params []any) ([]query.Row, error) {
	if err := rejectUnsafe(template); err != nil {
		return nil, err
	}
	if err := rejectUnsafe(strings.Join(toStrings(params), " ")); err != nil {
		return nil, err
	}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := configure(ctx, db, e.memoryLimit, e.threads); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, template, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var result []query.Row
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := make(query.Row, len(columns))
		for i, column := range columns {
			row[column] = values[i]
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func toStrings(values []any) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, fmt.Sprintf("%v", value))
	}
	return result
}

func rejectUnsafe(template string) error {
	upper := strings.ToUpper(template)
	for _, marker := range []string{"INSTALL", "LOAD ", "COPY ", "ATTACH", "HTTP", "S3://", "GS://", "FILE_READ", "READ_BLOB"} {
		if strings.Contains(upper, marker) {
			return fmt.Errorf("query template contains forbidden token %q", strings.TrimSpace(marker))
		}
	}
	return nil
}
