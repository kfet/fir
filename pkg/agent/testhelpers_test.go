package agent

import (
	"time"

	"github.com/kfet/fir/pkg/ai"
)

// testModel creates a test model for agent tests.
func testModel() *ai.Model {
	return &ai.Model{
		ID:            "test-model",
		Name:          "Test Model",
		Api:           ai.ApiAnthropicMessages,
		Provider:      ai.ProviderAnthropic,
		ContextWindow: 200000,
		MaxTokens:     4096,
	}
}

// mockStreamFn creates a StreamFn that returns canned responses.
func mockStreamFn(responses ...*ai.AssistantMessage) StreamFn {
	callIdx := 0
	return func(model *ai.Model, ctx ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		s := ai.NewAssistantMessageEventStream()
		var msg *ai.AssistantMessage
		if callIdx < len(responses) {
			msg = responses[callIdx]
			callIdx++
		} else {
			msg = responses[len(responses)-1]
		}
		go func() {
			s.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
			s.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: msg.StopReason, Message: msg})
			s.End(nil)
		}()
		return s
	}
}

// simpleResponse creates a simple text assistant response.
func simpleResponse(text string) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Role:       "assistant",
		Content:    []ai.AssistantContent{ai.NewTextContent(text)},
		Api:        ai.ApiAnthropicMessages,
		Provider:   ai.ProviderAnthropic,
		Model:      "test-model",
		StopReason: ai.StopReasonStop,
		Timestamp:  time.Now().UnixMilli(),
	}
}

// transportError creates an assistant message that ended with a transport/
// stream error (stop_reason=error) after emitting partialText. When partialText
// is empty the message carries no content, simulating a reset before any output.
func transportError(partialText, errMsg string) *ai.AssistantMessage {
	content := []ai.AssistantContent{}
	if partialText != "" {
		content = append(content, ai.NewTextContent(partialText))
	}
	return &ai.AssistantMessage{
		Role:         "assistant",
		Content:      content,
		Api:          ai.ApiAnthropicMessages,
		Provider:     ai.ProviderAnthropic,
		Model:        "test-model",
		StopReason:   ai.StopReasonError,
		ErrorMessage: errMsg,
		Timestamp:    time.Now().UnixMilli(),
	}
}
