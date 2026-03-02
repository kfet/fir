package extproc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/core"
	firlog "github.com/kfet/fir/pkg/log"
)

// SetupOptions configures extproc extension discovery for a session.
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
}

// SetupResult holds the running state of extproc extensions for a session.
type SetupResult struct {
	// Manager owns all external process extension bridges (may be nil if
	// no ProjectDir was configured or no extensions were discovered).
	Manager *Manager

	session *core.AgentSession
}

// Stop shuts down all external process extensions.
func (r *SetupResult) Stop() {
	if r.Manager != nil {
		_ = r.Manager.Stop()
	}
}

// EmitSessionStart emits session_start to all running extproc extensions.
func (r *SetupResult) EmitSessionStart() {
	if r.Manager != nil {
		r.Manager.EmitEvent("session_start", nil)
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
	firlog.Info("extproc extensions reloading")
	return r.Manager.Reload(ctx)
}

// Setup discovers and starts external process extensions for the given session.
// It wires:
//   - Tool hooks (hook/tool_call) so extproc extensions can intercept tool calls.
//   - Agent session event forwarding to the extproc manager.
//
// Returns nil (with no error) when ProjectDir is empty (extproc disabled).
func Setup(session *core.AgentSession, opts SetupOptions) (*SetupResult, error) {
	if opts.ProjectDir == "" {
		return nil, nil
	}

	logger := firlog.With("component", "extproc")
	mgr := NewManager(logger)
	if opts.TrustStorePath != "" {
		mgr.SetTrustStore(NewTrustStoreWithPath(opts.TrustStorePath))
	}

	// Wire trust confirmation.
	confirmFn := opts.ConfirmFn
	if confirmFn == nil {
		confirmFn = func(name, path string) bool {
			fmt.Fprintf(os.Stderr, "fir: trusting project extension %q (%s)\n", name, path)
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

	bridge := NewSessionBridge(session)
	cwd := opts.Cwd
	if cwd == "" {
		cwd = opts.ProjectDir
	}

	if err := mgr.Start(context.Background(), opts.ProjectDir, cwd, bridge); err != nil {
		firlog.Warn("extproc manager start failed", "err", err)
	}

	result := &SetupResult{
		Manager: mgr,
		session: session,
	}

	// Wire tool hooks so extproc extensions can intercept tool calls via
	// hook/tool_call. When no extensions are running the hook is a no-op.
	hooks := &core.AgentSessionHooks{
		OnToolCall: func(toolCallID, toolName string, input map[string]any) *core.ToolCallBlock {
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
					return &core.ToolCallBlock{Reason: h.Reason}
				}
			}
			return nil
		},
	}
	session.SetHooks(hooks)

	// Forward agent session events to extproc extensions.
	session.Subscribe(func(event core.AgentSessionEvent) {
		ae := event.AgentEvent
		if ae == nil {
			return
		}
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
