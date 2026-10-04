package extension

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// sigVerbScript is a minimal Python extension whose `sigtest` verb blocks
// on stdin and exits the process from its cli_signal handler — what an
// extension does when Ctrl-C / SIGTERM / an ssh hangup reaches it.
const sigVerbScript = `#!/usr/bin/env python3
# ---
# name: sigtest
# cli_verbs: sigtest
# ---
import os
import fir_ext

@fir_ext.cli_verb("sigtest")
def verb(argv, host):
    host.println("ready")
    host.readline()
    return 0

@fir_ext.on_cli_signal
def on_signal(name, host):
    os._exit(0)

fir_ext.run(name="sigtest")
`

// TestRunCLIVerb_SignalExitIsNotAnError: an extension that exits because a
// forwarded terminating signal told it to must yield a plain 128+signal exit,
// not "extension exited before returning a result" (which `timeout`, Ctrl-C
// or a dropped ssh session used to print on `fir observe`).
func TestRunCLIVerb_SignalExitIsNotAnError(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	for _, tc := range []struct {
		sig  syscall.Signal
		want int
	}{
		{syscall.SIGTERM, 128 + int(syscall.SIGTERM)},
		{syscall.SIGINT, 128 + int(syscall.SIGINT)},
		{syscall.SIGQUIT, 0}, // Ctrl-\: clean detach
	} {
		t.Run(tc.sig.String(), func(t *testing.T) {
			if code := runSigVerb(t, tc.sig); code != tc.want {
				t.Fatalf("exit code = %d; want %d", code, tc.want)
			}
		})
	}
}

func runSigVerb(t *testing.T, sig syscall.Signal) int {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "sigtest.py")
	if err := os.WriteFile(script, []byte(sigVerbScript), 0o755); err != nil {
		t.Fatal(err)
	}

	// The verb's stdin must stay open (so it blocks), and we need to see
	// its "ready" line before signalling.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origIn, origOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdinR, stdoutW
	defer func() {
		os.Stdin, os.Stdout = origIn, origOut
		stdinW.Close()
		stdoutW.Close()
		stdoutR.Close()
	}()

	go func() {
		line, _ := bufio.NewReader(stdoutR).ReadString('\n')
		if strings.TrimSpace(line) == "ready" {
			_ = syscall.Kill(os.Getpid(), sig)
		}
	}()

	binding := &CLIVerbBinding{Verb: "sigtest", Ext: ExtProcConfig{Name: "sigtest", Path: script, Scope: "project"}}
	code, err := RunCLIVerb(binding, nil, dir, dir)
	if err != nil {
		t.Fatalf("RunCLIVerb error = %v; want a quiet signal exit", err)
	}
	return code
}
