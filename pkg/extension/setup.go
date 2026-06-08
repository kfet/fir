package extension

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/ai/ratelimit"
	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/session"
)

// SetupOptions configures extension discovery for a session.
type SetupOptions struct {
	// ProjectDir is the project root for discovering external process extensions
	// (looks for .fir/extensions/*.py and .fir/extensions/*.sh).
	// If empty, discovery is skipped and Setup returns nil.
	ProjectDir string

	// Cwd is the current working directory passed to external process extensions.
	// Defaults to ProjectDir when empty.
	Cwd string

	// ConfirmFn is called to ask the user whether to trust a project-local
	// extension before its first execution. If nil, extensions are auto-trusted
	// and a notice is printed to stderr.
	ConfirmFn ConfirmFunc

	// NotifyFn is called when an extension sends a "notify" request. If nil,
	// notifications are silently dropped.
	NotifyFn NotifyFunc

	// SetStatusFn is called when an extension sends a "set_status" request. If
	// nil, status updates are silently dropped.
	SetStatusFn SetStatusFunc

	// TrustStorePath overrides the path used to persist trusted-extension hashes.
	// Leave empty to use the default (~/.config/fir/trusted-extensions.json).
	// Useful in tests to avoid polluting the user's global trust store.
	TrustStorePath string

	// Mode is the active fir mode for this session (interactive, text, json, rpc, acp).
	// Extensions can constrain themselves via comment frontmatter `mode`/`modes`.
	Mode string

	// Version is the host version string, surfaced via agent.info to extensions.
	Version string

	// EnabledNames is an optional allowlist of extension names to activate.
	// When non-empty, only extensions whose name matches an entry are started.
	// When empty (the default), all discovered extensions are started.
	// This is populated from the "extensions" settings key and --extension flags.
	EnabledNames []string

	// DisabledNames is an optional denylist of extension names to skip.
	// Extensions in this list are not started even if they appear in EnabledNames.
	// This is populated from --disable-extension flags.
	DisabledNames []string

	// ExtraExtensionDirs lists additional directories to scan for extension
	// scripts. Each directory is scanned with "package" scope, shadowed by
	// both global and project extensions. Use this to load extensions
	// contributed by installed fir packages.
	ExtraExtensionDirs []string

	// ExtraExtensionFiles lists individual extension script paths to load.
	// Each file is treated as a "package"-scoped extension and is shadowed by
	// global and project extensions with the same name.
	// Use this to load individual extension files discovered in installed packages.
	ExtraExtensionFiles []string

	// ConfigDirs is the priority-ordered list of directories sent to each
	// extension via the init handshake. Highest priority first. Typically
	// [projectDir/.fir, agentDir]. Extensions read/write their config from
	// these via the SDK (load_config / config_path in Python).
	ConfigDirs []string
}

// SetupResult holds the running state of extensions for a session.
type SetupResult struct {
	// Manager owns all external process extension bridges (may be nil if
	// no ProjectDir was configured or no extensions were discovered).
	Manager *Manager

	// Bridge is the per-session BridgeAPI implementation that fields
	// inbound JSON-RPC calls from extensions. Modes use it to register
	// mode-specific callbacks (e.g. SetRestartFn for /handoff support).
	Bridge *SessionBridge

	session *session.AgentSession
}

// Stop shuts down all external process extensions.
func (r *SetupResult) Stop() {
	if r.Manager != nil {
		_ = r.Manager.Stop()
	}
}

// EmitSessionStart emits session_start to all running extensions.
// If the session has an existing name (e.g. resumed via --session or /reexec),
// a session_named event follows so extensions can sync with it.
// reexecData, when non-nil, is seeded into each extension's session data store
// before the event fires, and is also passed as "session_data" in the event
// params so extensions can restore state immediately in their handler.
func (r *SetupResult) EmitSessionStart(reexecData map[string]map[string]string) {
	if r.Manager != nil {
		// EmitSessionStartWithData seeds per-extension data and fires the event.
		r.Manager.EmitSessionStartWithData(reexecData)

		// Emit session_named for resumed sessions so extensions like
		// tmuxspinner can distinguish the session suffix from the
		// original window name.
		if r.session != nil {
			if name := r.session.GetSessionName(); name != "" {
				r.Manager.EmitEvent("session_named", SessionNamedPayload{
					Name: name,
				})
			}
		}
	}
}

// EmitSessionShutdown notifies extensions of shutdown and stops all processes.
func (r *SetupResult) EmitSessionShutdown() {
	r.Stop()
}

// StartFailures returns extensions that failed during the most recent startup.
// Returns nil when the Manager is nil or no failures occurred.
func (r *SetupResult) StartFailures() []StartFailure {
	if r.Manager == nil {
		return nil
	}
	return r.Manager.StartFailures()
}

