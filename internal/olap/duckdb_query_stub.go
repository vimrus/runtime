//go:build !duckdb

package olap

import (
	"context"
	"time"
)

type DuckDBQueryEngine struct{}

func NewDuckDBQueryEngine(_ int, _ string, _ time.Duration, _, _ int) (*DuckDBQueryEngine, error) {
	return &DuckDBQueryEngine{}, nil
}

func (e *DuckDBQueryEngine) Query(context.Context, string, QueryRequest) (QueryResult, error) {
	return QueryResult{}, errDuckDBNotCompiled
}
