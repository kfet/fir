package mcp

import (
	"context"
	"encoding/base64"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kfet/fir/pkg/ai"
)

// NewSamplingFn returns a Manager.SamplingFn that fulfils
// sampling/createMessage requests using fir's AI registry.
//
// The provided model is used for all requests; model preferences from the MCP
// server are noted but currently not used to override the default model.
func NewSamplingFn(registry *ai.Registry, model *ai.Model) func(context.Context, *sdk.CreateMessageRequest) (*sdk.CreateMessageResult, error) {
	return func(ctx context.Context, req *sdk.CreateMessageRequest) (*sdk.CreateMessageResult, error) {
		p := req.Params

		// Convert MCP messages to fir AI messages.
		msgs, err := samplingMessagesToAI(p.Messages)
		if err != nil {
			return nil, fmt.Errorf("sampling: convert messages: %w", err)
		}
		if len(msgs) == 0 {
			return nil, fmt.Errorf("sampling: no messages provided")
		}

		prompt := ai.Context{
			SystemPrompt: p.SystemPrompt,
			Messages:     msgs,
		}
		opts := &ai.SimpleStreamOptions{}
		if p.MaxTokens > 0 {
			mt := int(p.MaxTokens)
			opts.MaxTokens = &mt
		}
		if p.Temperature > 0 {
			opts.Temperature = &p.Temperature
		}

		result := ai.CompleteSimple(ctx, registry, model, prompt, opts)
		if result == nil {
			return nil, fmt.Errorf("sampling: nil result from provider")
		}
		if result.StopReason == ai.StopReasonError {
			return nil, fmt.Errorf("sampling: %s", result.ErrorMessage)
		}

		return assistantToCreateMessageResult(result, model.ID), nil
	}
}

// samplingMessagesToAI converts a slice of MCP SamplingMessages to fir AI messages.
// Only "user" and "assistant" roles with text or image content are supported;
// unsupported content types are rendered as a placeholder text.
func samplingMessagesToAI(msgs []*sdk.SamplingMessage) ([]ai.Message, error) {
	out := make([]ai.Message, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "user":
			switch c := m.Content.(type) {
			case *sdk.TextContent:
				out = append(out, ai.NewUserMsg(c.Text, 0))
			case *sdk.ImageContent:
				out = append(out, ai.NewUserMsg([]any{map[string]any{
					"type":       "image",
					"media_type": c.MIMEType,
					"data":       base64.StdEncoding.EncodeToString(c.Data),
				}}, 0))
			default:
				out = append(out, ai.NewUserMsg("(unsupported content type)", 0))
			}
		case "assistant":
			switch c := m.Content.(type) {
			case *sdk.TextContent:
				out = append(out, ai.NewAssistantMsg(ai.AssistantMessage{
					Content: []ai.AssistantContent{
						{Text: &ai.TextContent{Text: c.Text}},
					},
				}))
			default:
				out = append(out, ai.NewAssistantMsg(ai.AssistantMessage{
					Content: []ai.AssistantContent{
						{Text: &ai.TextContent{Text: "(unsupported content type)"}},
					},
				}))
			}
		default:
			return nil, fmt.Errorf("unsupported role %q in sampling message", m.Role)
		}
	}
	return out, nil
}

// assistantToCreateMessageResult converts an ai.AssistantMessage to a
// sdk.CreateMessageResult. Only the first text content block is included.
func assistantToCreateMessageResult(msg *ai.AssistantMessage, modelID string) *sdk.CreateMessageResult {
	var text string
	for _, c := range msg.Content {
		if c.Text != nil {
			text = c.Text.Text
			break
		}
	}
	return &sdk.CreateMessageResult{
		Role:       "assistant",
		Model:      modelID,
		Content:    &sdk.TextContent{Text: text},
		StopReason: samplingStopReason(msg.StopReason),
	}
}

// samplingStopReason maps fir stop reasons to MCP stop reason strings.
func samplingStopReason(r ai.StopReason) string {
	switch r {
	case ai.StopReasonStop:
		return "endTurn"
	case ai.StopReasonLength:
		return "maxTokens"
	default:
		return string(r)
	}
}
