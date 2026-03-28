//go:build !windows

package exec

import (
	"os/exec"
	"syscall"
)

// setProcGroup configures cmd to run in its own process group and overrides
// the cancel function to kill the entire group (not just the leader).
// This ensures child processes (e.g. go run's compiled binary) are cleaned up.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative PID = kill entire process group.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
