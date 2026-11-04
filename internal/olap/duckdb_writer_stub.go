//go:build !duckdb

package olap

import (
	"context"
	"errors"
)

var errDuckDBNotCompiled = errors.New("DuckDB support is not compiled into this Runtime build")

type DuckDBWriter struct{}

func DuckDBSupported() bool { return false }

func NewDuckDBWriter(_ int, _ string) (*DuckDBWriter, error) {
	return &DuckDBWriter{}, nil
}

func (w *DuckDBWriter) WriteParquet(context.Context, Schema, []string, string) (uint64, error) {
	return 0, errDuckDBNotCompiled
}
