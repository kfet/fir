package extension

import (
	"context"
	"encoding/json"
	"os/exec"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
	fmsg "github.com/kfet/fir/pkg/session"
)

// SessionBridge implements BridgeAPI directly on top of a core.AgentSession.
// It is the concrete adapter used in production so that external process
// extensions can call back into the running session without going through
// the (now removed) Go extension layer.
type SessionBridge struct {
	session *core.AgentSession
}

// NewSessionBridge creates a SessionBridge wrapping the given session.
func NewSessionBridge(session *core.AgentSession) *SessionBridge {
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
		cm := &fmsg.CustomMessage{
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

func (b *SessionBridge) GetActiveTools() []string {
	state := b.session.Agent.State()
	names := make([]string, len(state.Tools))
	for i, t := range state.Tools {
		names[i] = t.Name
	}
	return names
}

func (b *SessionBridge) SetActiveTools(names []string) {
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	state := b.session.Agent.State()
	filtered := make([]agent.AgentTool, 0, len(names))
	for _, t := range state.Tools {
		if nameSet[t.Name] {
			filtered = append(filtered, t)
		}
	}
	b.session.Agent.SetTools(filtered)
}

func (b *SessionBridge) SetModel(model *ai.Model) bool {
	mr := b.session.ModelRegistryRef()
	if mr != nil && mr.GetApiKey(model) == "" {
		return false
	}
	b.session.SetModel(model)
	return true
}

// RegisterTool adds an externally-defined tool to the session's agent.
// The tool is wrapped with the session's hook interceptors so that
// hook/tool_call interception still fires for it.
func (b *SessionBridge) RegisterTool(def ToolDefinition) {
	at := agent.AgentTool{
		Tool: ai.Tool{
			Name:        def.Name,
			Description: def.Description,
			Parameters:  def.Parameters,
		},
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			r, err := def.Execute(ToolContext{
				ToolCallID: toolCallID,
				Params:     params,
			})
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{
				Content: r.Content,
				IsError: r.IsError,
			}, nil
		},
	}

	// Wrap with session hooks so the hook/tool_call interceptor fires.
	wrapped := b.session.WrapToolsWithHooks([]agent.AgentTool{at})

	state := b.session.Agent.State()
	tools := make([]agent.AgentTool, len(state.Tools)+1)
	copy(tools, state.Tools)
	tools[len(state.Tools)] = wrapped[0]
	b.session.Agent.SetTools(tools)
}
