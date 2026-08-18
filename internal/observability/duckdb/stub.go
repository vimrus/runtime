//go:build !duckdb

package duckdb

import (
	"context"
	"errors"

	"github.com/vimrus/runtime/internal/observability"
	"github.com/vimrus/runtime/internal/observability/query"
)

var errNotCompiled = errors.New("DuckDB support is not compiled into this Runtime build")

type Writer struct{}

func NewWriter(_ string, _ int) (*Writer, error) {
	return &Writer{}, nil
}

func (w *Writer) WriteParquet(context.Context, observability.Kind, string, []observability.Event) (int64, error) {
	return 0, errNotCompiled
}

type Engine struct{}

func NewEngine(_ int, _ string) (*Engine, error) {
	return &Engine{}, nil
}

func (e *Engine) Query(context.Context, string, int, []any) ([]query.Row, error) {
	return nil, errNotCompiled
}
