package acp

import (
	"context"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
)

// newRehydrateAgent builds a firAgent wired for re-hydration tests: an isolated
// agent dir + cwd (via env), no extensions/MCP/skills so createSession is cheap
// and deterministic, and a fixed clock.
func newRehydrateAgent(t *testing.T) (*firAgent, string) {
	t.Helper()
	agentDir := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", agentDir)
	t.Setenv("PWD", cwd)

	base := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	pa := &firAgent{
		conn:     newMockConn(),
		sessions: make(map[string]*firSession),
		reaped:   make(map[string]reapedSession),
		options:  Options{NoExtensions: true, NoMCP: true, NoSkills: true},
		idleTTL:  time.Hour,
		nowFn:    func() time.Time { return base },
	}
	return pa, cwd
}

func reapNow(t *testing.T, pa *firAgent, sid string, entry *firSession) {
	t.Helper()
	// Make the session look idle, then run a reaper pass.
	entry.touch(pa.now().Add(-2 * time.Hour))
	reaped := pa.reapIdle(pa.now())
	if len(reaped) != 1 || reaped[0] != sid {
		t.Fatalf("reapIdle = %v, want [%s]", reaped, sid)
	}
	pa.mu.Lock()
	_, present := pa.sessions[sid]
	_, recorded := pa.reaped[sid]
	pa.mu.Unlock()
	if present {
		t.Fatal("reaped session still present in sessions map")
	}
	if !recorded {
		t.Fatal("reaped session not recorded for re-hydration")
	}
}

func promptIsNotFound(err error) bool {
	re, ok := err.(*acpsdk.RequestError)
	return ok && re.Code == SessionNotFoundError
}

// TestPrompt_RehydratesReapedSession_RestoresConversation proves the core win:
// after the idle reaper tears a session down, a Prompt for the SAME sessionID
// transparently re-hydrates it in place (entry back in the map, same ID, prior
// conversation restored) instead of returning session-not-found.
func TestPrompt_RehydratesReapedSession_RestoresConversation(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	ctx := context.Background()
	const sid = "acp-sid-restore"

	entry, err := pa.createSession(ctx, sid, cwd, nil)
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}

	// Give the session a real on-disk transcript carrying one user turn.
	entry.session.SessionStore.NewSession(nil)
	entry.session.SessionStore.AppendAgentMessage(
		agent.NewAgentMessage(ai.NewUserMsg("remembered turn", time.Now().UnixMilli())))
	sessionFile := entry.session.SessionStore.GetSessionFile()
	if sessionFile == "" {
		t.Fatal("expected an on-disk session file after NewSession")
	}

	reapNow(t, pa, sid, entry)

	// The recorded transcript path must point at the file we created.
	pa.mu.Lock()
	rec := pa.reaped[sid]
	pa.mu.Unlock()
	if rec.file != sessionFile {
		t.Fatalf("recorded reaped file = %q, want %q", rec.file, sessionFile)
	}

	// Prompt the SAME id. No model is configured, so the prompt itself fails
	// with a no-model/auth error — but crucially NOT session-not-found, and
	// the session must be re-hydrated back into the map under the same ID.
	_, perr := pa.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: acpsdk.SessionId(sid),
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("hello again")},
	})
	if promptIsNotFound(perr) {
		t.Fatalf("Prompt returned session-not-found after reap; expected re-hydration")
	}

	pa.mu.Lock()
	rehydrated, back := pa.sessions[sid]
	_, stillReaped := pa.reaped[sid]
	pa.mu.Unlock()
	if !back {
		t.Fatal("session not re-hydrated into map under same ID")
	}
	if stillReaped {
		t.Error("reaped record not cleared after re-hydration")
	}

	// The restored session must carry the prior conversation.
	msgs := rehydrated.session.SessionStore.BuildSessionContext().Messages
	if len(msgs) == 0 {
		t.Error("re-hydrated session has no restored conversation history")
	}
}

