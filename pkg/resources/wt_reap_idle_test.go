package resources

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWTReapIdleClosesOnlyIdleDetachedSessions(t *testing.T) {
	m := newWTTmux(t)
	dir := t.TempDir()
	// -f /dev/null: the user's tmux.conf (e.g. tmux-resurrect) must not add
	// sessions of its own to the test server.
	sessions := [][]string{
		{"-f", "/dev/null", "new-session", "-d", "-s", "idle", "sh"},
		{"new-session", "-d", "-s", "busy", "sleep 600"},
		{"new-session", "-d", "-s", "shellbusy", "sh -c 'sleep 600; :'"},
		{"new-session", "-d", "-s", "mixed", "sh"},
		{"split-window", "-t", "mixed:", "sleep 600"},
		{"new-session", "-d", "-s", "att", "sh"},
		{"new-session", "-d", "-s", "dead", "sh"},
		{"set-option", "-t", "dead:", "remain-on-exit", "on"},
		{"respawn-pane", "-k", "-t", "dead:", "true"},
	}
	for _, args := range sessions {
		if out, err := m.run(dir, "tmux", args...); err != nil {
			t.Fatalf("tmux %v: %v\n%s", args, err, out)
		}
	}

	// Attach a client to "att" through a pseudo-terminal.
	var client *exec.Cmd
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script not installed")
	}
	if runtime.GOOS == "darwin" {
		client = m.cmd("script", "-q", "/dev/null", "tmux", "attach", "-t", "att")
	} else {
		client = m.cmd("script", "-qfc", "tmux attach -t att", "/dev/null")
	}
	var cbuf strings.Builder
	client.Stdout, client.Stderr = &cbuf, &cbuf
	// Keep stdin open: at EOF script sends ^D, which would end the pane's sh.
	stdin, err := client.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := client.Start(); err != nil {
		t.Fatalf("attach: %v", err)
	}
	t.Cleanup(func() { _ = client.Process.Kill(); _ = client.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _ := m.run(dir, "tmux", "display-message", "-p", "-t", "att:", "#{session_attached}")
		if strings.TrimSpace(out) != "0" && strings.TrimSpace(out) != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Skipf("could not attach a tmux client in this environment: %.200q", cbuf.String())
		}
		time.Sleep(100 * time.Millisecond)
	}

	script, err := filepath.Abs("builtin_skills/wt/scripts/reap-idle.sh")
	if err != nil {
		t.Fatal(err)
	}

	// Dry run changes nothing.
	out, err := m.run(dir, "bash", script)
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "REAP idle") || !m.hasSession("idle") {
		t.Fatalf("dry run should report but keep idle:\n%s", out)
	}

	out, err = m.run(dir, "bash", script, "--reap")
	if err != nil {
		t.Fatalf("reap: %v\n%s", err, out)
	}
	for _, gone := range []string{"idle", "dead"} {
		if m.hasSession(gone) {
			t.Errorf("session %q survived:\n%s", gone, out)
		}
	}
	for _, keep := range []string{"busy", "shellbusy", "mixed", "att"} {
		if !m.hasSession(keep) {
			t.Errorf("session %q was closed:\n%s", keep, out)
		}
	}
}