// Reload stops all running extensions, re-discovers them, and starts fresh.
func (r *SetupResult) Reload(ctx context.Context) error {
	if r.Manager == nil {
		return nil
	}
	firlog.Info("extensions reloading")
	return r.Manager.Reload(ctx)
}

// Setup discovers and starts external process extensions for the given session.
// It wires:
//   - Tool hooks (hook/tool_call) so extensions can intercept tool calls.
//   - Agent session event forwarding to the extension manager.
//
// Returns nil (with no error) when ProjectDir is empty (extension disabled).
func Setup(asession *session.AgentSession, opts SetupOptions) (*SetupResult, error) {
	if opts.ProjectDir == "" {
		return nil, nil
	}

	logger := firlog.With("component", "extension")
	mgr := NewManager(logger)
	if opts.TrustStorePath != "" {
		mgr.SetTrustStore(NewTrustStoreWithPath(opts.TrustStorePath))
	}
	if len(opts.EnabledNames) > 0 {
		mgr.SetAllowedNames(opts.EnabledNames)
	}
	if len(opts.DisabledNames) > 0 {
		mgr.SetDisabledNames(opts.DisabledNames)
	}
	if len(opts.ExtraExtensionDirs) > 0 {
		mgr.SetExtraExtensionDirs(opts.ExtraExtensionDirs)
	}
	if len(opts.ExtraExtensionFiles) > 0 {
		mgr.SetExtraExtensionFiles(opts.ExtraExtensionFiles)
	}
	if len(opts.ConfigDirs) > 0 {
		mgr.SetConfigDirs(opts.ConfigDirs)
	}
	mgr.ActiveMode = opts.Mode

	// Wire trust confirmation.
	confirmFn := opts.ConfirmFn
	if confirmFn == nil {
		confirmFn = func(name, path string) bool {
			logger.Info("trusting project extension", "ext", name, "path", path)
			return true
		}
	}
	mgr.ConfirmFn = confirmFn

	// Wire optional UI callbacks.
	if opts.NotifyFn != nil {
		mgr.SetNotifyFn(opts.NotifyFn)
	}
	if opts.SetStatusFn != nil {
		mgr.SetSetStatusFn(opts.SetStatusFn)
	}

	bridge := NewSessionBridge(asession)
	bridge.Mode = opts.Mode
	bridge.Version = opts.Version
	cwd := opts.Cwd
	if cwd == "" {
		cwd = opts.ProjectDir
	}

	if err := mgr.Start(context.Background(), opts.ProjectDir, cwd, bridge); err != nil {
		firlog.Warn("extension manager start failed", "err", err)
	}

	result := &SetupResult{
		Manager: mgr,
		Bridge:  bridge,
		session: asession,
	}

	// Wire tool hooks so extensions can intercept tool calls via
	// hook/tool_call. When no extensions are running the hook is a no-op.
	hooks := &session.AgentSessionHooks{
		OnToolCall: func(toolCallID, toolName string, input map[string]any) *session.ToolCallBlock {
			raws, err := mgr.CallHook(context.Background(), "hook/tool_call", ToolCallHookPayload{
				ToolCallID: toolCallID,
				ToolName:   toolName,
				Params:     input,
			}, 5*time.Second)
			if err != nil {
				return nil
			}
			for _, raw := range raws {
				var h struct {
					Block  bool   `json:"block"`
					Reason string `json:"reason"`
				}
				if json.Unmarshal(raw, &h) == nil && h.Block {
					return &session.ToolCallBlock{Reason: h.Reason}
				}
			}
			return nil
		},
	}
	asession.SetHooks(hooks)

	// Forward agent session events to extensions.
	asession.Subscribe(func(event session.AgentSessionEvent) {
		// Session-level events (no agent event)
		if event.AgentEvent == nil {
			switch event.Type {
			case "session_named":
				mgr.EmitEvent("session_named", SessionNamedPayload{
					Name: event.SessionName,
				})
				mgr.EmitEvent("session_update", SessionUpdatePayload{
					Type:        event.Type,
					SessionName: event.SessionName,
				})
			case "plan_update":
				mgr.EmitEvent("session_update", SessionUpdatePayload{
					Type:        event.Type,
					SessionName: event.SessionName,
					Plan: &PlanInfo{
						Total:     len(event.PlanEntries),
						Completed: countCompleted(event.PlanEntries),
						Metadata:  event.PlanMetadata,
					},
				})
			}
			return
		}
		ae := event.AgentEvent
		switch ae.Type {
		case agent.EventAgentStart:
			mgr.EmitEvent("agent_start", nil)
		case agent.EventAgentEnd:
			mgr.EmitEvent("agent_end", nil)
		case agent.EventTurnStart:
			mgr.EmitEvent("turn_start", nil)
		case agent.EventTurnEnd:
			mgr.EmitEvent("turn_end", nil)
			if p := providerErrorPayload(ae); p != nil {
				mgr.EmitEvent("provider_error", p)
			}
		case agent.EventMessageStart:
			mgr.EmitEvent("message_start", nil)
		case agent.EventMessageEnd:
			payload := messageEndPayload(ae)
			mgr.EmitEvent("message_end", payload)
		case agent.EventToolExecutionStart:
			mgr.EmitEvent("tool_execution_start", ToolExecutionStartPayload{
				ToolCallID: ae.ToolCallID,
				ToolName:   ae.ToolName,
			})
		case agent.EventToolExecutionEnd:
			payload := ToolExecutionEndPayload{
				ToolCallID: ae.ToolCallID,
				ToolName:   ae.ToolName,
				IsError:    ae.IsError,
			}
			if ae.IsError {
				if r, ok := ae.Result.(agent.AgentToolResult); ok {
					for _, c := range r.Content {
						if c.Text != "" {
							payload.ErrorText = c.Text
							break
						}
					}
				}
			}
			mgr.EmitEvent("tool_execution_end", payload)
		}
	})

	return result, nil
}

