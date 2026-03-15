package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/kfet/fir/pkg/agent"
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

	// EnabledNames is an optional allowlist of extension names to activate.
	// When non-empty, only extensions whose name matches an entry are started.
	// When empty (the default), all discovered extensions are started.
	// This is populated from the "extensions" settings key and --extension flags.
	EnabledNames []string

	// OfferFixFn is called when a frontmatter mismatch is detected after an
	// extension handshake. It receives the mismatch and returns true if the
	// frontmatter should be auto-fixed. When nil, a warning is printed to stderr.
	OfferFixFn func(mm FrontmatterMismatch) bool

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
}

// SetupResult holds the running state of extensions for a session.
type SetupResult struct {
	// Manager owns all external process extension bridges (may be nil if
	// no ProjectDir was configured or no extensions were discovered).
	Manager *Manager

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
				r.Manager.EmitEvent("session_named", map[string]any{
					"name": name,
				})
			}
		}
	}
}

// EmitSessionShutdown notifies extensions of shutdown and stops all processes.
func (r *SetupResult) EmitSessionShutdown() {
	r.Stop()
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
	if len(opts.ExtraExtensionDirs) > 0 {
		mgr.SetExtraExtensionDirs(opts.ExtraExtensionDirs)
	}
	if len(opts.ExtraExtensionFiles) > 0 {
		mgr.SetExtraExtensionFiles(opts.ExtraExtensionFiles)
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

	// Wire frontmatter fix offer.
	if opts.OfferFixFn != nil {
		mgr.OfferFixFn = opts.OfferFixFn
	} else {
		// Default: print warning to stderr, don't fix.
		mgr.OfferFixFn = func(mm FrontmatterMismatch) bool {
			fmt.Fprintln(os.Stderr, FormatFrontmatterWarning(mm))
			return false
		}
	}

	// Wire optional UI callbacks.
	if opts.NotifyFn != nil {
		mgr.SetNotifyFn(opts.NotifyFn)
	}
	if opts.SetStatusFn != nil {
		mgr.SetSetStatusFn(opts.SetStatusFn)
	}

	bridge := NewSessionBridge(asession)
	cwd := opts.Cwd
	if cwd == "" {
		cwd = opts.ProjectDir
	}

	if err := mgr.Start(context.Background(), opts.ProjectDir, cwd, bridge); err != nil {
		firlog.Warn("extension manager start failed", "err", err)
	}

	result := &SetupResult{
		Manager: mgr,
		session: asession,
	}

	// Wire tool hooks so extensions can intercept tool calls via
	// hook/tool_call. When no extensions are running the hook is a no-op.
	hooks := &session.AgentSessionHooks{
		OnToolCall: func(toolCallID, toolName string, input map[string]any) *session.ToolCallBlock {
			raws, err := mgr.CallHook("hook/tool_call", map[string]any{
				"tool_call_id": toolCallID,
				"tool_name":    toolName,
				"params":       input,
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
				mgr.EmitEvent("session_named", map[string]any{
					"name": event.SessionName,
				})
				mgr.EmitEvent("session_update", map[string]any{
					"type":         event.Type,
					"session_name": event.SessionName,
				})
			case "plan_update":
				mgr.EmitEvent("session_update", map[string]any{
					"type":         event.Type,
					"session_name": event.SessionName,
					"plan": map[string]any{
						"total":     len(event.PlanEntries),
						"completed": countCompleted(event.PlanEntries),
						"metadata":  event.PlanMetadata,
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
		case agent.EventMessageStart:
			mgr.EmitEvent("message_start", nil)
		case agent.EventMessageEnd:
			mgr.EmitEvent("message_end", nil)
		case agent.EventToolExecutionStart:
			mgr.EmitEvent("tool_execution_start", map[string]any{
				"tool_call_id": ae.ToolCallID,
				"tool_name":    ae.ToolName,
			})
		case agent.EventToolExecutionEnd:
			mgr.EmitEvent("tool_execution_end", map[string]any{
				"tool_call_id": ae.ToolCallID,
				"tool_name":    ae.ToolName,
				"is_error":     ae.IsError,
			})
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
