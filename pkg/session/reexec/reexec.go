// Package reexec provides the low-level "replace this process with a fresh
// copy of itself" primitive used by the interactive `/reexec` command and
// `/update` self-update flow.
//
// Design note: this package deliberately does NOT install a SIGHUP handler.
// Earlier versions turned SIGHUP into a graceful reexec across every mode,
// but SIGHUP has well-established Unix semantics ("the controlling terminal
// or parent went away") that many tools rely on:
//
//   - `tmux respawn-window -k` sends SIGHUP to the pane's process group
//   - ssh sends SIGHUP when the connection drops
//   - shells send SIGHUP to jobs when they exit
//
// Catching SIGHUP globally caused fir to re-exec itself in all of those
// situations, detaching from the (now-dying) tty and leaving an orphaned
// process behind. Because MCP/extension subprocesses run in their own
// process groups (Setpgid), syscall.Exec preserves them across the
// re-exec, so every unintended SIGHUP leaked another tree of agents.
// This was especially visible during `make poe-deploy`, which uses
// `tmux respawn-window -k` and accumulated zombie agents on every deploy.
//
// SIGHUP now takes its default action (terminate). The explicit /reexec
// and /update paths call Exec() directly and do not depend on signals.
package reexec

import (
	"os"
	"syscall"

	firlog "github.com/kfet/fir/pkg/log"
)

// Exec performs the actual syscall.Exec. If binary is empty, uses the
// current executable. If args is nil, uses os.Args.
// This function never returns on success.
func Exec(binary string, args []string) {
	if binary == "" {
		var err error
		binary, err = os.Executable()
		if err != nil {
			firlog.Warn("reexec: cannot determine executable", "err", err)
			return
		}
	}
	if args == nil {
		args = os.Args
	}

	restoreStdinBlocking()

	env := append(os.Environ(), "FIR_REEXEC_CONTINUE=1")
	if err := syscall.Exec(binary, args, env); err != nil {
		firlog.Warn("reexec: exec failed", "err", err)
	}
}
