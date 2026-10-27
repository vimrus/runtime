//go:build duckdb

package bi

import (
	"context"
	"database/sql"
	"math"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
)

func TestDuckDBCompatibilityMacros(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, DuckDBCompatibilitySQL()); err != nil {
		t.Fatal(err)
	}

	var formatted string
	var days, position, numericPosition int64
	var ifValue, ifnullValue int64
	var previousDay string
	var truncated float64
	err = db.QueryRowContext(ctx, `
		SELECT
			CAST(date_format(DATE '2026-10-26', '%Y-%m-%d') AS VARCHAR),
			datediff(DATE '2026-10-26', DATE '2026-10-20'),
			find_in_set('b', 'a,b,c'),
			find_in_set(2, '1,2,3'),
			truncate(12.349, 2),
			IF(1 = 1, 1, 0),
			IFNULL(NULL, 2),
			CAST(CAST(date_sub(DATE '2026-10-26', INTERVAL '1' DAY) AS DATE) AS VARCHAR)
	`).Scan(&formatted, &days, &position, &numericPosition, &truncated, &ifValue, &ifnullValue, &previousDay)
	if err != nil {
		t.Fatal(err)
	}
	if formatted != "2026-10-26" || days != 6 || position != 2 || numericPosition != 2 || math.Abs(truncated-12.34) > 0.000001 || ifValue != 1 || ifnullValue != 2 || previousDay != "2026-10-25" {
		t.Fatalf("unexpected compatibility results: %q %d %d %d %v", formatted, days, position, numericPosition, truncated)
	}
}
