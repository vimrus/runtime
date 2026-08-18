//go:build duckdb

package duckdb

// Version reports the compiled DuckDB Go binding version.
const Version = "v2.10505.0"

// Supported reports whether DuckDB support is compiled in.
func Supported() bool { return true }
