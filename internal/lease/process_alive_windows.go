//go:build windows

package lease

// processAlive conservatively reports any positive pid as alive: the
// Windows API has no portable stdlib-only liveness check, so a lease is
// never reported stale there and the operator removes a dead holder's
// lock by hand.
func processAlive(pid int) bool {
	return pid > 0
}
