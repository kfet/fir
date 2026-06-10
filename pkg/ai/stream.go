// Ported from: packages/ai/src/stream.ts
// Upstream hash: c99b9940
package ai

import (
	"context"
	"strings"
)

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

// StreamSimple calls the simplified provider stream function (with reasoning
// support).
//
// Reasoning policy is applied here, once, for every provider — providers
// themselves only render a (resolved) reasoning level to their wire format:
//
//   - resolveReasoning: a thinking-off request to a model that cannot disable
//     thinking (an always-on adaptive model) is downgraded to minimal effort
//     up front, so the bad combination never reaches a provider.
//   - thinking-off fallback: if we still send thinking-off (to a model we
//     believe can disable it) and the API rejects it, we transparently retry
//     with minimal thinking. This is the generic "try the other way" net for
//     any model/provider we have mis-flagged or do not know about.
func StreamSimple(ctx context.Context, registry *Registry, model *Model, prompt Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
	provider := registry.GetApiProvider(model.Api)
	if provider == nil {
		return errorStream(model, "no API provider registered for api: "+model.Api)
	}
	options = resolveReasoning(model, options)
	if options != nil && options.Reasoning == ThinkingOff && model != nil && model.Reasoning {
		return streamSimpleWithThinkingOffFallback(ctx, provider, model, prompt, options)
	}
	return provider.StreamSimple(ctx, model, prompt, options)
}

// resolveReasoning applies provider-agnostic reasoning policy before dispatch.
// A thinking-off request to a model that cannot disable thinking (always-on
// adaptive) is downgraded to minimal effort. Returns the possibly-copied
// options (never mutates the caller's struct).
func resolveReasoning(model *Model, options *SimpleStreamOptions) *SimpleStreamOptions {
	if options == nil || options.Reasoning != ThinkingOff {
		return options
	}
	if model != nil && model.Reasoning && model.AdaptiveThinking {
		cp := *options
		cp.Reasoning = ThinkingMinimal
		return &cp
	}
	return options
}

// streamSimpleWithThinkingOffFallback runs a thinking-off request and, if the
// provider rejects thinking-off before any output has streamed, transparently
// retries the request with minimal thinking. Provider-agnostic: it keys off the
// API error text, not the model or provider identity.
func streamSimpleWithThinkingOffFallback(ctx context.Context, provider *ApiProvider, model *Model, prompt Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
	out := NewAssistantMessageEventStream()
	go func() {
		inner := provider.StreamSimple(ctx, model, prompt, options)
		started := false
		for evt := range inner.Events {
			if evt.Type == EventStart {
				started = true
			}
			if !started && evt.Type == EventError {
				msg := ""
				if evt.Error != nil {
					msg = evt.Error.ErrorMessage
				}
				if isThinkingOffUnsupportedError(msg) {
					// Drain the rejected attempt, then retry with minimal
					// thinking. Nothing has streamed, so the swap is invisible.
					for range inner.Events {
					}
					cp := *options
					cp.Reasoning = ThinkingMinimal
					retry := provider.StreamSimple(ctx, model, prompt, &cp)
					for e2 := range retry.Events {
						out.Push(e2)
					}
					out.End(retry.Result())
					return
				}
			}
			out.Push(evt)
		}
		out.End(inner.Result())
	}()
	return out
}

// isThinkingOffUnsupportedError reports whether an API error indicates the model
// does not accept a request to disable thinking. Centralised here so the
// recovery works for every provider (e.g. Anthropic's
// "thinking.type.disabled" is not supported for this model).
func isThinkingOffUnsupportedError(msg string) bool {
	m := strings.ToLower(msg)
	if strings.Contains(m, "thinking.type.disabled") {
		return true
	}
	if strings.Contains(m, "not supported") || strings.Contains(m, "not support") || strings.Contains(m, "unsupported") {
		return strings.Contains(m, "thinking") || strings.Contains(m, "reasoning")
	}
	return false
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
