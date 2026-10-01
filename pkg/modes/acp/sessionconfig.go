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
	"sort"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/mcp"
)

// acpSetup is everything the ACP client supplied for a session, or set on it
// over ACP. It is saved and restored as a whole, so a field added here
// survives an idle reap or a process restart with no other code change.
type acpSetup struct {
	Cwd        string                      `json:"cwd,omitempty"`
	McpServers map[string]mcp.ServerConfig `json:"mcpServers,omitempty"`
	Meta       map[string]any              `json:"meta,omitempty"`
	Mode       string                      `json:"mode,omitempty"`
}

// sessionConfig is the persisted per-session blob: the client setup plus the
// live state fir owns, captured from the running session on every save.
type sessionConfig struct {
	Setup      acpSetup `json:"setup"`
	Transcript string   `json:"transcript,omitempty"`
	Model      string   `json:"model,omitempty"` // provider/id
	Thinking   string   `json:"thinking,omitempty"`
}

// clientSetup carries the values an ACP request supplied. Non-empty values
// override the saved config; nil/empty ones keep it.
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

func (pa *firAgent) sessionConfigPath(sessionID string) string {
	if pa.configDir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sessionID))
	return filepath.Join(pa.configDir, hex.EncodeToString(sum[:16])+".json")
}

func (pa *firAgent) loadSessionConfig(sessionID string) (sessionConfig, bool) {
	var cfg sessionConfig
	path := pa.sessionConfigPath(sessionID)
	if path == "" {
		return cfg, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			firlog.Warn("acp session config: read failed", "sessionId", sessionID, "err", err)
		}
		return cfg, false
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		firlog.Warn("acp session config: corrupt, ignoring", "sessionId", sessionID, "err", err)
		return sessionConfig{}, false
	}
	return cfg, true
}

func (pa *firAgent) deleteSessionConfig(sessionID string) bool {
	path := pa.sessionConfigPath(sessionID)
	return path != "" && os.Remove(path) == nil
}

// saveSessionConfig saves entry's config if it is still the live session
// for sessionID, so a save racing session/release cannot resurrect it.
func (pa *firAgent) saveSessionConfig(sessionID string, entry *firSession) {
	if pa.lookupSession(sessionID) != entry {
		return
	}
	pa.writeSessionConfig(sessionID, entry)
}

// writeSessionConfig snapshots entry and writes it atomically, mode 0600.
// Only MCP server names are logged: configs carry secrets in env/headers.
func (pa *firAgent) writeSessionConfig(sessionID string, entry *firSession) {
	path := pa.sessionConfigPath(sessionID)
	if path == "" || entry == nil {
		return
	}
	cfg := entry.snapshotConfig()
	data, err := json.Marshal(cfg)
	if err == nil {
		err = writeFileAtomic(path, data)
	}
	if err != nil {
		firlog.Warn("acp session config: save failed", "sessionId", sessionID, "err", err)
		return
	}
	firlog.Debug("acp session config: saved", "sessionId", sessionID,
		"mcpServers", serverNames(cfg.Setup.McpServers), "model", cfg.Model)
}

// sessionConfigMaxAge is how long an untouched saved config is kept. Every
// prompt rewrites it, so only abandoned sessions age out.
const sessionConfigMaxAge = 30 * 24 * time.Hour

// pruneSessionConfigs removes saved configs not written since before cutoff.
func (pa *firAgent) pruneSessionConfigs(cutoff time.Time) {
	if pa.configDir == "" {
		return
	}
	entries, err := os.ReadDir(pa.configDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(pa.configDir, e.Name()))
		}
	}
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func serverNames(m map[string]mcp.ServerConfig) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// snapshotConfig captures the persisted state of entry.
func (s *firSession) snapshotConfig() sessionConfig {
	cfg := sessionConfig{Setup: s.getSetup()}
	if s.session == nil {
		return cfg
	}
	if s.session.SessionStore != nil {
		cfg.Transcript = s.session.SessionStore.GetSessionFile()
	}
	if m := s.session.Model(); m != nil {
		cfg.Model = m.Provider + "/" + m.ID
	}
	cfg.Thinking = s.session.ThinkingLevel()
	return cfg
}

func (s *firSession) getSetup() acpSetup {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	return s.setup
}

func (s *firSession) updateSetup(fn func(*acpSetup)) {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	fn(&s.setup)
}

