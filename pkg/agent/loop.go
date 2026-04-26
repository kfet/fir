// Ported from: packages/agent/src/agent-loop.ts
// Upstream hash: 48aa882
package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
)

// AgentLoop starts an agent loop with new prompt messages.
// Events are emitted to the returned channel.
func AgentLoop(
	ctx context.Context,
	prompts []AgentMessage,
	agentCtx *AgentContext,
	config *AgentLoopConfig,
	streamFn StreamFn,
	events chan<- AgentEvent,
) []AgentMessage {
	newMessages := make([]AgentMessage, len(prompts))
	copy(newMessages, prompts)

	currentCtx := &AgentContext{
		SystemPrompt: agentCtx.SystemPrompt,
		Messages:     append(append([]AgentMessage{}, agentCtx.Messages...), prompts...),
		Tools:        agentCtx.Tools,
	}

	events <- AgentEvent{Type: EventAgentStart}
	events <- AgentEvent{Type: EventTurnStart}
	for i := range prompts {
		events <- AgentEvent{Type: EventMessageStart, Message: &prompts[i]}
		events <- AgentEvent{Type: EventMessageEnd, Message: &prompts[i]}
	}

	result := runLoop(ctx, currentCtx, newMessages, config, streamFn, events)
	return result
}

// AgentLoopContinue continues an agent loop from the current context.
// Used for retries where context already has user message or tool results.
func AgentLoopContinue(
	ctx context.Context,
	agentCtx *AgentContext,
	config *AgentLoopConfig,
	streamFn StreamFn,
	events chan<- AgentEvent,
) ([]AgentMessage, error) {
	if len(agentCtx.Messages) == 0 {
		return nil, fmt.Errorf("cannot continue: no messages in context")
	}

	currentCtx := &AgentContext{
		SystemPrompt: agentCtx.SystemPrompt,
		Messages:     append([]AgentMessage{}, agentCtx.Messages...),
		Tools:        agentCtx.Tools,
	}

	events <- AgentEvent{Type: EventAgentStart}
	events <- AgentEvent{Type: EventTurnStart}

	result := runLoop(ctx, currentCtx, nil, config, streamFn, events)
	return result, nil
}

// runLoop is the main loop logic shared by AgentLoop and AgentLoopContinue.
func runLoop(
	ctx context.Context,
	currentCtx *AgentContext,
	newMessages []AgentMessage,
	config *AgentLoopConfig,
	streamFn StreamFn,
	events chan<- AgentEvent,
) []AgentMessage {
	firstTurn := true

	// Check for steering messages at start
	var pendingMessages []AgentMessage
	if config.GetSteeringMessages != nil {
		var err error
		pendingMessages, err = config.GetSteeringMessages()
		if err != nil {
			pendingMessages = nil
		}
	}

	firlog.Debug("agent loop starting", "messages", len(currentCtx.Messages), "tools", currentCtx.Tools.Len())

	// Outer loop: continues when follow-up messages arrive
	for {
		hasMoreToolCalls := true

		// Inner loop: process tool calls and steering
		for hasMoreToolCalls || len(pendingMessages) > 0 {
			if !firstTurn {
				events <- AgentEvent{Type: EventTurnStart}
			} else {
				firstTurn = false
			}

			// Process pending messages
			if len(pendingMessages) > 0 {
				for i := range pendingMessages {
					events <- AgentEvent{Type: EventMessageStart, Message: &pendingMessages[i]}
					events <- AgentEvent{Type: EventMessageEnd, Message: &pendingMessages[i]}
					currentCtx.Messages = append(currentCtx.Messages, pendingMessages[i])
					newMessages = append(newMessages, pendingMessages[i])
				}
				pendingMessages = nil
			}

			// Stream assistant response
			message := streamAssistantResponse(ctx, currentCtx, config, streamFn, events)
			newMessages = append(newMessages, NewAgentMessage(ai.NewAssistantMsg(*message)))

			if message.StopReason == ai.StopReasonError || message.StopReason == ai.StopReasonAborted {
				am := NewAgentMessage(ai.NewAssistantMsg(*message))
				events <- AgentEvent{
					Type:        EventTurnEnd,
					TurnMessage: &am,
					ToolResults: nil,
				}

				// Before exiting, check for follow-up messages (e.g. channel
				// messages that arrived during the failed turn). Without this,
				// injected messages are silently dropped after an error.
				if config.GetFollowUpMessages != nil {
					followUp, err := config.GetFollowUpMessages()
					if err == nil && len(followUp) > 0 {
						pendingMessages = followUp
						hasMoreToolCalls = false
						continue
					}
				}

				events <- AgentEvent{Type: EventAgentEnd, Messages: newMessages}
				return newMessages
			}

			// Check for tool calls
			var toolCalls []ai.ToolCall
			for _, c := range message.Content {
				if c.IsToolCall() {
					toolCalls = append(toolCalls, *c.ToolCall)
				}
			}
			hasMoreToolCalls = len(toolCalls) > 0

			var toolResults []ai.ToolResultMessage
			if hasMoreToolCalls {
				batch := executeToolCalls(ctx, currentCtx, message, events)
				toolResults = batch.messages
				hasMoreToolCalls = !batch.terminate

				for _, result := range toolResults {
					currentCtx.Messages = append(currentCtx.Messages, NewAgentMessage(ai.NewToolResultMsg(result)))
					newMessages = append(newMessages, NewAgentMessage(ai.NewToolResultMsg(result)))
				}
			}

			am := NewAgentMessage(ai.NewAssistantMsg(*message))
			events <- AgentEvent{
				Type:        EventTurnEnd,
				TurnMessage: &am,
				ToolResults: toolResults,
			}

			// Get steering messages after turn completes
			if config.GetSteeringMessages != nil {
				var err error
				pendingMessages, err = config.GetSteeringMessages()
				if err != nil {
					pendingMessages = nil
				}
			}
		}

		// Agent would stop. Check for follow-up messages.
		if config.GetFollowUpMessages != nil {
			followUp, err := config.GetFollowUpMessages()
			if err == nil && len(followUp) > 0 {
				pendingMessages = followUp
				continue
			}
		}

		break
	}

	events <- AgentEvent{Type: EventAgentEnd, Messages: newMessages}
	return newMessages
}