// countCompleted returns the number of plan entries with status "completed".
func countCompleted(entries []agent.PlanEntry) int {
	n := 0
	for _, e := range entries {
		if e.Status == agent.PlanEntryStatusCompleted {
			n++
		}
	}
	return n
}

// messageEndPayload builds the extension payload for an `EventMessageEnd`
// event. For assistant messages with usage data we include role, provider,
// model, stop reason, response id, and the full Usage breakdown (tokens +
// cost). User and tool-result messages get only their role. The schema is
// stable enough for extensions (e.g. observe.py) to meter usage in their
// sidecars.
func messageEndPayload(ae *agent.AgentEvent) *MessageEndPayload {
	if ae == nil || ae.Message == nil {
		return nil
	}
	role := ae.Message.Role()
	if role == "" {
		return nil
	}
	payload := &MessageEndPayload{Role: role}
	am := ae.Message.AsAssistant()
	if am == nil {
		return payload
	}
	payload.Provider = string(am.Provider)
	payload.Model = am.Model
	if am.StopReason != "" {
		payload.StopReason = string(am.StopReason)
	}
	if am.ResponseID != "" {
		payload.ResponseID = am.ResponseID
	}
	u := am.Usage
	payload.Usage = &MessageEndUsage{
		Input:       u.Input,
		Output:      u.Output,
		CacheRead:   u.CacheRead,
		CacheWrite:  u.CacheWrite,
		TotalTokens: u.TotalTokens,
		Cost: MessageEndCost{
			Input:      u.Cost.Input,
			Output:     u.Cost.Output,
			CacheRead:  u.Cost.CacheRead,
			CacheWrite: u.Cost.CacheWrite,
			Total:      u.Cost.Total,
		},
	}
	return payload
}

// providerErrorPayload builds the extension payload for a `provider_error`
// event from an `EventTurnEnd`. It returns nil unless the turn message is an
// assistant message that stopped with an error and carries a non-empty error
// text. The error is classified via pkg/ai/ratelimit into a stable `kind`
// ("rate_limit", "overloaded", "server", "transport", "terminal") and tagged
// with `retryable` so extensions can decide whether to auto-resume.
func providerErrorPayload(ae *agent.AgentEvent) *ProviderErrorPayload {
	if ae == nil || ae.TurnMessage == nil {
		return nil
	}
	am := ae.TurnMessage.AsAssistant()
	if am == nil || am.StopReason != ai.StopReasonError {
		return nil
	}
	text := am.ErrorMessage
	if text == "" {
		return nil
	}
	retryable := ratelimit.IsRetryableError(text)
	payload := &ProviderErrorPayload{
		ErrorText: text,
		Kind:      classifyProviderError(text),
		Retryable: retryable,
		Provider:  string(am.Provider),
		Model:     am.Model,
	}
	if d := ratelimit.ExtractRetryDelayFromText(text); d > 0 {
		payload.RetryAfterMs = d.Milliseconds()
	}
	return payload
}

// classifyProviderError maps an error text to a stable kind. "overloaded" is
// distinguished from the broader rate-limit class (which also matches 529)
// because Anthropic overload is a distinct operational signal even though both
// are retryable. Order matters: overloaded is checked before the rate-limit
// catch-all.
func classifyProviderError(text string) string {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "overload") {
		return "overloaded"
	}
	if ratelimit.IsRateLimitText(text) {
		return "rate_limit"
	}
	if ratelimit.IsTransientServerError(text) {
		return "server"
	}
	if ratelimit.IsTransientNetworkError(text) {
		return "transport"
	}
	return "terminal"
}
