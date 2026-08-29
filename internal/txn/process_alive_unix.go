//go:build unix

package txn

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process with pid exists. EPERM means
// the process exists but belongs to another user — still alive for our
// purposes. A reused pid can make a dead holder look alive, which only
// makes recovery more conservative.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