// TestPrompt_RehydratesReapedSession_NoTranscript_FreshSameID covers a reaped
// session whose recorded transcript is absent (empty path, or the file vanished
// from disk): the next Prompt must still re-create it in place under the same
// ID, not error.
func TestPrompt_RehydratesReapedSession_NoTranscript_FreshSameID(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	ctx := context.Background()
	const sid = "acp-sid-fresh"

	// Seed a reaped record with no on-disk transcript, as if the session had
	// been reaped before it ever persisted anything.
	pa.mu.Lock()
	pa.reaped[sid] = reapedSession{file: "", cwd: cwd}
	pa.mu.Unlock()

	_, perr := pa.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: acpsdk.SessionId(sid),
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("first real prompt")},
	})
	if promptIsNotFound(perr) {
		t.Fatalf("Prompt returned session-not-found; expected fresh same-ID re-hydration")
	}

	pa.mu.Lock()
	_, back := pa.sessions[sid]
	_, stillReaped := pa.reaped[sid]
	pa.mu.Unlock()
	if !back {
		t.Fatal("session not re-created into map under same ID")
	}
	if stillReaped {
		t.Error("reaped record not cleared after fresh re-hydration")
	}
}

// TestPrompt_UnknownSession_StillTypedError verifies that a sessionID that was
// never known (not reaped, no on-disk file) still returns the typed
// session-not-found (-32001) — re-hydration must not mask genuinely unknown IDs.
func TestPrompt_UnknownSession_StillTypedError(t *testing.T) {
	pa, _ := newRehydrateAgent(t)
	_, err := pa.Prompt(context.Background(), acpsdk.PromptRequest{
		SessionId: "00000000-dead-beef-0000-unknownsession",
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("hi")},
	})
	if !promptIsNotFound(err) {
		t.Fatalf("expected session-not-found (-32001) for unknown id, got %v", err)
	}
}

// TestCreateSession_ClearsStaleReapedRecord verifies that bringing a sessionID
// back to life (as session/resume does) drops any stale reaped record for that
// ID, so it can never shadow the live session on a later Prompt.
func TestCreateSession_ClearsStaleReapedRecord(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	ctx := context.Background()
	const sid = "acp-sid-stale"

	entry, err := pa.createSession(ctx, sid, cwd, nil)
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	reapNow(t, pa, sid, entry) // pa.reaped[sid] is now set

	// Re-create the same id (as session/resume would). The stale reaped record
	// must be dropped so it cannot shadow the live session later.
	if _, err := pa.createSession(ctx, sid, cwd, nil); err != nil {
		t.Fatalf("re-createSession: %v", err)
	}

	pa.mu.Lock()
	_, stale := pa.reaped[sid]
	_, live := pa.sessions[sid]
	pa.mu.Unlock()
	if stale {
		t.Error("stale reaped record was not cleared on re-create")
	}
	if !live {
		t.Error("re-created session not registered in the map")
	}
}

// TestRelease_ReapedSession_ForgetsAndBlocksRehydration verifies that an
// explicit session/release on an already-reaped session is authoritative: it
// clears the reaped record (returning success), so a later Prompt for that ID
// returns session-not-found rather than re-hydrating.
func TestRelease_ReapedSession_ForgetsAndBlocksRehydration(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	ctx := context.Background()
	const sid = "acp-sid-release-reaped"

	entry, err := pa.createSession(ctx, sid, cwd, nil)
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	reapNow(t, pa, sid, entry) // pa.reaped[sid] is now set

	// Release the reaped session — must succeed and forget the record.
	if _, err := pa.ReleaseSession(ctx, ReleaseSessionRequest{SessionId: sid}); err != nil {
		t.Fatalf("ReleaseSession on reaped id: %v", err)
	}
	pa.mu.Lock()
	_, stillReaped := pa.reaped[sid]
	pa.mu.Unlock()
	if stillReaped {
		t.Error("reaped record not cleared by explicit release")
	}

	// A Prompt now must report session-not-found (it was authoritatively released).
	_, perr := pa.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: acpsdk.SessionId(sid),
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("anyone home?")},
	})
	if !promptIsNotFound(perr) {
		t.Fatalf("expected session-not-found after release of reaped session, got %v", perr)
	}
}
