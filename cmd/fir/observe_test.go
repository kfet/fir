package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withTmpStateDir points stateDir() at a temporary directory and writes the
// given sidecars into it. Returns the directory and a cleanup function.
func withTmpStateDir(t *testing.T, sidecars []sidecar) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	full := filepath.Join(dir, "fir", "agents")
	if err := os.MkdirAll(full, 0o700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	for _, s := range sidecars {
		// Sidecar JSON shape — fields must match the reader.
		data, err := json.MarshalIndent(map[string]any{
			"schema":       s.Schema,
			"session_id":   s.SessionID,
			"pid":          s.PID,
			"socket_path":  s.SocketPath,
			"store_path":   s.StorePath,
			"cwd":          s.Cwd,
			"started_at":   s.StartedAt,
			"status":       s.Status,
			"session_name": s.SessionName,
		}, "", "  ")
		if err != nil {
			t.Fatalf("marshal sidecar: %v", err)
		}
		if err := os.WriteFile(filepath.Join(full, s.SessionID+".json"), data, 0o600); err != nil {
			t.Fatalf("write sidecar: %v", err)
		}
	}
	return full
}

func TestReadSidecars_EmptyDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	got, err := readSidecars()
	if err != nil {
		t.Fatalf("readSidecars: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 sidecars, got %d", len(got))
	}
}

func TestReadSidecars_NoStateDirAtAll(t *testing.T) {
	// Point XDG_STATE_HOME at a directory that doesn't have fir/agents/.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// (mkdir intentionally skipped — readSidecars must tolerate ENOENT.)
	got, err := readSidecars()
	if err != nil {
		t.Fatalf("readSidecars must tolerate missing dir, got: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 sidecars, got %d", len(got))
	}
}

func TestReadSidecars_SortedNewestFirst(t *testing.T) {
	now := time.Now()
	withTmpStateDir(t, []sidecar{
		{Schema: 1, SessionID: "aaa00000", StartedAt: now.Add(-2 * time.Hour).UTC().Format(time.RFC3339), Status: "ended", PID: -1},
		{Schema: 1, SessionID: "bbb00000", StartedAt: now.Add(-30 * time.Minute).UTC().Format(time.RFC3339), Status: "ended", PID: -1},
		{Schema: 1, SessionID: "ccc00000", StartedAt: now.Add(-5 * time.Minute).UTC().Format(time.RFC3339), Status: "ended", PID: -1},
	})
	got, err := readSidecars()
	if err != nil {
		t.Fatalf("readSidecars: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 sidecars, got %d", len(got))
	}
	if got[0].SessionID != "ccc00000" || got[1].SessionID != "bbb00000" || got[2].SessionID != "aaa00000" {
		t.Errorf("expected newest-first order, got %s, %s, %s",
			got[0].SessionID, got[1].SessionID, got[2].SessionID)
	}
}

func TestReadSidecars_DeadPidReclassifiedAsCrashed(t *testing.T) {
	// PID -1 will never be a valid process; pidAlive returns false.
	withTmpStateDir(t, []sidecar{
		{Schema: 1, SessionID: "deadbeef", PID: -1, Status: "running",
			StartedAt: time.Now().UTC().Format(time.RFC3339)},
	})
	got, err := readSidecars()
	if err != nil {
		t.Fatalf("readSidecars: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 sidecar, got %d", len(got))
	}
	if got[0].Status != "crashed" {
		t.Errorf("expected status='crashed' for dead pid, got %q", got[0].Status)
	}
}

func TestReadSidecars_LiveOwnPidStaysRunning(t *testing.T) {
	withTmpStateDir(t, []sidecar{
		{Schema: 1, SessionID: "alive", PID: os.Getpid(), Status: "running",
			StartedAt: time.Now().UTC().Format(time.RFC3339)},
	})
	got, err := readSidecars()
	if err != nil {
		t.Fatalf("readSidecars: %v", err)
	}
	if got[0].Status != "running" {
		t.Errorf("expected status='running' for own pid, got %q", got[0].Status)
	}
}

func TestReadSidecars_EndedStatusUntouchedEvenIfPidDead(t *testing.T) {
	// Once a session is marked ended, we don't reclassify it as crashed.
	withTmpStateDir(t, []sidecar{
		{Schema: 1, SessionID: "endedsess", PID: -1, Status: "ended",
			StartedAt: time.Now().UTC().Format(time.RFC3339)},
	})
	got, _ := readSidecars()
	if got[0].Status != "ended" {
		t.Errorf("ended status should be preserved, got %q", got[0].Status)
	}
}

