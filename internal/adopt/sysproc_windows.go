//go:build windows

package adopt

import "os/exec"

// Windows has no portable process-group kill via SysProcAttr here, so the
// group kill degrades to the direct child. WaitDelay (set in runBaseline)
// still bounds Wait on orphaned pipe holders.
func setGroup(cmd *exec.Cmd) {}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
