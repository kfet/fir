package agent

import (
	"time"

	"github.com/kfet/pi-go/pkg/ai"
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
