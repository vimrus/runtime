package olap

import (
	"strings"
	"testing"
)

func TestDuckDBCompatibilitySQLDefinesRequiredMacros(t *testing.T) {
	sql := DuckDBCompatibilitySQL()
	for _, name := range []string{"date_format", "datediff", "date_sub", "find_in_set", "truncate"} {
		if !strings.Contains(sql, "CREATE MACRO "+name+"(") {
			t.Fatalf("compatibility SQL is missing macro %s", name)
		}
	}
	if strings.Contains(strings.ToLower(sql), "create macro to_days") {
		t.Fatal("DuckDB to_days must be compiled inline because the macro name is used internally for INTERVAL values")
	}
	if strings.Contains(strings.ToLower(sql), "create macro left") {
		t.Fatal("DuckDB LEFT is a parser keyword and must be compiled to substr")
	}
}

func TestDuckDBCompatibilitySQLDoesNotLoadExtensions(t *testing.T) {
	upper := strings.ToUpper(DuckDBCompatibilitySQL())
	for _, forbidden := range []string{"INSTALL ", "LOAD ", "ATTACH ", "HTTP://", "S3://"} {
		if strings.Contains(upper, forbidden) {
			t.Fatalf("compatibility SQL contains forbidden token %q", forbidden)
		}
	}
}
