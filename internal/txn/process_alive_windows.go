//go:build windows

package txn

// processAlive conservatively reports any positive pid as alive: the
// Windows API has no portable stdlib-only liveness check, so recovery
// refuses to touch locked transactions there until the operator removes
// a stale lock by hand.
func processAlive(pid int) bool {
	return pid > 0
}
