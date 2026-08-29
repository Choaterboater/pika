//go:build !windows

package verify

import (
	"os/exec"
	"syscall"
)

// setGroup puts the gate process in its own process group so a timeout
// kill reaches the whole tree, not just the direct child: grandchildren
// that inherited the combined-output pipe die with the gate instead of
// keeping CombinedOutput blocked past the deadline.
func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup SIGKILLs the gate's entire process group. It is used as
// cmd.Cancel, replacing exec.CommandContext's direct-child-only kill.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// Negative pid targets the process group set by Setpgid.
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
