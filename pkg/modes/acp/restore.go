package acp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// ACP keeps only protocol glue here. Session settings are saved and restored
// by core (session.SessionState, next to each transcript); the ACP sessionId
// is the session's handle, which core keeps pointed at its transcript.

// clientSetup carries the values an ACP request supplied. Non-empty values
// override the saved state; nil/empty ones keep it.
type clientSetup struct {
	cwd        string
	mcpServers []acpsdk.McpServer
	meta       map[string]any
	transcript string
}

// SessionNeedsReloadError is returned on a prompt when a restored session's
// client-supplied MCP server failed to start. The client should call
// session/load with fresh mcpServers.
const SessionNeedsReloadError = -32010

func newSessionNeedsReload(sessionID string, servers []string) *acpsdk.RequestError {
	return &acpsdk.RequestError{
		Code:    SessionNeedsReloadError,
		Message: "Session needs reload: restored MCP servers failed to start",
		Data: map[string]any{
			"sessionId": sessionID,
			"reason":    "session_needs_reload",
			"servers":   servers,
		},
	}
}

// restoredMCPWait bounds how long a restore waits for client MCP servers to
// finish their handshake before judging them failed.
var restoredMCPWait = 30 * time.Second

// handleMaxAge is how long an untouched sessionId binding is kept. Every
// turn refreshes it, so only abandoned sessions age out.
const handleMaxAge = 30 * 24 * time.Hour

// resolveTranscript returns the transcript bound to sessionID, migrating a
// legacy acp-sessions config on first sight.
func (pa *firAgent) resolveTranscript(sessionID string) string {
	if pa.agentDir == "" {
		return ""
	}
	if t := store.ResolveHandle(pa.agentDir, sessionID); t != "" {
		return t
	}
	return pa.migrateLegacyConfig(sessionID)
}

// legacyConfigDir is where fir v1.24.0 kept per-sessionId ACP configs.
func (pa *firAgent) legacyConfigDir() string {
	return filepath.Join(pa.agentDir, "acp-sessions")
}

// legacyConfig is the v1.24.0 acp-sessions file format.
type legacyConfig struct {
	Setup struct {
		Cwd        string                      `json:"cwd"`
		McpServers map[string]mcp.ServerConfig `json:"mcpServers"`
		Meta       map[string]any              `json:"meta"`
		Mode       string                      `json:"mode"`
	} `json:"setup"`
	Transcript string `json:"transcript"`
	Model      string `json:"model"`
	Thinking   string `json:"thinking"`
}

