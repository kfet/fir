package agent

import (
	"time"

	"github.com/kfet/fir/pkg/ai/core"
)

// testModel creates a test model for agent tests.
func testModel() *core.Model {
	return &core.Model{
		ID:            "test-model",
		Name:          "Test Model",
		Api:           core.ApiAnthropicMessages,
		Provider:      core.ProviderAnthropic,
		ContextWindow: 200000,
		MaxTokens:     4096,
	}
}

// mockStreamFn creates a StreamFn that returns canned responses.
func mockStreamFn(responses ...*core.AssistantMessage) StreamFn {
	callIdx := 0
	return func(model *core.Model, ctx core.Context, options *core.SimpleStreamOptions) *core.AssistantMessageEventStream {
		s := core.NewAssistantMessageEventStream()
		var msg *core.AssistantMessage
		if callIdx < len(responses) {
			msg = responses[callIdx]
			callIdx++
		} else {
			msg = responses[len(responses)-1]
		}
		go func() {
			s.Push(core.AssistantMessageEvent{Type: core.EventStart, Partial: msg})
			s.Push(core.AssistantMessageEvent{Type: core.EventDone, Reason: msg.StopReason, Message: msg})
			s.End(nil)
		}()
		return s
	}
}

// simpleResponse creates a simple text assistant response.
func simpleResponse(text string) *core.AssistantMessage {
	return &core.AssistantMessage{
		Role:       "assistant",
		Content:    []core.AssistantContent{core.NewTextContent(text)},
		Api:        core.ApiAnthropicMessages,
		Provider:   core.ProviderAnthropic,
		Model:      "test-model",
		StopReason: core.StopReasonStop,
		Timestamp:  time.Now().UnixMilli(),
	}
}

// transportError creates an assistant message that ended with a transport/
// stream error (stop_reason=error) after emitting partialText. When partialText
// is empty the message carries no content, simulating a reset before any output.
func transportError(partialText, errMsg string) *core.AssistantMessage {
	content := []core.AssistantContent{}
	if partialText != "" {
		content = append(content, core.NewTextContent(partialText))
	}
	return &core.AssistantMessage{
		Role:         "assistant",
		Content:      content,
		Api:          core.ApiAnthropicMessages,
		Provider:     core.ProviderAnthropic,
		Model:        "test-model",
		StopReason:   core.StopReasonError,
		ErrorMessage: errMsg,
		Timestamp:    time.Now().UnixMilli(),
	}
}
