// Package cluster defines Runtime node profiles and cluster admission state.
package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const (
	storageMarkerName = ".zentao-storage.json"
	storageLockName   = ".zentao-storage.lock"
)

// StorageIdentity binds a shared directory to one storage purpose.
// The identity file is stored in the shared directory and must therefore be
// identical on every node that mounts the same storage.
type StorageIdentity struct {
	SchemaVersion int    `json:"schemaVersion"`
	Purpose       string `json:"purpose"`
	StorageID     string `json:"storageID"`
	Generation    int    `json:"generation"`
}

// EnsureStorageIdentity reads or atomically creates one storage marker.
func EnsureStorageIdentity(ctx context.Context, root, purpose string) (StorageIdentity, error) {
	if root == "" || !filepath.IsAbs(root) {
		return StorageIdentity{}, errors.New("shared storage root must be absolute")
	}
	if purpose == "" {
		return StorageIdentity{}, errors.New("shared storage purpose is required")
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return StorageIdentity{}, fmt.Errorf("create shared storage root: %w", err)
	}

	lock := flock.New(filepath.Join(root, storageLockName))
	lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 50*time.Millisecond)
	if err != nil {
		return StorageIdentity{}, fmt.Errorf("lock shared storage marker: %w", err)
	}
	if !locked {
		return StorageIdentity{}, errors.New("shared storage marker lock is busy")
	}
	defer lock.Unlock()

	path := filepath.Join(root, storageMarkerName)
	identity, err := readStorageIdentity(path)
	if err == nil {
		if identity.Purpose != purpose {
			return StorageIdentity{}, fmt.Errorf(
				"shared storage identity mismatch: purpose=%q",
				identity.Purpose,
			)
		}
		return identity, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return StorageIdentity{}, err
	}

	storageID, err := randomStorageID()
	if err != nil {
		return StorageIdentity{}, err
	}
	identity = StorageIdentity{
		SchemaVersion: 1,
		Purpose:       purpose,
		StorageID:     storageID,
		Generation:    1,
	}
	if err := writeStorageIdentity(path, identity); err != nil {
		return StorageIdentity{}, err
	}
	return identity, nil
}

func readStorageIdentity(path string) (StorageIdentity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return StorageIdentity{}, err
	}
	var identity StorageIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return StorageIdentity{}, fmt.Errorf("decode shared storage marker: %w", err)
	}
	if identity.SchemaVersion != 1 || identity.StorageID == "" || identity.Purpose == "" || identity.Generation < 1 {
		return StorageIdentity{}, errors.New("invalid shared storage marker")
	}
	return identity, nil
}

func writeStorageIdentity(path string, identity StorageIdentity) error {
	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".zentao-storage-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create shared storage marker temp: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o640); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish shared storage marker: %w", err)
	}
	return nil
}

func randomStorageID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
