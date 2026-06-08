package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// SessionBridge implements BridgeAPI directly on top of a session.AgentSession.
// It is the concrete adapter used in production so that external process
// extensions can call back into the running session without going through
// the (now removed) Go extension layer.
type SessionBridge struct {
	session  *session.AgentSession
	mu       sync.Mutex // protects extTools and RegisterTool/UnregisterExtensionTools
	extTools []string   // names of tools registered by extensions

	restartMu sync.RWMutex
	restartFn RestartFn
	// pendingRestart records the most recent RestartSession request,
	// captured synchronously before the in-flight turn is aborted. Modes
	// that drive the new turn inline (ACP — see TakePendingRestart) consume
	// it after the aborted turn unwinds; modes that restart via the async
	// RestartFn (interactive) ignore it.
	pendingRestart *restartReq

	reloadMu sync.RWMutex
	reloadFn func(name string) error

	// Version and Mode are passed through into Introspect results.
	// Populated by Setup.
	Version string
	Mode    string
}

// NewSessionBridge creates a SessionBridge wrapping the given session.
func NewSessionBridge(session *session.AgentSession) *SessionBridge {
	return &SessionBridge{session: session}
}

// Introspect returns an introspection snapshot for the bound session.
func (b *SessionBridge) Introspect() session.Introspection {
	return b.session.Introspect(session.IntrospectOptions{
		Version: b.Version,
		Mode:    b.Mode,
	})
}

// GetObservableStore returns the session's observable cards store, or
// nil if the session isn't fully constructed (matches the nil pattern
// of GetSessionFile / GetSessionID on this bridge).
func (b *SessionBridge) GetObservableStore() *store.ObservableStore {
	if b.session == nil || b.session.SessionStore == nil {
		return nil
	}
	return b.session.SessionStore.Observables()
}

var _ BridgeAPI = (*SessionBridge)(nil)

func (b *SessionBridge) Exec(command string, args []string) (ExecResult, error) {
	cmd := exec.Command(command, args...)
	stdout, err := cmd.Output()
	result := ExecResult{Stdout: string(stdout)}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.Stderr = string(exitErr.Stderr)
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	if err != nil {
		return ExecResult{}, err
	}
	return result, nil
}

func (b *SessionBridge) SendMessage(spec CustomMessageSpec, opts *SendMessageOptions) {
	raw, err := json.Marshal(spec.Content)
	if err != nil {
		return
	}
	b.session.SessionStore.AppendCustomEntry(spec.CustomType, raw)

	if opts != nil && opts.DeliverAs != "" {
		cm := &store.CustomMessage{
			Role:       "custom",
			CustomType: spec.CustomType,
			Content:    spec.Content,
			Display:    spec.Display,
			Timestamp:  time.Now().UnixMilli(),
		}
		msg := agent.AgentMessage{Custom: cm}
		switch opts.DeliverAs {
		case "steer":
			b.session.Agent.Steer(msg)
		case "followUp":
			b.session.Agent.FollowUp(msg)
		}
	}

	if opts != nil && opts.TriggerTurn {
		go func() { _ = b.session.Agent.Continue() }()
	}
}

func (b *SessionBridge) SendUserMessage(content string, opts *SendUserMessageOptions) {
	deliverAs := ""
	if opts != nil {
		deliverAs = opts.DeliverAs
	}
	msg := agent.AgentMessage{
		Message: ai.NewUserMsg(content, time.Now().UnixMilli()),
	}
	switch deliverAs {
	case "steer":
		b.session.Agent.Steer(msg)
	case "followUp":
		b.session.Agent.FollowUp(msg)
	default:
		go func() { _ = b.session.Prompt(content) }()
	}
}

func (b *SessionBridge) SetSessionName(name string) {
	b.session.SetSessionName(name)
}

func (b *SessionBridge) GetSessionName() string {
	return b.session.GetSessionName()
}

// GetSessionFile returns the absolute path to the session's JSONL transcript,
// or "" for in-memory sessions. See BridgeAPI for full semantics.
func (b *SessionBridge) GetSessionFile() string {
	if b.session == nil || b.session.SessionStore == nil {
		return ""
	}
	return b.session.SessionStore.GetSessionFile()
}

// GetSessionID returns the unique identifier for the current session.
func (b *SessionBridge) GetSessionID() string {
	if b.session == nil || b.session.SessionStore == nil {
		return ""
	}
	return b.session.SessionStore.GetSessionID()
}

func (b *SessionBridge) SetLabel(entryID, label string) {
	b.session.SessionStore.AppendLabelChange(entryID, label)
}

func (b *SessionBridge) ClearLabel(entryID string) {
	b.session.SessionStore.AppendLabelChange(entryID, "")
}

