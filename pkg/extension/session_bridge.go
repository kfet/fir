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

	// activeCalls tracks per-tool-call context and update callbacks so that
	// concurrent extension tool executions don't clobber each other.
	// Key: toolCallID (string) → *activeCall.
	activeCalls sync.Map
}

// activeCall holds the context and update callback for a single in-flight
// extension tool execution.
type activeCall struct {
	ctx      context.Context
	onUpdate agent.AgentToolUpdateCallback
}

// NewSessionBridge creates a SessionBridge wrapping the given session.
func NewSessionBridge(session *session.AgentSession) *SessionBridge {
	return &SessionBridge{session: session}
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
	b.session.SessionManager.AppendCustomEntry(spec.CustomType, raw)

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

func (b *SessionBridge) SetLabel(entryID, label string) {
	b.session.SessionManager.AppendLabelChange(entryID, label)
}

func (b *SessionBridge) ClearLabel(entryID string) {
	b.session.SessionManager.AppendLabelChange(entryID, "")
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

func (b *SessionBridge) SideQuery(question string) (string, error) {
	return b.session.SideQuery(context.Background(), question)
}

// SetSessionData / GetSessionData on SessionBridge are no-ops: the real
// per-extension routing is done by bridgeScopedAPI in bridge_api.go, which
// intercepts these calls before they reach SessionBridge.
func (b *SessionBridge) SetSessionData(_, _ string)             {}
func (b *SessionBridge) GetSessionData(_ string) (string, bool) { return "", false }

// CallTool executes a registered tool by name and returns its result.
// It looks up the tool in the agent's current tool set and calls its
// Execute function directly.
func (b *SessionBridge) CallTool(name string, params map[string]any) (ToolResult, error) {
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

	// Use the active context from the outer tool call (if any) so that
	// cancellation propagates to nested tool calls (e.g. aside → Bash).
	// Pick the first active call's context (typically there's only one).
	var ctx context.Context
	b.activeCalls.Range(func(_, v any) bool {
		ctx = v.(*activeCall).ctx
		return false // stop after first
	})
	if ctx == nil {
		ctx = context.Background()
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
			// Store per-call context and callback so nested requests and
			// ReportProgress can find them without races between concurrent calls.
			b.activeCalls.Store(toolCallID, &activeCall{ctx: ctx, onUpdate: onUpdate})
			defer b.activeCalls.Delete(toolCallID)

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

// ReportProgress sends a transient status message to the UI.
func (b *SessionBridge) ReportProgress(message string) {
	// Find any active call's update callback.
	var cb agent.AgentToolUpdateCallback
	b.activeCalls.Range(func(_, v any) bool {
		cb = v.(*activeCall).onUpdate
		return false // stop after first
	})
	if cb != nil {
		// Fire in a separate goroutine so we never block the bridge's
		// read loop — the events channel may be temporarily full.
		go cb(agent.AgentToolResult{StatusMessage: message})
	}
}
