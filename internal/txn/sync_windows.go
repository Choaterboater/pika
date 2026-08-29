//go:build windows

package txn

import (
	"errors"
	"os"
	"syscall"
)

// On Windows, FlushFileBuffers requires a write-mode handle, so syncing
// a read-mode handle fails with access denied. Durability here is
// therefore weaker than on unix: content fsyncs on write-mode handles
// (temp files, the journal, the lock) are real, but read-mode sync
// attempts on files and directories are tolerated as best-effort and
// never fail an operation.

func syncFile(path string) error {
	return syncHandle(func() (*os.File, error) { return os.Open(path) })
}

func syncDir(dir string) error {
	return syncHandle(func() (*os.File, error) { return os.Open(dir) })
}

func syncHandle(open func() (*os.File, error)) error {
	f, err := open()
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		// Read-mode handles cannot FlushFileBuffers; directory handles
		// may lack any sync path. Tolerate the documented denials.
		if errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, syscall.Errno(1) /* ERROR_INVALID_FUNCTION */) {
			return nil
		}
		return err
	}
	return nil
}
