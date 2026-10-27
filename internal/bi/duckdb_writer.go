//go:build duckdb

package bi

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2"
)

// DuckDBWriter writes staged JSONL rows into one immutable Parquet part.
type DuckDBWriter struct {
	threads     int
	memoryLimit string
}

func DuckDBSupported() bool { return true }

func NewDuckDBWriter(threads int, memoryLimit string) (*DuckDBWriter, error) {
	if threads <= 0 {
		threads = 4
	}
	if memoryLimit == "" {
		memoryLimit = "1GB"
	}
	return &DuckDBWriter{threads: threads, memoryLimit: memoryLimit}, nil
}

func (w *DuckDBWriter) WriteParquet(ctx context.Context, schema Schema, stagingFiles []string, targetPath string) (uint64, error) {
	if err := schema.Validate(); err != nil {
		return 0, err
	}
	if !strings.HasSuffix(targetPath, ".parquet.tmp") {
		return 0, fmt.Errorf("BI DuckDB target must be a .parquet.tmp path")
	}
	if len(stagingFiles) == 0 {
		return 0, fmt.Errorf("BI staging files are required")
	}

	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if err := configureDuckDB(ctx, db, w.memoryLimit, w.threads); err != nil {
		return 0, err
	}

	columnDefs := make([]string, 0, len(schema.Columns))
	columnMap := make([]string, 0, len(schema.Columns))
	for _, column := range schema.Columns {
		columnType, err := normalizeColumnType(column.Type)
		if err != nil {
			return 0, err
		}
		columnDefs = append(columnDefs, quoteSQLIdentifier(column.Name)+" "+columnType)
		columnMap = append(columnMap, quoteSQLLiteral(column.Name)+":"+quoteSQLLiteral(columnType))
	}

	paths := make([]string, 0, len(stagingFiles))
	allowedDirs := make([]string, 0, len(stagingFiles)+1)
	var stagedBytes int64
	for _, path := range stagingFiles {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return 0, err
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return 0, err
		}
		stagedBytes += info.Size()
		paths = append(paths, quoteSQLLiteral(absolute))
		allowedDirs = append(allowedDirs, filepath.Dir(absolute))
	}
	sort.Strings(paths)
	allowedDirs = append(allowedDirs, filepath.Dir(targetPath))
	if err := restrictDuckDBFilesystem(ctx, db, allowedDirs); err != nil {
		return 0, err
	}

	source := fmt.Sprintf(
		"SELECT * FROM read_json([%s], format='newline_delimited', columns={%s}, ignore_errors=false, maximum_object_size=16777216)",
		strings.Join(paths, ","),
		strings.Join(columnMap, ","),
	)
	if _, err := db.ExecContext(ctx, "CREATE TABLE bi_rows ("+strings.Join(columnDefs, ",")+")"); err != nil {
		return 0, fmt.Errorf("create BI staging table: %w", err)
	}
	if stagedBytes > 0 {
		if _, err := db.ExecContext(ctx, "INSERT INTO bi_rows "+source); err != nil {
			return 0, fmt.Errorf("load BI staging JSONL: %w", err)
		}
	}

	var rows, unique uint64
	countSQL := fmt.Sprintf(
		"SELECT COUNT(*), COUNT(DISTINCT %s) FROM bi_rows",
		quoteSQLIdentifier(schema.PrimaryKey),
	)
	if err := db.QueryRowContext(ctx, countSQL).Scan(&rows, &unique); err != nil {
		return 0, fmt.Errorf("count BI rows: %w", err)
	}
	if rows != unique {
		return 0, fmt.Errorf("BI staging has duplicate primary keys: rows=%d unique=%d", rows, unique)
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		return 0, err
	}
	_ = os.Remove(targetPath)
	copySQL := fmt.Sprintf(
		"COPY (SELECT * FROM bi_rows ORDER BY %s) TO %s (FORMAT PARQUET, COMPRESSION ZSTD)",
		quoteSQLIdentifier(schema.PrimaryKey),
		quoteSQLLiteral(targetPath),
	)
	if _, err := db.ExecContext(ctx, copySQL); err != nil {
		return 0, fmt.Errorf("write BI Parquet: %w", err)
	}
	if info, err := os.Stat(targetPath); err != nil || info.Size() == 0 {
		if err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("BI Parquet output is empty")
	}
	return rows, nil
}

func configureDuckDB(ctx context.Context, db *sql.DB, memoryLimit string, threads int) error {
	for _, statement := range []string{
		"SET memory_limit=" + quoteSQLLiteral(memoryLimit),
		fmt.Sprintf("SET threads=%d", threads),
		"SET autoinstall_known_extensions=false",
		"SET autoload_known_extensions=false",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure BI DuckDB: %w", err)
		}
	}
	return nil
}

func quoteSQLIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteSQLLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}
