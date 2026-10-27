package bi

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testManifest() Manifest {
	sum := sha256.Sum256([]byte("part"))
	return Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Table:         "zt_bug",
		Generation:    2,
		PublishedAt:   time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC),
		RowCount:      2,
		Buckets: map[string][]Part{
			"000": {{Path: "zt_bug/schema=v1/bucket=000/part-a.parquet", Rows: 1, SHA256: hex.EncodeToString(sum[:])}},
			"001": {{Path: "zt_bug/schema=v1/bucket=001/part-b.parquet", Rows: 1, SHA256: hex.EncodeToString(sum[:])}},
		},
	}
}

func TestWriteAndReadManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifests", "zt_bug.json")
	manifest := testManifest()
	if err := WriteManifestAtomic(path, manifest); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Generation != manifest.Generation || loaded.RowCount != 2 {
		t.Fatalf("unexpected manifest: %#v", loaded)
	}
	if got := len(loaded.PartPaths()); got != 2 {
		t.Fatalf("part count = %d, want 2", got)
	}
}

func TestManifestValidation(t *testing.T) {
	cases := map[string]func(*Manifest){
		"schema":     func(m *Manifest) { m.SchemaVersion = 2 },
		"table":      func(m *Manifest) { m.Table = "../bug" },
		"generation": func(m *Manifest) { m.Generation = 0 },
		"empty":      func(m *Manifest) { m.Buckets = nil },
		"rows":       func(m *Manifest) { m.RowCount = 99 },
		"path":       func(m *Manifest) { m.Buckets["000"][0].Path = "../outside.parquet" },
		"checksum":   func(m *Manifest) { m.Buckets["000"][0].SHA256 = "bad" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := testManifest()
			mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestWriteManifestAtomicReplacesOnlyAfterValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zt_bug.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := testManifest()
	manifest.RowCount = 0
	if err := WriteManifestAtomic(path, manifest); err == nil {
		t.Fatal("invalid manifest must be rejected")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("existing manifest changed to %q", data)
	}
}
