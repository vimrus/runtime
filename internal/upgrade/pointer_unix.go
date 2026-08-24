//go:build !windows

package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func swapPointer(path, target string) error {
	temp := path + ".tmp-" + fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	if err := os.Symlink(target, temp); err != nil {
		return fmt.Errorf("create pointer: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("swap pointer: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open pointer directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync pointer directory: %w", err)
	}
	return nil
}
