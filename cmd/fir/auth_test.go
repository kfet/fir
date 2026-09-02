package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/auth"
)

// TestReportRefreshResults_Format pins the per-slot output contract: terse,
// tab-separated and greppable, because the intended consumer is a cron job's
// journal entry, not a human at a terminal.
func TestReportRefreshResults_Format(t *testing.T) {
	exp := time.Now().Add(8 * time.Hour)
	var buf bytes.Buffer
	err := reportRefreshResults(&buf, []auth.RefreshResult{
		{SlotKey: "anthropic", Label: "me@x.com", Outcome: auth.OutcomeRefreshed, Expires: exp.UnixMilli()},
		{SlotKey: "anthropic#work", Label: "work@x.com", Outcome: auth.OutcomeFresh, Expires: exp.UnixMilli()},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), buf.String())
	}
	fields := strings.Split(lines[0], "\t")
	if len(fields) != 5 {
		t.Fatalf("line has %d fields, want 5: %q", len(fields), lines[0])
	}
	if fields[0] != "anthropic" || fields[1] != "me@x.com" || fields[2] != "refreshed" {
		t.Errorf("unexpected leading fields: %q", lines[0])
	}
	if !strings.HasPrefix(fields[3], "expires=") || !strings.HasPrefix(fields[4], "in=") {
		t.Errorf("expiry fields malformed: %q", lines[0])
	}
	if !strings.Contains(lines[1], "\tfresh\t") {
		t.Errorf("second line missing fresh outcome: %q", lines[1])
	}
}

// TestReportRefreshResults_FailureExitStatus: any failed slot must produce a
// non-nil error (which becomes a non-zero exit code) while still reporting
// every slot, so cron sees both the failure and the slots that were fine.
func TestReportRefreshResults_FailureExitStatus(t *testing.T) {
	var buf bytes.Buffer
	err := reportRefreshResults(&buf, []auth.RefreshResult{
		{SlotKey: "anthropic", Label: "default", Outcome: auth.OutcomeRefreshed, Expires: time.Now().Add(time.Hour).UnixMilli()},
		{SlotKey: "anthropic#dead", Label: "dead@x.com", Outcome: auth.OutcomeFailed, Err: errFake},
	})
	if err == nil {
		t.Fatal("err = nil, want non-nil so the process exits non-zero")
	}
	if !strings.Contains(err.Error(), "1 of 2") {
		t.Errorf("error should count failures: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "anthropic\tdefault\trefreshed") {
		t.Errorf("healthy slot not reported: %q", out)
	}
	if !strings.Contains(out, "failed") || !strings.Contains(out, errFake.Error()) {
		t.Errorf("failure detail not reported: %q", out)
	}
}

// TestReportRefreshResults_SkipAndNoExpiry: a skipped slot prints its reason
// and a credential with no expiry prints placeholders rather than epoch zero.
func TestReportRefreshResults_SkipAndNoExpiry(t *testing.T) {
	var buf bytes.Buffer
	err := reportRefreshResults(&buf, []auth.RefreshResult{
		{SlotKey: "anthropic#key", Label: "key", Outcome: auth.OutcomeSkipped, Reason: "not an OAuth credential (api_key)"},
	})
	if err != nil {
		t.Fatalf("skipped slot must not fail the run: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "expires=-\tin=-") {
		t.Errorf("missing no-expiry placeholders: %q", out)
	}
	if !strings.Contains(out, "not an OAuth credential (api_key)") {
		t.Errorf("skip reason not printed: %q", out)
	}
	if strings.Contains(out, "1970") {
		t.Errorf("zero expiry rendered as epoch: %q", out)
	}
}

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

const errFake = fakeErr("invalid_grant: Refresh token expired")

// TestAuthSubcommandRegistered: `fir auth` must be dispatchable and appear in
// --help, since the registry drives both.
func TestAuthSubcommandRegistered(t *testing.T) {
	if dispatchSubcommand("auth") == nil {
		t.Fatal("`fir auth` is not dispatchable")
	}
	for _, sc := range subcommands {
		if sc.Name != "auth" {
			continue
		}
		if len(sc.Help) == 0 {
			t.Error("`fir auth` has no --help rows")
		}
		return
	}
	t.Fatal("`fir auth` missing from the subcommand registry")
}
