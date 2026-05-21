package extension

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// TestReservedSourceName verifies the matcher used by startOne.
func TestReservedSourceName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"plan", true},
		{"Plan", true},
		{"PLAN", true},
		{"model", true},
		{"session", true},
		{" session ", true}, // trims whitespace
		{"sessions", false},
		{"my-plan", false},
		{"", false},
		{"mood", false},
		{"observe", false},
	}
	for _, c := range cases {
		if got := reservedSourceName(c.name); got != c.want {
			t.Errorf("reservedSourceName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestManager_RejectsReservedSourceName loads an extension whose name
// is reserved ("plan") and asserts startOne returns a clear error rather
// than letting it start.
func TestManager_RejectsReservedSourceName(t *testing.T) {
	dir := t.TempDir()
	scriptPath := writeExtScript(t, dir, "plan") // name collides with reserved source

	ts := NewTrustStoreWithPath(filepath.Join(dir, "trust.json"))
	hash, _ := ComputeHash(scriptPath)
	_ = ts.RecordTrust(dir, "plan", hash)

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(ts)

	api := newMockAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start succeeds (it records failures rather than aborting), but
	// the "plan" extension must appear in startFailures with the
	// expected error message.
	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop() //nolint:errcheck

	failures := mgr.StartFailures()
	var planFailure *StartFailure
	for i := range failures {
		if failures[i].Name == "plan" {
			planFailure = &failures[i]
			break
		}
	}
	if planFailure == nil {
		t.Fatalf("expected 'plan' in startFailures, got %#v", failures)
	}
	msg := planFailure.Err.Error()
	if !strings.Contains(msg, "reserved core source") {
		t.Errorf("error should mention reserved source; got: %q", msg)
	}
	if !strings.Contains(msg, "plan") {
		t.Errorf("error should mention the extension name; got: %q", msg)
	}

	// And the tool the extension would have registered must not appear.
	for _, def := range api.toolsRegistered {
		if def.Name == "test_tool" {
			t.Errorf("reserved-name extension was allowed to register tools")
		}
	}
}