func (b *SessionBridge) SetModel(model *ai.Model) bool {
	mr := b.session.ModelRegistryRef()
	if mr != nil && mr.GetApiKey(model) == "" {
		return false
	}
	b.session.SetModel(model)
	return true
}

func (b *SessionBridge) ContinueSession() error {
	go func() { _ = b.session.Agent.Continue() }()
	return nil
}

func (b *SessionBridge) SideQuery(question string, opts *session.SideQueryOptions) (string, error) {
	return b.session.SideQuery(context.Background(), question, opts)
}

func (b *SessionBridge) SideQueryStream(question string, opts *session.SideQueryOptions, onDelta func(session.SideQueryDelta)) (session.SideQueryResult, error) {
	return b.session.SideQueryStream(context.Background(), question, opts, onDelta)
}

// SetSessionData / GetSessionData on SessionBridge are no-ops: the real
// per-extension routing is done by Bridge.handleInbound, which calls
// Bridge.SetSessionData / Bridge.GetSessionData directly.
func (b *SessionBridge) SetSessionData(_, _ string)             {}
func (b *SessionBridge) GetSessionData(_ string) (string, bool) { return "", false }

// CallTool executes a registered tool by name and returns its result.
// It looks up the tool in the agent's current tool set and calls its
// Execute function directly.
func (b *SessionBridge) CallTool(ctx context.Context, name string, params map[string]any) (ToolResult, error) {
	tools := b.session.GetTools()
	if tools == nil {
		return ToolResult{
			Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "no tools available"}},
			IsError: true,
		}, nil
	}

	tool, found := tools.Get(name)
	if !found {
		return ToolResult{
			Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: fmt.Sprintf("tool %q not found. Available tools: %s", name, strings.Join(tools.Names(), ", "))}},
			IsError: true,
		}, nil
	}

	if tool.Execute == nil {
		return ToolResult{
			Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: fmt.Sprintf("tool %q has no execute function", name)}},
			IsError: true,
		}, nil
	}

	if params == nil {
		params = make(map[string]any)
	}

	result, err := tool.Execute(ctx, fmt.Sprintf("ext-call-%s", name), params, nil)
	if err != nil {
		return ToolResult{}, err
	}

	return ToolResult{
		Content: result.Content,
		IsError: result.IsError,
	}, nil
}

// ListTools returns info about all registered tools.
func (b *SessionBridge) ListTools() []ToolInfo {
	tools := b.session.GetTools()
	if tools == nil {
		return nil
	}
	var infos []ToolInfo
	for _, t := range tools.Slice() {
		var params map[string]any
		if m, ok := t.Tool.Parameters.(map[string]any); ok {
			params = m
		}
		infos = append(infos, ToolInfo{
			Name:        t.Tool.Name,
			Description: t.Tool.Description,
			Parameters:  params,
		})
	}
	return infos
}

func (b *SessionBridge) PrependContext(content string) {
	b.session.PrependContext(content)
}

// RegisterTool adds an externally-defined tool to the session's agent.
// The tool is wrapped with the session's hook interceptors so that
// hook/tool_call interception still fires for it.
func (b *SessionBridge) RegisterTool(def ToolDefinition) {
	b.mu.Lock()
	defer b.mu.Unlock()
	at := agent.AgentTool{
		Tool: ai.Tool{
			Name:        def.Name,
			Description: def.Description,
			Parameters:  def.Parameters,
		},
		DisplayHint: def.DisplayHint,
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			// Wire the agent's progress callback to the Bridge so that
			// inbound report_progress calls reach the right place.
			if def.Bridge != nil && onUpdate != nil {
				fn := func(msg string) {
					go onUpdate(agent.AgentToolResult{StatusMessage: msg})
				}
				def.Bridge.activeReportProgress.Store(&fn)
				defer def.Bridge.activeReportProgress.Store(nil)
			}

			r, err := def.Execute(ToolContext{
				Context:    ctx,
				ToolCallID: toolCallID,
				Params:     params,
			})
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{
				Content: r.Content,
				IsError: r.IsError,
				Details: r.Details,
			}, nil
		},
	}

	// Wrap with session hooks so the hook/tool_call interceptor fires.
	wrapped := b.session.WrapToolsWithHooks([]agent.AgentTool{at})

	b.session.Agent.UpdateTools(func(ts *agent.ToolSet) {
		ts.Add(wrapped[0])
	})

	b.extTools = append(b.extTools, def.Name)
}

// UnregisterExtensionTools removes all tools previously registered by extensions.
// Called during reload to prevent duplicate tool names.
func (b *SessionBridge) UnregisterExtensionTools() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.extTools) == 0 {
		return
	}
	names := b.extTools
	b.session.Agent.UpdateTools(func(ts *agent.ToolSet) {
		for _, name := range names {
			ts.Remove(name)
		}
	})
	b.extTools = nil
}

