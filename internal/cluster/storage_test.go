package cluster

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureStorageIdentityIsStableAndPurposeBound(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shared")
	first, err := EnsureStorageIdentity(context.Background(), root, "app-data")
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureStorageIdentity(context.Background(), root, "app-data")
	if err != nil {
		t.Fatal(err)
	}
	if first.StorageID == "" || first.StorageID != second.StorageID {
		t.Fatalf("storage identity is not stable: %#v %#v", first, second)
	}
	if _, err := EnsureStorageIdentity(context.Background(), root, "observability"); err == nil {
		t.Fatal("a marker for another purpose must be rejected")
	}

	local, err := EnsureStorageIdentity(context.Background(), filepath.Join(t.TempDir(), "local"), "app-data")
	if err != nil {
		t.Fatal(err)
	}
	if local.StorageID == first.StorageID {
		t.Fatal("independent local data roots must not share a storage identity")
	}
}

func TestEnsureStorageIdentityRejectsCorruptMarker(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, storageMarkerName), []byte("{broken"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureStorageIdentity(context.Background(), root, "app-data"); err == nil {
		t.Fatal("corrupt storage marker must be rejected")
	}

	root = t.TempDir()
	data, err := json.Marshal(StorageIdentity{
		SchemaVersion: 1,
		Purpose:       "app-data",
		StorageID:     "storage-a",
		Generation:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, storageMarkerName), data, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureStorageIdentity(context.Background(), root, "app-data"); err == nil {
		t.Fatal("storage marker with invalid generation must be rejected")
	}
}
