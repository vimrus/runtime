package bi

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidateBIQuery(t *testing.T) {
	valid := []string{
		`SELECT "id" FROM "zt_bug"`,
		`WITH bugs AS (SELECT * FROM "zt_bug") SELECT COUNT(*) FROM bugs`,
		`SELECT 'read_parquet; safe' AS value`,
		`SELECT * FROM "zt_bug";`,
		`SELECT 1 AS create, truncate(1.234, 2) AS value`,
	}
	for _, sql := range valid {
		if err := ValidateBIQuery(sql, 0); err != nil {
			t.Fatalf("valid query rejected: %s: %v", sql, err)
		}
	}

	invalid := []string{
		`UPDATE zt_bug SET deleted = 1`,
		`SELECT * FROM read_parquet('/tmp/data.parquet')`,
		`SELECT * FROM read_text('/etc/passwd')`,
		`SELECT * FROM read_json_auto('/tmp/data.json')`,
		`SELECT * FROM read_csv_auto('/tmp/data.csv')`,
		`SELECT * FROM query('SELECT 1')`,
		`WITH changed AS (DELETE FROM zt_bug RETURNING id) SELECT * FROM changed`,
		`SELECT 1; SELECT 2`,
		`SELECT 'unterminated`,
	}
	for _, sql := range invalid {
		if err := ValidateBIQuery(sql, 0); err == nil {
			t.Fatalf("invalid query accepted: %s", sql)
		}
	}
}

func TestLoadManifests(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "manifests")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := testManifest()
	if err := WriteManifestAtomic(filepath.Join(dir, "zt_bug.json"), manifest); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifests(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded["zt_bug"].PublishedAt != time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC) {
		t.Fatalf("unexpected manifest: %#v", loaded["zt_bug"])
	}
}
