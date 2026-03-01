package extension

import (
	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/extproc"
)

// ExtProcAdapter wraps an extension API and implements extproc.BridgeAPI.
type ExtProcAdapter struct {
	api API
}

var _ extproc.BridgeAPI = (*ExtProcAdapter)(nil)

func (a *ExtProcAdapter) Exec(command string, args []string) (extproc.ExecResult, error) {
	r, err := a.api.Exec(command, args)
	if err != nil {
		return extproc.ExecResult{}, err
	}
	return extproc.ExecResult{
		Stdout:   r.Stdout,
		Stderr:   r.Stderr,
		ExitCode: r.ExitCode,
	}, nil
}

func (a *ExtProcAdapter) SendMessage(spec extproc.CustomMessageSpec, opts *extproc.SendMessageOptions) {
	eSpec := CustomMessageSpec{
		CustomType: spec.CustomType,
		Content:    spec.Content,
		Display:    spec.Display,
	}
	var eOpts *SendMessageOptions
	if opts != nil {
		eOpts = &SendMessageOptions{
			TriggerTurn: opts.TriggerTurn,
			DeliverAs:   opts.DeliverAs,
		}
	}
	a.api.SendMessage(eSpec, eOpts)
}

func (a *ExtProcAdapter) SendUserMessage(content string, opts *extproc.SendUserMessageOptions) {
	var eOpts *SendUserMessageOptions
	if opts != nil {
		eOpts = &SendUserMessageOptions{
			DeliverAs: opts.DeliverAs,
		}
	}
	a.api.SendUserMessage(content, eOpts)
}

func (a *ExtProcAdapter) SetSessionName(name string)      { a.api.SetSessionName(name) }
func (a *ExtProcAdapter) GetSessionName() string           { return a.api.GetSessionName() }
func (a *ExtProcAdapter) SetLabel(entryID, label string)   { a.api.SetLabel(entryID, label) }
func (a *ExtProcAdapter) ClearLabel(entryID string)        { a.api.ClearLabel(entryID) }
func (a *ExtProcAdapter) GetActiveTools() []string         { return a.api.GetActiveTools() }
func (a *ExtProcAdapter) SetActiveTools(names []string)    { a.api.SetActiveTools(names) }
func (a *ExtProcAdapter) SetModel(model *ai.Model) bool    { return a.api.SetModel(model) }

func (a *ExtProcAdapter) RegisterTool(def extproc.ToolDefinition) {
	a.api.RegisterTool(ToolDefinition{
		Name:        def.Name,
		Description: def.Description,
		Parameters:  def.Parameters,
		Execute: func(ctx ToolContext) (agent.AgentToolResult, error) {
			epCtx := extproc.ToolContext{
				ToolCallID: ctx.ToolCallID,
				Params:     ctx.Params,
			}
			r, err := def.Execute(epCtx)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{
				Content: r.Content,
				IsError: r.IsError,
			}, nil
		},
	})
}