// migrateLegacyConfig converts a v1.24.0 acp-sessions config for sessionID
// into core session state next to its transcript plus a handle binding, then
// deletes it. Returns the transcript, or "" when there is nothing to migrate.
func (pa *firAgent) migrateLegacyConfig(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	path := filepath.Join(pa.legacyConfigDir(), hex.EncodeToString(sum[:16])+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var old legacyConfig
	if err := json.Unmarshal(data, &old); err != nil || old.Transcript == "" || !fileExists(old.Transcript) {
		_ = os.Remove(path) // nothing to restore from
		return ""
	}
	if _, ok := session.LoadState(old.Transcript); !ok {
		st := session.SessionState{
			Runtime: session.RuntimeState{
				Cwd:        old.Setup.Cwd,
				McpServers: old.Setup.McpServers,
				Meta:       old.Setup.Meta,
				Mode:       old.Setup.Mode,
				Handle:     sessionID,
			},
			Conversation: session.ConversationState{Model: old.Model, Thinking: old.Thinking},
		}
		if err := store.WriteState(old.Transcript, st); err != nil {
			firlog.Warn("acp: legacy session config migration failed", "sessionId", sessionID, "err", err)
			return ""
		}
	}
	if err := store.BindHandle(pa.agentDir, sessionID, old.Transcript); err != nil {
		firlog.Warn("acp: legacy session config migration failed", "sessionId", sessionID, "err", err)
		return ""
	}
	_ = os.Remove(path)
	firlog.Info("acp: migrated legacy session config", "sessionId", sessionID,
		"mcpServers", session.ServerNames(old.Setup.McpServers))
	return old.Transcript
}

// pruneHandles drops sessionId bindings (and leftover legacy configs) not
// touched since cutoff.
func (pa *firAgent) pruneHandles(cutoff time.Time) {
	if pa.agentDir == "" {
		return
	}
	store.PruneHandles(pa.agentDir, cutoff)
	store.PruneDir(pa.legacyConfigDir(), cutoff)
}

// forgetSession drops sessionID's binding (and any legacy config) so it can
// no longer be rehydrated. Reports whether anything was bound.
func (pa *firAgent) forgetSession(sessionID string) bool {
	if pa.agentDir == "" {
		return false
	}
	bound := store.ForgetHandle(pa.agentDir, sessionID)
	sum := sha256.Sum256([]byte(sessionID))
	if os.Remove(filepath.Join(pa.legacyConfigDir(), hex.EncodeToString(sum[:16])+".json")) == nil {
		bound = true
	}
	return bound
}

// canRehydrate reports whether sessionID has saved state to come back to.
func (pa *firAgent) canRehydrate(sessionID string) bool {
	return pa.resolveTranscript(sessionID) != ""
}

// openSession is the single path that builds an in-memory session for
// session/new, session/load, session/resume and prompt-driven rehydration.
// It finds sessionID's transcript, lets req (nil for a rehydrate) override
// the saved client setup, and hands the rest to core, which restores the
// transcript's saved state.
func (pa *firAgent) openSession(ctx context.Context, sessionID string, req *clientSetup) (*firSession, bool, error) {
	transcript := ""
	if req != nil && req.transcript != "" {
		transcript = req.transcript
	} else {
		transcript = pa.resolveTranscript(sessionID)
	}
	saved, _ := session.LoadState(transcript)

	// The cwd decides tools and the session dir, so it is settled first.
	cwd := saved.Runtime.Cwd
	if req != nil && req.cwd != "" {
		cwd = req.cwd
	}
	if cwd == "" {
		cwd = defaultPromptCwd()
	}
	override := func(st *session.SessionState) {
		st.Runtime.Handle = sessionID
		st.Runtime.Cwd = cwd
		if req == nil {
			return
		}
		if req.mcpServers != nil {
			st.Runtime.McpServers = mergeRequestMCPServers(nil, req.mcpServers)
		}
		if req.meta != nil {
			st.Runtime.Meta = req.meta
		}
	}

	// A client retry would otherwise leak the old session's goroutines.
	if existing, ok := pa.removeSession(sessionID); ok {
		pa.teardownSession(ctx, sessionID, existing)
	}

	entry, forked, err := pa.createSession(ctx, sessionID, cwd, transcript, override)
	if err != nil {
		return nil, false, fmt.Errorf("create session: %w", err)
	}

	client := entry.session.SessionMCPServers()
	if req == nil && len(client) > 0 {
		if failed := restoredMCPFailures(ctx, entry, client); len(failed) > 0 {
			firlog.Warn("acp restore: client MCP servers failed to start", "sessionId", sessionID, "servers", failed)
			pa.discardSession(ctx, sessionID, entry)
			return nil, false, newSessionNeedsReload(sessionID, failed)
		}
	}

	firlog.Info("acp session opened", "sessionId", sessionID, "restore", req == nil,
		"mcpServers", session.ServerNames(client), "transcript", transcript != "")
	return entry, forked, nil
}

// discardSession drops a half-built session; its saved state stays on disk.
func (pa *firAgent) discardSession(ctx context.Context, sessionID string, entry *firSession) {
	pa.mu.Lock()
	if pa.sessions[sessionID] == entry {
		delete(pa.sessions, sessionID)
	}
	pa.mu.Unlock()
	pa.teardownSession(ctx, sessionID, entry)
}

// restoredMCPFailures waits for MCP startup and returns the client-supplied
// servers that are not connected.
func restoredMCPFailures(ctx context.Context, entry *firSession, client map[string]mcp.ServerConfig) []string {
	if entry.mcpManager == nil {
		return session.ServerNames(client)
	}
	waitCtx, cancel := context.WithTimeout(ctx, restoredMCPWait)
	_ = entry.mcpManager.WaitReady(waitCtx)
	cancel()
	connected := map[string]bool{}
	for _, st := range entry.mcpManager.Status() {
		// A server waiting on OAuth is not fixed by a reload.
		var authErr *mcp.AuthRequiredError
		connected[st.Name] = st.Connected || errors.As(st.Error, &authErr)
	}
	var failed []string
	for _, name := range session.ServerNames(client) {
		if !connected[name] {
			failed = append(failed, name)
		}
	}
	return failed
}