// streamAssistantResponse streams an LLM response, handling context transforms.
func streamAssistantResponse(
	ctx context.Context,
	agentCtx *AgentContext,
	config *AgentLoopConfig,
	streamFn StreamFn,
	events chan<- AgentEvent,
) *ai.AssistantMessage {
	// Apply context transform if configured
	messages := agentCtx.Messages
	if config.TransformContext != nil {
		var err error
		messages, err = config.TransformContext(ctx, messages)
		if err != nil {
			return errorAssistantMessage(config.Model, err.Error())
		}
	}

	// Convert to LLM-compatible messages
	if config.ConvertToLLM == nil {
		return errorAssistantMessage(config.Model, "no ConvertToLLM function configured")
	}
	llmMessages, err := config.ConvertToLLM(messages)
	if err != nil {
		return errorAssistantMessage(config.Model, err.Error())
	}

	// Build LLM context
	toolSlice := agentCtx.Tools.Slice()
	llmTools := make([]ai.Tool, len(toolSlice))
	for i, t := range toolSlice {
		llmTools[i] = t.Tool
	}

	llmContext := ai.Context{
		SystemPrompt: agentCtx.SystemPrompt,
		Messages:     llmMessages,
		Tools:        llmTools,
	}

	// Resolve API key
	apiKey := config.ApiKey
	var apiKeyError string
	if config.GetApiKey != nil {
		if resolved, err := config.GetApiKey(config.Model.Provider); err == nil && resolved != "" {
			apiKey = resolved
		} else if err != nil {
			apiKeyError = err.Error()
		}
	}

	// Stream
	var refreshApiKey func(string) string
	if config.GetApiKey != nil {
		refreshApiKey = func(provider string) string {
			if resolved, err := config.GetApiKey(provider); err == nil && resolved != "" {
				return resolved
			}
			return ""
		}
	}
	opts := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			ApiKey:          apiKey,
			ApiKeyError:     apiKeyError,
			Transport:       config.Transport,
			CacheRetention:  config.CacheRetention,
			SessionID:       config.SessionID,
			Headers:         config.Headers,
			MaxRetryDelayMs: config.MaxRetryDelayMs,
			Temperature:     config.Temperature,
			MaxTokens:       config.MaxTokens,
			ServerTools:     config.ServerTools,
			Compaction:      config.Compaction,
			OnPayload:       config.OnPayload,
			RefreshApiKey:   refreshApiKey,
		},
		Reasoning:       config.Reasoning,
		ThinkingBudgets: config.ThinkingBudgets,
	}

	firlog.Debug("streaming request",
		"provider", config.Model.Provider,
		"model", config.Model.ID,
		"messages", len(llmMessages),
		"tools", len(llmTools),
	)

	stream := streamFn(config.Model, llmContext, opts)

	var addedPartial bool
	var partialMsg *ai.AssistantMessage

	for event := range stream.Events {
		switch event.Type {
		case ai.EventStart:
			partialMsg = event.Partial
			if partialMsg != nil {
				agentCtx.Messages = append(agentCtx.Messages, NewAgentMessage(ai.NewAssistantMsg(*partialMsg)))
				addedPartial = true
				am := NewAgentMessage(ai.NewAssistantMsg(*partialMsg))
				events <- AgentEvent{Type: EventMessageStart, Message: &am}
			}

		case ai.EventTextStart, ai.EventTextDelta, ai.EventTextEnd,
			ai.EventThinkingStart, ai.EventThinkingDelta, ai.EventThinkingEnd,
			ai.EventToolcallStart, ai.EventToolcallDelta, ai.EventToolcallEnd:
			if event.Partial != nil {
				partialMsg = event.Partial
				if addedPartial {
					agentCtx.Messages[len(agentCtx.Messages)-1] = NewAgentMessage(ai.NewAssistantMsg(*partialMsg))
				}
				am := NewAgentMessage(ai.NewAssistantMsg(*partialMsg))
				events <- AgentEvent{
					Type:                  EventMessageUpdate,
					Message:               &am,
					AssistantMessageEvent: &event,
				}
			}

		case ai.EventDone, ai.EventError:
			finalMsg := stream.Result()
			if finalMsg == nil {
				finalMsg = errorAssistantMessage(config.Model, "stream ended without result")
			}
			firlog.Debug("stream complete",
				"stopReason", finalMsg.StopReason,
				"contentBlocks", len(finalMsg.Content),
				"error", finalMsg.ErrorMessage,
			)
			if addedPartial {
				agentCtx.Messages[len(agentCtx.Messages)-1] = NewAgentMessage(ai.NewAssistantMsg(*finalMsg))
			} else {
				agentCtx.Messages = append(agentCtx.Messages, NewAgentMessage(ai.NewAssistantMsg(*finalMsg)))
			}
			if !addedPartial {
				am := NewAgentMessage(ai.NewAssistantMsg(*finalMsg))
				events <- AgentEvent{Type: EventMessageStart, Message: &am}
			}
			am := NewAgentMessage(ai.NewAssistantMsg(*finalMsg))
			events <- AgentEvent{Type: EventMessageEnd, Message: &am}
			return finalMsg
		}
	}

	// Should not reach here normally
	result := stream.Result()
	if result == nil {
		return errorAssistantMessage(config.Model, "stream ended unexpectedly")
	}
	return result
}

