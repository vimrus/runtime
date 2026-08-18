//go:build !duckdb

package duckdb

// Version reports that DuckDB support is not compiled in.
const Version = "not-compiled"

// Supported reports whether DuckDB support is compiled in.
func Supported() bool { return false }
