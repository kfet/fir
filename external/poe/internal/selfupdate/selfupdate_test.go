package selfupdate

import (
	"context"
	"testing"
	"time"
)

func TestCheck_CurrentVersionNotCrash(t *testing.T) {
	// This test hits the real GitHub API (read-only, no auth needed).
	// It verifies the Check function doesn't crash and returns a valid
	// result structure. Since there may not be a real release yet, we
	// accept both "no update" and "update available" as valid outcomes.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := Check(ctx, "0.0.0")
	if err != nil {
		// Network failures in CI are acceptable; skip rather than fail.
		t.Skipf("Check failed (likely network): %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
	if res.Current != "0.0.0" {
		t.Errorf("Current: got %q want '0.0.0'", res.Current)
	}
	// If there's a release, Available should be true for version 0.0.0.
	// If no releases exist yet, Available should be false. Both are valid.
	t.Logf("Available=%v Latest=%q", res.Available, res.Latest)
}

func TestMapArch(t *testing.T) {
	// Just verify it doesn't panic and returns a non-empty string.
	a := mapArch()
	if a == "" {
		t.Error("mapArch returned empty")
	}
}
