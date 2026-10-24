package resources

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// wtTmux is an isolated tmux server: TMUX_TMPDIR points at a private dir so
// neither the test nor spawn.sh can touch the user's real sessions.
type wtTmux struct {
	t   *testing.T
	env []string
}

func newWTTmux(t *testing.T) *wtTmux {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// Short path: unix socket paths are length-limited.
	dir, err := os.MkdirTemp("", "wtt")
	if err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_PANE=") && !strings.HasPrefix(kv, "TMUX_TMPDIR=") {
			env = append(env, kv)
		}
	}
	env = append(env, "TMUX_TMPDIR="+dir, "WT_FIR_CMD=sleep 600 #")
	m := &wtTmux{t: t, env: env}
	t.Cleanup(func() {
		_ = m.cmd("tmux", "kill-server").Run()
		_ = os.RemoveAll(dir)
	})
	return m
}

func (m *wtTmux) cmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.Env = m.env
	return c
}

func (m *wtTmux) run(dir, name string, args ...string) (string, error) {
	c := m.cmd(name, args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	return string(out), err
}

func (m *wtTmux) hasSession(name string) bool {
	return m.cmd("tmux", "has-session", "-t", "="+name).Run() == nil
}

func wtRepo(t *testing.T, m *wtTmux) string {
	t.Helper()
	root, err := os.MkdirTemp("", "wtrepo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		matches, _ := filepath.Glob(root + "*")
		for _, p := range matches {
			_ = os.RemoveAll(p)
		}
	})
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := m.run(root, "git", args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

func spawnScript(t *testing.T) string {
	p, err := filepath.Abs("builtin_skills/wt/scripts/spawn.sh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWTSpawnOwnSessionSurvivesDecoyKill(t *testing.T) {
	m := newWTTmux(t)
	repo := wtRepo(t, m)
	// Decoy is created first so it is the most recently used session.
	if out, err := m.run(repo, "tmux", "new-session", "-d", "-s", "decoy", "sleep 600"); err != nil {
		t.Fatalf("decoy: %v\n%s", err, out)
	}
	out, err := m.run(repo, "bash", spawnScript(t), "feat.x", "Mode: do. task")
	if err != nil {
		t.Fatalf("spawn: %v\n%s", err, out)
	}
	for _, want := range []string{"session:  feat_x", "window:   feat.x", "attach:   tmux attach -t feat_x"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if out, err := m.run(repo, "tmux", "kill-session", "-t", "=decoy"); err != nil {
		t.Fatalf("kill decoy: %v\n%s", err, out)
	}
	if !m.hasSession("feat_x") {
		t.Fatal("agent session died with the decoy")
	}
}

func TestWTSpawnSessionCollision(t *testing.T) {
	m := newWTTmux(t)
	repo := wtRepo(t, m)
	if out, err := m.run(repo, "tmux", "new-session", "-d", "-s", "dup", "sleep 600"); err != nil {
		t.Fatalf("pre-create: %v\n%s", err, out)
	}
	out, err := m.run(repo, "bash", spawnScript(t), "dup", "task")
	if err == nil {
		t.Fatalf("spawn succeeded despite existing session:\n%s", out)
	}
	if !strings.Contains(out, "dup already exists") {
		t.Errorf("unclear failure:\n%s", out)
	}
	if _, statErr := os.Stat(repo + "-wt-dup"); statErr == nil {
		t.Error("worktree created despite collision")
	}
}
