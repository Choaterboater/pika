//go:build windows

package evidence

import (
	"errors"
	"os"
	"syscall"
)

// On Windows, FlushFileBuffers requires a write-mode handle, so syncing
// a read-mode handle fails with access denied. Durability here is
// therefore weaker than on unix: content fsyncs on write-mode handles
// (the temp file) are real, but read-mode sync attempts on directories
// are tolerated as best-effort and never fail a Write.

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		// Directory handles may lack any sync path. Tolerate the
		// documented denials.
		if errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, syscall.Errno(1) /* ERROR_INVALID_FUNCTION */) {
			return nil
		}
		return err
	}
	return nil
}