func TestReadSidecars_SkipsMalformedAndNonJSON(t *testing.T) {
	full := withTmpStateDir(t, []sidecar{
		{Schema: 1, SessionID: "good01", PID: os.Getpid(), Status: "running",
			StartedAt: time.Now().UTC().Format(time.RFC3339)},
	})
	// Drop in junk files that should be silently skipped.
	if err := os.WriteFile(filepath.Join(full, "bad.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "ignored.txt"), []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(full, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := readSidecars()
	if err != nil {
		t.Fatalf("readSidecars: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "good01" {
		t.Errorf("expected only 'good01', got %+v", got)
	}
}

func TestResolveSidecar_PrefixMatchOnID(t *testing.T) {
	withTmpStateDir(t, []sidecar{
		{Schema: 1, SessionID: "abc12345", PID: os.Getpid(), Status: "running",
			StartedAt: time.Now().UTC().Format(time.RFC3339), StorePath: "/tmp/x"},
	})
	s, err := resolveSidecar("abc", "")
	if err != nil {
		t.Fatalf("resolveSidecar: %v", err)
	}
	if s.SessionID != "abc12345" {
		t.Errorf("got %s", s.SessionID)
	}
}

func TestResolveSidecar_PrefixMatchOnName(t *testing.T) {
	withTmpStateDir(t, []sidecar{
		{Schema: 1, SessionID: "abc12345", SessionName: "my-feature",
			PID: os.Getpid(), Status: "running",
			StartedAt: time.Now().UTC().Format(time.RFC3339)},
	})
	s, err := resolveSidecar("my-feat", "")
	if err != nil {
		t.Fatalf("resolveSidecar: %v", err)
	}
	if s.SessionID != "abc12345" {
		t.Errorf("got %s", s.SessionID)
	}
}

func TestResolveSidecar_PrefixMatchOnCwdBasename(t *testing.T) {
	withTmpStateDir(t, []sidecar{
		{Schema: 1, SessionID: "abc12345", Cwd: "/Users/x/dev/myproj",
			PID: os.Getpid(), Status: "running",
			StartedAt: time.Now().UTC().Format(time.RFC3339)},
	})
	s, err := resolveSidecar("mypr", "")
	if err != nil {
		t.Fatalf("resolveSidecar: %v", err)
	}
	if s.SessionID != "abc12345" {
		t.Errorf("got %s", s.SessionID)
	}
}

func TestResolveSidecar_AmbiguityError(t *testing.T) {
	withTmpStateDir(t, []sidecar{
		{Schema: 1, SessionID: "aaa11111", PID: os.Getpid(), Status: "running",
			StartedAt: time.Now().UTC().Format(time.RFC3339)},
		{Schema: 1, SessionID: "aaa22222", PID: os.Getpid(), Status: "running",
			StartedAt: time.Now().UTC().Format(time.RFC3339)},
	})
	_, err := resolveSidecar("aaa", "")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected ambiguity error, got: %v", err)
	}
}

func TestResolveSidecar_NoMatch(t *testing.T) {
	withTmpStateDir(t, []sidecar{
		{Schema: 1, SessionID: "abc", PID: os.Getpid(), Status: "running",
			StartedAt: time.Now().UTC().Format(time.RFC3339)},
	})
	_, err := resolveSidecar("xyz", "")
	if err == nil {
		t.Error("expected no-match error")
	}
}

func TestResolveSidecar_CwdFlag(t *testing.T) {
	cwd, _ := os.Getwd()
	abs, _ := filepath.Abs(cwd)
	withTmpStateDir(t, []sidecar{
		{Schema: 1, SessionID: "match", Cwd: abs,
			PID: os.Getpid(), Status: "running",
			StartedAt: time.Now().UTC().Format(time.RFC3339)},
		{Schema: 1, SessionID: "elsewhere", Cwd: "/other",
			PID: os.Getpid(), Status: "running",
			StartedAt: time.Now().UTC().Format(time.RFC3339)},
	})
	s, err := resolveSidecar("", ".")
	if err != nil {
		t.Fatalf("resolveSidecar --cwd .: %v", err)
	}
	if s.SessionID != "match" {
		t.Errorf("got %s", s.SessionID)
	}
}

func TestResolveSidecar_NoSidecarsAtAll(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, err := resolveSidecar("foo", "")
	if err == nil || !strings.Contains(err.Error(), "no fir sessions") {
		t.Errorf("expected 'no fir sessions' error, got: %v", err)
	}
}

func TestAgeString(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		startedAt string
		want      string
	}{
		{now.Add(-30 * time.Second).Format(time.RFC3339), "30s"},
		{now.Add(-5 * time.Minute).Format(time.RFC3339), "5m00s"},
		{now.Add(-2*time.Hour - 30*time.Minute).Format(time.RFC3339), "2h30m"},
		{now.Add(-49 * time.Hour).Format(time.RFC3339), "2d"},
		{"not-a-date", "?"},
	}
	for _, c := range cases {
		got := ageString(c.startedAt, now)
		if got != c.want {
			t.Errorf("ageString(%q) = %q, want %q", c.startedAt, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Formatter
// ---------------------------------------------------------------------------

func TestFormatter_RawJSON(t *testing.T) {
	var buf bytes.Buffer
	f := &transcriptFormatter{w: &buf, rawJSON: true}
	f.write([]byte(`{"type":"message","timestamp":"2026-04-27T12:00:00Z"}`))
	if buf.String() != `{"type":"message","timestamp":"2026-04-27T12:00:00Z"}`+"\n" {
		t.Errorf("rawJSON should pass through verbatim, got: %q", buf.String())
	}
}

func TestFormatter_Header(t *testing.T) {
	var buf bytes.Buffer
	f := &transcriptFormatter{w: &buf, rawJSON: false, color: false}
	f.write([]byte(`{"type":"session","version":3,"id":"abcdef0123","cwd":"/path"}`))
	out := buf.String()
	if !strings.Contains(out, "session abcdef01") {
		t.Errorf("expected session-banner with truncated id, got: %q", out)
	}
	if !strings.Contains(out, "v3") {
		t.Errorf("expected version, got: %q", out)
	}
}

func TestFormatter_UserMessage(t *testing.T) {
	var buf bytes.Buffer
	f := &transcriptFormatter{w: &buf, rawJSON: false, color: false}
	f.write([]byte(`{"type":"message","timestamp":"2026-04-27T12:00:00Z","message":{"role":"user","content":"hi there"}}`))
	out := buf.String()
	if !strings.Contains(out, "user") || !strings.Contains(out, "hi there") {
		t.Errorf("expected role+text, got: %q", out)
	}
}

func TestFormatter_AssistantWithToolUse(t *testing.T) {
	var buf bytes.Buffer
	f := &transcriptFormatter{w: &buf, rawJSON: false, color: false}
	line := `{"type":"message","timestamp":"2026-04-27T12:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"thinking..."},{"type":"tool_use","name":"bash"}]}}`
	f.write([]byte(line))
	out := buf.String()
	if !strings.Contains(out, "assistant") {
		t.Errorf("expected role, got: %q", out)
	}
	if !strings.Contains(out, "thinking") || !strings.Contains(out, "→ bash") {
		t.Errorf("expected text + tool_use marker, got: %q", out)
	}
}

func TestFormatter_ModelChange(t *testing.T) {
	var buf bytes.Buffer
	f := &transcriptFormatter{w: &buf, rawJSON: false, color: false}
	f.write([]byte(`{"type":"model_change","timestamp":"2026-04-27T12:00:00Z","provider":"anthropic","modelId":"claude-opus-4"}`))
	out := buf.String()
	if !strings.Contains(out, "model →") || !strings.Contains(out, "anthropic") {
		t.Errorf("expected model change line, got: %q", out)
	}
}

func TestFormatter_Compaction(t *testing.T) {
	var buf bytes.Buffer
	f := &transcriptFormatter{w: &buf, rawJSON: false, color: false}
	f.write([]byte(`{"type":"compaction","timestamp":"2026-04-27T12:00:00Z","summary":"compressed 50 turns"}`))
	out := buf.String()
	if !strings.Contains(out, "compaction") || !strings.Contains(out, "50 turns") {
		t.Errorf("expected compaction summary, got: %q", out)
	}
}

func TestFormatter_PlanUpdate(t *testing.T) {
	var buf bytes.Buffer
	f := &transcriptFormatter{w: &buf, rawJSON: false, color: false}
	f.write([]byte(`{"type":"plan_update","timestamp":"2026-04-27T12:00:00Z","planTitle":"Implement caching"}`))
	if !strings.Contains(buf.String(), "Implement caching") {
		t.Errorf("expected plan title, got: %q", buf.String())
	}
}

func TestFormatter_Command(t *testing.T) {
	var buf bytes.Buffer
	f := &transcriptFormatter{w: &buf, rawJSON: false, color: false}
	f.write([]byte(`{"type":"command","timestamp":"2026-04-27T12:00:00Z","command":"bash","args":"go test ./..."}`))
	out := buf.String()
	if !strings.Contains(out, "bash") || !strings.Contains(out, "go test") {
		t.Errorf("expected command line, got: %q", out)
	}
}

func TestFormatter_HiddenTypes(t *testing.T) {
	// label/branch_summary/custom should produce no output.
	var buf bytes.Buffer
	f := &transcriptFormatter{w: &buf, rawJSON: false, color: false}
	for _, ty := range []string{"label", "branch_summary", "custom", "custom_message"} {
		buf.Reset()
		f.write([]byte(`{"type":"` + ty + `","timestamp":"2026-04-27T12:00:00Z"}`))
		if buf.Len() != 0 {
			t.Errorf("type %q should produce no output, got: %q", ty, buf.String())
		}
	}
}

func TestFormatter_UnknownTypeStillRenders(t *testing.T) {
	var buf bytes.Buffer
	f := &transcriptFormatter{w: &buf, rawJSON: false, color: false}
	f.write([]byte(`{"type":"future_type","timestamp":"2026-04-27T12:00:00Z"}`))
	if !strings.Contains(buf.String(), "future_type") {
		t.Errorf("unknown types should still render the type name, got: %q", buf.String())
	}
}

func TestSummariseContent_Strings(t *testing.T) {
	got := summariseContent("hello\nworld\nlong text", false)
	if got != "hello world long text" {
		t.Errorf("multi-line should collapse, got: %q", got)
	}
}

func TestSummariseContent_Truncates(t *testing.T) {
	got := summariseContent(strings.Repeat("x", 500), false)
	runes := []rune(got)
	if len(runes) > 200 {
		t.Errorf("expected ≤200 runes, got %d", len(runes))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got: %q", got[len(got)-3:])
	}
}

func TestSummariseContent_FullNoTruncation(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := summariseContent(long, true)
	if got != long {
		t.Errorf("full=true should not truncate, got len=%d want %d", len(got), len(long))
	}
}

// Ensure the dispatch in app.go exists. Failing this test means either a
// rename or a regression.
func TestRunObserve_NoArgs_DoesNotPanicAndPrintsHeader(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Capture stdout would be nice but runObserveList prints directly. Just
	// assert no error / panic for now; coverage is otherwise via the helpers.
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{"fir", "observe"}
	if err := runObserve(); err != nil {
		t.Errorf("runObserve no-args returned error: %v", err)
	}
}

func TestRunObserve_UnknownFlag(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{"fir", "observe", "--bogus"}
	err := runObserve()
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("expected unknown-flag error, got: %v", err)
	}
}

// Demonstrate via the fmt package that the Errorf usage compiles right.
// (Sanity check during development that doesn't actually depend on the fmt
// import — keep here so unused-import linting doesn't bite if we delete a
// helper.)
var _ = fmt.Errorf
var _ = errors.New

// nopWriteCloser adapts a bytes.Buffer to io.WriteCloser for testing.
type nopWriteCloser struct{ *bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }

// TestInteractSendLoop_FirstEnterSendsImmediately verifies that --interact
// sends each non-empty line on the first Enter (not the second). Driving via
// `tmux send-keys "msg" Enter` depends on this behaviour.
func TestInteractSendLoop_FirstEnterSendsImmediately(t *testing.T) {
	var buf bytes.Buffer
	input := strings.NewReader("hello agent\n!steer me\n+also this\n\n   \nfinal\n")
	interactSendLoop(input, nopWriteCloser{&buf})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 NDJSON messages (blank/whitespace skipped), got %d: %q", len(lines), buf.String())
	}
	var msgs []map[string]string
	for _, ln := range lines {
		var m map[string]string
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("parse %q: %v", ln, err)
		}
		msgs = append(msgs, m)
	}
	want := []struct {
		content, deliverAs string
	}{
		{"hello agent", ""},
		{"steer me", "steer"},
		{"also this", "followUp"},
		{"final", ""},
	}
	for i, w := range want {
		if msgs[i]["content"] != w.content || msgs[i]["deliver_as"] != w.deliverAs {
			t.Errorf("msg %d: got %+v, want content=%q deliver_as=%q", i, msgs[i], w.content, w.deliverAs)
		}
	}
}