// executedToolCallBatch is the result of executing a batch of tool calls.
type executedToolCallBatch struct {
	messages  []ai.ToolResultMessage
	terminate bool
}

// executeToolCalls executes tool calls from an assistant message.
func executeToolCalls(
	ctx context.Context,
	agentCtx *AgentContext,
	assistantMsg *ai.AssistantMessage,
	events chan<- AgentEvent,
) executedToolCallBatch {
	var toolCalls []ai.ToolCall
	for _, c := range assistantMsg.Content {
		if c.IsToolCall() {
			toolCalls = append(toolCalls, *c.ToolCall)
		}
	}

	var results []ai.ToolResultMessage
	var allTerminate bool = true

	for _, tc := range toolCalls {
		firlog.Debug("executing tool", "name", tc.Name, "id", tc.ID)

		// Look up the tool early so DisplayHint is available on the start event.
		tool, found := agentCtx.Tools.Get(tc.Name)
		var displayHint *ToolDisplayHint
		if found {
			displayHint = tool.DisplayHint
		}

		events <- AgentEvent{
			Type:        EventToolExecutionStart,
			ToolCallID:  tc.ID,
			ToolName:    tc.Name,
			Args:        tc.Arguments,
			DisplayHint: displayHint,
		}

		var result AgentToolResult
		var isError bool

		if !found {
			result = AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: fmt.Sprintf("Tool %s not found", tc.Name)}},
			}
			isError = true
		} else if tool.Execute == nil {
			result = AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: fmt.Sprintf("Tool %s has no execute function", tc.Name)}},
			}
			isError = true
		} else {
			var err error
			result, err = tool.Execute(ctx, tc.ID, tc.Arguments, func(partial AgentToolResult) {
				events <- AgentEvent{
					Type:          EventToolExecutionUpdate,
					ToolCallID:    tc.ID,
					ToolName:      tc.Name,
					Args:          tc.Arguments,
					DisplayHint:   displayHint,
					PartialResult: partial,
					StatusMessage: partial.StatusMessage,
				}
			})
			if err != nil {
				result = AgentToolResult{
					Content: []ai.ToolResultContent{{Type: "text", Text: err.Error()}},
					IsError: true,
				}
				isError = true
			} else {
				isError = result.IsError
			}
		}

		events <- AgentEvent{
			Type:        EventToolExecutionEnd,
			ToolCallID:  tc.ID,
			ToolName:    tc.Name,
			DisplayHint: displayHint,
			Result:      result,
			IsError:     isError,
		}

		toolResult := ai.ToolResultMessage{
			Role:       "toolResult",
			ToolCallID: tc.ID,
			ToolName:   tc.Name,
			Content:    result.Content,
			Details:    result.Details,
			IsError:    isError,
			Timestamp:  time.Now().UnixMilli(),
		}
		results = append(results, toolResult)

		if !result.Terminate {
			allTerminate = false
		}

		trMsg := NewAgentMessage(ai.NewToolResultMsg(toolResult))
		events <- AgentEvent{Type: EventMessageStart, Message: &trMsg}
		events <- AgentEvent{Type: EventMessageEnd, Message: &trMsg}
	}

	// Terminate only when there are tool calls AND every result sets terminate=true
	shouldTerminate := len(toolCalls) > 0 && allTerminate

	return executedToolCallBatch{messages: results, terminate: shouldTerminate}
}

// errorAssistantMessage creates an error assistant message.
func errorAssistantMessage(model *ai.Model, msg string) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Role:         "assistant",
		Content:      []ai.AssistantContent{},
		Api:          model.Api,
		Provider:     model.Provider,
		Model:        model.ID,
		StopReason:   ai.StopReasonError,
		ErrorMessage: msg,
		Timestamp:    time.Now().UnixMilli(),
	}
}