// clientMCP returns the client-supplied MCP configs, re-merged on /mcp reload.
func (s *firSession) clientMCP() map[string]mcp.ServerConfig {
	return s.getSetup().McpServers
}

// openSession is the single path that builds an in-memory session for
// session/new, session/load, session/resume and prompt-driven rehydration.
// It starts from the saved config for sessionID (if any), overlays req
// (nil for a rehydrate), creates the session, restores the transcript, model
// and thinking level, and saves the result.
func (pa *firAgent) openSession(ctx context.Context, sessionID string, req *clientSetup) (*firSession, bool, error) {
	cfg, _ := pa.loadSessionConfig(sessionID)
	if req != nil {
		if req.cwd != "" {
			cfg.Setup.Cwd = req.cwd
		}
		if req.mcpServers != nil {
			cfg.Setup.McpServers = mergeRequestMCPServers(nil, req.mcpServers)
		}
		if req.meta != nil {
			cfg.Setup.Meta = req.meta
		}
		if req.transcript != "" {
			cfg.Transcript = req.transcript
		}
	}
	if cfg.Setup.Cwd == "" {
		cfg.Setup.Cwd = defaultPromptCwd()
	}

	// A client retry would otherwise leak the old session's goroutines.
	if existing, ok := pa.removeSession(sessionID); ok {
		pa.teardownSession(ctx, sessionID, existing)
	}

	cwd := cfg.Setup.Cwd
	entry, err := pa.createSession(ctx, sessionID, cwd,
		withClientMCPConfigs(pa.sessionMCPConfigs(cwd), cfg.Setup.McpServers))
	if err != nil {
		return nil, false, fmt.Errorf("create session: %w", err)
	}
	entry.updateSetup(func(s *acpSetup) { *s = cfg.Setup })

	forked := false
	if cfg.Transcript != "" && fileExists(cfg.Transcript) {
		// SwitchSession must not race EmitSessionStart on SessionStore.
		if entry.extReady != nil {
			<-entry.extReady
		}
		if forked, err = entry.session.SwitchSession(cfg.Transcript); err != nil {
			pa.discardSession(ctx, sessionID, entry)
			return nil, false, fmt.Errorf("switch session: %w", err)
		}
	}
	restoreModelAndThinking(entry, cfg)

	if req == nil && len(cfg.Setup.McpServers) > 0 {
		if failed := restoredMCPFailures(ctx, entry, cfg.Setup.McpServers); len(failed) > 0 {
			firlog.Warn("acp restore: client MCP servers failed to start", "sessionId", sessionID, "servers", failed)
			pa.discardSession(ctx, sessionID, entry)
			return nil, false, newSessionNeedsReload(sessionID, failed)
		}
	}

	pa.saveSessionConfig(sessionID, entry)
	firlog.Info("acp session opened", "sessionId", sessionID, "restore", req == nil,
		"mcpServers", serverNames(cfg.Setup.McpServers), "transcript", cfg.Transcript != "")
	return entry, forked, nil
}

// discardSession drops a half-built session without touching its saved config.
func (pa *firAgent) discardSession(ctx context.Context, sessionID string, entry *firSession) {
	pa.mu.Lock()
	if pa.sessions[sessionID] == entry {
		delete(pa.sessions, sessionID)
	}
	pa.mu.Unlock()
	pa.teardownSession(ctx, sessionID, entry)
}

func restoreModelAndThinking(entry *firSession, cfg sessionConfig) {
	if cfg.Model != "" && entry.modelRegistry != nil {
		cur := entry.session.Model()
		if cur == nil || cur.Provider+"/"+cur.ID != cfg.Model {
			if provider, id, err := ParseModelID(cfg.Model); err == nil {
				if m := entry.modelRegistry.Find(provider, id); m != nil {
					if err := entry.session.SetModel(m); err != nil {
						firlog.Warn("acp restore: model not restored", "model", cfg.Model, "err", err)
					}
				}
			}
		}
	}
	if cfg.Thinking != "" && entry.session.ThinkingLevel() != cfg.Thinking {
		entry.session.SetThinkingLevel(cfg.Thinking)
	}
}

// restoredMCPFailures waits for MCP startup and returns the client-supplied
// servers that are not connected.
func restoredMCPFailures(ctx context.Context, entry *firSession, client map[string]mcp.ServerConfig) []string {
	if entry.mcpManager == nil {
		return serverNames(client)
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
	for _, name := range serverNames(client) {
		if !connected[name] {
			failed = append(failed, name)
		}
	}
	return failed
}
