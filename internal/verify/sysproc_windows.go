//go:build windows

package verify

import "os/exec"

// Windows has no portable process-group kill via SysProcAttr here, so the
// group kill degrades to the direct child. WaitDelay (set in runGate)
// still bounds Wait on orphaned pipe holders, so check can never hang.
func setGroup(cmd *exec.Cmd) {}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
