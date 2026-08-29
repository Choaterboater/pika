//go:build !windows

package adopt

import (
	"os/exec"
	"syscall"
)

// setGroup puts the baseline command in its own process group so a timeout
// kill reaches the whole tree, mirroring the verification ladder's gate
// execution.
func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup SIGKILLs the command's entire process group.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// Negative pid targets the process group set by Setpgid.
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
