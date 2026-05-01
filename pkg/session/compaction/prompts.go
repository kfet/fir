// Ported from: packages/coding-agent/src/core/compaction/compaction.ts
// Upstream hash: 1caadb2e
package compaction

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// ============================================================================
// Summarization prompts
// ============================================================================

const summarizationPromptText = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const updateSummarizationPromptText = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const turnPrefixSummarizationPromptText = `This is the PREFIX of a turn that was too large to keep. The SUFFIX (recent work) is retained.

Summarize the prefix to provide context for the retained suffix:

## Original Request
[What did the user ask for in this turn?]

## Early Progress
- [Key decisions and work done in the prefix]

## Context for Suffix
- [Information needed to understand the retained recent work]

Be concise. Focus on what's needed to understand the kept suffix.`

// BuildSummarizationPrompt builds the prompt for summarization.
func BuildSummarizationPrompt(conversationText, previousSummary, customInstructions string) string {
	basePrompt := summarizationPromptText
	if previousSummary != "" {
		basePrompt = updateSummarizationPromptText
	}
	if customInstructions != "" {
		basePrompt = basePrompt + "\n\nAdditional focus: " + customInstructions
	}

	promptText := "<conversation>\n" + conversationText + "\n</conversation>\n\n"
	if previousSummary != "" {
		promptText += "<previous-summary>\n" + previousSummary + "\n</previous-summary>\n\n"
	}
	promptText += basePrompt
	return promptText
}

// ============================================================================
// Summary generation
// ============================================================================

// GenerateSummary generates a summary of the conversation using the LLM.
// If the context carries a CompactionProgressFunc (via core.WithCompactionProgress),
// it is called with phase="summarizing history" and each text delta as the LLM streams.
func GenerateSummary(
	ctx context.Context,
	registry *ai.Registry,
	currentMessages []agent.AgentMessage,
	model *ai.Model,
	reserveTokens int,
	apiKey string,
	customInstructions string,
	previousSummary string,
) (string, error) {
	maxTokens := int(0.8 * float64(reserveTokens))

	llmMessages, err := store.ConvertToLLM(currentMessages)
	if err != nil {
		return "", fmt.Errorf("convert messages: %w", err)
	}
	conversationText := SerializeConversation(llmMessages)
	promptText := BuildSummarizationPrompt(conversationText, previousSummary, customInstructions)

	progress := session.CompactionProgressFromCtx(ctx)
	if progress != nil {
		progress("summarizing history", "")
	}

	stream := ai.StreamSimple(ctx, registry, model, ai.Context{
		SystemPrompt: SummarizationSystemPrompt,
		Messages:     []ai.Message{ai.NewUserMsg(promptText, time.Now().UnixMilli())},
	}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			MaxTokens: &maxTokens,
			ApiKey:    apiKey,
		},
		Reasoning: ai.ThinkingHigh,
	})

	var prevTextLen int
	for event := range stream.Events {
		if event.Type == ai.EventTextDelta && event.Partial != nil && progress != nil {
			current := extractTextFromResponse(event.Partial)
			if len(current) > prevTextLen {
				progress("summarizing history", current[prevTextLen:])
				prevTextLen = len(current)
			}
		}
	}

	response := stream.Result()
	if response == nil {
		return "", fmt.Errorf("no response from summarization")
	}
	if response.StopReason == ai.StopReasonError {
		return "", fmt.Errorf("summarization failed: %s", response.ErrorMessage)
	}

	return extractTextFromResponse(response), nil
}

func generateTurnPrefixSummary(
	ctx context.Context,
	registry *ai.Registry,
	messages []agent.AgentMessage,
	model *ai.Model,
	reserveTokens int,
	apiKey string,
) (string, error) {
	maxTokens := int(0.5 * float64(reserveTokens))

	llmMessages, err := store.ConvertToLLM(messages)
	if err != nil {
		return "", fmt.Errorf("convert messages: %w", err)
	}
	conversationText := SerializeConversation(llmMessages)
	promptText := "<conversation>\n" + conversationText + "\n</conversation>\n\n" + turnPrefixSummarizationPromptText

	progress := session.CompactionProgressFromCtx(ctx)
	if progress != nil {
		progress("summarizing turn context", "")
	}

	stream := ai.StreamSimple(ctx, registry, model, ai.Context{
		SystemPrompt: SummarizationSystemPrompt,
		Messages:     []ai.Message{ai.NewUserMsg(promptText, time.Now().UnixMilli())},
	}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			MaxTokens: &maxTokens,
			ApiKey:    apiKey,
		},
	})

	var prevTextLen int
	for event := range stream.Events {
		if event.Type == ai.EventTextDelta && event.Partial != nil && progress != nil {
			current := extractTextFromResponse(event.Partial)
			if len(current) > prevTextLen {
				progress("summarizing turn context", current[prevTextLen:])
				prevTextLen = len(current)
			}
		}
	}

	response := stream.Result()
	if response == nil {
		return "", fmt.Errorf("no response from turn prefix summarization")
	}
	if response.StopReason == ai.StopReasonError {
		return "", fmt.Errorf("turn prefix summarization failed: %s", response.ErrorMessage)
	}

	return extractTextFromResponse(response), nil
}

func extractTextFromResponse(response *ai.AssistantMessage) string {
	var textParts []string
	for _, c := range response.Content {
		if c.Text != nil {
			textParts = append(textParts, c.Text.Text)
		}
	}
	return strings.Join(textParts, "\n")
}
