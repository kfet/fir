package session

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/kfet/agent"

	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/session/store"
)

// SessionState is every per-session setting fir saves next to a transcript
// (<session>.jsonl.state.jsonl, 0600) and restores when the session is opened
// again, in any mode. It is saved and restored as a whole: a field added to
// either half survives a restart with no other code change.
type SessionState struct {
	// Runtime is how the running session was set up: its working directory,
	// session-scoped MCP servers, client metadata. It belongs to the running
	// session, so it is carried along when that session switches to another
	// transcript (/new, /resume, a fork).
	Runtime RuntimeState `json:"runtime"`
	// Conversation is the settings that follow the transcript. Switching to
	// another transcript adopts that transcript's saved values.
	Conversation ConversationState `json:"conversation"`
}

// RuntimeState is the half of SessionState bound to the running session.
type RuntimeState struct {
	Cwd string `json:"cwd,omitempty"`
	// McpServers are session-scoped MCP servers (e.g. supplied by an ACP
	// client or added at runtime), started on top of the config-file ones.
	McpServers map[string]mcp.ServerConfig `json:"mcpServers,omitempty"`
	Meta       map[string]any              `json:"meta,omitempty"`
	Mode       string                      `json:"mode,omitempty"`
	// Handle is an external name for the session (e.g. an ACP sessionId);
	// every save points it at the current transcript, see store.BindHandle.
	Handle string `json:"handle,omitempty"`
}

// ConversationState is the half of SessionState bound to the transcript.
type ConversationState struct {
	Model    string `json:"model,omitempty"` // provider/id
	Thinking string `json:"thinking,omitempty"`
	Name     string `json:"name,omitempty"`
}

// LoadState reads the saved state for a transcript.
func LoadState(sessionFile string) (SessionState, bool) {
	var st SessionState
	ok := store.ReadState(sessionFile, &st)
	return st, ok
}

// clone deep-copies st via its JSON form, so callers never share maps.
func (st SessionState) clone() SessionState {
	data, err := json.Marshal(st)
	if err != nil {
		return st
	}
	var out SessionState
	if json.Unmarshal(data, &out) != nil {
		return st
	}
	return out
}

// SessionState returns a copy of the session's current settings.
func (s *AgentSession) SessionState() SessionState {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.state.clone()
}

// UpdateSessionState changes the session's settings and saves them.
func (s *AgentSession) UpdateSessionState(fn func(*SessionState)) {
	s.updateStateNoSave(fn)
	s.SaveState()
}

func (s *AgentSession) updateStateNoSave(fn func(*SessionState)) {
	s.stateMu.Lock()
	fn(&s.state)
	s.stateMu.Unlock()
}

// SessionMCPServers returns the session-scoped MCP servers.
func (s *AgentSession) SessionMCPServers() map[string]mcp.ServerConfig {
	return s.SessionState().Runtime.McpServers
}

// SaveState writes the session's settings next to its transcript and points
// its handle (if any) at that transcript. In-memory sessions save nothing.
// Only MCP server names are logged: configs carry secrets in env/headers.
func (s *AgentSession) SaveState() {
	if s.closed.Load() || s.SessionStore == nil || !s.SessionStore.IsPersisted() {
		return
	}
	file := s.SessionStore.GetSessionFile()
	if file == "" {
		return
	}
	// saveMu orders writers; IO happens outside stateMu so readers never
	// wait on disk.
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if s.closed.Load() {
		return
	}
	st := s.SessionState()
	if err := store.WriteState(file, st); err != nil {
		firlog.Warn("session state: save failed", "file", file, "err", err)
		return
	}
	if h := st.Runtime.Handle; h != "" && s.agentDir != "" {
		if err := store.BindHandle(s.agentDir, h, file); err != nil {
			firlog.Warn("session state: bind handle failed", "err", err)
		}
	}
	firlog.Debug("session state: saved", "file", file,
		"mcpServers", ServerNames(st.Runtime.McpServers), "model", st.Conversation.Model)
}

// closeState stops all further saves, waiting out one in flight, so a late
// save cannot revive a session its owner has just forgotten.
func (s *AgentSession) closeState() {
	s.saveMu.Lock()
	s.closed.Store(true)
	s.saveMu.Unlock()
}

// restoreState is the single core restore path. It loads the saved state of
// the current transcript and applies it: runtime settings are replaced only
// when keepRuntime is false (opening a session, as opposed to switching the
// running one to another transcript); conversation settings are adopted and
// applied to the agent unless the caller pinned the model or thinking level.
func (s *AgentSession) restoreState(keepRuntime bool, override func(*SessionState), pinModel, pinThinking bool) {
	saved, ok := LoadState(s.SessionStore.GetSessionFile())
	s.stateMu.Lock()
	if ok {
		if keepRuntime {
			saved.Runtime = s.state.Runtime
		} else {
			// A handle is claimed only by its owner (via override): opening
			// someone else's transcript must not take over their name.
			saved.Runtime.Handle = ""
		}
		s.state = saved
	}
	if override != nil {
		override(&s.state)
	}
	conv := s.state.Conversation
	names := ServerNames(s.state.Runtime.McpServers)
	s.stateMu.Unlock()
	if ok {
		firlog.Info("session state: restored", "file", s.SessionStore.GetSessionFile(),
			"mcpServers", names, "model", conv.Model)
	}

	if !pinModel && conv.Model != "" && s.modelRegistry != nil {
		cur := s.Agent.State().Model
		if cur == nil || cur.Provider+"/"+cur.ID != conv.Model {
			if provider, id, found := strings.Cut(conv.Model, "/"); found {
				if m := s.modelRegistry.Find(provider, id); m != nil {
					s.Agent.SetModel(m)
					s.SessionStore.AppendModelChange(m.Provider, m.ID)
				}
			}
		}
	}
	if !pinThinking && conv.Thinking != "" && string(s.Agent.State().ThinkingLevel) != conv.Thinking {
		s.Agent.SetThinkingLevel(agent.ThinkingLevel(conv.Thinking))
		s.SessionStore.AppendThinkingLevelChange(conv.Thinking)
	}
	if conv.Name != "" && s.SessionStore.GetSessionName() == "" {
		s.SessionStore.AppendSessionInfo(conv.Name)
	}
	if !ok || pinModel || pinThinking {
		s.syncConversation()
	}
	s.updateStateNoSave(func(st *SessionState) {
		if st.Runtime.Cwd == "" {
			st.Runtime.Cwd = s.cwd
		}
	})
	s.SaveState()
}

// syncConversation records the live model, thinking level and name.
func (s *AgentSession) syncConversation() {
	st := s.Agent.State()
	name := s.SessionStore.GetSessionName()
	s.updateStateNoSave(func(ss *SessionState) {
		if st.Model != nil {
			ss.Conversation.Model = st.Model.Provider + "/" + st.Model.ID
		}
		if st.ThinkingLevel != "" {
			ss.Conversation.Thinking = string(st.ThinkingLevel)
		}
		if name != "" {
			ss.Conversation.Name = name
		}
	})
}

// ServerNames returns the sorted names of an MCP config map, for logging.
func ServerNames(m map[string]mcp.ServerConfig) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// mergeMCP returns base with overlay's servers added (overlay wins).
func mergeMCP(base, overlay map[string]mcp.ServerConfig) map[string]mcp.ServerConfig {
	if len(overlay) == 0 {
		return base
	}
	out := make(map[string]mcp.ServerConfig, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}
