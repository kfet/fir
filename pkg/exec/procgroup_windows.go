//go:build windows

package exec

import "os/exec"

// setProcGroup is a no-op on Windows; exec.CommandContext's default
// cancel (TerminateProcess) already kills the process tree.
func setProcGroup(cmd *exec.Cmd) {}
