// Ported from: packages/ai/src/stream.ts
// Upstream hash: c99b9940
package ai

import "context"

// Stream calls the raw provider stream function.
// If no provider is registered for the model's API, returns an error stream.
func Stream(ctx context.Context, registry *Registry, model *Model, prompt Context, options *StreamOptions) *AssistantMessageEventStream {
	provider := registry.GetApiProvider(model.Api)
	if provider == nil {
		return errorStream(model, "no API provider registered for api: "+model.Api)
	}
	return provider.Stream(ctx, model, prompt, options)
}

// Complete calls Stream and waits for the final result.
func Complete(ctx context.Context, registry *Registry, model *Model, prompt Context, options *StreamOptions) *AssistantMessage {
	s := Stream(ctx, registry, model, prompt, options)
	return s.Result()
}

// StreamSimple calls the simplified provider stream function (with reasoning support).
func StreamSimple(ctx context.Context, registry *Registry, model *Model, prompt Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
	provider := registry.GetApiProvider(model.Api)
	if provider == nil {
		return errorStream(model, "no API provider registered for api: "+model.Api)
	}
	return provider.StreamSimple(ctx, model, prompt, options)
}

// CompleteSimple calls StreamSimple and waits for the final result.
func CompleteSimple(ctx context.Context, registry *Registry, model *Model, prompt Context, options *SimpleStreamOptions) *AssistantMessage {
	s := StreamSimple(ctx, registry, model, prompt, options)
	return s.Result()
}

// errorStream returns a stream that immediately emits an error event.
func errorStream(model *Model, message string) *AssistantMessageEventStream {
	s := NewAssistantMessageEventStream()
	errMsg := &AssistantMessage{
		Role:         RoleAssistant,
		Content:      []AssistantContent{},
		Model:        model.ID,
		Api:          model.Api,
		Provider:     model.Provider,
		StopReason:   StopReasonError,
		ErrorMessage: message,
	}
	go func() {
		s.Push(AssistantMessageEvent{
			Type:   EventError,
			Reason: StopReasonError,
			Error:  errMsg,
		})
		s.End(nil)
	}()
	return s
}
