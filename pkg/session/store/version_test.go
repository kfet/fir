package store

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

// readHeader reads and decodes the first JSONL line of a session file.
func readHeader(t *testing.T, path string) SessionHeader {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	line := strings.SplitN(string(data), "\n", 2)[0]
	var h SessionHeader
	if err := json.Unmarshal([]byte(line), &h); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	return h
}

func appendUser(ss *SessionStore, text string) {
	ss.AppendAIMessage(ai.NewUserMsg(text, time.Now().UnixMilli()))
}

func TestNewSessionHeaderCarriesFirVersion(t *testing.T) {
	orig := currentFirVersion()
	t.Cleanup(func() { SetFirVersion(orig) })

	SetFirVersion("9.9.9-test")

	dir := t.TempDir()
	ss := NewSessionStore(dir, dir)
	path := ss.GetSessionFile()
	if path == "" {
		t.Fatal("expected a persisted session file")
	}

	h := readHeader(t, path)
	if h.FirVersion != "9.9.9-test" {
		t.Errorf("header FirVersion = %q, want %q", h.FirVersion, "9.9.9-test")
	}
	// The schema version must remain the transcript schema version, not the
	// binary version — the two are distinct.
	if h.Version != CurrentSessionVersion {
		t.Errorf("header Version = %d, want schema version %d", h.Version, CurrentSessionVersion)
	}
}

func TestSetFirVersionIgnoresEmpty(t *testing.T) {
	orig := currentFirVersion()
	t.Cleanup(func() { SetFirVersion(orig) })

	SetFirVersion("1.2.3")
	SetFirVersion("") // must not clobber
	if got := currentFirVersion(); got != "1.2.3" {
		t.Errorf("currentFirVersion() = %q, want %q after empty set", got, "1.2.3")
	}
}

func TestMaybeRecordAgentVersionChange(t *testing.T) {
	orig := currentFirVersion()
	t.Cleanup(func() { SetFirVersion(orig) })

	dir := t.TempDir()

	// Create a session under an "old" version.
	SetFirVersion("1.0.0")
	ss := NewSessionStore(dir, dir)
	appendUser(ss, "hi")
	path := ss.GetSessionFile()
	ss.Close()

	// Reopen under the same version — no delta should be appended.
	reopened, _ := OpenSessionStore(path)
	if !reopened.WasResumed() {
		t.Fatal("expected reopened store to be resumed")
	}
	if id := reopened.MaybeRecordAgentVersionChange(); id != "" {
		t.Errorf("same-version resume appended agent_version entry %q, want none", id)
	}
	reopened.Close()

	// Reopen under a NEW version — exactly one delta should be appended.
	SetFirVersion("2.0.0")
	reopened2, _ := OpenSessionStore(path)
	id := reopened2.MaybeRecordAgentVersionChange()
	if id == "" {
		t.Fatal("version-changed resume appended no agent_version entry")
	}
	var found *SessionEntry
	for _, e := range reopened2.GetEntries() {
		if e.Type == "agent_version" {
			found = e
		}
	}
	if found == nil {
		t.Fatal("no agent_version entry found after version change")
	}
	if found.FirVersion != "2.0.0" {
		t.Errorf("agent_version entry FirVersion = %q, want %q", found.FirVersion, "2.0.0")
	}
	reopened2.Close()

	// A second resume at the same new version must not append another delta.
	reopened3, _ := OpenSessionStore(path)
	if id := reopened3.MaybeRecordAgentVersionChange(); id != "" {
		t.Errorf("repeat-version resume appended agent_version entry %q, want none", id)
	}
	reopened3.Close()
}

func TestMaybeRecordAgentVersionChangeFreshSessionNoOp(t *testing.T) {
	orig := currentFirVersion()
	t.Cleanup(func() { SetFirVersion(orig) })

	SetFirVersion("3.0.0")
	dir := t.TempDir()
	ss := NewSessionStore(dir, dir)
	if id := ss.MaybeRecordAgentVersionChange(); id != "" {
		t.Errorf("fresh session appended agent_version entry %q, want none", id)
	}
}

func TestAgentVersionEntryNotSentToLLM(t *testing.T) {
	orig := currentFirVersion()
	t.Cleanup(func() { SetFirVersion(orig) })

	SetFirVersion("1.0.0")
	dir := t.TempDir()
	ss := NewSessionStore(dir, dir)
	appendUser(ss, "hi")
	path := ss.GetSessionFile()
	ss.Close()

	SetFirVersion("2.0.0")
	reopened, _ := OpenSessionStore(path)
	reopened.MaybeRecordAgentVersionChange()

	ctx := reopened.BuildSessionContext()
	for _, m := range ctx.Messages {
		raw, _ := json.Marshal(m)
		if strings.Contains(string(raw), "agent_version") {
			t.Errorf("agent_version leaked into LLM context: %s", raw)
		}
	}
	// Sanity: the underlying file must hold an agent_version entry.
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"agent_version"`) {
		t.Error("expected agent_version entry in session file")
	}
}

func TestForkPreservesFirVersionField(t *testing.T) {
	orig := currentFirVersion()
	t.Cleanup(func() { SetFirVersion(orig) })

	SetFirVersion("5.5.5")
	dir := t.TempDir()
	ss := NewSessionStore(dir, dir)
	appendUser(ss, "hi")
	src := ss.GetSessionFile()

	forked, err := ForkFrom(src, dir, dir)
	if err != nil {
		t.Fatalf("ForkFrom: %v", err)
	}
	h := readHeader(t, forked.GetSessionFile())
	if h.FirVersion != "5.5.5" {
		t.Errorf("forked header FirVersion = %q, want %q", h.FirVersion, "5.5.5")
	}
	if h.ParentSession == "" {
		t.Error("forked header missing ParentSession")
	}
}
