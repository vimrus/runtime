//go:build windows

package upgrade

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// Windows installations use a pointer file because ordinary service accounts
// cannot reliably create symbolic links. The replacement has a brief gap while
// the old pointer is removed; callers keep the transaction audit for recovery.
func swapPointer(path, target string) error {
	temp := path + ".tmp-" + fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(temp, []byte(target+"\n"), 0o600); err != nil {
		return fmt.Errorf("create pointer: %w", err)
	}
	if err := windows.MoveFileEx(windows.StringToUTF16Ptr(temp), windows.StringToUTF16Ptr(path), windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("swap pointer: %w", err)
	}
	return nil
}