// removeExtensionTools removes only the named extension tools from the
// session's tool set, leaving every other extension's tools intact. It is
// the targeted counterpart to UnregisterExtensionTools and is intentionally
// unexported: only Manager.ReloadOne (same package) reaches it, via a type
// assertion, so no caller-visible "unregister by name" surface is added.
func (b *SessionBridge) removeExtensionTools(names []string) {
	if len(names) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	remove := make(map[string]bool, len(names))
	for _, n := range names {
		remove[n] = true
	}
	b.session.Agent.UpdateTools(func(ts *agent.ToolSet) {
		for _, name := range names {
			ts.Remove(name)
		}
	})
	kept := b.extTools[:0]
	for _, n := range b.extTools {
		if !remove[n] {
			kept = append(kept, n)
		}
	}
	b.extTools = kept
}

// ReportProgress is a no-op on the shared SessionBridge.
// Bridge.handleInbound calls the active progress reporter directly.
func (b *SessionBridge) ReportProgress(message string) {}

// RestartFn is set by the active mode to perform a session restart with
// mode-specific UI cleanup. When nil, RestartSession returns an error.
//
// The function is invoked from a fresh goroutine *after* the in-flight
// stream has been aborted; it should clear UI state, call NewSessionCmd,
// (optionally) inject prependContext via session.PrependContext, and
// submit prompt via session.Prompt. See InteractiveMode.handleHandoff.
type RestartFn func(prompt, prependContext string) error

// SetRestartFn registers a mode-specific restart handler. Pass nil to
// remove. Safe to call after the bridge is in use; the field is read on
// every RestartSession call.
func (b *SessionBridge) SetRestartFn(fn RestartFn) {
	b.restartMu.Lock()
	b.restartFn = fn
	b.restartMu.Unlock()
}

// RestartSession aborts the in-flight stream synchronously and schedules
// session clear + optional prepend-context + prompt submission asynchronously.
// Returns an error when no RestartFn is registered (the current mode does
// not support restart).
func (b *SessionBridge) RestartSession(prompt, prependContext string) error {
	b.restartMu.RLock()
	fn := b.restartFn
	b.restartMu.RUnlock()
	if fn == nil {
		return fmt.Errorf("session restart is not supported in this mode")
	}
	// Record the request synchronously BEFORE the abort. A mode that runs
	// the new turn inline (ACP) consumes it via TakePendingRestart after the
	// aborted turn unwinds; this store happens-before Abort, which
	// happens-before the in-flight Prompt() returns, so the consumer always
	// observes it without racing the async RestartFn goroutine.
	b.restartMu.Lock()
	b.pendingRestart = &restartReq{Prompt: prompt, PrependContext: prependContext}
	b.restartMu.Unlock()
	// Abort synchronously so the tool-result writeback for the calling
	// extension tool is short-circuited and never lands in the session.
	if b.session != nil && b.session.Agent != nil {
		b.session.Agent.Abort()
	}
	// The rest must run on a goroutine: the bridge dispatch goroutine is
	// holding the JSON-RPC handler open, and the mode callback may need
	// to acquire UI locks that the dispatcher must not block on.
	go func() {
		_ = fn(prompt, prependContext)
	}()
	return nil
}

// restartReq is a captured RestartSession request awaiting inline consumption.
type restartReq struct {
	Prompt         string
	PrependContext string
}

// TakePendingRestart returns and clears any restart request recorded by the
// most recent RestartSession call. Modes that drive the restart inline
// (rather than via the async RestartFn) call this after the aborted turn
// unwinds. Returns ok=false when no restart is pending.
func (b *SessionBridge) TakePendingRestart() (prompt, prependContext string, ok bool) {
	b.restartMu.Lock()
	defer b.restartMu.Unlock()
	if b.pendingRestart == nil {
		return "", "", false
	}
	req := b.pendingRestart
	b.pendingRestart = nil
	return req.Prompt, req.PrependContext, true
}

// SetReloadFn registers the targeted single-extension reload handler. It is
// wired by the extension Manager at Start so that the inbound
// reload_extension RPC can delegate back into Manager.ReloadOne. Pass nil to
// remove. Mirrors SetRestartFn.
func (b *SessionBridge) SetReloadFn(fn func(name string) error) {
	b.reloadMu.Lock()
	b.reloadFn = fn
	b.reloadMu.Unlock()
}

// ReloadExtension delegates to the manager-registered reload handler to
// reload exactly one extension by name. Returns an error when no handler is
// registered (reload unsupported) or the manager refuses the reload.
func (b *SessionBridge) ReloadExtension(name string) error {
	b.reloadMu.RLock()
	fn := b.reloadFn
	b.reloadMu.RUnlock()
	if fn == nil {
		return fmt.Errorf("extension reload is not supported in this session")
	}
	return fn(name)
}
