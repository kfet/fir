package main

import (
	"os"
	"testing"
)

func TestParseChdirFlag(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantDir     string
		wantIdx     int
		wantConsume int
		wantFound   bool
		wantErr     bool
	}{
		{"none", []string{"sessions"}, "", 0, 0, false, false},
		{"short separate", []string{"-C", "/tmp", "sessions"}, "/tmp", 0, 2, true, false},
		{"short equals", []string{"-C=/tmp", "sessions"}, "/tmp", 0, 1, true, false},
		{"long cwd separate", []string{"--cwd", "dir"}, "dir", 0, 2, true, false},
		{"long cwd equals", []string{"--cwd=dir"}, "dir", 0, 1, true, false},
		{"long directory equals", []string{"--directory=x"}, "x", 0, 1, true, false},
		{"later position", []string{"sessions", "-C", "/tmp"}, "/tmp", 1, 2, true, false},
		{"missing arg", []string{"-C"}, "", 0, 0, true, true},
		{"empty equals", []string{"-C="}, "", 0, 0, true, true},
		{"empty long", []string{"--cwd="}, "", 0, 0, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, idx, consume, found, err := parseChdirFlag(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if dir != tc.wantDir || idx != tc.wantIdx || consume != tc.wantConsume || found != tc.wantFound {
				t.Fatalf("got dir=%q idx=%d consume=%d found=%v; want %q %d %d %v",
					dir, idx, consume, found, tc.wantDir, tc.wantIdx, tc.wantConsume, tc.wantFound)
			}
		})
	}
}

func TestParseAgentDirFlag(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantDir     string
		wantIdx     int
		wantConsume int
		wantFound   bool
		wantErr     bool
	}{
		{"none", []string{"sessions"}, "", 0, 0, false, false},
		{"separate", []string{"--agent-dir", "/tmp/fir", "sessions"}, "/tmp/fir", 0, 2, true, false},
		{"equals", []string{"--agent-dir=/tmp/fir", "sessions"}, "/tmp/fir", 0, 1, true, false},
		{"later position", []string{"sessions", "--agent-dir", "/tmp/fir"}, "/tmp/fir", 1, 2, true, false},
		{"missing arg", []string{"--agent-dir"}, "", 0, 0, true, true},
		{"empty separate", []string{"--agent-dir", ""}, "", 0, 0, true, true},
		{"empty equals", []string{"--agent-dir="}, "", 0, 0, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, idx, consume, found, err := parseAgentDirFlag(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if dir != tc.wantDir || idx != tc.wantIdx || consume != tc.wantConsume || found != tc.wantFound {
				t.Fatalf("got dir=%q idx=%d consume=%d found=%v; want %q %d %d %v",
					dir, idx, consume, found, tc.wantDir, tc.wantIdx, tc.wantConsume, tc.wantFound)
			}
		})
	}
}

func TestApplyAgentDirFlagOverridesEnvAndStripsArgs(t *testing.T) {
	envDir := t.TempDir()
	flagDir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", envDir)
	origArgs := os.Args
	os.Args = []string{"fir", "sessions", "--agent-dir", flagDir}
	t.Cleanup(func() { os.Args = origArgs })

	if err := applyAgentDirFlag(); err != nil {
		t.Fatalf("applyAgentDirFlag: %v", err)
	}
	if got := os.Getenv("FIR_AGENT_DIR"); got != flagDir {
		t.Fatalf("FIR_AGENT_DIR=%q, want %q", got, flagDir)
	}
	if got := resolveAgentDir(); got != flagDir {
		t.Fatalf("resolveAgentDir()=%q, want %q", got, flagDir)
	}
	wantArgs := []string{"fir", "sessions"}
	if len(os.Args) != len(wantArgs) {
		t.Fatalf("os.Args=%v, want %v", os.Args, wantArgs)
	}
	for i := range wantArgs {
		if os.Args[i] != wantArgs[i] {
			t.Fatalf("os.Args=%v, want %v", os.Args, wantArgs)
		}
	}
}
